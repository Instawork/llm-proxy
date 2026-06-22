import asyncio
import logging
import os
from concurrent.futures import ThreadPoolExecutor
from contextlib import asynccontextmanager
from typing import Any

from fastapi import FastAPI, File, HTTPException, UploadFile
from fastapi.responses import JSONResponse
from onnxtr.io import DocumentFile
from onnxtr.models import ocr_predictor

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


_default_workers = os.cpu_count() or 4
OCR_MAX_WORKERS = _env_int("OCR_MAX_WORKERS", _default_workers)
OCR_MAX_CONCURRENCY = _env_int("OCR_MAX_CONCURRENCY", OCR_MAX_WORKERS)
OCR_QUEUE_TIMEOUT_SEC = _env_float("OCR_QUEUE_TIMEOUT_SEC", 5.0)
OCR_INFER_TIMEOUT_SEC = _env_float("OCR_INFER_TIMEOUT_SEC", 30.0)

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
        "initializing OCR predictor (workers=%d concurrency=%d)",
        OCR_MAX_WORKERS,
        OCR_MAX_CONCURRENCY,
    )
    predictor = ocr_predictor(
        det_arch="fast_base",
        reco_arch="crnn_vgg16_bn",
        assume_straight_pages=False,
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


@app.post("/extract-text")
async def extract_text(image: UploadFile = File(...)) -> JSONResponse:
    if predictor is None or executor is None or gate is None:
        raise HTTPException(status_code=503, detail="OCR service not ready")

    try:
        img_bytes = await image.read()
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
