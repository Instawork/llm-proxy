# OCR / ID gate testdata

Synthetic PNG fixtures for validating the government-ID security gate. **No real identity documents.**

| File | Expect block | Presidio entity (typical score) |
|------|--------------|----------------------------------|
| `passport_us_block.png` | yes | `US_PASSPORT` (~0.40) |
| `driver_license_us_block.png` | yes | `US_DRIVER_LICENSE` (~0.65) |
| `receipt_clear.png` | no | — |

## Regenerate images

```bash
docker compose build ocr-sidecar
docker run --rm -v "$PWD/ocr_sidecar:/app" -w /app llm-proxy-id-gate-ocr-sidecar \
  python scripts/generate_testdata.py
```

## Validate OCR + Presidio (no Go proxy)

Requires `ocr-sidecar` and Presidio (`make test-pii-up`):

```bash
OCR_SIDECAR_URL=http://localhost:8010 \
PRESIDIO_ANALYZER_URL=http://localhost:5004 \
python3 ocr_sidecar/scripts/validate_pipeline.py
```

## Go integration tests

```bash
make test-id-gate
```

## Score threshold note

Stock Presidio `US_PASSPORT` recognizer typically scores **0.40** on a 9-digit passport number. `US_DRIVER_LICENSE` scores **~0.65** when the OCR text includes the phrase `driver license`. The ID gate therefore uses **`score_threshold: 0.4`** (block when `score >= threshold`). A threshold of **0.6 blocks driver licenses but misses passports** with the stock recognizer.

The redactor passed to the ID gate should use a low Presidio wire threshold (e.g. `0.01`) so spans below the gate threshold are still returned for middleware filtering.

## Fargate-like baseline (1 vCPU, 2 GiB)

Captured with `ocr_sidecar/scripts/run_fargate_baseline.sh` (Docker `--cpus=1 --memory=2g`, `OCR_MAX_WORKERS=1`):

| Metric | Serial | Concurrent (1 worker) |
|--------|--------|------------------------|
| p50 | 8208 ms | 8502 ms |
| p95 | 8705 ms | 9099 ms |
| mean | 8187 ms | 8446 ms |

Full JSON: `baseline_fargate_1vcpu_2gb.json`

End-to-end ID gate latency on an unconstrained local OCR container (OCR + Presidio, one image): **~500 ms p50** per integration test run.
