import asyncio
import importlib.util
import os
import subprocess
import sys
import types
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

SIDECAR_DIR = Path(__file__).resolve().parent
MAIN_PATH = SIDECAR_DIR / "main.py"
ENTRYPOINT_PATH = SIDECAR_DIR / "entrypoint.sh"


def _install_ml_stubs(monkeypatch: pytest.MonkeyPatch) -> None:
    """Stub onnxruntime and onnxtr so main.py can be imported without ML deps."""
    ort_mod = types.ModuleType("onnxruntime")

    class _SessionOptions:
        intra_op_num_threads = 1
        inter_op_num_threads = 1

    ort_mod.SessionOptions = _SessionOptions

    onnxtr_mod = types.ModuleType("onnxtr")
    io_mod = types.ModuleType("onnxtr.io")
    models_mod = types.ModuleType("onnxtr.models")
    engine_mod = types.ModuleType("onnxtr.models.engine")

    class _DocumentFile:
        @staticmethod
        def from_images(img_bytes: bytes) -> bytes:
            return img_bytes

    class _EngineConfig:
        def __init__(self, session_options=None):
            self.session_options = session_options

    class _Result:
        def __init__(self, text: str):
            self._text = text

        def render(self) -> str:
            return self._text

    def _ocr_predictor(**_kwargs):
        return lambda doc: _Result(f"EXTRACTED:{len(doc)}")

    io_mod.DocumentFile = _DocumentFile
    models_mod.ocr_predictor = _ocr_predictor
    engine_mod.EngineConfig = _EngineConfig

    monkeypatch.setitem(sys.modules, "onnxruntime", ort_mod)
    monkeypatch.setitem(sys.modules, "onnxtr", onnxtr_mod)
    monkeypatch.setitem(sys.modules, "onnxtr.io", io_mod)
    monkeypatch.setitem(sys.modules, "onnxtr.models", models_mod)
    monkeypatch.setitem(sys.modules, "onnxtr.models.engine", engine_mod)


def _load_main(monkeypatch: pytest.MonkeyPatch, env: dict[str, str | None]):
    _install_ml_stubs(monkeypatch)
    for key in (
        "OCR_SIDECAR_TOKEN",
        "OCR_SIDECAR_PREVIOUS_TOKEN",
        "OCR_MAX_IMAGE_BYTES",
        "OCR_MAX_WORKERS",
        "OCR_MAX_CONCURRENCY",
        "OCR_QUEUE_TIMEOUT_SEC",
        "OCR_INFER_TIMEOUT_SEC",
    ):
        monkeypatch.delenv(key, raising=False)
    for key, val in env.items():
        if val is not None:
            monkeypatch.setenv(key, val)

    spec = importlib.util.spec_from_file_location("ocr_sidecar_main_under_test", MAIN_PATH)
    assert spec is not None and spec.loader is not None
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def _multipart_payload(field: str, data: bytes, boundary: str = "----testbound") -> tuple[bytes, str]:
    body = (
        f"--{boundary}\r\n"
        f'Content-Disposition: form-data; name="{field}"; filename="scan.png"\r\n'
        f"Content-Type: image/png\r\n\r\n"
    ).encode() + data + f"\r\n--{boundary}--\r\n".encode()
    return body, f"multipart/form-data; boundary={boundary}"


async def _run_asgi_request(
    app,
    method: str,
    path: str,
    headers: list[tuple[bytes, bytes]],
    chunks: list[bytes],
    *,
    with_lifespan: bool = True,
) -> tuple[int, dict[bytes, bytes], bytes, int]:
    """Drive an ASGI app directly with explicit multi-message request chunks."""
    sent_messages: list[dict] = []
    receive_calls = 0

    async def send(message: dict) -> None:
        sent_messages.append(message)

    if with_lifespan:
        lifespan_in: asyncio.Queue[dict] = asyncio.Queue()
        lifespan_out: asyncio.Queue[dict] = asyncio.Queue()
        await lifespan_in.put({"type": "lifespan.startup"})
        lifespan_task = asyncio.create_task(
            app({"type": "lifespan", "asgi": {"version": "3.0"}}, lifespan_in.get, lifespan_out.put)
        )
        startup_msg = await lifespan_out.get()
        assert startup_msg["type"] == "lifespan.startup.complete"
    else:
        lifespan_in = None
        lifespan_out = None
        lifespan_task = None

    try:
        idx = 0

        async def receive() -> dict:
            nonlocal idx, receive_calls
            receive_calls += 1
            if idx < len(chunks):
                chunk = chunks[idx]
                idx += 1
                return {"type": "http.request", "body": chunk, "more_body": idx < len(chunks)}
            return {"type": "http.request", "body": b"", "more_body": False}

        scope = {
            "type": "http",
            "asgi": {"version": "3.0"},
            "http_version": "1.1",
            "method": method,
            "scheme": "http",
            "path": path,
            "raw_path": path.encode("ascii"),
            "query_string": b"",
            "headers": headers,
        }
        await app(scope, receive, send)
    finally:
        if lifespan_task is not None and lifespan_in is not None and lifespan_out is not None:
            await lifespan_in.put({"type": "lifespan.shutdown"})
            shutdown_msg = await lifespan_out.get()
            assert shutdown_msg["type"] == "lifespan.shutdown.complete"
            await lifespan_task

    start = next(m for m in sent_messages if m["type"] == "http.response.start")
    resp_headers = {k.lower(): v for k, v in start.get("headers", [])}
    resp_body = b"".join(m.get("body", b"") for m in sent_messages if m["type"] == "http.response.body")
    return start["status"], resp_headers, resp_body, receive_calls


@pytest.mark.parametrize("token", [None, "", "   \t\n", " , , "])
def test_startup_requires_token(monkeypatch: pytest.MonkeyPatch, token: str | None) -> None:
    with pytest.raises(SystemExit, match="OCR_SIDECAR_TOKEN is required"):
        _load_main(monkeypatch, {"OCR_SIDECAR_TOKEN": token})


def test_startup_requires_primary_token_even_with_previous(monkeypatch: pytest.MonkeyPatch) -> None:
    with pytest.raises(SystemExit, match="OCR_SIDECAR_TOKEN is required"):
        _load_main(monkeypatch, {"OCR_SIDECAR_TOKEN": "", "OCR_SIDECAR_PREVIOUS_TOKEN": "old-secret"})


@pytest.mark.parametrize("limit", ["0", "-1", "-1048576"])
def test_startup_requires_positive_max_image_bytes(monkeypatch: pytest.MonkeyPatch, limit: str) -> None:
    with pytest.raises(SystemExit, match="OCR_MAX_IMAGE_BYTES must be positive"):
        _load_main(monkeypatch, {"OCR_SIDECAR_TOKEN": "secret", "OCR_MAX_IMAGE_BYTES": limit})


def test_health_is_unauthenticated(monkeypatch: pytest.MonkeyPatch) -> None:
    mod = _load_main(monkeypatch, {"OCR_SIDECAR_TOKEN": "secret"})
    with TestClient(mod.app) as client:
        resp = client.get("/health")
    assert resp.status_code == 200
    assert resp.json() == {"status": "ok"}


@pytest.mark.parametrize(
    "headers",
    [
        {},
        {"X-OCR-Token": "wrong"},
        [(b"x-ocr-token", b"secr\xe9t")],
    ],
)
def test_extract_text_rejects_missing_wrong_and_non_ascii_token(
    monkeypatch: pytest.MonkeyPatch,
    headers,
) -> None:
    mod = _load_main(monkeypatch, {"OCR_SIDECAR_TOKEN": "secret"})
    with TestClient(mod.app) as client:
        resp = client.post(
            "/extract-text",
            headers=headers,
            files={"image": ("scan.png", b"fake-png", "image/png")},
        )
    assert resp.status_code == 401
    assert resp.headers.get("connection") == "close"


def test_extract_text_matches_non_ascii_utf8_token(monkeypatch: pytest.MonkeyPatch) -> None:
    mod = _load_main(monkeypatch, {"OCR_SIDECAR_TOKEN": "s\u00e9cret"})
    with TestClient(mod.app) as client:
        resp = client.post(
            "/extract-text",
            headers=[(b"x-ocr-token", "s\u00e9cret".encode("utf-8"))],
            files={"image": ("scan.png", b"fake-png", "image/png")},
        )
    assert resp.status_code == 200
    assert resp.json() == {"text": "EXTRACTED:8"}


def test_extract_text_supports_comma_separated_and_previous_token_rotation(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    mod = _load_main(
        monkeypatch,
        {
            "OCR_SIDECAR_TOKEN": " tok-new , tok-mid ",
            "OCR_SIDECAR_PREVIOUS_TOKEN": "tok-old",
        },
    )
    with TestClient(mod.app) as client:
        for valid in ("tok-new", "tok-mid", "tok-old"):
            resp = client.post(
                "/extract-text",
                headers={"X-OCR-Token": valid},
                files={"image": ("scan.png", b"img", "image/png")},
            )
            assert resp.status_code == 200
        rejected = client.post(
            "/extract-text",
            headers={"X-OCR-Token": "tok-other"},
            files={"image": ("scan.png", b"img", "image/png")},
        )
        assert rejected.status_code == 401
        assert rejected.headers.get("connection") == "close"


def test_extract_text_returns_503_before_readiness(monkeypatch: pytest.MonkeyPatch) -> None:
    mod = _load_main(monkeypatch, {"OCR_SIDECAR_TOKEN": "secret"})
    client = TestClient(mod.app)
    try:
        resp = client.post(
            "/extract-text",
            headers={"X-OCR-Token": "secret"},
            files={"image": ("scan.png", b"fake-png", "image/png")},
        )
    finally:
        client.close()
    assert resp.status_code == 503


def test_extract_text_rejects_declared_content_length_over_limit(monkeypatch: pytest.MonkeyPatch) -> None:
    mod = _load_main(monkeypatch, {"OCR_SIDECAR_TOKEN": "secret", "OCR_MAX_IMAGE_BYTES": "64"})
    with TestClient(mod.app) as client:
        resp = client.post(
            "/extract-text",
            headers={
                "X-OCR-Token": "secret",
                "Content-Length": str(64 + mod._MULTIPART_OVERHEAD + 1),
                "Content-Type": "multipart/form-data; boundary=----b",
            },
            content=b"",
        )
    assert resp.status_code == 413
    assert resp.headers.get("connection") == "close"


def test_extract_text_ignores_non_ascii_content_length_digits(monkeypatch: pytest.MonkeyPatch) -> None:
    mod = _load_main(monkeypatch, {"OCR_SIDECAR_TOKEN": "secret", "OCR_MAX_IMAGE_BYTES": "64"})
    body, content_type = _multipart_payload("image", b"ok")
    status, _headers, _resp_body, _calls = asyncio.run(
        _run_asgi_request(
            mod.app,
            "POST",
            "/extract-text",
            headers=[
                (b"x-ocr-token", b"secret"),
                (b"content-type", content_type.encode("ascii")),
                (b"content-length", "\u00b2".encode("latin-1")),
            ],
            chunks=[body],
        )
    )
    assert status == 200


def test_extract_text_auth_runs_before_body_and_before_content_length(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    mod = _load_main(monkeypatch, {"OCR_SIDECAR_TOKEN": "secret", "OCR_MAX_IMAGE_BYTES": "64"})
    body, content_type = _multipart_payload("image", b"x" * 1024)
    status, headers, _resp_body, receive_calls = asyncio.run(
        _run_asgi_request(
            mod.app,
            "POST",
            "/extract-text",
            headers=[
                (b"content-type", content_type.encode("ascii")),
                (b"content-length", b"1000000000"),
            ],
            chunks=[body[:256], body[256:]],
        )
    )
    assert status == 401
    assert headers.get(b"connection") == b"close"
    assert receive_calls == 0


def test_extract_text_rejects_multi_chunk_streaming_body_before_trailing_chunks(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    mod = _load_main(monkeypatch, {"OCR_SIDECAR_TOKEN": "secret", "OCR_MAX_IMAGE_BYTES": "64"})
    body_limit = 64 + mod._MULTIPART_OVERHEAD
    # Build >2x body_limit split into 4 KiB ASGI messages (no Content-Length).
    body, content_type = _multipart_payload("image", b"x" * (body_limit * 2))
    chunk_size = 4096
    chunks = [body[i : i + chunk_size] for i in range(0, len(body), chunk_size)]
    assert len(chunks) >= 8

    status, headers, _resp_body, receive_calls = asyncio.run(
        _run_asgi_request(
            mod.app,
            "POST",
            "/extract-text",
            headers=[
                (b"x-ocr-token", b"secret"),
                (b"content-type", content_type.encode("ascii")),
            ],
            chunks=chunks,
        )
    )
    assert status == 413
    assert headers.get(b"connection") == b"close"
    # Must stop as soon as cumulative bytes exceed body_limit, before reading all chunks.
    assert 2 <= receive_calls < len(chunks)


def test_extract_text_rejects_image_over_exact_cap_within_multipart_overhead(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    mod = _load_main(monkeypatch, {"OCR_SIDECAR_TOKEN": "secret", "OCR_MAX_IMAGE_BYTES": "64"})
    with TestClient(mod.app) as client:
        resp = client.post(
            "/extract-text",
            headers={"X-OCR-Token": "secret"},
            files={"image": ("scan.png", b"x" * 65, "image/png")},
        )
    assert resp.status_code == 413
    assert resp.headers.get("connection") == "close"


def test_extract_text_validates_multipart_field_and_empty_body(monkeypatch: pytest.MonkeyPatch) -> None:
    mod = _load_main(monkeypatch, {"OCR_SIDECAR_TOKEN": "secret"})
    with TestClient(mod.app) as client:
        missing = client.post(
            "/extract-text",
            headers={"X-OCR-Token": "secret"},
            files={"other": ("scan.png", b"data", "image/png")},
        )
        empty = client.post(
            "/extract-text",
            headers={"X-OCR-Token": "secret"},
            files={"image": ("scan.png", b"", "image/png")},
        )
    assert missing.status_code == 400
    assert empty.status_code == 400


def test_extract_text_succeeds_for_valid_authenticated_upload(monkeypatch: pytest.MonkeyPatch) -> None:
    mod = _load_main(monkeypatch, {"OCR_SIDECAR_TOKEN": "secret", "OCR_MAX_IMAGE_BYTES": "64"})
    with TestClient(mod.app) as client:
        resp = client.post(
            "/extract-text",
            headers={"X-OCR-Token": "secret"},
            files={"image": ("scan.png", b"a" * 64, "image/png")},
        )
    assert resp.status_code == 200
    assert resp.json() == {"text": "EXTRACTED:64"}


@pytest.mark.parametrize(
    "env_overrides,expected_err",
    [
        ({"OCR_SIDECAR_TOKEN": ""}, "OCR_SIDECAR_TOKEN is required"),
        ({"OCR_SIDECAR_TOKEN": "  ,  "}, "OCR_SIDECAR_TOKEN is required"),
        ({"OCR_SIDECAR_TOKEN": "secret", "OCR_MAX_IMAGE_BYTES": "0"}, "OCR_MAX_IMAGE_BYTES must be positive"),
        ({"OCR_SIDECAR_TOKEN": "secret", "OCR_MAX_IMAGE_BYTES": "-5"}, "OCR_MAX_IMAGE_BYTES must be positive"),
        ({"OCR_SIDECAR_TOKEN": "secret", "OCR_MAX_IMAGE_BYTES": "abc"}, "OCR_MAX_IMAGE_BYTES must be positive"),
    ],
)
def test_entrypoint_fails_fast_before_spawning_workers(
    env_overrides: dict[str, str],
    expected_err: str,
) -> None:
    env = {k: v for k, v in os.environ.items() if not k.startswith("OCR_")}
    env.update(env_overrides)
    proc = subprocess.run(
        ["sh", str(ENTRYPOINT_PATH)],
        env=env,
        capture_output=True,
        text=True,
        check=False,
    )
    assert proc.returncode == 1
    assert expected_err in proc.stderr
