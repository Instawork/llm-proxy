# OnnxTR OCR Sidecar

CPU-only FastAPI service that extracts text from uploaded images for the llm-proxy ID security gate.

## Run locally

```bash
docker compose up ocr-sidecar
```

## Test

```bash
curl -s -F "image=@/path/to/id.jpg" http://localhost:8000/extract-text
curl -s http://localhost:8000/health
```

## Tuning

| Env var | Default | Purpose |
|---------|---------|---------|
| `OCR_MAX_WORKERS` | CPU count | Thread pool size for inference |
| `OCR_MAX_CONCURRENCY` | same as workers | Max in-flight OCR requests (semaphore) |
| `OCR_QUEUE_TIMEOUT_SEC` | 5 | Max wait to acquire semaphore |
| `OCR_INFER_TIMEOUT_SEC` | 30 | Max per-image inference time |
| `OMP_NUM_THREADS` | 1 | ONNX Runtime intra-op threads |

For high scale, run uvicorn with multiple workers (each loads its own model copy) or move inference to Triton/Ray Serve.
