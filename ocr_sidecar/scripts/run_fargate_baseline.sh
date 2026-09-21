#!/usr/bin/env bash
# Mimic a Fargate-style OCR sidecar: 1 vCPU, 2 GiB RAM, single worker.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
REPO_ROOT="$(cd "$ROOT/.." && pwd)"
PORT="${OCR_SIDECAR_PORT:-8020}"
IMAGE="${OCR_BENCH_IMAGE:-llm-proxy-id-gate-ocr-sidecar:latest}"
RESULTS="$ROOT/testdata/baseline_fargate_1vcpu_2gb.json"

cd "$REPO_ROOT"

echo "Building OCR sidecar image..."
docker compose build ocr-sidecar >/dev/null

echo "Starting Fargate-like container (cpus=1, memory=2g) on port $PORT..."
docker rm -f ocr-fargate-bench >/dev/null 2>&1 || true
docker run -d --name ocr-fargate-bench \
  --cpus=1 \
  --memory=2g \
  -e OCR_MAX_WORKERS=1 \
  -e OCR_MAX_CONCURRENCY=1 \
  -e OMP_NUM_THREADS=1 \
  -p "${PORT}:8000" \
  "$IMAGE"

cleanup() { docker rm -f ocr-fargate-bench >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "Waiting for health..."
for i in $(seq 1 60); do
  if curl -sf "http://localhost:${PORT}/health" >/dev/null; then
    break
  fi
  sleep 2
  if [ "$i" -eq 60 ]; then
    echo "OCR sidecar did not become healthy" >&2
    docker logs ocr-fargate-bench 2>&1 | tail -20
    exit 1
  fi
done

echo "Running benchmark..."
python3 "$ROOT/scripts/benchmark.py" \
  --url "http://localhost:${PORT}" \
  --image "$ROOT/testdata/passport_us_block.png" \
  --warmup 3 \
  --iterations 15 \
  --concurrency 1 \
  --output "$RESULTS"

echo "Baseline written to $RESULTS"
