package transport

// NATSTransport reaches scd over NATS request/reply — the production path
// for phase 5.2 and beyond. Subjects mirror scd/nats.go:
//
//   scd.{applianceID}.revoke
//   scd.{applianceID}.pms.test
//   scd.{applianceID}.pms.cache
//   scd.{applianceID}.pms.health
//
// Replies carry a "Nats-Status" header with an HTTP-ish status code so the
// client can distinguish transport/protocol errors from scd-side errors.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/nats-io/nats.go"
)

type NATSTransport struct {
	nc      *nats.Conn
	timeout time.Duration
}

// NewNATS builds a transport backed by the given NATS connection. Callers
// own the connection lifecycle; Close() is a no-op here so a single nc can
// back both the transport and (future) config-push publishers.
func NewNATS(nc *nats.Conn, timeout time.Duration) *NATSTransport {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &NATSTransport{nc: nc, timeout: timeout}
}

func (t *NATSTransport) request(ctx context.Context, applianceID, method string, in, out any) (int, error) {
	if applianceID == "" {
		return 0, errors.New("nats transport: empty applianceID")
	}
	subj := "scd." + applianceID + "." + method
	var body []byte
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = b
	}
	rctx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()
	msg, err := t.nc.RequestWithContext(rctx, subj, body)
	if err != nil {
		return 0, fmt.Errorf("nats request %q: %w", subj, err)
	}
	status := 200
	if s := msg.Header.Get("Nats-Status"); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			status = n
		}
	}
	if out != nil && len(msg.Data) > 0 {
		// Best-effort: some error replies shape differently from `out`; we
		// don't fail the caller just because the shape doesn't match.
		_ = json.Unmarshal(msg.Data, out)
	}
	return status, nil
}

type errEnvelope struct {
	Error   string `json:"error,omitempty"`
	Message string `json:"message,omitempty"`
}

// ----- ApplianceTransport implementation -----

func (t *NATSTransport) Revoke(ctx context.Context, applianceID, ip, reason string) error {
	if reason == "" {
		reason = "admin"
	}
	var rep errEnvelope
	status, err := t.request(ctx, applianceID, "revoke",
		map[string]string{"ip": ip, "reason": reason}, &rep)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("scd revoke status=%d %s", status, firstNonEmpty(rep.Message, rep.Error))
	}
	return nil
}

func (t *NATSTransport) PMSTest(ctx context.Context, applianceID, name string) (PMSTestResult, error) {
	var out PMSTestResult
	// scd returns 502 with {ok:false, error} when the probe fails — surface
	// that as a non-error result (same behavior as the Unix transport).
	status, err := t.request(ctx, applianceID, "pms.test", map[string]any{"name": name}, &out)
	if err != nil {
		return PMSTestResult{}, err
	}
	if status == 404 {
		return PMSTestResult{}, fmt.Errorf("provider not registered on appliance")
	}
	if status == 501 {
		return PMSTestResult{}, fmt.Errorf("provider doesn't support TestConnection")
	}
	return out, nil
}

func (t *NATSTransport) PMSCache(ctx context.Context, applianceID, name string, limit int) (PMSCacheResult, error) {
	var out PMSCacheResult
	status, err := t.request(ctx, applianceID, "pms.cache",
		map[string]any{"name": name, "limit": limit}, &out)
	if err != nil {
		return PMSCacheResult{}, err
	}
	if status == 404 {
		return PMSCacheResult{}, fmt.Errorf("provider not registered on appliance")
	}
	if status == 501 {
		return PMSCacheResult{}, fmt.Errorf("provider doesn't support cache snapshot")
	}
	if status != 200 {
		return PMSCacheResult{}, fmt.Errorf("scd pms cache status=%d", status)
	}
	return out, nil
}

func (t *NATSTransport) PMSHealth(ctx context.Context, applianceID, name string) (PMSHealthResult, error) {
	var out PMSHealthResult
	status, err := t.request(ctx, applianceID, "pms.health", map[string]any{"name": name}, &out)
	if err != nil {
		return PMSHealthResult{}, err
	}
	if status == 404 {
		return PMSHealthResult{}, fmt.Errorf("provider not registered on appliance")
	}
	if status != 200 {
		return PMSHealthResult{}, fmt.Errorf("scd pms health status=%d", status)
	}
	return out, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
