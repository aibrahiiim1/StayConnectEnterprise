package main

// COMMAND-LINE ACCESS TO THE ONE BACKUP SYSTEM.
//
//	scd backup-now --reason "<what is about to change>" [--by <name>]
//	scd backup-restore --name <backup> --identity-file <key file> [--by <name>]
//
// These are not a second engine: each is a request to the running scd on its private socket, which is reachable only by root and
// the service group, and each is answered by the same code that serves Hotel Admin. backup-now is what a controlled migration or
// deployment runs first (scripts/edge-migrate.sh); backup-restore is the way to restore when Hotel Admin itself cannot be used.

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

func scdSocketPath() string { return envOr("SCD_SOCKET", "/run/stayconnect/scd.sock") }

func scdCall(method, path string, body any, timeout time.Duration) (int, []byte, error) {
	cl := &http.Client{Timeout: timeout, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", scdSocketPath())
		}}}
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, "http://scd"+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := cl.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, b, nil
}

// backupNowMain takes a pre-change backup and exits 0 only when it is complete, encrypted and proven restorable.
func backupNowMain(args []string) int {
	fs := flag.NewFlagSet("backup-now", flag.ContinueOnError)
	reason := fs.String("reason", "", "what is about to change (required)")
	by := fs.String("by", "", "who is asking")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if len(strings.TrimSpace(*reason)) < 4 {
		fmt.Fprintln(os.Stderr, "backup-now: --reason is required (what is about to change)")
		return 2
	}
	if *by == "" {
		*by = "controlled change"
	}
	code, b, err := scdCall(http.MethodPost, "/v1/backup/run", backupRunReq{Kind: kindPreChange, Reason: *reason, By: *by}, 45*time.Minute)
	if err != nil {
		fmt.Fprintln(os.Stderr, "backup-now: scd could not be reached:", err)
		return 1
	}
	var out map[string]any
	_ = json.Unmarshal(b, &out)
	if code != http.StatusOK {
		fmt.Fprintf(os.Stderr, "backup-now: no backup was taken (HTTP %d): %v %v\n", code, out["message"], out["error"])
		return 1
	}
	fmt.Printf("backup-now: %v (%v bytes, %v tables, ledger entry %v), encrypted and proven restorable\n", out["name"], out["size_bytes"], out["tables"], out["ledger_seq"])
	return 0
}

// backupRestoreMain starts a restore and waits for its recorded outcome.
func backupRestoreMain(args []string) int {
	fs := flag.NewFlagSet("backup-restore", flag.ContinueOnError)
	name := fs.String("name", "", "the backup to restore (a name from the Backups screen)")
	keyFile := fs.String("identity-file", "", "file holding a backup key holder's private key")
	by := fs.String("by", "", "who is asking")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *name == "" || *keyFile == "" {
		fmt.Fprintln(os.Stderr, "backup-restore: --name and --identity-file are required")
		return 2
	}
	key, err := os.ReadFile(*keyFile)
	if err != nil {
		fmt.Fprintln(os.Stderr, "backup-restore: the key file could not be read:", err)
		return 2
	}
	if *by == "" {
		*by = "command line"
	}
	started := time.Now()
	code, b, err := scdCall(http.MethodPost, "/v1/backup/restore", restoreReq{Name: *name, Identity: string(key), By: *by}, time.Minute)
	if err != nil {
		fmt.Fprintln(os.Stderr, "backup-restore: scd could not be reached:", err)
		return 1
	}
	if code != http.StatusAccepted {
		fmt.Fprintf(os.Stderr, "backup-restore: not started (HTTP %d): %s\n", code, strings.TrimSpace(string(b)))
		return 1
	}
	fmt.Println("backup-restore: started; waiting for the recorded outcome")
	for time.Since(started) < restoreOverallDeadline()+5*time.Minute {
		time.Sleep(5 * time.Second)
		c, rb, err := scdCall(http.MethodGet, "/v1/backup/last-restore", nil, 30*time.Second)
		if err != nil || c != http.StatusOK {
			continue // scd restarts the services around it; keep waiting
		}
		var res map[string]any
		if json.Unmarshal(rb, &res) != nil || res["present"] != true || res["running"] == true {
			continue
		}
		if t, _ := time.Parse(time.RFC3339, fmt.Sprint(res["started"])); !t.IsZero() && t.Before(started.Add(-2*time.Second)) {
			continue // an earlier restore's record
		}
		if res["ok"] == true {
			fmt.Printf("backup-restore: done. %v\nMoney movement is HELD until recovery is reconciled in Hotel Admin -> Financial recovery.\n", res["summary"])
			return 0
		}
		fmt.Fprintf(os.Stderr, "backup-restore: FAILED: %v\n", res["summary"])
		return 1
	}
	fmt.Fprintln(os.Stderr, "backup-restore: no outcome was recorded in time; read /run/stayconnect/last-restore.json")
	return 1
}
