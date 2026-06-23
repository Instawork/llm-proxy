# OnnxTR OCR Sidecar

CPU-only FastAPI service that extracts text from uploaded images for the llm-proxy government ID security gate.

## Quick start (Docker)

From the repo root:

```bash
docker compose build ocr-sidecar
OCR_SIDECAR_PORT=8010 docker compose up -d ocr-sidecar   # use 8010 if :8000 is taken
curl -s http://localhost:8010/health
curl -s -F "image=@/path/to/scan.jpg" http://localhost:8010/extract-text
```

First startup downloads OnnxTR model weights (~100 MB) and can take 1–2 minutes before `/health` returns 200.

## Local development (Python venv)

Use a venv when iterating on the sidecar without rebuilding Docker on every change.

```bash
cd ocr_sidecar

# Create and activate a virtual environment (Python 3.11+ recommended)
python3 -m venv .venv
source .venv/bin/activate          # macOS/Linux
# .venv\Scripts\activate           # Windows

# Install dependencies
pip install --upgrade pip
pip install -r requirements.txt

# Run the server (downloads model weights on first request)
export OMP_NUM_THREADS=1 MKL_NUM_THREADS=1
uvicorn main:app --host 0.0.0.0 --port 8000 --reload
```

Smoke test (in another terminal, with the venv active or not):

```bash
curl -s http://localhost:8000/health
curl -s -F "image=@/path/to/scan.jpg" http://localhost:8000/extract-text
```

Generate a quick test image with text (requires Pillow, installed via onnxtr):

```bash
python3 - <<'PY'
from PIL import Image, ImageDraw, ImageFont
img = Image.new("RGB", (400, 120), "white")
draw = ImageDraw.Draw(img)
draw.text((20, 40), "PASSPORT 123456789", fill="black")
img.save("/tmp/ocr-test.png")
PY
curl -s -F "image=@/tmp/ocr-test.png" http://localhost:8000/extract-text
```

Deactivate the venv when done: `deactivate`. The `.venv/` directory is gitignored.

## Testdata and validation

Committed synthetic fixtures live in `testdata/` (see `testdata/README.md`).

```bash
# From repo root — OCR + Presidio against PNG fixtures
make validate-id-gate-pipeline

# Full Go middleware integration (OCR + Presidio + IDGateMiddleware)
make test-id-gate

# Fargate-like OCR benchmark (Docker --cpus=1 --memory=2g)
make benchmark-id-gate-fargate
```

Regenerate PNGs after editing `scripts/generate_testdata.py`:

```bash
docker run --rm -v "$PWD/ocr_sidecar:/app" -w /app llm-proxy-id-gate-ocr-sidecar \
  python scripts/generate_testdata.py
```

## Go proxy + full gate

The ID gate middleware lives in the main llm-proxy binary. Unit tests:

```bash
# from repo root
go test ./internal/ocr/... ./internal/middleware/... -count=1
go build ./cmd/llm-proxy/...
```

End-to-end (OCR + Presidio + proxy) requires Presidio's large sidecar image:

```bash
docker compose --profile pii_redact up -d ocr-sidecar presidio llm-proxy
# Send a multimodal chat request with a base64 image_url to the proxy on :9002
```

With `features.id_gate.enabled: true` in `configs/dev.yml`, the proxy OCRs embedded images and returns **403** when Presidio detects `US_PASSPORT` or `US_DRIVER_LICENSE` at or above the score threshold (default **0.4** — see `testdata/README.md`).

## Tuning

| Env var | Default | Purpose |
|---------|---------|---------|
| `OCR_MAX_WORKERS` | CPU count | Thread pool size for inference |
| `OCR_MAX_CONCURRENCY` | same as workers | Max in-flight OCR requests (semaphore) |
| `OCR_QUEUE_TIMEOUT_SEC` | 5 | Max wait to acquire semaphore |
| `OCR_INFER_TIMEOUT_SEC` | 30 | Max per-image inference time |
| `OMP_NUM_THREADS` | 1 | ONNX Runtime intra-op threads |

For high scale, run uvicorn with multiple workers (each loads its own model copy) or move inference to Triton/Ray Serve.
