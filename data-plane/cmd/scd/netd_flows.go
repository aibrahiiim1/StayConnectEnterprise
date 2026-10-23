package main

// The kernel half of a staff disconnect is done by netd, which owns the kernel state: removing an address from a set never
// ends an open flow (the forward chain accepts established traffic first), and deleting the tc class early left the guest
// authorized with no class, unshaped and uncounted. netd's POST /v1/flows/end revokes the Phase-3 authorization and deletes
// every conntrack entry naming the address; when netd manages the address its tc class is left to netd's reconciliation.

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
