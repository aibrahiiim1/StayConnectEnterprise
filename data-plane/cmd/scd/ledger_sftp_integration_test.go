//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"github.com/stayconnect/enterprise/data-plane/internal/ledger"
	"github.com/stayconnect/enterprise/data-plane/internal/sealbox"
	"golang.org/x/crypto/ssh"
)

// chmodOnServer makes a folder on the SFTP server read-only for the account (the host key is still checked).
func chmodOnServer(t *testing.T, host string, port int, user, pw, fp, dir string) {
	t.Helper()
	cfg := &ssh.ClientConfig{User: user, Auth: []ssh.AuthMethod{ssh.Password(pw)}, Timeout: 20 * time.Second,
		HostKeyCallback: func(_ string, _ net.Addr, k ssh.PublicKey) error {
			if ssh.FingerprintSHA256(k) != fp {
				return errors.New("host key changed")
			}
			return nil
		}}
	c, err := ssh.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(port)), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cl, err := sftp.NewClient(c)
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	if err := cl.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
}

// THE SFTP FALSE SUCCESS (2026-10-31), against OpenSSH in a chroot (atmoz/sftp; LEDGER_SFTP_* as for
// scripts/ledger-destinations-compat.sh). Save answers OK only when every existing ledger entry is in the folder the SERVER
// shows, proven by reading it back there; a folder the account cannot write is "not saved" and leaves the previous setting in
// use; Remove clears the setting, keeps the remote copy, and every change is in the history.
func TestIntegrationLedger_SFTPSaveIsTrueOnlyWhenTheLedgerIsReadBackThere(t *testing.T) {
	host := os.Getenv("LEDGER_SFTP_HOST")
	if host == "" {
		t.Skip("LEDGER_SFTP_HOST not set")
	}
	port, _ := strconv.Atoi(os.Getenv("LEDGER_SFTP_PORT"))
	user, pw := os.Getenv("LEDGER_SFTP_USER"), os.Getenv("LEDGER_SFTP_PASSWORD")
	r := newLedgerRig(t)
	k, err := sealbox.NewKey(ledgerSecretDomain, bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	r.s.ledgerRT.key = k
	r.reconcile()
	for i := 0; i < 3; i++ {
		r.appendCheckpoint()
	}
	r.reconcile()
	ctx := context.Background()
	probe := &ledger.SFTPDest{Host: host, Port: port, User: user, Password: pw, AllowLocal: true, Timeout: 20 * time.Second}
	fp, err := probe.ProbeHostKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method string, v map[string]any) (int, map[string]any) {
		b, _ := json.Marshal(v)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/v1/ledger/destination", bytes.NewReader(b))
		if method == http.MethodDelete {
			r.s.ledgerDestinationDelete(rec, req)
		} else {
			r.s.ledgerDestinationPut(rec, req)
		}
		out := map[string]any{}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	rel := "upload/scd-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	sftp := func(dir string) map[string]any {
		return map[string]any{"kind": "sftp", "host": host, "port": port, "user": user, "password": pw, "dir": dir, "fingerprint": fp, "by": "operator"}
	}

	// A connectivity test is labelled as such and copies nothing.
	if code, out := call(http.MethodPut, map[string]any{"kind": "sftp", "host": host, "port": port, "user": user, "password": pw,
		"dir": rel, "fingerprint": fp, "test_only": true}); code != 200 || out["test"] != "connectivity" || out["replicated"] != false {
		t.Fatalf("connectivity test = %d %v", code, out)
	}

	code, out := call(http.MethodPut, sftp(rel))
	if code != 200 || out["ok"] != true || out["saved"] != true {
		t.Fatalf("save = %d %v", code, out)
	}
	loc, _ := out["location"].(map[string]any)
	if where, _ := loc["where"].(string); !strings.HasPrefix(where, "/") || !strings.HasSuffix(where, rel) {
		t.Fatalf("the folder the server resolved is not shown: %v", loc)
	}
	proof, _ := out["initial_copy"].(map[string]any)
	if proof["written"] != true || int64(proof["entries"].(float64)) != r.dbSeq() {
		t.Fatalf("the initial copy did not carry every existing entry: %v (database %d)", proof, r.dbSeq())
	}
	// Read it back independently, by SHA-256, from the folder the server shows.
	reader := &ledger.SFTPDest{Host: host, Port: port, User: user, Password: pw, Dir: rel, Fingerprint: fp, AllowLocal: true, Timeout: 20 * time.Second}
	sum, _, err := reader.Digest(ctx, proof["object"].(string))
	if err != nil || sum != proof["object_sha256"] {
		t.Fatalf("the ledger object at the server: %s %v, want %v", sum, err, proof["object_sha256"])
	}

	// A folder the account cannot write: not saved, and the previous setting stays in use.
	ro := rel + "-ro"
	cl := &ledger.SFTPDest{Host: host, Port: port, User: user, Password: pw, Dir: ro, Fingerprint: fp, AllowLocal: true, Timeout: 20 * time.Second}
	if err := cl.Put(ctx, "seed", []byte("x")); err != nil {
		t.Fatal(err)
	}
	chmodOnServer(t, host, port, user, pw, fp, ro)
	if code, out := call(http.MethodPut, sftp(ro)); code == 200 || out["saved"] == true || out["ok"] == true {
		t.Fatalf("a read-only folder was reported saved: %d %v", code, out)
	}
	if got := r.s.ledgerRT.store.Destination().Describe(); !strings.Contains(got, rel) || strings.Contains(got, "-ro") {
		t.Fatalf("a failed save changed the storage in use: %s", got)
	}

	// Remove: the setting goes, the remote copy stays, both changes are in the history.
	if code, out := call(http.MethodDelete, map[string]any{"by": "operator"}); code != 200 || out["removed"] != true {
		t.Fatalf("remove = %d %v", code, out)
	}
	if _, _, err := reader.Digest(ctx, proof["object"].(string)); err != nil {
		t.Fatalf("remove deleted the remote copy: %v", err)
	}
	var hist []map[string]any
	var raw []byte
	if err := r.pool.QueryRow(ctx, `SELECT public.ledger_destination_history(20)`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &hist)
	if len(hist) < 2 || !strings.Contains(string(raw), "sftp") || strings.Contains(string(raw), pw) {
		t.Fatalf("history = %s", raw)
	}
}
