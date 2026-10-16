package main

// DOWNLOADING A BACKUP (audit N-2, Product-Owner decision D-B).
//
// A database backup is a complete pg_dump of the site: every client account, stay, session and the activity
// trail. It used to download for every role that could open the Backups page -- the IT manager, both desk roles
// and the site viewer -- with nothing but an audit row. Now:
//
//   - its own resource key, backup-download: site_admin and hotel_it_manager only. Every role with backups
//     read still sees the list.
//   - the operator's password, every time. POST /backup-download/{name}/ticket with the password returns a
//     single-use ticket that is valid for 60 seconds, for that backup and that operator only.
//   - the file is streamed by POST /backup-download/{name} with the ticket as a form field. A form post rather
//     than a link because the browser then saves the file without leaving the page, and the ticket never
//     appears in a URL (edged logs request lines).
//   - both steps are audited: backup.download_authorized when the password is accepted, backup.downloaded
//     when the file is sent.

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

// downloadTickets is in memory on purpose: a ticket lives for a minute, and edged restarting is a fine reason
// for an operator to type the password again.
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

// redeem consumes a ticket: it is gone after the first use whatever the outcome.
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
