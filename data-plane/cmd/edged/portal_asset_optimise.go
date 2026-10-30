package main

// AD-58: an uploaded portal background or logo is decoded, scaled so its longest side is at most the site's setting
// (default 2,560 px), re-encoded at the site's quality and stored under its content-addressed name, in the background.
// The upload itself is limited to 1 MB (maxAssetBytes) and answered at once with the file "optimising"; the operator's
// list shows the final image when the work is done. Quality is preserved by never keeping a re-encode that is not
// clearly smaller, never enlarging, applying a JPEG's orientation before the metadata that carries it is dropped, and
// keeping GIFs (which may be animated) exactly as uploaded.

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // decoder registration only: a GIF is kept as uploaded
	"image/jpeg"
	"image/png"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // decoder only: a WebP is re-encoded as JPEG or PNG
)

const (
	assetPendingDir = ".pending"
	// maxAssetPixels bounds the decoded size (a small compressed file can describe a huge image).
	maxAssetPixels = 24_000_000
	// keepOriginalUnlessSmallerBy: a re-encode of an image that did not need scaling must be this much smaller to replace it.
	keepOriginalUnlessSmallerBy = 0.9
)

type optimiseParams struct {
	MaxEdge int
	Quality int
}

func defaultOptimiseParams() optimiseParams { return optimiseParams{MaxEdge: 2560, Quality: 82} }

// optimiseImage returns the bytes and extension to store. changed is false when the original is kept.
func optimiseImage(data []byte, ext string, p optimiseParams) (out []byte, outExt string, changed bool, err error) {
	if p.MaxEdge <= 0 {
		p.MaxEdge = defaultOptimiseParams().MaxEdge
	}
	if p.Quality <= 0 || p.Quality > 100 {
		p.Quality = defaultOptimiseParams().Quality
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, "", false, fmt.Errorf("not a decodable image: %w", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxAssetPixels {
		return nil, "", false, errors.New("the image is too large to process")
	}
	if format == "gif" {
		return data, ext, false, nil // may be animated; already within the byte cap
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", false, fmt.Errorf("decode: %w", err)
	}
	orientation := 1
	if format == "jpeg" {
		orientation = jpegOrientation(data)
	}
	img = applyOrientation(img, orientation)
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	resized := false
	if long := max(w, h); long > p.MaxEdge {
		nw, nh := w*p.MaxEdge/long, h*p.MaxEdge/long
		if nw < 1 {
			nw = 1
		}
		if nh < 1 {
			nh = 1
		}
		dst := image.NewNRGBA(image.Rect(0, 0, nw, nh))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Over, nil)
		img, resized = dst, true
	}
	needsRotation := orientation != 1
	var buf bytes.Buffer
	switch {
	case format == "jpeg" || (format == "webp" && !hasAlpha(img)):
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: p.Quality}); err != nil {
			return nil, "", false, err
		}
		outExt = ".jpg"
	default: // png, and webp with transparency
		enc := png.Encoder{CompressionLevel: png.BestCompression}
		if err := enc.Encode(&buf, img); err != nil {
			return nil, "", false, err
		}
		outExt = ".png"
	}
	smaller := float64(buf.Len()) < keepOriginalUnlessSmallerBy*float64(len(data))
	if outExt == ext && !resized && !needsRotation && !smaller {
		return data, ext, false, nil // nothing gained; keep the original bytes exactly
	}
	if outExt != ext && !resized && !needsRotation && buf.Len() >= len(data) {
		return data, ext, false, nil
	}
	return buf.Bytes(), outExt, true, nil
}

func hasAlpha(img image.Image) bool {
	if o, ok := img.(interface{ Opaque() bool }); ok {
		return !o.Opaque()
	}
	b := img.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y += max(1, b.Dy()/64) {
		for x := b.Min.X; x < b.Max.X; x += max(1, b.Dx()/64) {
			if _, _, _, a := img.At(x, y).RGBA(); a != 0xffff {
				return true
			}
		}
	}
	return false
}

// jpegOrientation reads the EXIF orientation (1-8); 1 when absent or unreadable.
func jpegOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 1
	}
	i := 2
	for i+4 <= len(b) {
		if b[i] != 0xFF {
			return 1
		}
		marker := b[i+1]
		if marker == 0xDA || marker == 0xD9 {
			return 1
		}
		segLen := int(b[i+2])<<8 | int(b[i+3])
		if segLen < 2 || i+2+segLen > len(b) {
			return 1
		}
		if marker == 0xE1 && segLen >= 16 && string(b[i+4:i+10]) == "Exif\x00\x00" {
			return exifOrientation(b[i+10 : i+2+segLen])
		}
		i += 2 + segLen
	}
	return 1
}

func exifOrientation(t []byte) int {
	if len(t) < 12 {
		return 1
	}
	var u16 func([]byte) int
	var u32 func([]byte) int
	switch string(t[:2]) {
	case "II":
		u16 = func(p []byte) int { return int(p[0]) | int(p[1])<<8 }
		u32 = func(p []byte) int { return int(p[0]) | int(p[1])<<8 | int(p[2])<<16 | int(p[3])<<24 }
	case "MM":
		u16 = func(p []byte) int { return int(p[0])<<8 | int(p[1]) }
		u32 = func(p []byte) int { return int(p[0])<<24 | int(p[1])<<16 | int(p[2])<<8 | int(p[3]) }
	default:
		return 1
	}
	off := u32(t[4:8])
	if off < 8 || off+2 > len(t) {
		return 1
	}
	n := u16(t[off : off+2])
	for k := 0; k < n; k++ {
		e := off + 2 + 12*k
		if e+12 > len(t) {
			return 1
		}
		if u16(t[e:e+2]) == 0x0112 {
			if v := u16(t[e+8 : e+10]); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// applyOrientation turns the pixels the way the metadata said to, so dropping the metadata does not rotate the picture.
func applyOrientation(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	swap := o >= 5
	dw, dh := w, h
	if swap {
		dw, dh = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)))
		}
	}
	return dst
}

// ---------------------------------------------------------------------------------------------------------
// The background queue. An upload is first written to the hidden pending directory; the worker turns it into the
// stored, content-addressed file and removes the pending one. A pending file left by a restart is picked up again.
// ---------------------------------------------------------------------------------------------------------

type assetOptimiser struct {
	mu     sync.Mutex
	dir    string
	params func() optimiseParams
	failed map[string]string // pending id -> reason, kept until the operator clears it by uploading again
	// done maps a pending id to the name its optimised file was stored under, so a client that uploaded several images at
	// once can tell which result is its own (the name is a hash of the optimised bytes, so it cannot be guessed from the id).
	// Bounded to the most recent assetDoneKeep entries.
	done      map[string]string
	doneOrder []string
	wake      chan struct{}
	started   sync.Once
}

func newAssetOptimiser(dir string, params func() optimiseParams) *assetOptimiser {
	return &assetOptimiser{dir: dir, params: params, failed: map[string]string{}, done: map[string]string{}, wake: make(chan struct{}, 1)}
}

// assetDoneKeep is how many finished uploads the optimiser remembers the final name of.
const assetDoneKeep = 256

// resolved reports the final name of a finished upload, whether it is still pending, or the reason it failed.
func (o *assetOptimiser) resolved(id string) (name string, state string, why string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if n, ok := o.done[id]; ok {
		return n, "ready", ""
	}
	if w, ok := o.failed[id]; ok {
		return "", "failed", w
	}
	if _, err := os.Stat(filepath.Join(o.pendingDir(), id)); err == nil {
		return "", "optimising", ""
	}
	return "", "unknown", ""
}

func (o *assetOptimiser) remember(id, name string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.done[id]; !ok {
		o.doneOrder = append(o.doneOrder, id)
	}
	o.done[id] = name
	for len(o.doneOrder) > assetDoneKeep {
		delete(o.done, o.doneOrder[0])
		o.doneOrder = o.doneOrder[1:]
	}
}

func (o *assetOptimiser) pendingDir() string { return filepath.Join(o.dir, assetPendingDir) }

// enqueue stores the uploaded bytes as a pending file and returns its id.
func (o *assetOptimiser) enqueue(data []byte, ext string) (string, error) {
	if err := os.MkdirAll(o.pendingDir(), 0o755); err != nil {
		return "", err
	}
	var raw [12]byte
	_, _ = rand.Read(raw[:])
	id := hex.EncodeToString(raw[:]) + ext
	if err := writeFileAtomically(o.pendingDir(), id, data, 0o644); err != nil {
		return "", err
	}
	o.start()
	select {
	case o.wake <- struct{}{}:
	default:
	}
	return id, nil
}

// pending lists ids still waiting or failed, so the operator's list can say "optimising" or why not.
func (o *assetOptimiser) pending() (ids []string, failed map[string]string) {
	o.start()
	entries, _ := os.ReadDir(o.pendingDir())
	for _, e := range entries {
		if !e.IsDir() && len(e.Name()) > 0 && e.Name()[0] != '.' {
			ids = append(ids, e.Name())
		}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	failed = make(map[string]string, len(o.failed))
	for k, v := range o.failed {
		failed[k] = v
	}
	return ids, failed
}

func (o *assetOptimiser) start() {
	o.started.Do(func() {
		go func() {
			t := time.NewTicker(time.Minute)
			defer t.Stop()
			o.drain()
			for {
				select {
				case <-o.wake:
				case <-t.C:
				}
				o.drain()
			}
		}()
	})
}

func (o *assetOptimiser) drain() {
	entries, _ := os.ReadDir(o.pendingDir())
	for _, e := range entries {
		if e.IsDir() || e.Name()[0] == '.' {
			continue
		}
		o.mu.Lock()
		_, bad := o.failed[e.Name()]
		o.mu.Unlock()
		if bad {
			continue
		}
		if err := o.process(e.Name()); err != nil {
			slog.Warn("portal image optimisation failed", "id", e.Name(), "err", err)
			o.mu.Lock()
			o.failed[e.Name()] = err.Error()
			o.mu.Unlock()
		}
	}
}

// process turns one pending file into its stored form. The original is removed only after the result is in place.
func (o *assetOptimiser) process(id string) error {
	src := filepath.Join(o.pendingDir(), id)
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	ext, _ := sniffImage(data)
	if ext == "" {
		_ = os.Remove(src)
		return errors.New("not a recognised image")
	}
	out, outExt, _, err := optimiseImage(data, ext, o.params())
	if err != nil {
		return err
	}
	sum := sha256.Sum256(out)
	name := hex.EncodeToString(sum[:8]) + outExt
	if err := writeFileAtomically(o.dir, name, out, 0o644); err != nil {
		return err
	}
	o.remember(id, name)
	return os.Remove(src)
}
