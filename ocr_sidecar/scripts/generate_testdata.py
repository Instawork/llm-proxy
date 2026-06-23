#!/usr/bin/env python3
"""Generate synthetic OCR test images (no real PII)."""

from __future__ import annotations

import json
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont

ROOT = Path(__file__).resolve().parents[1]
OUT = ROOT / "testdata"


def _font(size: int) -> ImageFont.FreeTypeFont | ImageFont.ImageFont:
    for path in (
        "/System/Library/Fonts/Supplemental/Arial.ttf",
        "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
        "/usr/share/fonts/truetype/liberation/LiberationSans-Regular.ttf",
    ):
        if Path(path).exists():
            return ImageFont.truetype(path, size)
    return ImageFont.load_default()


def _render_card(title: str, lines: list[str], filename: str, *, font_size: int = 40) -> dict:
    w, h = 900, 640
    img = Image.new("RGB", (w, h), "white")
    draw = ImageDraw.Draw(img)
    draw.rectangle((20, 20, w - 20, h - 20), outline="#000000", width=4)
    title_font = _font(font_size + 8)
    body_font = _font(font_size)
    draw.text((50, 40), title, fill="#000000", font=title_font)
    y = 130
    for line in lines:
        draw.text((50, y), line, fill="#000000", font=body_font)
        y += font_size + 24
    path = OUT / filename
    img.save(path, format="PNG")
    return {"file": filename, "path": str(path.relative_to(ROOT))}


def main() -> None:
    OUT.mkdir(parents=True, exist_ok=True)

    cases = [
        {
            **_render_card(
                "UNITED STATES PASSPORT",
                [
                    "passport number 123456789",
                    "Name: SYNTHETIC TEST",
                    "Nationality: USA",
                ],
                "passport_us_block.png",
            ),
            "description": "Synthetic US passport card; OCR should yield a 9-digit passport number.",
            "expect_block": True,
            "expected_entities": ["US_PASSPORT"],
        },
        {
            **_render_card(
                "CALIFORNIA DRIVER LICENSE",
                [
                    "driver license D1234567",
                    "Name: SYNTHETIC TEST",
                    "Class: C",
                ],
                "driver_license_us_block.png",
            ),
            "description": "Synthetic California-style driver license text.",
            "expect_block": True,
            "expected_entities": ["US_DRIVER_LICENSE"],
        },
        {
            **_render_card(
                "COFFEE SHOP RECEIPT",
                [
                    "Order #4821",
                    "Latte ................ $4.50",
                    "Tax .................. $0.36",
                    "Total ................ $4.86",
                    "Thank you!",
                ],
                "receipt_clear.png",
            ),
            "description": "Non-ID document; should pass the ID gate.",
            "expect_block": False,
            "expected_entities": [],
        },
    ]

    manifest = {
        "version": 1,
        "note": "Synthetic images only. Regenerate with: python scripts/generate_testdata.py",
        "cases": cases,
    }
    manifest_path = OUT / "manifest.json"
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    print(f"Wrote {len(cases)} images and {manifest_path}")


if __name__ == "__main__":
    main()
