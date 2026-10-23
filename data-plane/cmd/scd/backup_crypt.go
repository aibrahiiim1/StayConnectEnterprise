package main

// Backups are encrypted with age to every active backup key holder's public key as they are written, so no plaintext copy touches disk.
// The appliance holds public keys only; verify and restore take a private key per request, use it in memory, and never store or log it.
// Fails closed: if holders are registered but unreadable, the backup fails rather than falling back to plaintext.

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

// nightlyBackupDir is where the retired nightly job wrote; the restore test looks there too.
var nightlyBackupDir = "/var/backups/stayconnect"

// backupRecipients returns nil when encryption is not set up; any other failure is an error so the caller never writes plaintext.
func (s *server) backupRecipients(ctx context.Context) ([]age.Recipient, error) {
	keys, err := s.backupRecipientKeys(ctx)
	if err != nil || len(keys) == 0 {
		return nil, err
	}
	return backupcrypt.ParseRecipients(keys)
}

func (s *server) backupRecipientKeys(ctx context.Context) ([]string, error) {
	if s.db == nil {
		return nil, errors.New("the database is not reachable, so the backup keys could not be read")
	}
	// With no site of its own, ask as the nightly backup does for the serving site's holders: never dump in the clear while they exist.
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

// writeDumpTee streams pg_dump through gzip (and age) into dest and hands the plain SQL to tee, so it can be loaded to prove restorability without touching disk.
func writeDumpTee(ctx context.Context, dest string, recipients []age.Recipient, tee io.Writer) error {
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
	if tee != nil {
		cmd.Stdout = io.MultiWriter(gz, tee)
	} else {
		cmd.Stdout = gz
	}
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

// errNeedKey: an encrypted backup needs a key; a key that does not open it is backupcrypt.ErrWrongKey.
var errNeedKey = errors.New("this backup is encrypted: provide a backup key holder's key to open it")

// errNotSQLBackup: a pg_dump custom-format archive (the nightly shape) is refused up front since psql would fail late and obscurely.
var errNotSQLBackup = errors.New("this is a nightly (pg_dump custom-format) backup, not a database backup taken on the Backups screen: it cannot be loaded here")

// backupFormat is what a backup holds once decrypted; the bytes, not the name, say which writer produced it.
type backupFormat int

const (
	formatGzipSQL  backupFormat = iota + 1 // gzip magic 1f 8b
	formatPGCustom                         // "PGDMP"
)

// openBackupStream decrypts if needed and reports the format; gzip is returned decompressed, custom-format as is.
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

// openBackup refuses a nightly custom-format archive with errNotSQLBackup rather than misreading it.
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

// backupKeyTest proves the hotel still holds a working key by opening the newest encrypted backup and reading its first bytes; nothing is loaded or kept.
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

func newestEncryptedBackup() string {
	var cands []string
	for _, pat := range []string{filepath.Join(scdBackupDir, "db-*.sql.gz.age")} {
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
