import importlib.util
import sys
import types
from pathlib import Path

import pytest
from fastapi.testclient import TestClient

MAIN_PATH = Path(__file__).resolve().parent / "main.py"


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


@pytest.mark.parametrize("token", [None, "", "   \t\n"])
def test_startup_requires_token(monkeypatch: pytest.MonkeyPatch, token: str | None) -> None:
    with pytest.raises(SystemExit, match="OCR_SIDECAR_TOKEN is required"):
        _load_main(monkeypatch, {"OCR_SIDECAR_TOKEN": token})


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


def test_extract_text_rejects_missing_wrong_and_non_ascii_token(monkeypatch: pytest.MonkeyPatch) -> None:
    mod = _load_main(monkeypatch, {"OCR_SIDECAR_TOKEN": "secret"})
    with TestClient(mod.app) as client:
        for headers in [
            {},
            {"X-OCR-Token": "wrong"},
            [(b"x-ocr-token", b"secr\xe9t")],
        ]:
            resp = client.post(
                "/extract-text",
                headers=headers,
                files={"image": ("scan.png", b"fake-png", "image/png")},
            )
            assert resp.status_code == 401


def test_extract_text_returns_503_before_readiness(monkeypatch: pytest.MonkeyPatch) -> None:
    mod = _load_main(monkeypatch, {"OCR_SIDECAR_TOKEN": "secret"})
    client = TestClient(mod.app)
    resp = client.post(
        "/extract-text",
        headers={"X-OCR-Token": "secret"},
        files={"image": ("scan.png", b"fake-png", "image/png")},
    )
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


def test_extract_text_rejects_streaming_body_without_content_length(monkeypatch: pytest.MonkeyPatch) -> None:
    mod = _load_main(monkeypatch, {"OCR_SIDECAR_TOKEN": "secret", "OCR_MAX_IMAGE_BYTES": "64"})
    body, content_type = _multipart_payload("image", b"x" * (64 + mod._MULTIPART_OVERHEAD + 1024))

    def _chunks():
        step = 4096
        for i in range(0, len(body), step):
            yield body[i : i + step]

    with TestClient(mod.app) as client:
        resp = client.post(
            "/extract-text",
            headers={"X-OCR-Token": "secret", "Content-Type": content_type},
            content=_chunks(),
        )
    assert resp.status_code == 413


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
