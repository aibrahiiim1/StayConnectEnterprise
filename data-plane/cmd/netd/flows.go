package main

// POST /v1/flows/end — END AN ADDRESS'S ACCESS AND ITS OPEN FLOWS NOW.
//
// netd ends access on its own whenever durable state says so (the Phase-3 reconciliation, flushingGate). One
// path did not go through netd: scd's staff disconnect (cmd/scd/main.go denyAtGate) removed the address from the
// legacy set and deleted the tc class directly, then waited for netd's next pass to revoke the Phase-3
// element. Until that pass the guest stayed authorized with NO class -- every open flow, and any new one,
// forwarded unshaped through the bridge's default class and uncounted (audit B-1, F-14).
//
// This route is that path's kernel half, owned by the component that owns the kernel state: the Phase-3
// element for the address is revoked (on every bridge that carries it, or the one named) and proven gone, and
// every conntrack entry naming the address is deleted. The tc class is left to the reconciliation, which removes
// it once durable state no longer lists the session -- by then nothing can reach the class, because nothing is
// authorized. Revoking ahead of the reconciliation only ever REMOVES access; if durable state still lists the
// session the next pass re-admits it, so a caller cannot use this to grant anything.
//
// Callers: NETD_FLOW_CALLERS (scd). See caller_authz.go.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"

	"github.com/stayconnect/enterprise/data-plane/internal/conntrack"
)

type flowsEndReq struct {
	IP string `json:"ip"`
	// Bridge narrows the revocation to one ingress bridge. Empty: every Phase-3 element for the address.
	Bridge string `json:"bridge,omitempty"`
}

type flowsEndResp struct {
	IP string `json:"ip"`
	// Phase3Revoked is how many Phase-3 authorizations for the address were removed (and proven gone).
	Phase3Revoked int `json:"phase3_revoked"`
	// Phase3Managed reports whether the address was in the Phase-3 set at all. When it was, its tc class belongs
	// to netd and the caller must NOT delete it.
	Phase3Managed bool `json:"phase3_managed"`
	FlowsEnded    int  `json:"flows_ended"`
}

func (s *server) flowsEnd(w http.ResponseWriter, r *http.Request) {
	var req flowsEndReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "malformed body"})
		return
	}
	ip := net.ParseIP(req.IP).To4()
	if ip == nil || ip.IsUnspecified() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ip must be an IPv4 address"})
		return
	}
	out, err := s.endFlows(r.Context(), ip, req.Bridge, conntrack.New())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error(), "result": out})
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// endFlows revokes the address's Phase-3 authorization(s), then flushes its flows. The flush runs AFTER the
// revocation, so a flow cannot re-establish against a still-present element.
func (s *server) endFlows(ctx context.Context, ip net.IP, bridge string, flows conntrack.Flusher) (flowsEndResp, error) {
	out := flowsEndResp{IP: ip.String()}
	var problems []error
	if p := s.phase3; p != nil && p.gate != nil {
		p.mu.Lock()
		els, err := p.gate.List(ctx)
		if err != nil {
			problems = append(problems, fmt.Errorf("phase-3 authorization set unreadable: %w", err))
		}
		for _, el := range els {
			if el.IP == nil || !el.IP.Equal(ip) || (bridge != "" && el.Bridge != bridge) {
				continue
			}
			out.Phase3Managed = true
			if derr := p.denyAccess(ctx, el.Bridge, el.IP); derr != nil {
				problems = append(problems, fmt.Errorf("phase-3 authorization on %s not proven removed: %w", el.Bridge, derr))
				continue
			}
			out.Phase3Revoked++
		}
		p.mu.Unlock()
	}
	// Whatever the sets said, the address's flows end. The legacy set is the caller's to revoke (scd removed it
	// before calling), and a flow from either set dies the same way.
	n, err := flows.FlushAddrs(ctx, []net.IP{ip})
	out.FlowsEnded = n
	if err != nil {
		problems = append(problems, err)
	}
	return out, errors.Join(problems...)
}

// runFlowsEndCommand is `netd flows-end --ip A.B.C.D`: the same flush, in the CALLING network namespace, with no
// database and no socket. scripts/flow-teardown-netns-test.sh drives it inside a disposable router namespace
// (`ip netns exec <router> netd flows-end ...`) to prove that a long transfer stops once its flows are flushed.
func runFlowsEndCommand(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("flows-end", flag.ContinueOnError)
	fs.SetOutput(stderr)
	ipFlag := fs.String("ip", "", "IPv4 address whose conntrack entries are deleted")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ip := net.ParseIP(*ipFlag).To4()
	if ip == nil {
		fmt.Fprintln(stderr, "flows-end: --ip must be an IPv4 address")
		return 2
	}
	n, err := conntrack.New().FlushAddrs(context.Background(), []net.IP{ip})
	if err != nil {
		fmt.Fprintln(stderr, "flows-end:", err)
		return 1
	}
	fmt.Fprintf(stdout, "%d\n", n)
	return 0
}
