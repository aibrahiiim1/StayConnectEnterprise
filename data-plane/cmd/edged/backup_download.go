package main

// A backup is a complete pg_dump of the site, so downloading needs the backup-download key (site_admin and
// hotel_it_manager only) and the operator's password every time: POST .../ticket returns a single-use 60 s
// ticket for that backup and operator. The file is then streamed by a form POST carrying the ticket, because
// edged logs request lines and the ticket must never appear in a URL.

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

const downloadTicketTTL = 60 * time.Second

type downloadTicket struct {
	operatorID string
	name       string
	expires    time.Time
}

// Tickets are in memory on purpose: they live a minute and a restart may ask for the password again.
type downloadTicketStore struct {
	mu sync.Mutex
	m  map[string]downloadTicket
}

var backupTickets = &downloadTicketStore{m: map[string]downloadTicket{}}

func (d *downloadTicketStore) issue(operatorID, name string) (string, time.Time) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	tok := base64.RawURLEncoding.EncodeToString(b)
	exp := time.Now().Add(downloadTicketTTL)
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now()
	for k, v := range d.m {
		if now.After(v.expires) {
			delete(d.m, k)
		}
	}
	d.m[tok] = downloadTicket{operatorID: operatorID, name: name, expires: exp}
	return tok, exp
}

// redeem consumes the ticket whatever the outcome.
func (d *downloadTicketStore) redeem(tok, operatorID, name string) bool {
	if tok == "" {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	t, ok := d.m[tok]
	delete(d.m, tok)
	return ok && time.Now().Before(t.expires) && t.operatorID == operatorID && t.name == name
}

func (s *server) backupDownloadRoutes() http.Handler {
	r := chi.NewRouter()
	r.Post("/{name}/ticket", s.backupDownloadTicket)
	r.Post("/{name}", s.backupDownloadFile)
	return r
}

func (s *server) backupDownloadTicket(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var in struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	if _, ok := resolveArtifact(name); !ok {
		jsonErr(w, http.StatusNotFound, "not_found", "no such backup artifact")
		return
	}
	if ok, _, why := downloadPolicy(name, false); !ok {
		s.audit(r, "backup.download_refused", "backup", name, map[string]any{"source_ip": clientIP(r)})
		jsonErr(w, http.StatusForbidden, "download_refused", why)
		return
	}
	if !s.reauth(r, in.Password) {
		s.reauthRefused(w, r, "Confirm your password to download a backup. It is a complete copy of this site's data.")
		return
	}
	sess := sessFrom(r.Context())
	tok, exp := backupTickets.issue(sess.OperatorID, name)
	s.audit(r, "backup.download_authorized", "backup", name, map[string]any{"source_ip": clientIP(r)})
	writeJSON(w, http.StatusOK, map[string]any{"ticket": tok, "expires_at": exp.UTC()})
}

func (s *server) backupDownloadFile(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	if err := r.ParseForm(); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}
	sess := sessFrom(r.Context())
	if sess == nil || !backupTickets.redeem(r.PostForm.Get("ticket"), sess.OperatorID, name) {
		jsonErr(w, http.StatusForbidden, "ticket_required",
			"Downloading a backup needs your password. Use Download on the Backups page again.")
		return
	}
	path, ok := resolveArtifact(name)
	if !ok {
		jsonErr(w, http.StatusNotFound, "not_found", "no such backup artifact")
		return
	}
	// Checked again here: the policy must hold even for a ticket issued before it was tightened.
	if ok, _, why := downloadPolicy(name, false); !ok {
		jsonErr(w, http.StatusForbidden, "download_refused", why)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the artifact could not be opened")
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the artifact could not be measured")
		return
	}
	s.audit(r, "backup.downloaded", "backup", name, map[string]any{"size_bytes": fi.Size(), "source_ip": clientIP(r)})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, name, fi.ModTime(), f)
}
