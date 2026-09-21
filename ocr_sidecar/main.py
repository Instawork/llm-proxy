import asyncio
import hmac
import logging
import os
from concurrent.futures import ThreadPoolExecutor
from contextlib import asynccontextmanager
from typing import Any

import onnxruntime as ort
from fastapi import FastAPI, HTTPException, Request
from fastapi.responses import JSONResponse
from onnxtr.io import DocumentFile
from onnxtr.models import ocr_predictor
from onnxtr.models.engine import EngineConfig
from starlette.datastructures import UploadFile

logger = logging.getLogger("ocr_sidecar")
logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")


def _env_int(name: str, default: int) -> int:
    raw = os.getenv(name, "").strip()
    if raw == "":
        return default
    return int(raw)


def _env_float(name: str, default: float) -> float:
    raw = os.getenv(name, "").strip()
    if raw == "":
        return default
    return float(raw)


# Throughput model: OnnxTR CPU inference does not scale across threads inside a
# single Python process (GIL + a single ORT session serialize work to ~2 img/s
# regardless of thread count). Horizontal parallelism therefore comes from
# running ONE uvicorn worker process PER CPU core (see entrypoint.sh), each with
# its own model and pinned to a single math thread (OMP_NUM_THREADS=1) so the
# processes do not oversubscribe the cores.
#
# Consequently each process is sized to do ONE inference at a time. The
# semaphore only adds a tiny accept-queue so a brief request overlap (HTTP read
# while the previous inference finishes) does not 503 immediately.
OCR_MAX_WORKERS = _env_int("OCR_MAX_WORKERS", 1)
OCR_MAX_CONCURRENCY = _env_int("OCR_MAX_CONCURRENCY", OCR_MAX_WORKERS + 1)
OCR_QUEUE_TIMEOUT_SEC = _env_float("OCR_QUEUE_TIMEOUT_SEC", 5.0)
OCR_INFER_TIMEOUT_SEC = _env_float("OCR_INFER_TIMEOUT_SEC", 30.0)

# ONNX Runtime keeps its OWN intra-op thread pool and ignores OMP_NUM_THREADS.
# Left at the default it sizes that pool to the core count, so with one worker
# process per core (see entrypoint.sh) every process would spawn N threads and
# oversubscribe the CPU — collapsing throughput (2 concurrent requests end up
# SLOWER than 1). Pin each process's ORT session to a single thread so
# parallelism comes cleanly from the process fan-out instead.
# Default 4 = sub-second latency with in-task concurrency on an 8-core task.
# entrypoint.sh sets this explicitly and derives worker count from it.
OCR_ORT_INTRA_THREADS = _env_int("OCR_ORT_INTRA_THREADS", 4)
OCR_ORT_INTER_THREADS = _env_int("OCR_ORT_INTER_THREADS", 1)

# The sidecar sits on an internal ALB reachable from the whole VPC, so every
# caller must present the shared token llm-proxy sends in X-OCR-Token. Startup
# refuses to run without one rather than silently accepting anonymous uploads.
OCR_SIDECAR_TOKEN = os.getenv("OCR_SIDECAR_TOKEN", "").strip()
if not OCR_SIDECAR_TOKEN:
    raise SystemExit("OCR_SIDECAR_TOKEN is required")
# Cap the upload before it is buffered; matches id_gate.max_image_bytes upstream.
OCR_MAX_IMAGE_BYTES = _env_int("OCR_MAX_IMAGE_BYTES", 10 * 1024 * 1024)
_READ_CHUNK = 1024 * 1024
# Slack for multipart boundaries and part headers when checking Content-Length.
_MULTIPART_OVERHEAD = 16 * 1024


def _engine_cfg() -> EngineConfig:
    opts = ort.SessionOptions()
    opts.intra_op_num_threads = OCR_ORT_INTRA_THREADS
    opts.inter_op_num_threads = OCR_ORT_INTER_THREADS
    return EngineConfig(session_options=opts)


predictor: Any = None
executor: ThreadPoolExecutor | None = None
gate: asyncio.Semaphore | None = None


def _infer(img_bytes: bytes) -> str:
    doc = DocumentFile.from_images(img_bytes)
    result = predictor(doc)
    return result.render()


@asynccontextmanager
async def lifespan(app: FastAPI):
    global predictor, executor, gate

    os.environ.setdefault("OMP_NUM_THREADS", "1")
    os.environ.setdefault("MKL_NUM_THREADS", "1")

    logger.info(
        "initializing OCR predictor (pid=%d workers=%d concurrency=%d ort_intra=%d ort_inter=%d)",
        os.getpid(),
        OCR_MAX_WORKERS,
        OCR_MAX_CONCURRENCY,
        OCR_ORT_INTRA_THREADS,
        OCR_ORT_INTER_THREADS,
    )
    engine_cfg = _engine_cfg()
    predictor = ocr_predictor(
        det_arch="fast_base",
        reco_arch="crnn_vgg16_bn",
        assume_straight_pages=False,
        det_engine_cfg=engine_cfg,
        reco_engine_cfg=engine_cfg,
        clf_engine_cfg=engine_cfg,
    )
    executor = ThreadPoolExecutor(max_workers=OCR_MAX_WORKERS)
    gate = asyncio.Semaphore(OCR_MAX_CONCURRENCY)
    yield
    if executor is not None:
        executor.shutdown(wait=True, cancel_futures=False)


app = FastAPI(title="OnnxTR OCR Sidecar", lifespan=lifespan)


@app.get("/health")
async def health() -> dict[str, str]:
    return {"status": "ok"}


async def _read_capped(image: UploadFile) -> bytes:
    chunks: list[bytes] = []
    total = 0
    while chunk := await image.read(_READ_CHUNK):
        total += len(chunk)
        if total > OCR_MAX_IMAGE_BYTES:
            raise HTTPException(status_code=413, detail="image exceeds OCR_MAX_IMAGE_BYTES")
        chunks.append(chunk)
    return b"".join(chunks)


@app.post("/extract-text")
async def extract_text(request: Request) -> JSONResponse:
    # Authenticate and size-check from headers alone, before the multipart
    # body is parsed; a declared UploadFile parameter would spool the upload
    # first and let anonymous callers burn disk and CPU.
    if not hmac.compare_digest(request.headers.get("x-ocr-token", ""), OCR_SIDECAR_TOKEN):
        raise HTTPException(status_code=401, detail="missing or invalid X-OCR-Token")
    declared = request.headers.get("content-length")
    if declared is not None and declared.isdigit() and int(declared) > OCR_MAX_IMAGE_BYTES + _MULTIPART_OVERHEAD:
        raise HTTPException(status_code=413, detail="image exceeds OCR_MAX_IMAGE_BYTES")
    if predictor is None or executor is None or gate is None:
        raise HTTPException(status_code=503, detail="OCR service not ready")

    try:
        form = await request.form()
        image = form.get("image")
        if not isinstance(image, UploadFile):
            raise HTTPException(status_code=400, detail="multipart field 'image' is required")
        img_bytes = await _read_capped(image)
    except HTTPException:
        raise
    except Exception as exc:
        logger.exception("failed to read upload")
        raise HTTPException(status_code=400, detail=f"invalid upload: {exc}") from exc

    if not img_bytes:
        raise HTTPException(status_code=400, detail="empty upload")

    try:
        async with asyncio.timeout(OCR_QUEUE_TIMEOUT_SEC):
            await gate.acquire()
    except TimeoutError as exc:
        raise HTTPException(status_code=503, detail="OCR busy; try later") from exc

    try:
        loop = asyncio.get_running_loop()
        try:
            text = await asyncio.wait_for(
                loop.run_in_executor(executor, _infer, img_bytes),
                timeout=OCR_INFER_TIMEOUT_SEC,
            )
        except TimeoutError as exc:
            raise HTTPException(status_code=504, detail="OCR inference timed out") from exc
        except Exception as exc:
            logger.exception("OCR inference failed")
            raise HTTPException(status_code=500, detail=f"OCR inference failed: {exc}") from exc
    finally:
        gate.release()

    return JSONResponse(content={"text": text})
