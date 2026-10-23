//go:build integration

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/backupcrypt"
)

// fakeBackupSCD serves the two scd endpoints used; the key test does a real decryption with the library scd uses.
func fakeBackupSCD(t *testing.T, months int, backup []byte) *scdClient {
	t.Helper()
	dir, err := os.MkdirTemp("", "scd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "s"))
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/backup/settings":
			_ = json.NewEncoder(w).Encode(map[string]any{"retention": []map[string]any{{"key": "RESTORE_TEST_MONTHS", "value": months}}})
		case "/v1/backup/key-test":
			var in struct{ Identity string }
			_ = json.NewDecoder(r.Body).Decode(&in)
			id, err := backupcrypt.ParseIdentity(in.Identity)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
			rd, err := backupcrypt.Decrypt(bytes.NewReader(backup), id)
			if errors.Is(err, backupcrypt.ErrWrongKey) {
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "backup": "db-x.sql.gz.age", "detail": "this key does not open this backup"})
				return
			}
			_, _ = io.ReadAll(rd)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "backup": "db-x.sql.gz.age", "public_key": id.Recipient().String()})
		default:
			http.NotFound(w, r)
		}
	})}
	go srv.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { _ = srv.Close() })
	return newSCDClient(filepath.Join(dir, "s"))
}

func TestIntegration_BackupKeys(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	f.app.scd = fakeBackupSCD(t, 6, nil)

	st, body := f.do(t, "GET", "/backup-keys/", nil)
	if st != 200 || body["available"] != true || body["encrypted"] != false {
		t.Fatalf("before setup = %d %v", st, body)
	}

	add := func(label string) (id, secret, public, check string) {
		t.Helper()
		st, b := f.do(t, "POST", "/backup-keys/holders", map[string]any{"label": label, "acknowledge_loss": true, "password": f.password})
		if st != 200 {
			t.Fatalf("add %s = %d %v", label, st, b)
		}
		id, secret, public, check = b["id"].(string), b["private_key"].(string), b["public_key"].(string), b["check_code"].(string)
		file := b["key_file"].(string)
		if !strings.Contains(file, secret) || !strings.Contains(file, check) || !strings.Contains(file, public) {
			t.Fatalf("the key file lacks the key, the public key or the check code:\n%s", file)
		}
		return
	}
	if st, _ := f.do(t, "POST", "/backup-keys/holders", map[string]any{"label": "IT", "acknowledge_loss": false, "password": f.password}); st != 400 {
		t.Fatalf("added a holder without the loss acknowledgement: %d", st)
	}
	if st, _ := f.do(t, "POST", "/backup-keys/holders", map[string]any{"label": "IT", "acknowledge_loss": true, "password": "wrong"}); st != 401 {
		t.Fatalf("added a holder with a wrong password: %d", st)
	}
	id1, secret1, public1, check1 := add("IT manager")

	// The secret is nowhere in the database, the audit log included.
	var leaks int
	if err := f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM public.backup_key_holders h WHERE h::text LIKE '%'||$1||'%')
		+ (SELECT count(*) FROM public.audit_log l WHERE l.payload::text LIKE '%'||$1||'%')
		+ (SELECT count(*) FROM public.backup_key_holders h WHERE h.check_code_hash = $2)`, secret1, check1).Scan(&leaks); err != nil || leaks != 0 {
		t.Fatalf("the private key or the check code is stored (%d, %v)", leaks, err)
	}

	if _, body := f.do(t, "GET", "/backup-keys/", nil); body["encrypted"] != false {
		t.Fatal("a holder that never confirmed switched encryption on")
	}
	if st, _ := f.do(t, "POST", "/backup-keys/holders/"+id1+"/confirm", map[string]any{"check_code": "AAAA-BBBB"}); st != 400 {
		t.Fatalf("a wrong check code confirmed the holder: %d", st)
	}
	if st, b := f.do(t, "POST", "/backup-keys/holders/"+id1+"/confirm", map[string]any{"check_code": strings.ToLower(strings.ReplaceAll(check1, "-", " "))}); st != 200 {
		t.Fatalf("confirm = %d %v", st, b)
	}
	st, body = f.do(t, "GET", "/backup-keys/", nil)
	test, _ := body["restore_test"].(map[string]any)
	if st != 200 || body["encrypted"] != true || test["overdue"] != false || test["due_at"] == nil || test["interval_months"] != float64(6) {
		t.Fatalf("after confirming = %v", body)
	}
	recipients := func() []string {
		var out []string
		rows, _ := f.pool.Query(ctx, `SELECT k FROM public.backup_key_recipients($1::uuid,$2::uuid) k`, f.tenant, f.site)
		defer rows.Close()
		for rows.Next() {
			var k string
			_ = rows.Scan(&k)
			out = append(out, k)
		}
		return out
	}
	if got := recipients(); len(got) != 1 || got[0] != public1 {
		t.Fatalf("recipients = %v", got)
	}

	if st, b := f.do(t, "POST", "/backup-keys/holders/"+id1+"/remove", map[string]any{"reason": "left the hotel", "password": f.password}); st != 409 {
		t.Fatalf("removed the last holder: %d %v", st, b)
	}
	id2, secret2, public2, check2 := add("Hotel safe")
	if st, _ := f.do(t, "POST", "/backup-keys/holders/"+id2+"/confirm", map[string]any{"check_code": check2}); st != 200 {
		t.Fatal("confirm second")
	}
	if st, _ := f.do(t, "POST", "/backup-keys/holders/"+id1+"/remove", map[string]any{"reason": "", "password": f.password}); st != 400 {
		t.Fatal("removed a holder without a reason")
	}
	if st, b := f.do(t, "POST", "/backup-keys/holders/"+id1+"/remove", map[string]any{"reason": "left the hotel", "password": f.password}); st != 200 {
		t.Fatalf("remove = %d %v", st, b)
	}
	if got := recipients(); len(got) != 1 || got[0] != public2 {
		t.Fatalf("recipients after removal = %v", got)
	}

	// Restore test against a backup encrypted to the current holder only.
	rs, _ := backupcrypt.ParseRecipients([]string{public2})
	var enc bytes.Buffer
	w, _ := backupcrypt.Encrypt(&enc, rs)
	_, _ = w.Write([]byte("dump"))
	_ = w.Close()
	f.app.scd = fakeBackupSCD(t, 1, enc.Bytes())
	st, body = f.do(t, "POST", "/backup-keys/test", map[string]any{"identity": secret1})
	if st != 200 || body["ok"] != false {
		t.Fatalf("a removed holder's key passed a test on a newer backup: %d %v", st, body)
	}
	st, body = f.do(t, "POST", "/backup-keys/test", map[string]any{"identity": "# key file\n" + secret2 + "\n"})
	if st != 200 || body["ok"] != true || body["holder"] != "Hotel safe" {
		t.Fatalf("restore test = %d %v", st, body)
	}
	if _, body := f.do(t, "GET", "/backup-keys/", nil); body["restore_test"].(map[string]any)["last_tested_at"] == nil {
		t.Fatal("the passed test was not recorded")
	}
	if _, err := f.pool.Exec(ctx, `UPDATE public.backup_key_holders SET last_tested_at = now() - interval '2 months' WHERE id=$1`, id2); err != nil {
		t.Fatal(err)
	}
	if _, body := f.do(t, "GET", "/backup-keys/", nil); body["restore_test"].(map[string]any)["overdue"] != true {
		t.Fatalf("a test two months old is not overdue on a one-month interval: %v", body["restore_test"])
	}

	it := newAPIIn(t, f.tenant, "hotel_it_manager")
	it.app.scd = f.app.scd
	if st, _ := it.do(t, "GET", "/backup-keys/", nil); st != 200 {
		t.Fatalf("the IT manager cannot see the backup keys: %d", st)
	}
	if st, _ := it.do(t, "POST", "/backup-keys/holders", map[string]any{"label": "x", "acknowledge_loss": true, "password": it.password}); st != 403 {
		t.Fatalf("the IT manager added a holder: %d", st)
	}

	// The nightly backup names no site: with holders active on two sites it must refuse, never encrypt to the other hotel's keys.
	other := newAPIIn(t, f.tenant)
	other.app.scd = f.app.scd
	st, b := other.do(t, "POST", "/backup-keys/holders", map[string]any{"label": "Other hotel", "acknowledge_loss": true, "password": other.password})
	if st != 200 {
		t.Fatalf("add on a second site = %d %v", st, b)
	}
	if st, _ := other.do(t, "POST", "/backup-keys/holders/"+b["id"].(string)+"/confirm", map[string]any{"check_code": b["check_code"]}); st != 200 {
		t.Fatal("confirm on a second site")
	}
	if got := recipients(); len(got) != 1 || got[0] != public2 {
		t.Fatalf("this site's recipients include another site's: %v", got)
	}
	if _, err := f.pool.Exec(ctx, `SELECT count(*) FROM public.backup_key_recipients()`); err == nil {
		t.Fatal("the site-less recipient list answered with holders of two sites active")
	}

	if _, err := f.pool.Exec(ctx, `DELETE FROM public.backup_key_holders WHERE id=$1`, id1); err == nil {
		t.Fatal("a holder was deleted")
	}
	if _, err := f.pool.Exec(ctx, `DELETE FROM public.backup_key_holder_events WHERE holder_id=$1`, id1); err == nil {
		t.Fatal("a holder event was deleted")
	}
	var events int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FROM public.backup_key_holder_events WHERE tenant_id=$1 AND site_id=$2`, f.tenant, f.site).Scan(&events)
	if events != 6 { // added x2, confirmed x2, removed, restore_tested
		t.Fatalf("events = %d, want 6", events)
	}
}
