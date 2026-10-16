package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"testing"
)

type recordingFlows struct {
	calls []string
	res   flowsEnded
	err   error
}

func (r *recordingFlows) EndFlows(_ context.Context, ip net.IP) (flowsEnded, error) {
	r.calls = append(r.calls, ip.String())
	return r.res, r.err
}

// A staff disconnect asks netd to end the address's flows, whether or not netd manages it.
func TestDenyAtGateEndsTheFlowsThroughNetd(t *testing.T) {
	for _, tc := range []recordingFlows{
		{res: flowsEnded{Phase3Managed: true, Phase3Revoked: 1, FlowsEnded: 3}},
		{res: flowsEnded{}},
		{err: errors.New("netd unreachable")},
	} {
		tc := tc
		s := &server{flows: &tc}
		s.denyAtGate(context.Background(), net.ParseIP("10.20.0.50"))
		if len(tc.calls) != 1 || tc.calls[0] != "10.20.0.50" {
			t.Fatalf("netd was not asked to end the flows: %v", tc.calls)
		}
	}
}

// The client speaks netd's contract over the unix socket and reports whether netd owns the class.
func TestNetdFlowsClient(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "netd.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("unix sockets unavailable here: %v", err)
	}
	var got map[string]string
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/flows/end" {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		if got["ip"] == "10.20.0.99" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"flush failed","result":{"phase3_managed":true,"phase3_revoked":1,"flows_ended":0}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ip":"10.20.0.50","phase3_managed":true,"phase3_revoked":1,"flows_ended":4}`))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	c := newNetdFlows(sock)
	res, err := c.EndFlows(context.Background(), net.ParseIP("10.20.0.50"))
	if err != nil || !res.Phase3Managed || res.Phase3Revoked != 1 || res.FlowsEnded != 4 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if got["ip"] != "10.20.0.50" {
		t.Fatalf("netd received %v", got)
	}
	// A failure still carries netd's statement that it owns the class, so scd does not delete it.
	res, err = c.EndFlows(context.Background(), net.ParseIP("10.20.0.99"))
	if err == nil || !res.Phase3Managed {
		t.Fatalf("partial result lost: res=%+v err=%v", res, err)
	}
}
