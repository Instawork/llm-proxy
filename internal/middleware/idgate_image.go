package middleware

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
	"sync"
)

const (
	// defaultIDGateMaxImageDim caps the longest edge of images sent to the OCR
	// sidecar. OnnxTR's fast_base detector resizes inputs to 1024x1024
	// internally, so 1600px preserves crisp text for recognition while cutting
	// upload size and PIL/numpy decode time on multi-megapixel photos.
	defaultIDGateMaxImageDim = 1600

	// maxDecodePixels bounds single-image pixel count before decode.
	maxDecodePixels = 50_000_000

	// maxSingleDecodeBytes caps estimated decoded buffer memory (source +
	// resized RGBA destination) for a single image so 16-bit PNGs (8 B/px)
	// cannot allocate hundreds of MB.
	maxSingleDecodeBytes = 64 * 1024 * 1024

	// maxAggregateDecodeBytes caps total decoded image memory held across all
	// concurrent downscales in the proxy process so bursts fit comfortably
	// inside the llm-proxy container memory limit (default 2048 MiB).
	maxAggregateDecodeBytes = 128 * 1024 * 1024

	ocrDownscaleJPEGQuality = 85
)

type decodeMemoryBudget struct {
	mu    sync.Mutex
	limit int64
	used  int64
}

func (b *decodeMemoryBudget) tryAcquire(bytes int64) bool {
	if bytes <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.used+bytes > b.limit {
		return false
	}
	b.used += bytes
	return true
}

func (b *decodeMemoryBudget) release(bytes int64) {
	if bytes <= 0 {
		return
	}
	b.mu.Lock()
	b.used -= bytes
	if b.used < 0 {
		b.used = 0
	}
	b.mu.Unlock()
}

var ocrDecodeBudget = &decodeMemoryBudget{limit: maxAggregateDecodeBytes}

// decodedBytesPerPixel estimates the decoded source buffer footprint per pixel
// for the format and ColorModel reported by image.DecodeConfig.
func decodedBytesPerPixel(format string, m color.Model) int64 {
	switch m {
	case color.RGBA64Model, color.NRGBA64Model:
		return 8
	case color.Gray16Model:
		if format == "png" {
			// png.DecodeConfig inspects IHDR before any tRNS chunk; a grayscale
			// PNG with tRNS decodes to *image.NRGBA64 (8 B/px).
			return 8
		}
		return 2
	case color.GrayModel:
		if format == "png" {
			// Grayscale PNG with tRNS decodes to *image.NRGBA (4 B/px).
			return 4
		}
		return 1
	case color.RGBAModel, color.NRGBAModel, color.YCbCrModel, color.NYCbCrAModel:
		return 4
	default:
		if _, ok := m.(color.Palette); ok {
			return 1
		}
		return 4
	}
}

// isProgressiveJPEG scans JPEG segment headers to determine whether the stream
// uses progressive DCT (SOF2 et al.). Malformed or truncated streams before a
// sequential SOF0/SOF1 marker conservatively return true.
func isProgressiveJPEG(raw []byte) bool {
	if len(raw) < 4 || raw[0] != 0xFF || raw[1] != 0xD8 {
		return true
	}
	i := 2
	for i < len(raw) {
		if raw[i] != 0xFF {
			return true
		}
		for i < len(raw) && raw[i] == 0xFF {
			i++
		}
		if i >= len(raw) {
			return true
		}
		marker := raw[i]
		i++

		switch {
		case marker == 0xC0 || marker == 0xC1:
			return false
		case marker == 0xC2 || marker == 0xC6 || marker == 0xCA || marker == 0xCE:
			return true
		case marker == 0xD8 || marker == 0x01 || (marker >= 0xD0 && marker <= 0xD7):
			continue
		case marker == 0xD9 || marker == 0xDA:
			return true
		default:
			if i+2 > len(raw) {
				return true
			}
			segLen := int(raw[i])<<8 | int(raw[i+1])
			if segLen < 2 || i+segLen > len(raw) {
				return true
			}
			i += segLen
		}
	}
	return true
}

// isInterlacedPNG checks the PNG IHDR interlace method byte (offset 28).
// Valid PNG files always place the 13-byte IHDR chunk immediately after the
// 8-byte PNG signature.
func isInterlacedPNG(raw []byte) bool {
	if len(raw) < 29 || string(raw[12:16]) != "IHDR" {
		return false
	}
	return raw[28] != 0
}

// estimatedDecodeBytes estimates peak decoded memory (source image, decoder
// working buffers such as Adam7 PNG interlace passes, CMYK JPEG conversion
// planes, and progressive JPEG int32 DCT coefficient blocks, plus resized RGBA
// output).
func estimatedDecodeBytes(raw []byte, format string, cfg image.Config, dstW, dstH int) int64 {
	pixels := int64(cfg.Width) * int64(cfg.Height)
	bpp := decodedBytesPerPixel(format, cfg.ColorModel)
	srcBytes := pixels * bpp
	if format == "png" && isInterlacedPNG(raw) {
		// Go's image/png holds the full output image and the current Adam7 pass
		// simultaneously; the final pass allocates up to half the image pixels.
		srcBytes += ((pixels + 1) / 2) * bpp
	}
	if format == "jpeg" {
		if cfg.ColorModel == color.CMYKModel {
			// Go's image/jpeg decodes four separate planes (YCbCr + black) and
			// holds them live while allocating the interleaved *image.CMYK.
			srcBytes += pixels * 4
		}
		if isProgressiveJPEG(raw) {
			// Go's image/jpeg decoder holds progCoeffs ([maxComponents][]block,
			// 256 bytes per 8x8 int32 block) alongside the decoded pixel buffer.
			nComp := int64(3)
			switch cfg.ColorModel {
			case color.GrayModel:
				nComp = 1
			case color.CMYKModel:
				nComp = 4
			}
			blocksX := ((int64(cfg.Width) + 15) / 16) * 2
			blocksY := ((int64(cfg.Height) + 15) / 16) * 2
			srcBytes += nComp * blocksX * blocksY * 256
		}
	}
	return srcBytes + int64(dstW)*int64(dstH)*4
}

// downscaleImageForOCR shrinks JPEG/PNG images whose width or height exceeds
// maxDim so the OCR sidecar does not spend upload bandwidth and decode CPU on
// multi-megapixel inputs. It only transforms the copy sent to OCR — never the
// upstream LLM request body. Small images, unsupported formats, images that
// exceed per-image or aggregate decoded-memory limits, or any error fall back
// to raw unchanged. admissionRefused is true only when the transient shared
// decode memory budget refused the image.
func downscaleImageForOCR(raw []byte, maxDim int) ([]byte, bool) {
	return downscaleImageForOCRWithBudget(raw, maxDim, ocrDecodeBudget)
}

func downscaleImageForOCRWithBudget(raw []byte, maxDim int, budget *decodeMemoryBudget) ([]byte, bool) {
	if maxDim <= 0 || len(raw) == 0 {
		return raw, false
	}

	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return raw, false
	}
	if format != "jpeg" && format != "png" {
		return raw, false
	}
	if cfg.Width <= maxDim && cfg.Height <= maxDim {
		return raw, false
	}
	pixels := int64(cfg.Width) * int64(cfg.Height)
	if pixels > maxDecodePixels {
		return raw, false
	}

	dstW, dstH := fitWithinMaxDim(cfg.Width, cfg.Height, maxDim)
	estimatedBytes := estimatedDecodeBytes(raw, format, cfg, dstW, dstH)
	if estimatedBytes > maxSingleDecodeBytes {
		return raw, false
	}
	if budget != nil && !budget.tryAcquire(estimatedBytes) {
		return raw, true
	}
	if budget != nil {
		defer budget.release(estimatedBytes)
	}

	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return raw, false
	}

	dst := resizeImageBox(src, dstW, dstH)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: ocrDownscaleJPEGQuality}); err != nil {
		return raw, false
	}
	if buf.Len() == 0 {
		return raw, false
	}
	return buf.Bytes(), false
}

func fitWithinMaxDim(w, h, maxDim int) (int, int) {
	if w >= h {
		dstW := maxDim
		dstH := max(1, (h*maxDim+w/2)/w)
		return dstW, dstH
	}
	dstH := maxDim
	dstW := max(1, (w*maxDim+h/2)/h)
	return dstW, dstH
}

// resizeImageBox downscales src to dstW x dstH by averaging premultiplied
// source pixels in each destination cell (area/box filter), avoiding aliasing
// on small text while matching Go's premultiplied image.Image.At() semantics
// consistently across PNG color types (RGBA, NRGBA, Paletted) and JPEG YCbCr.
func resizeImageBox(src image.Image, dstW, dstH int) *image.RGBA {
	switch img := src.(type) {
	case *image.RGBA:
		return resizeRGBABox(img.Pix, img.Stride, img.Rect, dstW, dstH)
	case *image.NRGBA:
		return resizeNRGBABox(img.Pix, img.Stride, img.Rect, dstW, dstH)
	case *image.YCbCr:
		return resizeYCbCrBox(img, dstW, dstH)
	default:
		return resizeGenericBox(src, dstW, dstH)
	}
}

func resizeRGBABox(pix []uint8, stride int, bounds image.Rectangle, dstW, dstH int) *image.RGBA {
	srcW := bounds.Dx()
	srcH := bounds.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))

	for dy := range dstH {
		sy0 := (dy * srcH) / dstH
		sy1 := max(sy0+1, min(srcH, ((dy+1)*srcH)/dstH))
		dstRow := dy * dst.Stride
		for dx := range dstW {
			sx0 := (dx * srcW) / dstW
			sx1 := max(sx0+1, min(srcW, ((dx+1)*srcW)/dstW))

			var rSum, gSum, bSum, aSum uint64
			count := uint64((sy1 - sy0) * (sx1 - sx0))
			for sy := sy0; sy < sy1; sy++ {
				rowOff := sy*stride + sx0*4
				for sx := sx0; sx < sx1; sx++ {
					rSum += uint64(pix[rowOff+0])
					gSum += uint64(pix[rowOff+1])
					bSum += uint64(pix[rowOff+2])
					aSum += uint64(pix[rowOff+3])
					rowOff += 4
				}
			}
			off := dstRow + dx*4
			dst.Pix[off+0] = uint8(rSum / count)
			dst.Pix[off+1] = uint8(gSum / count)
			dst.Pix[off+2] = uint8(bSum / count)
			dst.Pix[off+3] = uint8(aSum / count)
		}
	}
	return dst
}

// resizeNRGBABox premultiplies straight-alpha NRGBA pixels into 16-bit
// premultiplied channels matching color.NRGBA.RGBA() before averaging, so
// transparent pixels match indexed/Paletted PNGs on JPEG encoding.
func resizeNRGBABox(pix []uint8, stride int, bounds image.Rectangle, dstW, dstH int) *image.RGBA {
	srcW := bounds.Dx()
	srcH := bounds.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))

	for dy := range dstH {
		sy0 := (dy * srcH) / dstH
		sy1 := max(sy0+1, min(srcH, ((dy+1)*srcH)/dstH))
		dstRow := dy * dst.Stride
		for dx := range dstW {
			sx0 := (dx * srcW) / dstW
			sx1 := max(sx0+1, min(srcW, ((dx+1)*srcW)/dstW))

			var rSum, gSum, bSum, aSum uint64
			count := uint64((sy1 - sy0) * (sx1 - sx0))
			for sy := sy0; sy < sy1; sy++ {
				rowOff := sy*stride + sx0*4
				for sx := sx0; sx < sx1; sx++ {
					a := uint64(pix[rowOff+3])
					a16 := a | (a << 8)
					r := uint64(pix[rowOff+0])
					r = ((r | (r << 8)) * a) / 0xff
					g := uint64(pix[rowOff+1])
					g = ((g | (g << 8)) * a) / 0xff
					b := uint64(pix[rowOff+2])
					b = ((b | (b << 8)) * a) / 0xff
					rSum += r
					gSum += g
					bSum += b
					aSum += a16
					rowOff += 4
				}
			}
			off := dstRow + dx*4
			dst.Pix[off+0] = uint8((rSum / count) >> 8)
			dst.Pix[off+1] = uint8((gSum / count) >> 8)
			dst.Pix[off+2] = uint8((bSum / count) >> 8)
			dst.Pix[off+3] = uint8((aSum / count) >> 8)
		}
	}
	return dst
}

func resizeYCbCrBox(src *image.YCbCr, dstW, dstH int) *image.RGBA {
	bounds := src.Rect
	srcW := bounds.Dx()
	srcH := bounds.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))

	for dy := range dstH {
		sy0 := bounds.Min.Y + (dy*srcH)/dstH
		sy1 := max(sy0+1, min(bounds.Max.Y, bounds.Min.Y+((dy+1)*srcH)/dstH))
		dstRow := dy * dst.Stride
		for dx := range dstW {
			sx0 := bounds.Min.X + (dx*srcW)/dstW
			sx1 := max(sx0+1, min(bounds.Max.X, bounds.Min.X+((dx+1)*srcW)/dstW))

			var rSum, gSum, bSum uint64
			count := uint64((sy1 - sy0) * (sx1 - sx0))
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					yi := src.YOffset(sx, sy)
					ci := src.COffset(sx, sy)
					r, g, b := color.YCbCrToRGB(src.Y[yi], src.Cb[ci], src.Cr[ci])
					rSum += uint64(r)
					gSum += uint64(g)
					bSum += uint64(b)
				}
			}
			off := dstRow + dx*4
			dst.Pix[off+0] = uint8(rSum / count)
			dst.Pix[off+1] = uint8(gSum / count)
			dst.Pix[off+2] = uint8(bSum / count)
			dst.Pix[off+3] = 255
		}
	}
	return dst
}

func resizeGenericBox(src image.Image, dstW, dstH int) *image.RGBA {
	bounds := src.Bounds()
	srcW := bounds.Dx()
	srcH := bounds.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))

	for dy := range dstH {
		sy0 := bounds.Min.Y + (dy*srcH)/dstH
		sy1 := max(sy0+1, min(bounds.Max.Y, bounds.Min.Y+((dy+1)*srcH)/dstH))
		for dx := range dstW {
			sx0 := bounds.Min.X + (dx*srcW)/dstW
			sx1 := max(sx0+1, min(bounds.Max.X, bounds.Min.X+((dx+1)*srcW)/dstW))

			var rSum, gSum, bSum, aSum uint64
			count := uint64((sy1 - sy0) * (sx1 - sx0))
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					r, g, b, a := src.At(sx, sy).RGBA()
					rSum += uint64(r)
					gSum += uint64(g)
					bSum += uint64(b)
					aSum += uint64(a)
				}
			}

			off := dst.PixOffset(dx, dy)
			dst.Pix[off+0] = uint8((rSum / count) >> 8)
			dst.Pix[off+1] = uint8((gSum / count) >> 8)
			dst.Pix[off+2] = uint8((bSum / count) >> 8)
			dst.Pix[off+3] = uint8((aSum / count) >> 8)
		}
	}
	return dst
}
