package main

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func noisyImage(w, h int, alpha bool) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := uint8(255)
			if alpha && x%7 == 0 {
				a = 128
			}
			img.SetNRGBA(x, y, color.NRGBA{uint8(x * 3), uint8(y * 5), uint8(x ^ y), a})
		}
	}
	return img
}

func encodeJPEG(t *testing.T, img image.Image, q int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: q}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func dims(t *testing.T, b []byte) (int, int) {
	t.Helper()
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Width, cfg.Height
}

// AD-58: a picture wider than the setting is scaled to it, proportions kept, and the hard upload limit is 1 MB.
func TestOptimiseScalesDownToTheConfiguredLongestSide(t *testing.T) {
	if maxAssetBytes != 1<<20 {
		t.Fatalf("the upload limit is %d bytes; the decision is 1 MB", maxAssetBytes)
	}
	src := encodeJPEG(t, noisyImage(1600, 800, false), 95)
	out, ext, changed, err := optimiseImage(src, ".jpg", optimiseParams{MaxEdge: 800, Quality: 80})
	if err != nil || !changed || ext != ".jpg" {
		t.Fatalf("changed=%v ext=%q err=%v", changed, ext, err)
	}
	if w, h := dims(t, out); w != 800 || h != 400 {
		t.Fatalf("scaled to %dx%d, want 800x400", w, h)
	}
	if len(out) >= len(src) {
		t.Fatalf("the optimised file (%d) is not smaller than the upload (%d)", len(out), len(src))
	}
}

// Nothing is enlarged and nothing is degraded when there was nothing to gain: the original bytes are kept.
func TestOptimiseKeepsASmallWellCompressedImageExactly(t *testing.T) {
	src := encodeJPEG(t, noisyImage(200, 100, false), 40)
	out, _, changed, err := optimiseImage(src, ".jpg", optimiseParams{MaxEdge: 2560, Quality: 95})
	if err != nil {
		t.Fatal(err)
	}
	if changed || !bytes.Equal(out, src) {
		t.Fatal("an image that needed nothing was rewritten")
	}
}

// A PNG with transparency stays a PNG with its transparency.
func TestOptimiseKeepsTransparency(t *testing.T) {
	var b bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.NoCompression}).Encode(&b, noisyImage(300, 300, true)); err != nil {
		t.Fatal(err)
	}
	out, ext, changed, err := optimiseImage(b.Bytes(), ".png", optimiseParams{MaxEdge: 150, Quality: 80})
	if err != nil || !changed || ext != ".png" {
		t.Fatalf("changed=%v ext=%q err=%v", changed, ext, err)
	}
	img, err := png.Decode(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if hasAlpha(img) == false {
		t.Fatal("the optimised PNG lost its transparency")
	}
	if w, _ := dims(t, out); w != 150 {
		t.Fatalf("width %d, want 150", w)
	}
}

// A GIF may be animated; it is kept exactly (it is already under the byte cap).
func TestOptimiseKeepsGIFExactly(t *testing.T) {
	gifBytes := append([]byte("GIF89a"), make([]byte, 0)...)
	// A minimal valid 1x1 GIF.
	gifBytes = []byte{'G', 'I', 'F', '8', '9', 'a', 1, 0, 1, 0, 0x80, 0, 0, 0, 0, 0, 255, 255, 255, 0x2c, 0, 0, 0, 0, 1, 0, 1, 0, 0, 2, 2, 0x44, 1, 0, 0x3b}
	out, ext, changed, err := optimiseImage(gifBytes, ".gif", defaultOptimiseParams())
	if err != nil || changed || ext != ".gif" || !bytes.Equal(out, gifBytes) {
		t.Fatalf("changed=%v ext=%q err=%v", changed, ext, err)
	}
}

// A decompression bomb is refused before it is decoded.
func TestOptimiseRefusesAnAbsurdlyLargeCanvas(t *testing.T) {
	var b bytes.Buffer
	img := image.NewGray(image.Rect(0, 0, 6000, 6000)) // 36 MP, a few KB as PNG
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := optimiseImage(b.Bytes(), ".png", defaultOptimiseParams()); err == nil {
		t.Fatal("a 36-megapixel canvas was processed")
	}
}

// An EXIF orientation is applied to the pixels before the metadata is dropped, so a phone photo is not turned on its side.
func TestOptimiseAppliesJPEGOrientation(t *testing.T) {
	src := encodeJPEG(t, noisyImage(400, 200, false), 90)
	// Insert an APP1/Exif segment with orientation 6 (rotate 90 clockwise) right after SOI.
	exif := []byte{
		0xFF, 0xE1, 0x00, 0x22, 'E', 'x', 'i', 'f', 0, 0,
		'M', 'M', 0x00, 0x2A, 0x00, 0x00, 0x00, 0x08,
		0x00, 0x01, 0x01, 0x12, 0x00, 0x03, 0x00, 0x00, 0x00, 0x01, 0x00, 0x06, 0x00, 0x00,
		0x00, 0x00, 0x00, 0x00,
	}
	withExif := append(append([]byte{0xFF, 0xD8}, exif...), src[2:]...)
	if got := jpegOrientation(withExif); got != 6 {
		t.Fatalf("orientation read as %d, want 6", got)
	}
	out, _, changed, err := optimiseImage(withExif, ".jpg", optimiseParams{MaxEdge: 2560, Quality: 85})
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if w, h := dims(t, out); w != 200 || h != 400 {
		t.Fatalf("orientation not applied: %dx%d, want 200x400", w, h)
	}
}

// The background step: a pending upload becomes the stored, content-addressed file and the pending one is removed.
func TestPendingUploadBecomesAStoredAsset(t *testing.T) {
	dir := t.TempDir()
	o := newAssetOptimiser(dir, func() optimiseParams { return optimiseParams{MaxEdge: 500, Quality: 80} })
	src := encodeJPEG(t, noisyImage(1000, 500, false), 90)
	id, err := o.enqueue(src, ".jpg")
	if err != nil {
		t.Fatal(err)
	}
	// The queue's own worker does the work; wait for it.
	deadline := time.Now().Add(10 * time.Second)
	for {
		left, _ := os.ReadDir(o.pendingDir())
		if len(left) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("pending file %q was never processed (failed: %v)", id, func() map[string]string { _, f := o.pending(); return f }())
		}
		time.Sleep(20 * time.Millisecond)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.jpg"))
	var stored []string
	for _, m := range matches {
		if regexpAssetName.MatchString(filepath.Base(m)) {
			stored = append(stored, m)
		}
	}
	if len(stored) != 1 {
		t.Fatalf("stored files = %v", matches)
	}
	b, _ := os.ReadFile(stored[0])
	if w, _ := dims(t, b); w != 500 {
		t.Fatalf("stored width %d, want 500", w)
	}
}
