package middleware

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func makeSyntheticPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8((x*13 + y*7) % 256),
				G: uint8((x*5 + y*17) % 256),
				B: uint8((x*11 + y*3) % 256),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func makeSyntheticJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.SetRGBA(x, y, color.RGBA{
				R: uint8((x*19 + y*3) % 256),
				G: uint8((x*7 + y*23) % 256),
				B: uint8((x*13 + y*11) % 256),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encode jpeg: %v", err)
	}
	return buf.Bytes()
}

func TestDownscaleImageForOCR_LargePNGDownscales(t *testing.T) {
	raw := makeSyntheticPNG(t, 600, 400)
	out := downscaleImageForOCR(raw, 400)

	cfg, format, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode downscaled config: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("format = %q, want jpeg", format)
	}
	if cfg.Width != 400 || cfg.Height != 267 {
		t.Fatalf("dims = %dx%d, want 400x267", cfg.Width, cfg.Height)
	}
}

func TestDownscaleImageForOCR_PortraitPreservesAspectRatio(t *testing.T) {
	raw := makeSyntheticJPEG(t, 300, 600)
	out := downscaleImageForOCR(raw, 400)

	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatalf("decode downscaled config: %v", err)
	}
	if cfg.Width != 200 || cfg.Height != 400 {
		t.Fatalf("dims = %dx%d, want 200x400", cfg.Width, cfg.Height)
	}
}

func TestDownscaleImageForOCR_SmallImageUnchanged(t *testing.T) {
	raw := makeSyntheticPNG(t, 200, 150)
	out := downscaleImageForOCR(raw, 400)
	if !bytes.Equal(out, raw) {
		t.Fatal("expected small image to pass through unchanged")
	}
}

func TestDownscaleImageForOCR_NonImageBytesUnchanged(t *testing.T) {
	raw := []byte("not-a-real-image")
	out := downscaleImageForOCR(raw, 1600)
	if !bytes.Equal(out, raw) {
		t.Fatal("expected non-image bytes to pass through unchanged")
	}
}

func TestDownscaleImageForOCR_DisabledWhenMaxDimNonPositive(t *testing.T) {
	raw := makeSyntheticPNG(t, 400, 200)
	out := downscaleImageForOCR(raw, -1)
	if !bytes.Equal(out, raw) {
		t.Fatal("expected disabled downscale to return original bytes")
	}
}

func TestDownscaleImageForOCR_NRGBAAndPalettedTransparencyMatch(t *testing.T) {
	const w, h = 400, 200
	nrgba := image.NewNRGBA(image.Rect(0, 0, w, h))
	palette := color.Palette{
		color.NRGBA{R: 255, G: 0, B: 0, A: 0},      // transparent with hidden red RGB
		color.NRGBA{R: 30, G: 120, B: 210, A: 255}, // opaque blue
	}
	paletted := image.NewPaletted(image.Rect(0, 0, w, h), palette)

	for y := range h {
		for x := range w {
			if x < w/2 {
				nrgba.SetNRGBA(x, y, color.NRGBA{R: 255, G: 0, B: 0, A: 0})
				paletted.SetColorIndex(x, y, 0)
			} else {
				nrgba.SetNRGBA(x, y, color.NRGBA{R: 30, G: 120, B: 210, A: 255})
				paletted.SetColorIndex(x, y, 1)
			}
		}
	}

	var nrgbaBuf, palettedBuf bytes.Buffer
	if err := png.Encode(&nrgbaBuf, nrgba); err != nil {
		t.Fatalf("encode nrgba png: %v", err)
	}
	if err := png.Encode(&palettedBuf, paletted); err != nil {
		t.Fatalf("encode paletted png: %v", err)
	}

	outNRGBA := downscaleImageForOCR(nrgbaBuf.Bytes(), 200)
	outPaletted := downscaleImageForOCR(palettedBuf.Bytes(), 200)
	if !bytes.Equal(outNRGBA, outPaletted) {
		t.Fatalf("expected equivalent NRGBA and Paletted PNGs to produce identical OCR JPEG (lens %d vs %d)", len(outNRGBA), len(outPaletted))
	}

	decoded, _, err := image.Decode(bytes.NewReader(outNRGBA))
	if err != nil {
		t.Fatalf("decode output jpeg: %v", err)
	}
	// Transparent left half must decode dark (not bright hidden red), and opaque
	// right half must retain its blue color.
	lr, _, _, _ := decoded.At(20, 50).RGBA()
	if lr>>8 > 16 {
		t.Fatalf("transparent pixel leaked hidden red channel: R=%d", lr>>8)
	}
	rr, rg, rb, _ := decoded.At(180, 50).RGBA()
	if rb>>8 < 180 || rg>>8 < 90 || rr>>8 > 60 {
		t.Fatalf("opaque blue pixel distorted: RGB=(%d,%d,%d)", rr>>8, rg>>8, rb>>8)
	}
}

func TestDownscaleImageForOCR_RespectsAggregateMemoryBudget(t *testing.T) {
	if got := decodedBytesPerPixel(color.NRGBA64Model); got != 8 {
		t.Fatalf("NRGBA64 decodedBytesPerPixel = %d, want 8", got)
	}

	raw := makeSyntheticPNG(t, 400, 200)
	// Budget too small to cover 400*200*4 + 200*100*4 = 400,000 bytes.
	tinyBudget := &decodeMemoryBudget{limit: 100_000}
	out := downscaleImageForOCRWithBudget(raw, 200, tinyBudget)
	if !bytes.Equal(out, raw) {
		t.Fatal("expected fallback to raw when aggregate decode budget is exceeded")
	}

	// Budget sufficient -> downscales and releases memory cleanly.
	okBudget := &decodeMemoryBudget{limit: 1_000_000}
	out = downscaleImageForOCRWithBudget(raw, 200, okBudget)
	if bytes.Equal(out, raw) {
		t.Fatal("expected downscale to succeed within decode budget")
	}
	if okBudget.used != 0 {
		t.Fatalf("decode budget leak: used = %d, want 0", okBudget.used)
	}
}
