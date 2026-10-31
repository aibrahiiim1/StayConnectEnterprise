//go:build integration

package ledger

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// writeRandom makes a local file of n random bytes and returns its path and SHA-256.
func writeRandom(t *testing.T, n int) (string, string) {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "set.tar.age")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	sum, size, err := FileDigest(p)
	if err != nil || size != int64(n) {
		t.Fatalf("local digest: %v %d", err, size)
	}
	return p, sum
}

// The 2026-10-31 SFTP corrections against OpenSSH in a chroot (atmoz/sftp chroots every account to its home): a folder relative to
// the login folder works and the path the SERVER reports is shown; a whole file is streamed and read back by SHA-256; a folder the
// account cannot write is refused and leaves no temporary file; ".." is refused before anything is sent.
func TestRealSFTP_RelativeFolderInAChrootAndAWholeFileReadBack(t *testing.T) {
	host := os.Getenv("LEDGER_SFTP_HOST")
	if host == "" {
		t.Skip("LEDGER_SFTP_HOST not set")
	}
	port, _ := strconv.Atoi(os.Getenv("LEDGER_SFTP_PORT"))
	rel := "upload/rel-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	d := &SFTPDest{Host: host, Port: port, User: os.Getenv("LEDGER_SFTP_USER"), Password: os.Getenv("LEDGER_SFTP_PASSWORD"),
		Dir: rel, AllowLocal: true, Timeout: 20 * time.Second}
	ctx := context.Background()
	fp, err := d.ProbeHostKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	d.Fingerprint = fp
	if err := d.Check(ctx); err != nil {
		t.Fatalf("a relative folder was refused: %v", err)
	}
	loc, err := d.Locate(ctx)
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	// Inside the chroot the server resolves the folder to an absolute path ending in what was typed.
	if !strings.HasPrefix(loc.Where, "/") || !strings.HasSuffix(loc.Where, rel) {
		t.Fatalf("the resolved folder %q is not the server's absolute path of %q", loc.Where, rel)
	}
	local, sum := writeRandom(t, 3<<20)
	if err := d.PutFile(ctx, "set-1.tar.age", local); err != nil {
		t.Fatalf("put file: %v", err)
	}
	got, size, err := d.Digest(ctx, "set-1.tar.age")
	if err != nil || got != sum || size != 3<<20 {
		t.Fatalf("read back %s/%d, want %s/%d (%v)", got, size, sum, 3<<20, err)
	}
	if _, _, err := d.Digest(ctx, "absent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a missing file: %v", err)
	}
	// A folder the account cannot write: refused, and nothing is left behind.
	cl, closeFn, err := d.connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer closeFn()
	if err := cl.Chmod(d.Dir, 0o555); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	defer func() { _ = cl.Chmod(d.Dir, 0o755) }()
	if err := d.Put(ctx, "probe", []byte("x")); err == nil {
		t.Fatal("a write into a read-only folder was reported as done")
	}
	entries, err := cl.ReadDir(d.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".partial") {
			t.Fatalf("a failed write left %s behind", e.Name())
		}
	}
	bad := *d
	bad.Dir = "upload/../etc"
	if err := bad.Check(ctx); err == nil || !strings.Contains(err.Error(), "..") {
		t.Fatalf("a folder with .. was not refused: %v", err)
	}
}

// S3 streaming upload (signed with the precomputed SHA-256, never UNSIGNED-PAYLOAD) and read-back, against a server that checks
// SigV4 itself.
func TestRealS3_AWholeFileIsStreamedSignedAndReadBack(t *testing.T) {
	ep := os.Getenv("LEDGER_S3_ENDPOINT")
	if ep == "" {
		t.Skip("LEDGER_S3_ENDPOINT not set")
	}
	d := &S3Dest{Endpoint: ep, Region: "us-east-1", Bucket: "files-" + strconv.FormatInt(time.Now().UnixNano(), 36), Prefix: "site 1/backups",
		AccessKey: os.Getenv("LEDGER_S3_KEY"), SecretKey: os.Getenv("LEDGER_S3_SECRET"), AllowLocal: true}
	ctx := context.Background()
	resp, err := d.signed(ctx, "PUT", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	local, sum := writeRandom(t, 5<<20)
	if err := d.PutFile(ctx, "set-1.tar.age", local); err != nil {
		t.Fatalf("put file: %v", err)
	}
	got, size, err := d.Digest(ctx, "set-1.tar.age")
	if err != nil || got != sum || size != 5<<20 {
		t.Fatalf("read back %s/%d, want %s (%v)", got, size, sum, err)
	}
	bad := *d
	bad.SecretKey = "wrong-secret"
	if err := bad.PutFile(ctx, "set-2.tar.age", local); err == nil {
		t.Fatal("a streamed upload with a wrong secret was accepted")
	}
	loc, err := d.Locate(ctx)
	if err != nil || !strings.Contains(loc.Where, d.Bucket) {
		t.Fatalf("locate: %+v %v", loc, err)
	}
}
