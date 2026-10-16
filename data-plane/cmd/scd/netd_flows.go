package main

// THE KERNEL HALF OF A STAFF DISCONNECT, DONE BY THE COMPONENT THAT OWNS THE KERNEL STATE.
//
// denyAtGate used to remove the address from the legacy set and delete its tc class itself, then wait for
// netd's next pass to revoke the Phase-3 authorization. In between, the guest was still authorized with NO
// class: every open flow -- and any new one -- forwarded unshaped through the bridge's default class, uncounted
// (audit B-1 / F-14). Removing an address from a set also never ended a flow that was already open, because the
// forward chain accepts established traffic before it consults the sets.
//
// netd's POST /v1/flows/end revokes the Phase-3 authorization for the address (proven gone) and deletes every
// conntrack entry naming it. When netd reports the address as Phase-3-managed, its tc class is netd's and is
// left to netd's reconciliation, which removes it once durable state no longer lists the session.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

// flowEnder ends an address's access and open flows. nil disables the call (tests without netd).
type flowEnder interface {
	EndFlows(ctx context.Context, ip net.IP) (flowsEnded, error)
}

type flowsEnded struct {
	Phase3Managed bool `json:"phase3_managed"`
	Phase3Revoked int  `json:"phase3_revoked"`
	FlowsEnded    int  `json:"flows_ended"`
}

type netdFlows struct {
	http *http.Client
}

func newNetdFlows(socket string) *netdFlows {
	return &netdFlows{http: &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		}},
	}}
}

func (n *netdFlows) EndFlows(ctx context.Context, ip net.IP) (flowsEnded, error) {
	body, _ := json.Marshal(map[string]string{"ip": ip.String()})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://netd/v1/flows/end", bytes.NewReader(body))
	if err != nil {
		return flowsEnded{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := n.http.Do(req)
	if err != nil {
		return flowsEnded{}, err
	}
	defer resp.Body.Close()
	var out struct {
		flowsEnded
		Result *flowsEnded `json:"result"`
		Error  string      `json:"error"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK {
		// A partial result still says whether netd manages the address, which decides who owns the class.
		if out.Result != nil {
			return *out.Result, fmt.Errorf("netd /v1/flows/end: %s: %s", resp.Status, out.Error)
		}
		return flowsEnded{}, fmt.Errorf("netd /v1/flows/end: %s: %s", resp.Status, out.Error)
	}
	return out.flowsEnded, nil
}
