package media

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	_ "image/png" // allow PNG sources to be decoded too
	"os"
)

// EncodeImageJPEG loads srcPath, downscales it to maxDim on its longest
// side, and re-encodes at a low quality to fit inside the media budget
// alongside vitals traffic. Uses only the Go stdlib JPEG encoder plus a
// minimal resize helper — no external image library needed.
func EncodeImageJPEG(srcPath string, maxDim int, quality int) ([]byte, error) {
	f, err := os.Open(srcPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	src, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("media: decode image: %w", err)
	}

	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	scale := 1.0
	if w > h && w > maxDim {
		scale = float64(maxDim) / float64(w)
	} else if h >= w && h > maxDim {
		scale = float64(maxDim) / float64(h)
	}

	dstW, dstH := w, h
	if scale != 1.0 {
		dstW = int(float64(w) * scale)
		dstH = int(float64(h) * scale)
	}

	dst := resizeNearest(src, dstW, dstH)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality}); err != nil {
		return nil, fmt.Errorf("media: encode jpeg: %w", err)
	}
	return buf.Bytes(), nil
}

// resizeNearest scales src to dstW x dstH using nearest-neighbor sampling.
// Good enough for a low-bandwidth field thumbnail; avoids pulling in an
// external resize library for a single call site.
func resizeNearest(src image.Image, dstW, dstH int) *image.RGBA {
	bounds := src.Bounds()
	srcW, srcH := bounds.Dx(), bounds.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	for y := 0; y < dstH; y++ {
		sy := bounds.Min.Y + y*srcH/dstH
		for x := 0; x < dstW; x++ {
			sx := bounds.Min.X + x*srcW/dstW
			dst.Set(x, y, color.RGBAModel.Convert(src.At(sx, sy)))
		}
	}
	return dst
}
