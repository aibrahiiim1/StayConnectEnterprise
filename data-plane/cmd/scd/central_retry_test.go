package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/assignment"
)

func TestClassifyAckStatus(t *testing.T) {
	for code, want := range map[int]ackOutcome{
		200: ackConfirmed, 204: ackConfirmed,
		500: ackRetry, 502: ackRetry, 503: ackRetry, 408: ackRetry, 429: ackRetry,
		400: ackRefused, 401: ackRefused, 403: ackRefused, 404: ackRefused, 409: ackRefused,
	} {
		if got := classifyAckStatus(code); got != want {
			t.Errorf("HTTP %d classified as %d, want %d", code, got, want)
		}
	}
}

// ackFixture is an appliance holding a TERMINAL assignment, and a Central that answers the acknowledgement
// with the scripted statuses in order (the last one repeats).
type ackFixture struct {
	srv   *server
	pub   ed25519.PublicKey
	doc   *assignment.Document
	calls atomic.Int32
}

func newAckFixture(t *testing.T, state string, answers ...int) *ackFixture {
	t.Helper()
	t.Setenv("SCD_ASSIGNMENT_DIR", t.TempDir())
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	f := &ackFixture{pub: pub, doc: &assignment.Document{
		AssignmentID: "a-1", ApplianceID: "appl-1", Version: 7, State: state, Signature: "sig"}}
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(f.calls.Add(1))
		if r.URL.Path != "/v1/appliance/assignment/ack" || r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var ack assignment.Ack
		if err := json.NewDecoder(r.Body).Decode(&ack); err != nil || !assignment.VerifyAck(pub, &ack) ||
			ack.Version != f.doc.Version || ack.Fingerprint != assignment.DocFingerprint(f.doc) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		code := answers[len(answers)-1]
		if n <= len(answers) {
			code = answers[n-1]
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(central.Close)
	f.srv = &server{applID: "appl-1", idPriv: priv,
		centralTransport: func() (*http.Client, string, bool) { return central.Client(), central.URL, true }}
	if err := f.srv.assignmentStore().Adopt(f.doc); err != nil {
		t.Fatal(err)
	}
	old := terminalAckBackoff
	terminalAckBackoff = []time.Duration{time.Millisecond}
	t.Cleanup(func() { terminalAckBackoff = old })
	return f
}

// The acknowledgement is retried through Central outages until Central confirms it, and then never again --
// not by this process and not by a restarted one.
func TestTerminalAckIsRetriedUntilCentralConfirms(t *testing.T) {
	f := newAckFixture(t, assignment.StateRevoked, 503, 502, 200)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	f.srv.runTerminalAck(ctx)
	if got := f.calls.Load(); got != 3 {
		t.Fatalf("Central saw %d acknowledgements, want 3 (two failures, then the confirmed one)", got)
	}
	if !f.srv.terminalAckSettled(f.doc) {
		t.Fatal("a confirmed acknowledgement was not recorded")
	}
	// Idempotent: a re-executed or restarted scd sees the confirmation and sends nothing.
	restarted := &server{applID: "appl-1", idPriv: f.srv.idPriv, centralTransport: f.srv.centralTransport}
	restarted.ensureTerminalAck(ctx)
	time.Sleep(50 * time.Millisecond)
	if got := f.calls.Load(); got != 3 {
		t.Fatalf("a confirmed acknowledgement was sent again (%d calls)", got)
	}
}

// A definitive refusal ends the retries: Central will not accept this acknowledgement however often it is sent.
func TestTerminalAckStopsOnADefinitiveRefusal(t *testing.T) {
	f := newAckFixture(t, assignment.StateDecommissioned, 409)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f.srv.runTerminalAck(ctx)
	if got := f.calls.Load(); got != 1 {
		t.Fatalf("a refused acknowledgement was sent %d times", got)
	}
	if !f.srv.terminalAckSettled(f.doc) {
		t.Fatal("the refusal was not recorded, so a restart would send it again")
	}
}

// No transport yet (the mTLS certificate is still being collected) is not an answer: keep trying.
func TestTerminalAckWaitsForTheTransport(t *testing.T) {
	f := newAckFixture(t, assignment.StateUnassigned, 200)
	var ready atomic.Bool
	real := f.srv.centralTransport
	f.srv.centralTransport = func() (*http.Client, string, bool) {
		if !ready.Load() {
			ready.Store(true) // available from the next attempt on
			return nil, "", false
		}
		return real()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	f.srv.runTerminalAck(ctx)
	if got := f.calls.Load(); got != 1 || !f.srv.terminalAckSettled(f.doc) {
		t.Fatalf("the acknowledgement was not delivered once the transport came up (%d calls)", got)
	}
}

// Only a TERMINAL assignment is acknowledged, and a newer non-terminal one supersedes a pending retry.
func TestTerminalAckOnlyForAnAdoptedTerminalAssignment(t *testing.T) {
	f := newAckFixture(t, assignment.StateAssigned, 200)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	f.srv.runTerminalAck(ctx)
	f.srv.ensureTerminalAck(ctx)
	time.Sleep(50 * time.Millisecond)
	if got := f.calls.Load(); got != 0 {
		t.Fatalf("a granting assignment was acknowledged as terminal (%d calls)", got)
	}
}

// ---- offline-package reconciliation --------------------------------------------------------------------------

func TestOfflineReconcilePassStampsOnlyWhatCentralConfirmed(t *testing.T) {
	marked := map[string]bool{}
	rec := offlineReconciler{
		pending: func(context.Context) ([]string, error) { return []string{"p1", "p2", "p3"}, nil },
		send: func(_ context.Context, id string) (bool, error) {
			switch id {
			case "p1":
				return true, nil
			case "p2":
				return false, errors.New("connection reset")
			default:
				return false, nil // Central answered but not 200
			}
		},
		mark: func(_ context.Context, id string) error { marked[id] = true; return nil },
	}
	done, left := rec.runOnce(context.Background())
	if done != 1 || left != 2 || !marked["p1"] || marked["p2"] || marked["p3"] {
		t.Fatalf("reconciled=%d remaining=%d marked=%v; want only p1 stamped", done, left, marked)
	}
}

func TestOfflineReconcilePassWaitsForTheTransport(t *testing.T) {
	sends := 0
	rec := offlineReconciler{
		pending: func(context.Context) ([]string, error) { return []string{"p1", "p2"}, nil },
		send: func(context.Context, string) (bool, error) {
			sends++
			return false, errNoCentralTransport
		},
		mark: func(context.Context, string) error { t.Fatal("nothing was confirmed"); return nil },
	}
	if done, left := rec.runOnce(context.Background()); done != 0 || left != 2 || sends != 1 {
		t.Fatalf("done=%d left=%d sends=%d; with no transport the pass stops at the first package", done, left, sends)
	}
}

func TestSendOfflineReconcile(t *testing.T) {
	var status atomic.Int32
	status.Store(http.StatusServiceUnavailable)
	central := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if r.URL.Path != "/v1/appliance/offline-reconcile" || r.Header.Get("Authorization") == "" ||
			json.NewDecoder(r.Body).Decode(&body) != nil || body["package_id"] != "pkg-1" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(int(status.Load()))
	}))
	defer central.Close()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	s := &server{applID: "appl-1", idPriv: priv,
		centralTransport: func() (*http.Client, string, bool) { return central.Client(), central.URL, true }}
	if ok, err := s.sendOfflineReconcile(context.Background(), "pkg-1"); ok || err != nil {
		t.Fatalf("a 503 was taken as reconciled (ok=%v err=%v)", ok, err)
	}
	status.Store(http.StatusOK)
	if ok, err := s.sendOfflineReconcile(context.Background(), "pkg-1"); !ok || err != nil {
		t.Fatalf("a 200 was not taken as reconciled (ok=%v err=%v)", ok, err)
	}
	s.centralTransport = func() (*http.Client, string, bool) { return nil, "", false }
	if _, err := s.sendOfflineReconcile(context.Background(), "pkg-1"); !errors.Is(err, errNoCentralTransport) {
		t.Fatalf("no transport should say so, got %v", err)
	}
}
