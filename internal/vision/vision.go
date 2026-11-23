// Package vision preprocesses screenshots before sending them to vision LLMs,
// decoding common image formats, enforcing a pixel-bomb guard, proportionally
// downscaling large images to a bounded long edge, compositing alpha over white,
// and encoding as JPEG.
package vision

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"

	_ "image/gif"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

const (
	// DefaultMaxScreenshotEdge caps the longest image edge before vision LLM upload.
	DefaultMaxScreenshotEdge = 1600
	// DefaultJPEGQuality balances screenshot text legibility and payload size.
	DefaultJPEGQuality = 88
	// MaxDecodePixels prevents OOM from header-spoofed decompression bombs (~256 MiB RGBA).
	MaxDecodePixels = 64 * 1024 * 1024
)

// PrepareVisionImage decodes raw image bytes, proportionally downscales images
// whose long edge exceeds maxEdge (never upscaling), composites transparent
// regions over opaque white, and encodes the result as JPEG. Already-small
// valid JPEGs are returned verbatim without re-encoding.
func PrepareVisionImage(raw []byte, maxEdge int) (data []byte, mime string, err error) {
	if len(raw) == 0 {
		return nil, "", errors.New("empty image data")
	}
	if maxEdge <= 0 {
		maxEdge = DefaultMaxScreenshotEdge
	}

	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, "", fmt.Errorf("decode image config: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, "", fmt.Errorf("invalid image dimensions: %dx%d", cfg.Width, cfg.Height)
	}
	if int64(cfg.Width) > MaxDecodePixels/int64(cfg.Height) {
		return nil, "", fmt.Errorf("image dimensions %dx%d exceed %d pixel safety limit", cfg.Width, cfg.Height, MaxDecodePixels)
	}

	src, decFormat, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, "", fmt.Errorf("decode image (%s): %w", format, err)
	}

	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, "", fmt.Errorf("invalid decoded image bounds: %v", b)
	}

	if decFormat == "jpeg" && w <= maxEdge && h <= maxEdge {
		return bytes.Clone(raw), "image/jpeg", nil
	}

	newW, newH := w, h
	if w > maxEdge || h > maxEdge {
		if w >= h {
			newW = maxEdge
			newH = max(1, int(math.Round(float64(h)*float64(maxEdge)/float64(w))))
		} else {
			newH = maxEdge
			newW = max(1, int(math.Round(float64(w)*float64(maxEdge)/float64(h))))
		}
	}

	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	if newW == w && newH == h {
		draw.Draw(dst, dst.Bounds(), src, b.Min, draw.Over)
	} else {
		draw.BiLinear.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: DefaultJPEGQuality}); err != nil {
		return nil, "", fmt.Errorf("encode jpeg: %w", err)
	}
	return buf.Bytes(), "image/jpeg", nil
}
