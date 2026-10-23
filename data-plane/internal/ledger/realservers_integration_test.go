//go:build integration

package ledger

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"

	"filippo.io/age"
)

// The destinations against real servers (scripts/ledger-destinations-compat.sh starts them): Scality CloudServer for
// S3 SigV4 and OpenSSH's SFTP subsystem.

func TestRealS3_SignedRequestsAndAFullLedgerCommit(t *testing.T) {
	ep := os.Getenv("LEDGER_S3_ENDPOINT")
	if ep == "" {
		t.Skip("LEDGER_S3_ENDPOINT not set")
	}
	d := &S3Dest{Endpoint: ep, Region: "us-east-1", Bucket: "ledger-" + strconv.FormatInt(time.Now().UnixNano(), 36), Prefix: "site 1/ledger",
		AccessKey: os.Getenv("LEDGER_S3_KEY"), SecretKey: os.Getenv("LEDGER_S3_SECRET"), AllowLocal: true}
	ctx := context.Background()
	resp, err := d.signed(ctx, "PUT", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Fatalf("create bucket: %s", resp.Status)
	}
	if err := d.Check(ctx); err != nil {
		t.Fatalf("check: %v", err)
	}
	if _, err := d.Get(ctx, "nothing-here"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing key: %v", err)
	}
	bad := *d
	bad.SecretKey = "wrong-secret"
	if err := bad.Put(ctx, "x", []byte("y")); err == nil {
		t.Fatal("a wrong secret was accepted by a real S3")
	}
	nb := *d
	nb.Bucket = "no-such-bucket-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err := nb.Get(ctx, "x"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing bucket must be an error, not an empty ledger: %v", err)
	}

	id, _ := age.GenerateX25519Identity()
	st := NewStore(Config{LocalPath: t.TempDir() + "/l.jsonl", Dest: d, Cipher: func(context.Context) (*Cipher, error) {
		return &Cipher{Recipients: []age.Recipient{id.Recipient()}, Fingerprint: "k1"}, nil
	}})
	all := mk(5)
	if err := st.Commit(ctx, all[:2]); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := st.Commit(ctx, all[2:]); err != nil {
		t.Fatalf("commit: %v", err)
	}
	c := st.ReadExternal(ctx, id)
	if c.Reach != nil || !c.Intact() || len(c.Parse.Entries) != 5 {
		t.Fatalf("read back = %+v", c)
	}
	if _, ok, found, err := st.ReadExternalHead(ctx); err != nil || !ok || !found {
		t.Fatalf("head note: ok=%v found=%v err=%v", ok, found, err)
	}
}

func TestRealSFTP_PinnedHostKeyAndAFullLedgerCommit(t *testing.T) {
	host := os.Getenv("LEDGER_SFTP_HOST")
	if host == "" {
		t.Skip("LEDGER_SFTP_HOST not set")
	}
	port, _ := strconv.Atoi(os.Getenv("LEDGER_SFTP_PORT"))
	d := &SFTPDest{Host: host, Port: port, User: os.Getenv("LEDGER_SFTP_USER"), Password: os.Getenv("LEDGER_SFTP_PASSWORD"),
		Dir: "/upload/ledger-" + strconv.FormatInt(time.Now().UnixNano(), 36), AllowLocal: true, Timeout: 20 * time.Second}
	ctx := context.Background()
	fp, err := d.ProbeHostKey(ctx)
	if err != nil || fp == "" {
		t.Fatalf("probe: %q %v", fp, err)
	}
	if err := d.Put(ctx, "x", []byte("y")); err == nil {
		t.Fatal("an unpinned real server was used")
	}
	d.Fingerprint = fp
	if err := d.Check(ctx); err != nil {
		t.Fatalf("check: %v", err)
	}
	wrong := *d
	wrong.Fingerprint = "SHA256:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	if err := wrong.Put(ctx, "x", []byte("y")); err == nil {
		t.Fatal("a different host key was accepted by a real server")
	}
	st := NewStore(Config{LocalPath: t.TempDir() + "/l.jsonl", Dest: d})
	all := mk(4)
	if err := st.Commit(ctx, all[:2]); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := st.Commit(ctx, all[2:]); err != nil {
		t.Fatalf("commit: %v", err)
	}
	c := st.ReadExternal(ctx, nil)
	if c.Reach != nil || !c.Intact() || len(c.Parse.Entries) != 4 {
		t.Fatalf("read back = %+v", c)
	}
	b, err := d.Get(ctx, ExternalHead)
	if err != nil || len(b) == 0 {
		t.Fatalf("head: %v", err)
	}
}
