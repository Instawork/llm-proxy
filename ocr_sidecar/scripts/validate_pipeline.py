#!/usr/bin/env python3
"""Validate OCR + Presidio pipeline against committed testdata."""

from __future__ import annotations

import json
import os
import sys
import urllib.error
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
TESTDATA = ROOT / "testdata"
MANIFEST = TESTDATA / "manifest.json"
GOV_ENTITIES = ["US_PASSPORT", "US_DRIVER_LICENSE"]
SCORE_THRESHOLD = float(os.getenv("ID_GATE_SCORE_THRESHOLD", "0.4"))


def _post_multipart(url: str, field: str, filename: str, data: bytes) -> dict:
    boundary = "----validate"
    body = (
        f"--{boundary}\r\n"
        f'Content-Disposition: form-data; name="{field}"; filename="{filename}"\r\n'
        f"Content-Type: image/png\r\n\r\n"
    ).encode() + data + f"\r\n--{boundary}--\r\n".encode()
    req = urllib.request.Request(
        url,
        data=body,
        headers={"Content-Type": f"multipart/form-data; boundary={boundary}"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=120) as resp:
        return json.loads(resp.read().decode())


def _analyze(presidio_url: str, text: str) -> list[dict]:
    payload = json.dumps(
        {
            "text": text,
            "language": "en",
            "entities": GOV_ENTITIES,
            "score_threshold": 0.0,
        }
    ).encode()
    req = urllib.request.Request(
        f"{presidio_url.rstrip('/')}/analyze",
        data=payload,
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read().decode())


def _blocked(spans: list[dict]) -> tuple[bool, list[str]]:
    hits = []
    for span in spans:
        if span.get("entity_type") not in GOV_ENTITIES:
            continue
        if float(span.get("score", 0)) >= SCORE_THRESHOLD:
            hits.append(f"{span['entity_type']}:{span['score']:.2f}")
    return bool(hits), hits


def main() -> int:
    ocr_url = os.getenv("OCR_SIDECAR_URL", "http://localhost:8010")
    presidio_url = os.getenv("PRESIDIO_ANALYZER_URL", "http://localhost:5004")

    manifest = json.loads(MANIFEST.read_text(encoding="utf-8"))
    failures = 0

    for case in manifest["cases"]:
        img_path = TESTDATA / case["file"]
        ocr = _post_multipart(
            f"{ocr_url.rstrip('/')}/extract-text",
            "image",
            case["file"],
            img_path.read_bytes(),
        )
        text = ocr.get("text", "")
        spans = _analyze(presidio_url, text)
        blocked, hits = _blocked(spans)
        expect = case["expect_block"]
        ok = blocked == expect
        status = "PASS" if ok else "FAIL"
        print(f"[{status}] {case['file']} blocked={blocked} hits={hits}")
        print(f"       ocr_text={text[:120]!r}")
        if not ok:
            failures += 1

    return 1 if failures else 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except urllib.error.URLError as exc:
        print(f"connection error: {exc}", file=sys.stderr)
        raise SystemExit(2) from exc
