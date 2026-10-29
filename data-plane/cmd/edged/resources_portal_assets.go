package main

// The hotel's own logo and photographs, held on the appliance and served by the portal from the page's own
// origin, so a guest's sign-in never depends on reaching anything else. This directory is served
// unauthenticated to every guest device before sign-in, so the content type is decided by inspecting the
// bytes, SVG is refused (it can carry script), and the stored name is generated here.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"regexp"

	"github.com/go-chi/chi/v5"
)

const portalAssetDir = "/opt/stayconnect/portal-assets"

// The hard upload limit (AD-58): enforced here and stated in the Admin Console. The stored file is then scaled and
// compressed in the background (portal_asset_optimise.go), so what guests download is usually much smaller.
const maxAssetBytes = 1 << 20 // 1 MB

var regexpAssetName = regexp.MustCompile(`^[0-9a-f]{16}\.(png|jpg|webp|gif)$`)

// Decides the type from the content; returns "" for anything not positively recognised. SVG is deliberately
// absent: it can carry script and would be a stored-XSS primitive on a page that collects room numbers and vouchers.
func sniffImage(b []byte) (ext, mime string) {
	switch {
	case len(b) > 8 && bytes.Equal(b[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return ".png", "image/png"
	// SOI followed by a marker segment (FF C0..FE): three bytes alone also match non-images (audit P9 L9).
	case len(b) > 4 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF && b[3] >= 0xC0 && b[3] != 0xFF:
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
	// The bytes for Hotel Admin: the portal serves /assets/<name> on the guest network, which is a different origin
	// from the admin, so thumbnails and previews read the file back through the authenticated operator API.
	r.Get("/{name}/raw", s.rawPortalAsset)
	r.Delete("/{name}", s.deletePortalAsset)
	return r
}

type portalAsset struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	SizeBytes int64  `json:"size_bytes"`
	Uploaded  string `json:"uploaded_at"`
	InUse     bool   `json:"in_use"`
	// Status is "ready", "optimising" (stored, not yet selectable: the final name is not known until it is done) or
	// "failed" (Reason says why). An optimising or failed entry has no URL.
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// assetOptimiser returns the background worker, created on first use.
func (s *server) assetOptimiser() *assetOptimiser {
	s.assetOptOnce.Do(func() {
		s.assetOpt = newAssetOptimiser(portalAssetDir, s.portalImageParams)
	})
	return s.assetOpt
}

// portalImageParams reads the site's image settings (migration 0162); the defaults apply when they cannot be read.
func (s *server) portalImageParams() optimiseParams {
	p := defaultOptimiseParams()
	if s.db == nil || s.tenantID == "" || s.siteID == "" {
		return p
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var edge, q int
	if err := s.db.QueryRow(ctx, `SELECT portal_image_max_edge_px, portal_image_quality
	     FROM iam_v2.signin_capacity_settings_get($1::uuid,$2::uuid)`, s.tenantID, s.siteID).Scan(&edge, &q); err == nil {
		p.MaxEdge, p.Quality = edge, q
	}
	return p
}

func (s *server) listPortalAssets(w http.ResponseWriter, r *http.Request) {
	out := []portalAsset{}
	// What any portal design (the site default or a profile) points at, so the UI can refuse to delete a file a portal is showing.
	used, usedErr := s.assetsInUse(r)
	if usedErr != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the images in use could not be read")
		return
	}
	entries, err := os.ReadDir(portalAssetDir)
	if err != nil {
		writeList(w, out) // an appliance with no assets yet is not an error
		return
	}
	for _, e := range entries {
		// Only stored images: an upload in flight is a hidden temporary file.
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
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
			Status:    "ready",
		})
	}
	pendingIDs, failed := s.assetOptimiser().pending()
	for _, id := range pendingIDs {
		a := portalAsset{Name: id, Status: "optimising"}
		if fi, err := os.Stat(filepath.Join(portalAssetDir, assetPendingDir, id)); err == nil {
			a.SizeBytes, a.Uploaded = fi.Size(), fi.ModTime().UTC().Format(time.RFC3339)
		}
		if why, bad := failed[id]; bad {
			a.Status, a.Reason = "failed", why
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Uploaded > out[j].Uploaded })
	writeList(w, out)
}

func (s *server) uploadPortalAsset(w http.ResponseWriter, r *http.Request) {
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
	// Read the whole part, never more than the limit plus one byte: a single Read may return less than the file,
	// and a fixed 8 MB buffer per request is not needed (audit P9 L9).
	data, err := io.ReadAll(io.LimitReader(file, maxAssetBytes+1))
	if err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "the image could not be read")
		return
	}
	n := len(data)
	if n == 0 {
		jsonErr(w, http.StatusBadRequest, "bad_request", "the image was empty")
		return
	}
	if n > maxAssetBytes {
		jsonErr(w, http.StatusBadRequest, "too_large",
			fmt.Sprintf("the image must be %d MB or smaller", maxAssetBytes>>20))
		return
	}

	// The bytes decide: neither the filename nor the client Content-Type says what a browser will do with the file.
	ext, mime := sniffImage(data)
	if ext == "" {
		jsonErr(w, http.StatusBadRequest, "unsupported_image",
			"only PNG, JPEG, WebP and GIF images are accepted. SVG is refused because it can carry script, "+
				"and this file is served to every client device before sign-in.")
		return
	}

	if err := os.MkdirAll(portalAssetDir, 0o755); err != nil {
		jsonErr(w, http.StatusInternalServerError, "asset_dir_unavailable",
			"the portal asset directory "+portalAssetDir+" is not writable by the appliance service account")
		return
	}
	// The stored name is derived, never the uploaded one (content-addressing dedupes repeated uploads). The file is
	// first held aside and optimised in the background; the name it ends up with is known only when that is done.
	id, err := s.assetOptimiser().enqueue(data, ext)
	if err != nil {
		slog.Error("portal asset not stored", "dir", portalAssetDir, "err", err)
		jsonErr(w, http.StatusInternalServerError, "asset_dir_unavailable",
			"the image could not be written to "+portalAssetDir+": "+err.Error())
		return
	}
	s.audit(r, "portal_asset.uploaded", "portal_asset", id, map[string]any{
		"size_bytes": n, "content_type": mime, "original_name": filepath.Base(hdr.Filename),
	})
	writeJSON(w, http.StatusAccepted, portalAsset{
		Name: id, SizeBytes: int64(n), Status: "optimising",
		Uploaded: time.Now().UTC().Format(time.RFC3339),
	})
}

// Re-derives the type from the content as the upload does; anything that no longer sniffs as an image is refused.
func (s *server) rawPortalAsset(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !regexpAssetName.MatchString(name) {
		jsonErr(w, http.StatusNotFound, "not_found", "no such asset")
		return
	}
	data, err := os.ReadFile(filepath.Join(portalAssetDir, name))
	if err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "no such asset")
		return
	}
	_, mime := sniffImage(data)
	if mime == "" {
		jsonErr(w, http.StatusUnsupportedMediaType, "unsupported_image", "the stored file is not a recognised image")
		return
	}
	w.Header().Set("Content-Type", mime)
	// Content-addressed, so cacheable; never sniffed by the browser into something else.
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *server) deletePortalAsset(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !regexpAssetName.MatchString(name) {
		jsonErr(w, http.StatusNotFound, "not_found", "no such asset")
		return
	}
	// Refuse to delete what any portal design is showing: guests would see a broken image on the sign-in page. Unknown is refused too.
	used, err := s.assetsInUse(r)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the images in use could not be read, so nothing was deleted")
		return
	}
	if used[name] {
		jsonErr(w, http.StatusConflict, "in_use", "this image is used by a portal design. Change the design first, then delete it.")
		return
	}
	if err := os.Remove(filepath.Join(portalAssetDir, name)); err != nil {
		jsonErr(w, http.StatusNotFound, "not_found", "no such asset")
		return
	}
	s.audit(r, "portal_asset.deleted", "portal_asset", name, nil)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": name})
}

// writeFileAtomically writes to a temporary file in dir and renames it into place, so portald never serves a
// half-written image to a guest (audit P9 L9).
func writeFileAtomically(dir, name string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // a no-op once renamed
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, filepath.Join(dir, name))
}
