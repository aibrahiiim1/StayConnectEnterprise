package main

// Terminal-assignment acks and offline-package reconciliation are retried idempotently until Central confirms (its retirement flow waits on the ack).
// Neither is on a guest path: a Central outage only delays them.

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

type ackOutcome int

const (
	// ackConfirmed: Central accepted the acknowledgement (2xx). Done for this document.
	ackConfirmed ackOutcome = iota
	// ackRetry: nothing definitive -- no transport yet, a network error, a timeout, 408/429 or a 5xx.
	ackRetry
	// ackRefused: Central answered definitively and will not accept this ack however often it is repeated
	// (409, 404, 400, 401/403); retrying would only hammer it.
	ackRefused
)

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

// terminalAckBackoff is quick at first (a transport coming up), then gentle on a down Central. A var so tests can shorten it.
var terminalAckBackoff = []time.Duration{
	15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute, 15 * time.Minute,
}

func backoffAt(steps []time.Duration, attempt int) time.Duration {
	if attempt < len(steps) {
		return steps[attempt]
	}
	return steps[len(steps)-1]
}

// terminalAckRecord marks that Central has answered, so a restart or re-exec does not resend a confirmed ack but does resend an unconfirmed one.
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

func (s *server) adoptedTerminal() (*assignment.Document, time.Time) {
	rec, err := s.assignmentStore().Load()
	if err != nil || rec == nil || rec.Current == nil || !assignment.Clears(rec.Current.State) {
		return nil, time.Time{}
	}
	at, _ := time.Parse(time.RFC3339, rec.AdoptedAt)
	return rec.Current, at
}

// deliverTerminalAck makes ONE signed attempt and classifies the answer.
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

type terminalAckRunner struct {
	mu      sync.Mutex
	running bool
}

// ensureTerminalAck starts the retry loop when a terminal assignment is unanswered; called on adoption and agent start, idempotent.
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

// offlineReconcileEvery is how often unreconciled packages are offered to Central again. A var for tests.
var offlineReconcileEvery = 10 * time.Minute

var errNoCentralTransport = errors.New("central mTLS transport not ready")

// sendOfflineReconcile: Central answers 200 both when it recorded the consumption and when it has no matching package, so 200 ends it; anything else is retried.
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

// offlineReconciler is the retry pass; its steps are functions so it can be tested without a database or Central.
type offlineReconciler struct {
	pending func(ctx context.Context) ([]string, error)
	send    func(ctx context.Context, packageID string) (bool, error)
	mark    func(ctx context.Context, packageID string) error
}

// runOnce stops early when the transport is missing: every remaining call would fail the same way.
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

func (s *server) kickOfflineReconcile() {
	if s.reconcileKick == nil {
		return
	}
	select {
	case s.reconcileKick <- struct{}{}:
	default:
	}
}
