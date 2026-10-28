package main

// POST /v1/flows/end revokes the address's Phase-3 element (on every bridge carrying it, or the one named),
// proves it gone and deletes every conntrack entry naming it. The tc class is left to the reconciliation, which
// removes it once durable state stops listing the session. Revoking early only removes access: if durable state
// still lists the session the next pass re-admits it, so a caller cannot use this to grant anything.
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
	// Bridge narrows the revocation to one ingress bridge; empty means every one.
	Bridge string `json:"bridge,omitempty"`
}

type flowsEndResp struct {
	IP            string `json:"ip"`
	Phase3Revoked int    `json:"phase3_revoked"`
	// Phase3Managed: when true the tc class belongs to netd and the caller must not delete it.
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

// endFlows flushes after the revocation so a flow cannot re-establish against a still-present element.
func (s *server) endFlows(ctx context.Context, ip net.IP, bridge string, flows conntrack.Flusher) (flowsEndResp, error) {
	out := flowsEndResp{IP: ip.String()}
	var problems []error
	if p := s.phase3; p != nil && p.gate != nil {
		// A closure so the lock is released by defer: a panic in a gate call must not leave enforcement locked (audit Phase 8 L8).
		func() {
			p.mu.Lock()
			defer p.mu.Unlock()
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
		}()
	}
	// Flows end whatever the sets said; the legacy set is the caller's to revoke.
	n, err := flows.FlushAddrs(ctx, []net.IP{ip})
	out.FlowsEnded = n
	if err != nil {
		problems = append(problems, err)
	}
	return out, errors.Join(problems...)
}

// runFlowsEndCommand is `netd flows-end --ip A.B.C.D`: the same flush in the calling network namespace, with no
// database or socket (driven by scripts/flow-teardown-netns-test.sh).
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
