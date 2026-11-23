package vision

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
)

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png.Encode: %v", err)
	}
	return buf.Bytes()
}

func decodeJPEGConfig(t *testing.T, data []byte) image.Config {
	t.Helper()
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if format != "jpeg" {
		t.Fatalf("decoded format = %q, want jpeg", format)
	}
	return cfg
}

func TestPrepareVisionImageDownscaleAndNoUpscale(t *testing.T) {
	// Landscape 3200x2000 -> 1600x1000
	landscape := image.NewRGBA(image.Rect(0, 0, 3200, 2000))
	data, mime, err := PrepareVisionImage(encodePNG(t, landscape), 1600)
	if err != nil {
		t.Fatalf("landscape error: %v", err)
	}
	if mime != "image/jpeg" {
		t.Fatalf("mime = %q, want image/jpeg", mime)
	}
	cfg := decodeJPEGConfig(t, data)
	if cfg.Width != 1600 || cfg.Height != 1000 {
		t.Fatalf("landscape got %dx%d, want 1600x1000", cfg.Width, cfg.Height)
	}

	// Portrait 1200x3000 -> 640x1600
	portrait := image.NewRGBA(image.Rect(0, 0, 1200, 3000))
	data, mime, err = PrepareVisionImage(encodePNG(t, portrait), 1600)
	if err != nil {
		t.Fatalf("portrait error: %v", err)
	}
	if mime != "image/jpeg" {
		t.Fatalf("mime = %q, want image/jpeg", mime)
	}
	cfg = decodeJPEGConfig(t, data)
	if cfg.Width != 640 || cfg.Height != 1600 {
		t.Fatalf("portrait got %dx%d, want 640x1600", cfg.Width, cfg.Height)
	}

	// Small 800x600 with maxEdge=0 -> never upscale, default maxEdge
	small := image.NewRGBA(image.Rect(0, 0, 800, 600))
	data, mime, err = PrepareVisionImage(encodePNG(t, small), 0)
	if err != nil {
		t.Fatalf("small error: %v", err)
	}
	cfg = decodeJPEGConfig(t, data)
	if cfg.Width != 800 || cfg.Height != 600 {
		t.Fatalf("small got %dx%d, want 800x600", cfg.Width, cfg.Height)
	}

	// 2000x1000 with maxEdge=-1 -> default 1600 -> 1600x800
	med := image.NewRGBA(image.Rect(0, 0, 2000, 1000))
	data, _, err = PrepareVisionImage(encodePNG(t, med), -1)
	if err != nil {
		t.Fatalf("med error: %v", err)
	}
	cfg = decodeJPEGConfig(t, data)
	if cfg.Width != 1600 || cfg.Height != 800 {
		t.Fatalf("med got %dx%d, want 1600x800", cfg.Width, cfg.Height)
	}
}

func TestPrepareVisionImageExtremeAspectAndNonZeroBounds(t *testing.T) {
	// Extreme aspect ratio 5000x1 -> 1600x1 (never collapses height to 0)
	strip := image.NewRGBA(image.Rect(0, 0, 5000, 1))
	data, mime, err := PrepareVisionImage(encodePNG(t, strip), 1600)
	if err != nil {
		t.Fatalf("extreme strip error: %v", err)
	}
	if mime != "image/jpeg" {
		t.Fatalf("mime = %q, want image/jpeg", mime)
	}
	cfg := decodeJPEGConfig(t, data)
	if cfg.Width != 1600 || cfg.Height != 1 {
		t.Fatalf("strip got %dx%d, want 1600x1", cfg.Width, cfg.Height)
	}

	// Non-zero Bounds().Min subimage
	full := image.NewRGBA(image.Rect(0, 0, 2400, 2400))
	sub := full.SubImage(image.Rect(200, 300, 2200, 1300))
	data, _, err = PrepareVisionImage(encodePNG(t, sub), 1600)
	if err != nil {
		t.Fatalf("subimage error: %v", err)
	}
	cfg = decodeJPEGConfig(t, data)
	if cfg.Width != 1600 || cfg.Height != 800 {
		t.Fatalf("subimage got %dx%d, want 1600x800", cfg.Width, cfg.Height)
	}
}

func TestPrepareVisionImageAlphaCompositedOverWhite(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 64, 64))
	// Fully transparent pixels (0,0,0,0) must composite to white, not black.
	data, mime, err := PrepareVisionImage(encodePNG(t, img), 1600)
	if err != nil {
		t.Fatalf("alpha error: %v", err)
	}
	if mime != "image/jpeg" {
		t.Fatalf("mime = %q, want image/jpeg", mime)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("jpeg.Decode: %v", err)
	}
	r, g, b, _ := decoded.At(0, 0).RGBA()
	if uint8(r>>8) < 245 || uint8(g>>8) < 245 || uint8(b>>8) < 245 {
		t.Fatalf("transparent pixel composited to (%d,%d,%d), want near-white >= 245", r>>8, g>>8, b>>8)
	}
}

func TestPrepareVisionImageFormats(t *testing.T) {
	// Small valid JPEG -> preserved verbatim
	smallJPEG := func() []byte {
		var buf bytes.Buffer
		img := image.NewRGBA(image.Rect(0, 0, 400, 300))
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}()
	out, mime, err := PrepareVisionImage(smallJPEG, 1600)
	if err != nil {
		t.Fatalf("small jpeg error: %v", err)
	}
	if mime != "image/jpeg" || !bytes.Equal(out, smallJPEG) {
		t.Fatalf("small jpeg should be returned verbatim without re-encoding")
	}

	// Oversized JPEG 2400x1200 -> 1600x800
	var largeJPEGBuf bytes.Buffer
	if err := jpeg.Encode(&largeJPEGBuf, image.NewRGBA(image.Rect(0, 0, 2400, 1200)), nil); err != nil {
		t.Fatal(err)
	}
	out, mime, err = PrepareVisionImage(largeJPEGBuf.Bytes(), 1600)
	if err != nil || mime != "image/jpeg" {
		t.Fatalf("large jpeg failed: mime=%q err=%v", mime, err)
	}
	if cfg := decodeJPEGConfig(t, out); cfg.Width != 1600 || cfg.Height != 800 {
		t.Fatalf("large jpeg got %dx%d, want 1600x800", cfg.Width, cfg.Height)
	}

	// GIF
	var gifBuf bytes.Buffer
	palImg := image.NewPaletted(image.Rect(0, 0, 200, 100), color.Palette{color.White, color.Black})
	if err := gif.Encode(&gifBuf, palImg, nil); err != nil {
		t.Fatal(err)
	}
	if _, mime, err := PrepareVisionImage(gifBuf.Bytes(), 1600); err != nil || mime != "image/jpeg" {
		t.Fatalf("gif failed: mime=%q err=%v", mime, err)
	}

	// BMP
	var bmpBuf bytes.Buffer
	if err := bmp.Encode(&bmpBuf, image.NewRGBA(image.Rect(0, 0, 200, 100))); err != nil {
		t.Fatal(err)
	}
	if _, mime, err := PrepareVisionImage(bmpBuf.Bytes(), 1600); err != nil || mime != "image/jpeg" {
		t.Fatalf("bmp failed: mime=%q err=%v", mime, err)
	}

	// TIFF
	var tiffBuf bytes.Buffer
	if err := tiff.Encode(&tiffBuf, image.NewRGBA(image.Rect(0, 0, 200, 100)), nil); err != nil {
		t.Fatal(err)
	}
	if _, mime, err := PrepareVisionImage(tiffBuf.Bytes(), 1600); err != nil || mime != "image/jpeg" {
		t.Fatalf("tiff failed: mime=%q err=%v", mime, err)
	}

	// Minimal valid 1x1 lossless WebP (RIFF....WEBPVP8L...)
	webp1x1 := []byte{
		'R', 'I', 'F', 'F', 26, 0, 0, 0, 'W', 'E', 'B', 'P',
		'V', 'P', '8', 'L', 13, 0, 0, 0,
		0x2f, 0x00, 0x00, 0x00, 0x00, 0x07, 0x10, 0x11, 0x11, 0x88, 0x88, 0xfe, 0x07, 0x00,
	}
	if _, _, cfgErr := image.Decode(bytes.NewReader(webp1x1)); cfgErr == nil {
		if _, mime, err := PrepareVisionImage(webp1x1, 1600); err != nil || mime != "image/jpeg" {
			t.Fatalf("webp failed: mime=%q err=%v", mime, err)
		}
	}
}

func TestPrepareVisionImageErrorsAndPixelBomb(t *testing.T) {
	if _, _, err := PrepareVisionImage(nil, 1600); err == nil {
		t.Fatal("expected error for nil bytes")
	}
	if _, _, err := PrepareVisionImage([]byte{}, 1600); err == nil {
		t.Fatal("expected error for empty bytes")
	}
	if _, _, err := PrepareVisionImage([]byte("png"), 1600); err == nil {
		t.Fatal("expected error for invalid image bytes")
	}

	// Craft a PNG IHDR claiming 20000x20000 pixels (400 MP > MaxDecodePixels)
	bomb := makeFakePNGHeader(20000, 20000)
	if _, _, err := PrepareVisionImage(bomb, 1600); err == nil {
		t.Fatal("expected pixel-bomb guard error for 20000x20000 PNG")
	}
}

func makeFakePNGHeader(w, h uint32) []byte {
	var buf bytes.Buffer
	buf.WriteString("\x89PNG\r\n\x1a\n")
	var ihdr [13]byte
	binary.BigEndian.PutUint32(ihdr[0:4], w)
	binary.BigEndian.PutUint32(ihdr[4:8], h)
	ihdr[8] = 8 // bit depth
	ihdr[9] = 6 // RGBA
	binary.Write(&buf, binary.BigEndian, uint32(13))
	buf.WriteString("IHDR")
	buf.Write(ihdr[:])
	crc := crc32.ChecksumIEEE(append([]byte("IHDR"), ihdr[:]...))
	binary.Write(&buf, binary.BigEndian, crc)
	return buf.Bytes()
}
