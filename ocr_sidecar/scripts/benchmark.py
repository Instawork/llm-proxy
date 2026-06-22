#!/usr/bin/env python3
"""Benchmark OCR sidecar latency (single-image and concurrent)."""

from __future__ import annotations

import argparse
import json
import statistics
import time
import urllib.error
import urllib.request
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path


def _post_image(url: str, image_path: Path, timeout: float) -> tuple[float, int, str]:
    boundary = "----ocrbench"
    data = image_path.read_bytes()
    body = (
        f"--{boundary}\r\n"
        f'Content-Disposition: form-data; name="image"; filename="{image_path.name}"\r\n'
        f"Content-Type: image/png\r\n\r\n"
    ).encode() + data + f"\r\n--{boundary}--\r\n".encode()
    req = urllib.request.Request(
        f"{url.rstrip('/')}/extract-text",
        data=body,
        headers={"Content-Type": f"multipart/form-data; boundary={boundary}"},
        method="POST",
    )
    start = time.perf_counter()
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            payload = resp.read().decode()
            elapsed_ms = (time.perf_counter() - start) * 1000
            return elapsed_ms, resp.status, payload
    except urllib.error.HTTPError as exc:
        elapsed_ms = (time.perf_counter() - start) * 1000
        return elapsed_ms, exc.code, exc.read().decode(errors="replace")


def _percentile(values: list[float], pct: float) -> float:
    if not values:
        return 0.0
    ordered = sorted(values)
    idx = int(round((pct / 100) * (len(ordered) - 1)))
    return ordered[idx]


def main() -> None:
    parser = argparse.ArgumentParser(description="Benchmark OCR sidecar")
    parser.add_argument("--url", default="http://localhost:8010")
    parser.add_argument(
        "--image",
        default=str(Path(__file__).resolve().parents[1] / "testdata" / "passport_us_block.png"),
    )
    parser.add_argument("--warmup", type=int, default=3)
    parser.add_argument("--iterations", type=int, default=20)
    parser.add_argument("--concurrency", type=int, default=4)
    parser.add_argument("--timeout", type=float, default=60.0)
    parser.add_argument("--output", default="")
    args = parser.parse_args()

    image_path = Path(args.image)
    if not image_path.exists():
        raise SystemExit(f"image not found: {image_path}")

    for _ in range(args.warmup):
        _post_image(args.url, image_path, args.timeout)

    serial_ms: list[float] = []
    errors = 0
    for _ in range(args.iterations):
        elapsed, status, _ = _post_image(args.url, image_path, args.timeout)
        if status == 200:
            serial_ms.append(elapsed)
        else:
            errors += 1

    concurrent_ms: list[float] = []
    with ThreadPoolExecutor(max_workers=args.concurrency) as pool:
        futures = [
            pool.submit(_post_image, args.url, image_path, args.timeout)
            for _ in range(args.iterations)
        ]
        for fut in as_completed(futures):
            elapsed, status, _ = fut.result()
            if status == 200:
                concurrent_ms.append(elapsed)
            else:
                errors += 1

    report = {
        "url": args.url,
        "image": str(image_path),
        "warmup": args.warmup,
        "iterations": args.iterations,
        "concurrency": args.concurrency,
        "errors": errors,
        "serial": {
            "count": len(serial_ms),
            "mean_ms": round(statistics.mean(serial_ms), 1) if serial_ms else 0,
            "p50_ms": round(_percentile(serial_ms, 50), 1),
            "p95_ms": round(_percentile(serial_ms, 95), 1),
            "p99_ms": round(_percentile(serial_ms, 99), 1),
            "min_ms": round(min(serial_ms), 1) if serial_ms else 0,
            "max_ms": round(max(serial_ms), 1) if serial_ms else 0,
        },
        "concurrent": {
            "count": len(concurrent_ms),
            "mean_ms": round(statistics.mean(concurrent_ms), 1) if concurrent_ms else 0,
            "p50_ms": round(_percentile(concurrent_ms, 50), 1),
            "p95_ms": round(_percentile(concurrent_ms, 95), 1),
            "p99_ms": round(_percentile(concurrent_ms, 99), 1),
            "min_ms": round(min(concurrent_ms), 1) if concurrent_ms else 0,
            "max_ms": round(max(concurrent_ms), 1) if concurrent_ms else 0,
        },
    }

    text = json.dumps(report, indent=2)
    print(text)
    if args.output:
        Path(args.output).write_text(text + "\n", encoding="utf-8")


if __name__ == "__main__":
    main()
