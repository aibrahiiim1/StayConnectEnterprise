package main

// PORTAL ASSETS — the hotel's own logo and photographs, held on the appliance.
//
// WHY URL-ONLY WAS NOT ENOUGH
// ---------------------------
// Branding accepted an https URL or an appliance path. The https option asks a hotel to find somewhere to
// host its logo and makes the captive portal depend on the open internet to look right — on a page a guest
// reaches precisely because they have no internet yet. The appliance-path option pointed at a directory
// nothing served: /assets/logo.png returned 404, so the only workable answer was an external URL.
//
// Assets now live on the appliance, are uploaded through Hotel Admin, and are served by the portal from the
// same origin as the page. Nothing about a guest's sign-in depends on reaching anything else.
//
// WHAT IS ACCEPTED, AND WHY IT IS NARROW
// --------------------------------------
// This directory is served unauthenticated to every device on the guest network before sign-in — it is the
// most exposed surface the appliance has. So the content type is decided by INSPECTING THE BYTES rather than
// trusting a filename or a client-supplied Content-Type, SVG is refused outright (it is a document format
// that can carry script), and the stored name is generated here rather than taken from the upload.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"regexp"

	"github.com/go-chi/chi/v5"
)

// portalAssetDir is created by deployment and owned by the service user; both edged (which writes) and
// portald (which serves) run as it.
const portalAssetDir = "/opt/stayconnect/portal-assets"

// maxAssetBytes bounds one upload. A background photograph is the large case; anything beyond this is not a
// hotel photograph, and the file is served to every guest device before they have signed in.
const maxAssetBytes = 8 << 20 // 8 MB

// regexpAssetName matches exactly what uploadPortalAsset generates, so delete can name nothing else.
var regexpAssetName = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|webp|gif)$`)

// sniffImage decides the type from the CONTENT, and returns "" for anything it does not positively recognise.
//
// SVG IS DELIBERATELY ABSENT. It is XML, it can carry <script> and event handlers, and browsers execute that
// when it is loaded as a document. On a page that collects room numbers and voucher codes, an uploadable SVG
// is a stored-XSS primitive wearing a picture's clothes.
func sniffImage(b []byte) (ext, mime string) {
	switch {
	case len(b) > 8 && bytes.Equal(b[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return ".png", "image/png"
	case len(b) > 3 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return ".jpg", "image/jpeg"
	case len(b) > 12 && bytes.Equal(b[:4], []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")):
		return ".webp", "image/webp"
	case len(b) > 6 && (bytes.Equal(b[:6], []byte("GIF87a")) || bytes.Equal(b[:6], []byte("GIF89a"))):
		return ".gif", "image/gif"
	}
	return "", ""
}

func (s *server) portalAssetRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listPortalAssets)
	r.Post("/", s.uploadPortalAsset)
	r.Delete("/{name}", s.deletePortalAsset)
	return r
}

type portalAsset struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	SizeBytes int64  `json:"size_bytes"`
	Uploaded  string `json:"uploaded_at"`
	InUse     bool   `json:"in_use"`
}

func (s *server) listPortalAssets(w http.ResponseWriter, r *http.Request) {
	out := []portalAsset{}
	// What the published design and the working draft point at, so the UI can refuse to delete something the
	// portal is currently showing rather than discovering it afterwards.
	used := map[string]bool{}
	if doc, err := s.loadBranding(r); err == nil {
		for _, d := range []map[string]any{doc.Design, doc.Draft} {
			for _, k := range []string{"logo_url", "background_url"} {
				if v, ok := d[k].(string); ok {
					used[strings.TrimPrefix(v, "/assets/")] = true
				}
			}
		}
	}
	entries, err := os.ReadDir(portalAssetDir)
	if err != nil {
		writeList(w, out) // an appliance with no assets yet is not an error
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, portalAsset{
			Name:      e.Name(),
			URL:       "/assets/" + e.Name(),
			SizeBytes: fi.Size(),
			Uploaded:  fi.ModTime().UTC().Format(time.RFC3339),
			InUse:     used[e.Name()],
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Uploaded > out[j].Uploaded })
	writeList(w, out)
}

func (s *server) uploadPortalAsset(w http.ResponseWriter, r *http.Request) {
	// The body limit is enforced before anything is read into memory, not after.
	r.Body = http.MaxBytesReader(w, r.Body, maxAssetBytes+1024)
	if err := r.ParseMultipartForm(maxAssetBytes); err != nil {
		jsonErr(w, http.StatusBadRequest, "too_large",
			fmt.Sprintf("the image must be %d MB or smaller", maxAssetBytes>>20))
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "no image was supplied")
		return
	}
	defer file.Close()
	buf := make([]byte, maxAssetBytes+1)
	n, _ := file.Read(buf)
	if n == 0 {
		jsonErr(w, http.StatusBadRequest, "bad_request", "the image was empty")
		return
	}
	if n > maxAssetBytes {
		jsonErr(w, http.StatusBadRequest, "too_large",
			fmt.Sprintf("the image must be %d MB or smaller", maxAssetBytes>>20))
		return
	}
	data := buf[:n]

	// THE BYTES DECIDE. A filename ending .png and a Content-Type of image/png are both things the uploader
	// chose; neither says what the browser will do with the file.
	ext, mime := sniffImage(data)
	if ext == "" {
		jsonErr(w, http.StatusBadRequest, "unsupported_image",
			"only PNG, JPEG, WebP and GIF images are accepted. SVG is refused because it can carry script, "+
				"and this file is served to every guest device before sign-in.")
		return
	}

	if err := os.MkdirAll(portalAssetDir, 0o755); err != nil {
		jsonErr(w, http.StatusInternalServerError, "asset_dir_unavailable",
			"the portal asset directory "+portalAssetDir+" is not writable by the appliance service account")
		return
	}
	// The stored name is DERIVED, never the uploaded one: an operator's filename can contain anything, and
	// content-addressing means uploading the same image twice does not accumulate copies.
	sum := sha256.Sum256(data)
	name := hex.EncodeToString(sum[:8]) + ext
	dest := filepath.Join(portalAssetDir, name)
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the image could not be stored")
		return
	}
	s.audit(r, "portal_asset.uploaded", "portal_asset", name, map[string]any{
		"size_bytes": n, "content_type": mime, "original_name": filepath.Base(hdr.Filename),
	})
	writeJSON(w, http.StatusOK, portalAsset{
		Name: name, URL: "/assets/" + name, SizeBytes: int64(n),
		Uploaded: time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *server) deletePortalAsset(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	// Same shape this handler generates, so nothing else can be named.
	if !regexpAssetName.MatchString(name) {
		jsonErr(w, http.StatusNotFound, "not_found", "no such asset")
		return
	}
	// REFUSE TO DELETE SOMETHING THE PORTAL IS SHOWING. Removing it would leave guests looking at a broken
	// image on the sign-in page, and the operator would have no idea why.
	if doc, err := s.loadBranding(r); err == nil {
		for _, d := range []map[string]any{doc.Design, doc.Draft} {
			for _, k := range []string{"logo_url", "background_url"} {
				if v, ok := d[k].(string); ok && strings.TrimPrefix(v, "/assets/") == name {
					jsonErr(w, http.StatusConflict, "in_use",
						"this image is used by the portal design. Change the design first, then delete it.")
					return
				}
			}
		}
	}
	if err := os.Remove(filepath.Join(portalAssetDir, name)); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "no such asset")
		return
	}
	s.audit(r, "portal_asset.deleted", "portal_asset", name, nil)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": name})
}
