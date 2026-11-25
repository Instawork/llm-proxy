package middleware

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png"
)

const (
	// defaultIDGateMaxImageDim caps the longest edge of images sent to the OCR
	// sidecar. OnnxTR's fast_base detector resizes inputs to 1024x1024
	// internally, so 1600px preserves crisp text for recognition while cutting
	// upload size and PIL/numpy decode time on multi-megapixel photos.
	defaultIDGateMaxImageDim = 1600

	// maxDecodePixels bounds memory spent decoding an oversized image before
	// downscaling so a decompression bomb cannot exhaust proxy RAM.
	maxDecodePixels = 50_000_000

	ocrDownscaleJPEGQuality = 85
)

// downscaleImageForOCR shrinks JPEG/PNG images whose width or height exceeds
// maxDim so the OCR sidecar does not spend upload bandwidth and decode CPU on
// multi-megapixel inputs. It only transforms the copy sent to OCR — never the
// upstream LLM request body. Small images, unsupported formats, or any error
// fall back to raw unchanged.
func downscaleImageForOCR(raw []byte, maxDim int) []byte {
	if maxDim <= 0 || len(raw) == 0 {
		return raw
	}

	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return raw
	}
	if format != "jpeg" && format != "png" {
		return raw
	}
	if cfg.Width <= maxDim && cfg.Height <= maxDim {
		return raw
	}
	if int64(cfg.Width)*int64(cfg.Height) > maxDecodePixels {
		return raw
	}

	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return raw
	}

	dstW, dstH := fitWithinMaxDim(cfg.Width, cfg.Height, maxDim)
	dst := resizeImageBox(src, dstW, dstH)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: ocrDownscaleJPEGQuality}); err != nil {
		return raw
	}
	if buf.Len() == 0 {
		return raw
	}
	return buf.Bytes()
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

// resizeImageBox downscales src to dstW x dstH by averaging source pixels in
// each destination cell (area/box filter), avoiding aliasing on small text.
// Concrete fast paths avoid per-pixel interface dispatch for JPEG/PNG decodes.
func resizeImageBox(src image.Image, dstW, dstH int) *image.RGBA {
	switch img := src.(type) {
	case *image.RGBA:
		return resizeRGBABox(img.Pix, img.Stride, img.Rect, dstW, dstH)
	case *image.NRGBA:
		return resizeRGBABox(img.Pix, img.Stride, img.Rect, dstW, dstH)
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
