package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"time"
)

// netdClient talks to the privileged netd daemon over its unix socket. edged is
// unprivileged; all actual network changes happen inside netd. This mirrors the
// scdClient pattern.
type netdClient struct {
	http *http.Client
}

func newNetdClient(socket string) *netdClient {
	return &netdClient{http: &http.Client{
		Timeout: 100 * time.Second, // apply can take a while (netplan/kea/health)
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		},
	}}
}

func (c *netdClient) call(ctx context.Context, method, path string, body any) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rd = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, rd)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return resp.StatusCode, raw, err
}

func (c *netdClient) proxy(w http.ResponseWriter, r *http.Request, method, path string, body any) {
	st, raw, err := c.call(r.Context(), method, path, body)
	if err != nil {
		jsonErr(w, http.StatusBadGateway, "netd_unreachable", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(st)
	_, _ = w.Write(raw)
}
