package main

// TWO MESSAGES TO CENTRAL THAT USED TO BE SENT ONCE.
//
// Both were fire-and-forget, and both matter more now that Central's replacement and retirement flows wait on
// them:
//
//   - The TERMINAL-ASSIGNMENT ACKNOWLEDGEMENT. Central retires an appliance in two phases: it signs a terminal
//     assignment, and only when the appliance acknowledges having adopted it does Central revoke the
//     appliance's credentials and mark it retired (the acknowledged-retirement flow a hardware replacement now
//     relies on). The ack was sent once, just before scd re-executed. A Central that was briefly unreachable,
//     an mTLS transport still coming up, or a lost reply left Central waiting for an acknowledgement that
//     would never be sent again -- the re-executed process holds the same terminal version, so the poll that
//     sent it never fires again.
//
//   - The OFFLINE-PACKAGE RECONCILIATION. An offline package is single-use locally (edge_offline_packages);
//     telling Central it was consumed is what lets Central account for it. That call ran once, in a goroutine,
//     right after the import -- which is by definition a moment the appliance may well be offline -- and the
//     "boot reconcile will retry" its comment promised did not exist.
//
// Both are now retried until Central confirms, idempotently. Neither is on a guest path: guest service never
// waits for either, and a Central outage only delays them.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/applianceauth"
	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
)

// centralMTLS is the mutual-TLS transport both messages ride on. Tests substitute s.centralTransport.
func (s *server) centralMTLS() (*http.Client, string, bool) {
	if s.centralTransport != nil {
		return s.centralTransport()
	}
	return s.mtlsTransport()
}

// ---- the terminal acknowledgement --------------------------------------------------------------------------

// ackOutcome is what one delivery attempt learned.
type ackOutcome int

const (
	// ackConfirmed: Central accepted the acknowledgement (2xx). Done for this document.
	ackConfirmed ackOutcome = iota
	// ackRetry: nothing definitive -- no transport yet, a network error, a timeout, 408/429 or a 5xx.
	ackRetry
	// ackRefused: Central answered definitively and will not accept this acknowledgement however often it is
	// repeated: 409 (Central's current assignment is no longer this document -- the poll will fetch the new
	// one), 404, 400, or 401/403 (after a processed ack Central has already revoked this appliance's
	// credentials, so a repeat is refused at the door). Retrying would only hammer it.
	ackRefused
)

// classifyAckStatus maps Central's answer to an outcome.
func classifyAckStatus(code int) ackOutcome {
	switch {
	case code >= 200 && code < 300:
		return ackConfirmed
	case code == http.StatusRequestTimeout || code == http.StatusTooManyRequests || code >= 500:
		return ackRetry
	case code >= 400:
		return ackRefused
	default:
		return ackRetry
	}
}

// terminalAckBackoff is the wait before each retry: quick at first (a transport coming up), then settling at a
// cadence that is gentle on a Central that is down. A var so tests can shorten it.
var terminalAckBackoff = []time.Duration{
	15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute,
}

func backoffAt(steps []time.Duration, attempt int) time.Duration {
	if attempt < len(steps) {
		return steps[attempt]
	}
	return steps[len(steps)-1]
}

// terminalAckRecord is the durable "Central has answered this acknowledgement" marker, kept beside the
// assignment so a restart or re-exec does not resend a confirmed ack -- and does resend an unconfirmed one.
type terminalAckRecord struct {
	Version     int64  `json:"assignment_version"`
	Fingerprint string `json:"assignment_fingerprint"`
	Outcome     string `json:"outcome"` // confirmed | refused
	HTTPStatus  int    `json:"http_status"`
	At          string `json:"at"`
}

func (s *server) terminalAckPath() string {
	return filepath.Join(s.assignmentStore().Dir, "terminal-ack.json")
}

// terminalAckSettled reports whether Central has already answered the acknowledgement for this document.
func (s *server) terminalAckSettled(doc *assignment.Document) bool {
	b, err := os.ReadFile(s.terminalAckPath())
	if err != nil {
		return false
	}
	var rec terminalAckRecord
	if json.Unmarshal(b, &rec) != nil {
		return false
	}
	return rec.Version == doc.Version && rec.Fingerprint == assignment.DocFingerprint(doc)
}

func (s *server) settleTerminalAck(doc *assignment.Document, outcome string, code int) {
	b, _ := json.Marshal(terminalAckRecord{Version: doc.Version, Fingerprint: assignment.DocFingerprint(doc),
		Outcome: outcome, HTTPStatus: code, At: time.Now().UTC().Format(time.RFC3339)})
	p := s.terminalAckPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		slog.Warn("assignment: could not record the terminal acknowledgement outcome", "err", err)
		return
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err == nil {
		err = os.Rename(tmp, p)
		if err != nil {
			slog.Warn("assignment: could not record the terminal acknowledgement outcome", "err", err)
		}
	}
}

// adoptedTerminal returns the terminal document this appliance currently holds, or nil.
func (s *server) adoptedTerminal() (*assignment.Document, time.Time) {
	rec, err := s.assignmentStore().Load()
	if err != nil || rec == nil || rec.Current == nil || !assignment.Clears(rec.Current.State) {
		return nil, time.Time{}
	}
	at, _ := time.Parse(time.RFC3339, rec.AdoptedAt)
	return rec.Current, at
}

// deliverTerminalAck makes ONE signed delivery attempt and classifies the answer. adoptedAt is the moment the
// document was adopted (from the store), so every attempt states the same fact.
func (s *server) deliverTerminalAck(ctx context.Context, doc *assignment.Document, adoptedAt time.Time) (ackOutcome, int) {
	if s.idPriv == nil || s.applID == "" {
		return ackRetry, 0
	}
	cl, base, ready := s.centralMTLS()
	if !ready || base == "" {
		return ackRetry, 0
	}
	if adoptedAt.IsZero() {
		adoptedAt = time.Now()
	}
	ack := &assignment.Ack{
		ApplianceID: s.applID, Version: doc.Version, TerminalState: doc.State,
		Fingerprint: assignment.DocFingerprint(doc), AdoptedAt: adoptedAt.Unix(),
	}
	assignment.SignAck(s.idPriv, ack)
	body, _ := json.Marshal(ack)
	tok, err := applianceauth.SignRequest(s.idPriv, s.applID, http.MethodPost, "/v1/appliance/assignment/ack", body)
	if err != nil {
		return ackRetry, 0
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/appliance/assignment/ack", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		return ackRetry, 0
	}
	resp.Body.Close()
	return classifyAckStatus(resp.StatusCode), resp.StatusCode
}

// terminalAckRunner makes sure at most one retry loop exists per process.
type terminalAckRunner struct {
	mu      sync.Mutex
	running bool
}

// ensureTerminalAck starts the retry loop when this appliance holds a terminal assignment Central has not
// answered yet. Called when a terminal document is adopted and again whenever the assignment agent starts,
// so an ack left unconfirmed by a re-exec, a restart or a reboot is picked up again. Idempotent.
func (s *server) ensureTerminalAck(ctx context.Context) {
	doc, _ := s.adoptedTerminal()
	if doc == nil || s.terminalAckSettled(doc) {
		return
	}
	s.ackRunner.mu.Lock()
	if s.ackRunner.running {
		s.ackRunner.mu.Unlock()
		return
	}
	s.ackRunner.running = true
	s.ackRunner.mu.Unlock()
	go func() {
		defer func() {
			s.ackRunner.mu.Lock()
			s.ackRunner.running = false
			s.ackRunner.mu.Unlock()
		}()
		s.runTerminalAck(ctx)
	}()
}

// runTerminalAck retries with backoff until Central answers definitively, the appliance stops holding that
// terminal document (a newer assignment superseded it), or ctx ends.
func (s *server) runTerminalAck(ctx context.Context) {
	for attempt := 0; ; attempt++ {
		doc, adoptedAt := s.adoptedTerminal()
		if doc == nil || s.terminalAckSettled(doc) {
			return
		}
		outcome, code := s.deliverTerminalAck(ctx, doc, adoptedAt)
		switch outcome {
		case ackConfirmed:
			s.settleTerminalAck(doc, "confirmed", code)
			slog.Info("assignment: Central confirmed the terminal-adoption acknowledgement",
				"version", doc.Version, "state", doc.State, "attempts", attempt+1)
			return
		case ackRefused:
			s.settleTerminalAck(doc, "refused", code)
			slog.Warn("assignment: Central refused the terminal-adoption acknowledgement; not retrying",
				"version", doc.Version, "state", doc.State, "http", code)
			return
		}
		wait := backoffAt(terminalAckBackoff, attempt)
		slog.Info("assignment: terminal-adoption acknowledgement not confirmed yet; retrying",
			"version", doc.Version, "http", code, "attempt", attempt+1, "retry_in", wait.String())
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// ---- offline-package reconciliation --------------------------------------------------------------------------

// offlineReconcileEvery is how often unreconciled packages are offered to Central again. A var for tests.
var offlineReconcileEvery = 10 * time.Minute

// errNoCentralTransport means the mTLS transport is not established yet: nothing was sent.
var errNoCentralTransport = errors.New("central mTLS transport not ready")

// sendOfflineReconcile tells Central one package was consumed. Central answers 200 both when it recorded the
// consumption and when it has no matching package, so 200 is the end of the matter either way; anything else
// is retried later.
func (s *server) sendOfflineReconcile(ctx context.Context, packageID string) (bool, error) {
	if s.idPriv == nil || s.applID == "" {
		return false, errNoCentralTransport
	}
	cl, base, ok := s.centralMTLS()
	if !ok || base == "" {
		return false, errNoCentralTransport
	}
	body, _ := json.Marshal(map[string]string{"package_id": packageID})
	tok, err := applianceauth.SignRequest(s.idPriv, s.applID, http.MethodPost, "/v1/appliance/offline-reconcile", body)
	if err != nil {
		return false, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/appliance/offline-reconcile", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := cl.Do(req)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

// offlineReconciler is the retry pass over the local single-use ledger. Its three steps are functions so the
// pass can be tested without a database or a Central.
type offlineReconciler struct {
	pending func(ctx context.Context) ([]string, error)
	send    func(ctx context.Context, packageID string) (bool, error)
	mark    func(ctx context.Context, packageID string) error
}

// runOnce offers every unreconciled package to Central and stamps the ones Central confirmed. It stops early
// when the transport is not there: every remaining call would fail the same way.
func (o offlineReconciler) runOnce(ctx context.Context) (reconciled, remaining int) {
	ids, err := o.pending(ctx)
	if err != nil {
		slog.Warn("offline reconcile: could not read the package ledger", "err", err)
		return 0, 0
	}
	for i, id := range ids {
		ok, err := o.send(ctx, id)
		if errors.Is(err, errNoCentralTransport) {
			return reconciled, len(ids) - i
		}
		if err != nil || !ok {
			remaining++
			continue
		}
		if err := o.mark(ctx, id); err != nil {
			slog.Warn("offline reconcile: Central confirmed but the local stamp failed; will re-send (idempotent)",
				"package_id", id, "err", err)
			remaining++
			continue
		}
		reconciled++
	}
	return reconciled, remaining
}

func (s *server) offlineReconcilerFor() offlineReconciler {
	return offlineReconciler{
		pending: func(ctx context.Context) ([]string, error) {
			rows, err := s.db.Query(ctx, `SELECT package_id::text FROM edge_offline_packages
			                               WHERE reconciled_at IS NULL ORDER BY consumed_at LIMIT 100`)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			var ids []string
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					return nil, err
				}
				ids = append(ids, id)
			}
			return ids, rows.Err()
		},
		send: s.sendOfflineReconcile,
		mark: func(ctx context.Context, id string) error {
			_, err := s.db.Exec(ctx, `UPDATE edge_offline_packages SET reconciled_at=now()
			                           WHERE package_id=$1 AND reconciled_at IS NULL`, id)
			return err
		},
	}
}

// offlineReconcileLoop retries every unreconciled offline package until Central confirms it: shortly after
// boot, after every import (kicked), and then every offlineReconcileEvery.
func (s *server) offlineReconcileLoop(ctx context.Context) {
	if s.db == nil {
		return
	}
	rec := s.offlineReconcilerFor()
	pass := func() {
		pctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		if done, left := rec.runOnce(pctx); done > 0 || left > 0 {
			slog.Info("offline reconcile pass", "reconciled", done, "still_pending", left)
		}
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Minute):
	case <-s.reconcileKick:
	}
	pass()
	t := time.NewTicker(offlineReconcileEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pass()
		case <-s.reconcileKick:
			pass()
		}
	}
}

// kickOfflineReconcile asks the loop for an immediate pass (non-blocking).
func (s *server) kickOfflineReconcile() {
	if s.reconcileKick == nil {
		return
	}
	select {
	case s.reconcileKick <- struct{}{}:
	default:
	}
}
