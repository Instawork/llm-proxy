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
