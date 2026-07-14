package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// LocalUnixTransport calls scd over its local Unix socket. Only valid when
// ctrlapi and scd run on the same host (Phase 1–4 dev layout). applianceID
// is ignored — there's exactly one appliance on this machine.
type LocalUnixTransport struct {
	SocketPath string
	client     *http.Client
}

func NewLocalUnix(path string) *LocalUnixTransport {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}
	return &LocalUnixTransport{
		SocketPath: path,
		client:     &http.Client{Transport: tr, Timeout: 5 * time.Second},
	}
}

// do is a tiny JSON-in/JSON-out helper shared by all scd calls.
func (t *LocalUnixTransport) do(ctx context.Context, method, path string, reqBody, out any) (int, error) {
	var body io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, body)
	if err != nil {
		return 0, err
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil && err != io.EOF {
			return resp.StatusCode, fmt.Errorf("decode: %w", err)
		}
	}
	return resp.StatusCode, nil
}

func (t *LocalUnixTransport) Revoke(ctx context.Context, _ /*applianceID*/, ip, reason string) error {
	if reason == "" {
		reason = "admin"
	}
	body, _ := json.Marshal(map[string]string{"ip": ip, "reason": reason})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://unix/v1/sessions/revoke", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("scd revoke: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("scd revoke status=%d body=%s", resp.StatusCode, string(b))
	}
	return nil
}

func (t *LocalUnixTransport) PMSTest(ctx context.Context, _ /*applianceID*/, name string) (PMSTestResult, error) {
	var out PMSTestResult
	code, err := t.do(ctx, http.MethodPost, "/v1/admin/pms/"+url.PathEscape(name)+"/test", nil, &out)
	if err != nil {
		return PMSTestResult{}, fmt.Errorf("scd pms test: %w", err)
	}
	if code == http.StatusNotFound {
		return PMSTestResult{}, fmt.Errorf("provider not registered on appliance")
	}
	// scd returns 502 with {ok:false, error:...} when the probe fails;
	// surface that as a non-error result so the UI can render it.
	return out, nil
}

func (t *LocalUnixTransport) PMSCache(ctx context.Context, _ /*applianceID*/, name string, limit int) (PMSCacheResult, error) {
	path := "/v1/admin/pms/" + url.PathEscape(name) + "/cache"
	if limit > 0 {
		path += "?limit=" + strconv.Itoa(limit)
	}
	var out PMSCacheResult
	code, err := t.do(ctx, http.MethodGet, path, nil, &out)
	if err != nil {
		return PMSCacheResult{}, fmt.Errorf("scd pms cache: %w", err)
	}
	if code == http.StatusNotFound {
		return PMSCacheResult{}, fmt.Errorf("provider not registered on appliance")
	}
	if code == http.StatusNotImplemented {
		return PMSCacheResult{}, fmt.Errorf("provider doesn't support cache snapshot")
	}
	return out, nil
}

func (t *LocalUnixTransport) PMSHealth(ctx context.Context, _ /*applianceID*/, name string) (PMSHealthResult, error) {
	var out PMSHealthResult
	code, err := t.do(ctx, http.MethodGet, "/v1/admin/pms/"+url.PathEscape(name)+"/health", nil, &out)
	if err != nil {
		return PMSHealthResult{}, fmt.Errorf("scd pms health: %w", err)
	}
	if code == http.StatusNotFound {
		return PMSHealthResult{}, fmt.Errorf("provider not registered on appliance")
	}
	return out, nil
}
