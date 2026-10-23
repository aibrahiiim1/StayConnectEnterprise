package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/backupcrypt"
)

// Nightly backup encrypt/decrypt round trip, and the reader verify and restore load from.

func TestBackupCryptCLIRoundTrip(t *testing.T) {
	dir := t.TempDir()
	secret, public, _ := backupcrypt.GenerateKey()
	_, other, _ := backupcrypt.GenerateKey()
	rcpt := filepath.Join(dir, "rcpt")
	os.WriteFile(rcpt, []byte("# holders\n"+public+"\n"+other+"\n"), 0o600)
	key := filepath.Join(dir, "key")
	os.WriteFile(key, []byte(backupcrypt.KeyFile("IT", "Hotel", secret, public, "ABCD-EFGH", t0())), 0o600)
	plain := filepath.Join(dir, "site.dump")
	os.WriteFile(plain, []byte("PGDMP guest rows"), 0o600)

	enc := filepath.Join(dir, "site.dump.age")
	if rc := backupCryptMain([]string{"encrypt", "-R", rcpt, plain, enc}); rc != 0 {
		t.Fatalf("encrypt rc=%d", rc)
	}
	if b, _ := os.ReadFile(enc); strings.Contains(string(b), "guest rows") {
		t.Fatal("the encrypted file contains the plaintext")
	}
	if _, err := os.Stat(enc + ".partial"); err == nil {
		t.Fatal("a .partial file was left behind")
	}
	out := filepath.Join(dir, "back.dump")
	if rc := backupCryptMain([]string{"decrypt", "-i", key, enc, out}); rc != 0 {
		t.Fatalf("decrypt rc=%d", rc)
	}
	if b, _ := os.ReadFile(out); string(b) != "PGDMP guest rows" {
		t.Fatalf("round trip gave %q", b)
	}
	// Nothing to encrypt to is a failure, never a plaintext copy.
	os.WriteFile(rcpt, []byte("# nobody\n"), 0o600)
	if rc := backupCryptMain([]string{"encrypt", "-R", rcpt, plain, filepath.Join(dir, "x.age")}); rc == 0 {
		t.Fatal("encrypting to no holder succeeded")
	}
	if _, err := os.Stat(filepath.Join(dir, "x.age")); err == nil {
		t.Fatal("a failed encryption left an output file")
	}
	if rc := backupCryptMain([]string{"encrypt", plain}); rc == 0 {
		t.Fatal("a malformed command line succeeded")
	}
}

func TestOpenBackupNeedsTheRightKeyOnlyWhenEncrypted(t *testing.T) {
	dir := t.TempDir()
	secret, public, _ := backupcrypt.GenerateKey()
	outsider, _, _ := backupcrypt.GenerateKey()
	rs, _ := backupcrypt.ParseRecipients([]string{public})

	write := func(name string, encrypt bool) string {
		p := filepath.Join(dir, name)
		f, _ := os.Create(p)
		var w io.WriteCloser = f
		if encrypt {
			w, _ = backupcrypt.Encrypt(f, rs)
		}
		gz := gzip.NewWriter(w)
		io.WriteString(gz, "CREATE TABLE guests();")
		gz.Close()
		if encrypt {
			w.Close()
		}
		f.Close()
		return p
	}
	plain := write("db-1.sql.gz", false)
	enc := write("db-2.sql.gz.age", true)

	read := func(path, id string) (string, error) {
		r, c, err := openBackup(path, id)
		if err != nil {
			return "", err
		}
		defer c.Close()
		b, err := io.ReadAll(r)
		return string(b), err
	}
	if got, err := read(plain, ""); err != nil || got != "CREATE TABLE guests();" {
		t.Fatalf("a clear backup = %q %v", got, err)
	}
	if _, err := read(enc, ""); !errors.Is(err, errNeedKey) {
		t.Fatalf("an encrypted backup without a key = %v, want errNeedKey", err)
	}
	if _, err := read(enc, outsider); !errors.Is(err, backupcrypt.ErrWrongKey) {
		t.Fatalf("an encrypted backup with another key = %v, want ErrWrongKey", err)
	}
	if msg, ok := keyProblem(backupcrypt.ErrWrongKey); !ok || !strings.Contains(msg, "does not open") {
		t.Fatalf("wrong-key message %q", msg)
	}
	if got, err := read(enc, "# pasted file\n"+secret+"\n"); err != nil || got != "CREATE TABLE guests();" {
		t.Fatalf("an encrypted backup with the holder's key = %q %v", got, err)
	}
	if !safeBackupName.MatchString("db-20261017T101010Z.sql.gz.age") || !safeBackupName.MatchString("db-safety-20261017T101010Z.sql.gz.age") ||
		safeBackupName.MatchString("db-20261017T101010Z.sql.gz.age.age") {
		t.Fatal("the backup name guard does not admit exactly the encrypted shapes")
	}
}

func TestNewestEncryptedBackupLooksOnlyAtBackupsTheEngineMade(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	oldA, oldB := scdBackupDir, nightlyBackupDir
	scdBackupDir, nightlyBackupDir = a, b
	defer func() { scdBackupDir, nightlyBackupDir = oldA, oldB }()
	if newestEncryptedBackup() != "" {
		t.Fatal("found a backup in empty directories")
	}
	os.WriteFile(filepath.Join(a, "db-1.sql.gz"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(a, "db-2.sql.gz.age"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(b, "site-20261017-020000.dump.age"), []byte("x"), 0o600)
	if got, want := newestEncryptedBackup(), filepath.Join(a, "db-2.sql.gz.age"); got != want {
		t.Fatalf("newest = %q, want %q", got, want)
	}
}

func t0() time.Time { return time.Date(2026, 10, 17, 2, 10, 0, 0, time.UTC) }

func TestBackupKeyTestOpensTheNewestEncryptedBackup(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	oldA, oldB := scdBackupDir, nightlyBackupDir
	scdBackupDir, nightlyBackupDir = a, b
	defer func() { scdBackupDir, nightlyBackupDir = oldA, oldB }()
	srv := &server{}
	call := func(identity string) (int, map[string]any) {
		body, _ := json.Marshal(map[string]string{"identity": identity})
		rec := httptest.NewRecorder()
		srv.backupKeyTest(rec, httptest.NewRequest(http.MethodPost, "/v1/backup/key-test", bytes.NewReader(body)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	secret, public, _ := backupcrypt.GenerateKey()
	outsider, _, _ := backupcrypt.GenerateKey()
	if code, _ := call(secret); code != http.StatusConflict {
		t.Fatalf("with no encrypted backup = %d, want 409", code)
	}
	rs, _ := backupcrypt.ParseRecipients([]string{public})
	f, _ := os.Create(filepath.Join(a, "db-20261017T020000Z.sql.gz.age"))
	w, _ := backupcrypt.Encrypt(f, rs)
	gz := gzip.NewWriter(w)
	io.WriteString(gz, strings.Repeat("CREATE TABLE guests();\n", 10))
	gz.Close()
	w.Close()
	f.Close()
	if code, out := call(secret); code != 200 || out["ok"] != true || out["public_key"] != public {
		t.Fatalf("the holder's key = %d %v", code, out)
	}
	if code, out := call(outsider); code != 200 || out["ok"] != false {
		t.Fatalf("another key = %d %v", code, out)
	}
	if code, _ := call("not a key"); code != http.StatusBadRequest {
		t.Fatalf("no key = %d", code)
	}
}

// The nightly backup is raw pg_dump -Fc, not gzip: the key test must still pass for it; verify and restore refuse it by name.
func TestBackupKeyTestAcceptsANightlyCustomFormatDump(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	oldA, oldB := scdBackupDir, nightlyBackupDir
	scdBackupDir, nightlyBackupDir = a, b
	defer func() { scdBackupDir, nightlyBackupDir = oldA, oldB }()
	secret, public, _ := backupcrypt.GenerateKey()
	outsider, _, _ := backupcrypt.GenerateKey()
	rs, _ := backupcrypt.ParseRecipients([]string{public})

	fa, _ := os.Create(filepath.Join(a, "db-20261016T020000Z.sql.gz.age"))
	wa, _ := backupcrypt.Encrypt(fa, rs)
	gz := gzip.NewWriter(wa)
	io.WriteString(gz, "CREATE TABLE guests();\n")
	gz.Close()
	wa.Close()
	fa.Close()
	nightly := filepath.Join(a, "db-20261017T020000Z.sql.gz.age")
	fn, _ := os.Create(nightly)
	wn, _ := backupcrypt.Encrypt(fn, rs)
	io.WriteString(wn, "PGDMP\x01\x0e\x00\x04\x08\x01\x01\x01"+strings.Repeat("\x00", 200))
	wn.Close()
	fn.Close()
	future := t0().AddDate(10, 0, 0)
	os.Chtimes(nightly, future, future)
	if got := newestEncryptedBackup(); got != nightly {
		t.Fatalf("newest = %q, want the nightly dump", got)
	}

	srv := &server{}
	call := func(identity string) (int, map[string]any) {
		body, _ := json.Marshal(map[string]string{"identity": identity})
		rec := httptest.NewRecorder()
		srv.backupKeyTest(rec, httptest.NewRequest(http.MethodPost, "/v1/backup/key-test", bytes.NewReader(body)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, out := call(secret); code != 200 || out["ok"] != true || out["backup"] != filepath.Base(nightly) || out["public_key"] != public {
		t.Fatalf("the holder's key on a nightly dump = %d %v", code, out)
	}
	if code, out := call(outsider); code != 200 || out["ok"] != false {
		t.Fatalf("another key on a nightly dump = %d %v", code, out)
	}

	if _, _, err := openBackup(nightly, secret); !errors.Is(err, errNotSQLBackup) {
		t.Fatalf("openBackup on a nightly dump = %v, want errNotSQLBackup", err)
	}
	if _, err := loadBackupInto(t.Context(), nightly, secret, "nowhere"); !errors.Is(err, errNotSQLBackup) {
		t.Fatalf("loadBackupInto on a nightly dump = %v, want errNotSQLBackup", err)
	}
	if _, format, c, err := openBackupStream(nightly, secret); err != nil || format != formatPGCustom {
		t.Fatalf("openBackupStream on a nightly dump = %v %v", format, err)
	} else {
		c.Close()
	}

	junk := filepath.Join(a, "db-20261018T020000Z.sql.gz")
	os.WriteFile(junk, []byte("not an archive"), 0o600)
	if _, _, _, err := openBackupStream(junk, ""); err == nil || !strings.Contains(err.Error(), "not a readable archive") {
		t.Fatalf("junk = %v", err)
	}
}
