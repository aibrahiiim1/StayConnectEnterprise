package main

// BACKUPS ARE ENCRYPTED TO THE HOTEL'S OWN KEYS (migration 0129; GDPR remediation plan C5; Product-Owner decision
// D-C5).
//
// Once the hotel has registered a backup key holder, every dump this appliance writes -- an operator's backup,
// the safety copy a restore takes, and (through `scd backup-crypt`) the nightly backup -- is encrypted with age
// to every ACTIVE holder's public key, as it is written: no plaintext copy ever touches the disk. Before the
// first holder is confirmed, backups are written as they always were and the Backups screen says so.
//
// The appliance holds public keys only, so it can write backups and never read them back. Verify, restore and
// the restore test each take a holder's private key in the request, use it in memory for that one operation,
// and drop it. It is never written, logged or echoed.
//
// FAILS CLOSED. If holders are registered but cannot be read, the backup fails rather than falling back to
// plaintext: a hotel that switched encryption on must never find an unencrypted copy of its guests.

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"filippo.io/age"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/stayconnect/enterprise/data-plane/internal/backupcrypt"
)

// nightlyBackupDir is where stayconnect-site-backup writes. The restore test looks there too.
var nightlyBackupDir = "/var/backups/stayconnect"

// backupRecipients returns the keys to encrypt to, or nil when the hotel has not set encryption up (or the
// database predates 0129). Any other failure is an error: the caller must not write plaintext instead.
func (s *server) backupRecipients(ctx context.Context) ([]age.Recipient, error) {
	keys, err := s.backupRecipientKeys(ctx)
	if err != nil || len(keys) == 0 {
		return nil, err
	}
	return backupcrypt.ParseRecipients(keys)
}

// backupRecipientKeys is the public keys of the confirmed backup key holders, or nil when none is set up.
func (s *server) backupRecipientKeys(ctx context.Context) ([]string, error) {
	if s.db == nil {
		return nil, errors.New("the database is not reachable, so the backup keys could not be read")
	}
	// Without its own site scd asks as the nightly backup does, for the holders of the site the appliance serves
	// (0144): a dump is never written in the clear while that site's holders exist.
	q, args := `SELECT k FROM public.backup_key_recipients($1::uuid,$2::uuid) k`, []any{s.tenID, s.siteID}
	if s.tenID == "" || s.siteID == "" {
		q, args = `SELECT k FROM public.backup_key_recipients() k`, nil
	}
	rows, err := s.db.Query(ctx, q, args...)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42883" {
			return nil, nil
		}
		return nil, fmt.Errorf("the backup keys could not be read: %w", err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("the backup keys could not be read: %w", err)
	}
	return keys, nil
}

// writeDump streams pg_dump through gzip -- and age, when recipients are given -- into dest.
func writeDump(ctx context.Context, dest string, recipients []age.Recipient) error {
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	var sink io.WriteCloser = nopCloser{f}
	if len(recipients) > 0 {
		if sink, err = backupcrypt.Encrypt(f, recipients); err != nil {
			return err
		}
	}
	gz := gzip.NewWriter(sink)
	cmd := exec.CommandContext(ctx, "docker", "exec", pgContainer, "pg_dump", "-U", pgUser, "-d", pgDatabase,
		"--no-owner", "--no-privileges")
	cmd.Stdout = gz
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s", tailLine(stderr.String()+" "+err.Error()))
	}
	if err := gz.Close(); err != nil {
		return err
	}
	if err := sink.Close(); err != nil {
		return err
	}
	return f.Sync()
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// openBackup returns the plain SQL of a backup: decrypted with the supplied key when the file is encrypted,
// then decompressed. errNeedKey tells the operator an encrypted backup needs a key; a key that does not open
// it is backupcrypt.ErrWrongKey.
var errNeedKey = errors.New("this backup is encrypted: provide a backup key holder's key to open it")

// errNotSQLBackup is a backup that opened but is a pg_dump custom-format archive -- the nightly backup's shape
// -- rather than the gzip SQL text verify and restore load with psql. Feeding one to psql would fail late and
// obscurely, so it is refused up front and by name.
var errNotSQLBackup = errors.New("this is a nightly (pg_dump custom-format) backup, not a database backup taken on the Backups screen: it cannot be loaded here")

// backupFormat is what a backup holds once any encryption is removed. The two writers on this appliance do not
// agree: an on-demand backup is gzip SQL text (writeDump), the nightly one is `pg_dump -Fc` encrypted as it
// stands (stayconnect-site-backup.sh). The bytes, not the name, say which.
type backupFormat int

const (
	formatGzipSQL  backupFormat = iota + 1 // gzip magic 1f 8b
	formatPGCustom                         // "PGDMP"
)

// openBackupStream decrypts a backup when it is encrypted and reports which format it holds. A gzip backup is
// returned decompressed; a custom-format archive is returned as it is. Anything else is an error.
func openBackupStream(path, identity string) (io.Reader, backupFormat, io.Closer, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, nil, err
	}
	var src io.Reader = f
	if strings.HasSuffix(path, backupcrypt.Suffix) {
		if strings.TrimSpace(identity) == "" {
			f.Close()
			return nil, 0, nil, errNeedKey
		}
		id, err := backupcrypt.ParseIdentity(identity)
		if err != nil {
			f.Close()
			return nil, 0, nil, err
		}
		if src, err = backupcrypt.Decrypt(f, id); err != nil {
			f.Close()
			return nil, 0, nil, err
		}
	}
	br := bufio.NewReader(src)
	magic, err := br.Peek(5)
	switch {
	case len(magic) >= 2 && magic[0] == 0x1f && magic[1] == 0x8b:
		gz, err := gzip.NewReader(br)
		if err != nil {
			f.Close()
			return nil, 0, nil, fmt.Errorf("the backup is not a readable archive: %w", err)
		}
		return gz, formatGzipSQL, f, nil
	case len(magic) == 5 && string(magic) == "PGDMP":
		return br, formatPGCustom, f, nil
	}
	f.Close()
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, 0, nil, fmt.Errorf("the backup could not be read: %w", err)
	}
	return nil, 0, nil, errors.New("the backup is not a readable archive: it is neither gzip SQL nor a pg_dump archive")
}

// openBackup is openBackupStream for the callers that load SQL text with psql (verify, restore): a nightly
// custom-format archive is refused with errNotSQLBackup rather than misread.
func openBackup(path, identity string) (io.Reader, io.Closer, error) {
	src, format, closer, err := openBackupStream(path, identity)
	if err != nil {
		return nil, nil, err
	}
	if format != formatGzipSQL {
		closer.Close()
		return nil, nil, errNotSQLBackup
	}
	return src, closer, nil
}

// keyProblem reports whether err is about the key rather than the backup, and says so in the operator's terms.
func keyProblem(err error) (string, bool) {
	switch {
	case errors.Is(err, errNeedKey):
		return err.Error(), true
	case errors.Is(err, backupcrypt.ErrWrongKey):
		return "this key does not open this backup: use the key of a holder who was registered when it was taken", true
	case errors.Is(err, backupcrypt.ErrNoIdentity):
		return "no backup key was found in what was provided: paste the whole key file, or the line starting AGE-SECRET-KEY-1", true
	case err != nil && strings.Contains(err.Error(), "backup key is damaged"):
		return err.Error(), true
	}
	return "", false
}

// loadBackupInto loads a backup into the named database with psql, decrypting and decompressing in-process.
func loadBackupInto(ctx context.Context, path, identity, db string) ([]byte, error) {
	src, closer, err := openBackup(path, identity)
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", pgContainer,
		"psql", "-U", pgUser, "-d", db, "-v", "ON_ERROR_STOP=1", "-q")
	cmd.Stdin = src
	return cmd.CombinedOutput()
}

// ---------------------------------------------------------------------------- the restore test ----------

// backupKeyTest proves the hotel still holds a working key: the newest encrypted backup on the appliance is
// opened with the key supplied and its first bytes are read -- decompressed for an on-demand gzip backup, as
// they stand for a nightly pg_dump archive, whose "PGDMP" header is itself the proof it decrypted correctly. Nothing is loaded or kept. It answers
// which holder the key belongs to (its public key) so edged can record the test against that holder.
func (s *server) backupKeyTest(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Identity string `json:"identity"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil {
		httpErr(w, http.StatusBadRequest, "a backup key is required")
		return
	}
	id, err := backupcrypt.ParseIdentity(in.Identity)
	if err != nil {
		msg, _ := keyProblem(err)
		httpErr(w, http.StatusBadRequest, msg)
		return
	}
	path := newestEncryptedBackup()
	if path == "" {
		httpErr(w, http.StatusConflict, "there is no encrypted backup on this appliance yet: take a backup first, then test the key")
		return
	}
	src, _, closer, err := openBackupStream(path, id.String())
	if err != nil {
		if msg, ok := keyProblem(err); ok {
			writeJSON(w, http.StatusOK, map[string]any{"ok": false, "backup": filepath.Base(path), "detail": msg})
			return
		}
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer closer.Close()
	if _, err := io.ReadFull(src, make([]byte, 64)); err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "backup": filepath.Base(path),
			"detail": "the key opened the backup but its content could not be read: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "backup": filepath.Base(path), "public_key": id.Recipient().String(),
	})
}

// newestEncryptedBackup is the most recent .age file among the on-demand and nightly database backups.
func newestEncryptedBackup() string {
	var cands []string
	for _, pat := range []string{filepath.Join(scdBackupDir, "db-*.sql.gz.age"), filepath.Join(nightlyBackupDir, "site-*.dump.age")} {
		m, _ := filepath.Glob(pat)
		cands = append(cands, m...)
	}
	sort.Slice(cands, func(i, j int) bool {
		a, _ := os.Stat(cands[i])
		b, _ := os.Stat(cands[j])
		return a != nil && b != nil && a.ModTime().After(b.ModTime())
	})
	if len(cands) == 0 {
		return ""
	}
	return cands[0]
}

// --------------------------------------------------------------------------- `scd backup-crypt` -------

// backupCryptMain is the nightly shell backup's way to the same encryption:
//
//	scd backup-crypt encrypt -R <recipients-file> <in|-> <out>   one age public key per line
//	scd backup-crypt decrypt -i <key-file> <in> <out|->          for an engineer restoring a nightly backup
//
// The output is written to <out>.partial and renamed only when complete.
func backupCryptMain(args []string) int {
	fail := func(format string, a ...any) int {
		fmt.Fprintf(os.Stderr, "backup-crypt: "+format+"\n", a...)
		return 1
	}
	if len(args) != 5 || (args[0] != "encrypt" || args[1] != "-R") && (args[0] != "decrypt" || args[1] != "-i") {
		return fail("usage: scd backup-crypt encrypt -R <recipients-file> <in|-> <out> | decrypt -i <key-file> <in> <out|->")
	}
	keyText, err := os.ReadFile(args[2])
	if err != nil {
		return fail("%v", err)
	}
	in, out := args[3], args[4]
	var src io.Reader = os.Stdin
	if in != "-" {
		f, err := os.Open(in)
		if err != nil {
			return fail("%v", err)
		}
		defer f.Close()
		src = f
	}
	var dst io.Writer = os.Stdout
	var partial *os.File
	if out != "-" {
		if partial, err = os.OpenFile(out+".partial", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600); err != nil {
			return fail("%v", err)
		}
		defer func() { partial.Close(); os.Remove(out + ".partial") }()
		dst = partial
	}
	switch args[0] {
	case "encrypt":
		var keys []string
		for _, l := range strings.Split(string(keyText), "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
				keys = append(keys, l)
			}
		}
		rs, err := backupcrypt.ParseRecipients(keys)
		if err != nil {
			return fail("%v", err)
		}
		w, err := backupcrypt.Encrypt(dst, rs)
		if err != nil {
			return fail("%v", err)
		}
		if _, err := io.Copy(w, src); err != nil {
			return fail("%v", err)
		}
		if err := w.Close(); err != nil {
			return fail("%v", err)
		}
	case "decrypt":
		id, err := backupcrypt.ParseIdentity(string(keyText))
		if err != nil {
			return fail("%v", err)
		}
		r, err := backupcrypt.Decrypt(src, id)
		if err != nil {
			return fail("%v", err)
		}
		if _, err := io.Copy(dst, r); err != nil {
			return fail("%v", err)
		}
	}
	if partial != nil {
		if err := partial.Sync(); err != nil {
			return fail("%v", err)
		}
		partial.Close()
		if err := os.Rename(out+".partial", out); err != nil {
			return fail("%v", err)
		}
	}
	return 0
}
