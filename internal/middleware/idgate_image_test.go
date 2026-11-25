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
	out, refused := downscaleImageForOCR(raw, 400)
	if refused {
		t.Fatal("unexpected decode admission refusal")
	}

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
	out, refused := downscaleImageForOCR(raw, 400)
	if refused {
		t.Fatal("unexpected decode admission refusal")
	}

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
	out, refused := downscaleImageForOCR(raw, 400)
	if refused {
		t.Fatal("unexpected decode admission refusal")
	}
	if !bytes.Equal(out, raw) {
		t.Fatal("expected small image to pass through unchanged")
	}
}

func TestDownscaleImageForOCR_NonImageBytesUnchanged(t *testing.T) {
	raw := []byte("not-a-real-image")
	out, refused := downscaleImageForOCR(raw, 1600)
	if refused {
		t.Fatal("unexpected decode admission refusal")
	}
	if !bytes.Equal(out, raw) {
		t.Fatal("expected non-image bytes to pass through unchanged")
	}
}

func TestDownscaleImageForOCR_DisabledWhenMaxDimNonPositive(t *testing.T) {
	raw := makeSyntheticPNG(t, 400, 200)
	out, refused := downscaleImageForOCR(raw, -1)
	if refused {
		t.Fatal("unexpected decode admission refusal")
	}
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

	outNRGBA, refused1 := downscaleImageForOCR(nrgbaBuf.Bytes(), 200)
	outPaletted, refused2 := downscaleImageForOCR(palettedBuf.Bytes(), 200)
	if refused1 || refused2 {
		t.Fatal("unexpected decode admission refusal")
	}
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

func TestDownscaleImageForOCR_DecodedMemoryEstimates(t *testing.T) {
	if got := decodedBytesPerPixel("png", color.NRGBA64Model); got != 8 {
		t.Fatalf("PNG NRGBA64 decodedBytesPerPixel = %d, want 8", got)
	}
	if got := decodedBytesPerPixel("png", color.Gray16Model); got != 8 {
		t.Fatalf("PNG Gray16 decodedBytesPerPixel = %d, want 8 (accounts for tRNS -> NRGBA64)", got)
	}
	if got := decodedBytesPerPixel("png", color.GrayModel); got != 4 {
		t.Fatalf("PNG Gray decodedBytesPerPixel = %d, want 4 (accounts for tRNS -> NRGBA)", got)
	}
	if got := decodedBytesPerPixel("jpeg", color.GrayModel); got != 1 {
		t.Fatalf("JPEG Gray decodedBytesPerPixel = %d, want 1", got)
	}

	baseline := makeSyntheticJPEG(t, 400, 200)
	if isProgressiveJPEG(baseline) {
		t.Fatal("expected stdlib baseline JPEG to be detected as non-progressive")
	}

	// Minimal JPEG header containing SOI + SOF2 (0xFFC2, progressive DCT).
	progressiveHeader := []byte{
		0xFF, 0xD8, // SOI
		0xFF, 0xE0, 0x00, 0x04, 0x00, 0x00, // APP0 length 4
		0xFF, 0xC2, 0x00, 0x0B, 0x08, 0x0E, 0x10, 0x0E, 0x10, 0x03, 0x01, 0x11, 0x00, // SOF2
	}
	if !isProgressiveJPEG(progressiveHeader) {
		t.Fatal("expected SOF2 header to be detected as progressive JPEG")
	}

	// 3600x3600 baseline YCbCr fits within 64 MiB single-image limit, while
	// 3600x3600 progressive YCbCr exceeds it due to ~156 MB coefficient blocks.
	largeCfg := image.Config{Width: 3600, Height: 3600, ColorModel: color.YCbCrModel}
	baseEst := estimatedDecodeBytes(baseline, "jpeg", largeCfg, 1600, 1600)
	progEst := estimatedDecodeBytes(progressiveHeader, "jpeg", largeCfg, 1600, 1600)
	if baseEst > maxSingleDecodeBytes {
		t.Fatalf("3600x3600 baseline estimate = %d, want <= %d", baseEst, maxSingleDecodeBytes)
	}
	if progEst <= maxSingleDecodeBytes {
		t.Fatalf("3600x3600 progressive estimate = %d, want > %d", progEst, maxSingleDecodeBytes)
	}

	// Smaller 1800x1200 progressive JPEG still fits within 64 MiB.
	medCfg := image.Config{Width: 1800, Height: 1200, ColorModel: color.YCbCrModel}
	medProgEst := estimatedDecodeBytes(progressiveHeader, "jpeg", medCfg, 1600, 1067)
	if medProgEst > maxSingleDecodeBytes {
		t.Fatalf("1800x1200 progressive estimate = %d, want <= %d", medProgEst, maxSingleDecodeBytes)
	}
}

func TestDownscaleImageForOCR_RespectsAggregateMemoryBudget(t *testing.T) {
	raw := makeSyntheticPNG(t, 400, 200)
	// Required estimate for 400x200 RGBA -> 200x100 RGBA is 400,000 bytes.
	// Budget too small even when empty -> refuses admission without mutating used.
	tinyBudget := &decodeMemoryBudget{limit: 100_000}
	out, refused := downscaleImageForOCRWithBudget(raw, 200, tinyBudget)
	if !refused || !bytes.Equal(out, raw) {
		t.Fatal("expected admission refusal and raw fallback when aggregate decode budget is exceeded")
	}
	if tinyBudget.used != 0 {
		t.Fatalf("tinyBudget.used = %d, want 0", tinyBudget.used)
	}

	// Hold an existing reservation so the resize fits within limit alone
	// (400,000 <= 1,000,000) but exceeds limit alongside the held reservation
	// (700,000 + 400,000 > 1,000,000).
	budget := &decodeMemoryBudget{limit: 1_000_000}
	const held int64 = 700_000
	if !budget.tryAcquire(held) {
		t.Fatal("expected initial reservation to succeed")
	}
	out, refused = downscaleImageForOCRWithBudget(raw, 200, budget)
	if !refused || !bytes.Equal(out, raw) {
		t.Fatal("expected admission refusal while another reservation occupies the budget")
	}
	if budget.used != held {
		t.Fatalf("budget.used = %d, want %d (must not mutate held reservation)", budget.used, held)
	}

	// Releasing the reservation restores admission and releases cleanly.
	budget.release(held)
	out, refused = downscaleImageForOCRWithBudget(raw, 200, budget)
	if refused || bytes.Equal(out, raw) {
		t.Fatal("expected downscale to succeed once held reservation is released")
	}
	if budget.used != 0 {
		t.Fatalf("decode budget leak: used = %d, want 0", budget.used)
	}
}
