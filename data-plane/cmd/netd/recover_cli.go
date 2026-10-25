package main

// Local recovery for an administrator at the appliance's console, when the Admin Console cannot be reached:
//
//	netd recover status               what the running service says about the confirmed configuration and whether changes are refused
//	netd recover reassert-confirmed   put the appliance back on its last confirmed configuration (from the database, or from the saved snapshot when the
//	                                  database is down), through the same reconcile and health checks as a restart, and record it
//
// It talks to the running service over its socket, so it is limited to the callers the service admits (root, as installed). Restoring factory defaults or a
// full reset needs the database and the Admin Console's review of what would change; it is deliberately not offered here.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

func (s *server) reassertConfirmed(w http.ResponseWriter, r *http.Request) {
	converged := s.ap.ReconcileActiveOnBoot(r.Context())
	m, blocked := s.ap.rollbackIncomplete()
	out := map[string]any{"converged": converged, "changes_refused": blocked}
	if blocked {
		out["detail"] = m.describe()
	}
	writeJSON(w, http.StatusOK, out)
}

func runRecoverCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "status" && args[0] != "reassert-confirmed") {
		fmt.Fprintln(stderr, "usage: netd recover status | reassert-confirmed")
		return 2
	}
	sock := envOr("NETD_SOCKET", "/run/stayconnect/netd.sock")
	hc := &http.Client{Timeout: 5 * time.Minute, Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	method, path := http.MethodGet, "/v1/health"
	if args[0] == "reassert-confirmed" {
		method, path = http.MethodPost, "/v1/reassert-confirmed"
	}
	req, _ := http.NewRequest(method, "http://netd"+path, bytes.NewReader(nil))
	resp, err := hc.Do(req)
	if err != nil {
		fmt.Fprintf(stderr, "the network service is not answering on %s (%v). Start it with: systemctl start stayconnect-netd\n", sock, err)
		return 1
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		fmt.Fprintf(stderr, "refused (%d): %s\n", resp.StatusCode, bytes.TrimSpace(body))
		return 1
	}
	var doc map[string]any
	if json.Unmarshal(body, &doc) != nil {
		fmt.Fprintln(stdout, string(body))
		return 0
	}
	pretty, _ := json.MarshalIndent(doc, "", "  ")
	fmt.Fprintln(stdout, string(pretty))
	if args[0] == "reassert-confirmed" && doc["converged"] != true {
		return 1
	}
	return 0
}
