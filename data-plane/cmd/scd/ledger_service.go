package main

// The erasure ledger (internal/ledger): an append-only hash-chained record of every erasure and closed data-protection request, kept in the
// database (same transaction as the erasure), a primary file on the appliance, and a mandatory independent secondary storage; until that is set up and verified, erasure is refused.
// edged hands new entries to /v1/ledger/commit before it commits and commits only when both copies are durable and read back; otherwise it
// rolls back and nothing was erased. Hourly reconciliation heals a missing or damaged copy and refuses to overwrite a copy that is ahead.
// Nothing here goes to OneGate Central.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"filippo.io/age"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/stayconnect/enterprise/data-plane/internal/backupcrypt"
	"github.com/stayconnect/enterprise/data-plane/internal/ledger"
	"github.com/stayconnect/enterprise/data-plane/internal/localkeys"
	"github.com/stayconnect/enterprise/data-plane/internal/sealbox"
)

// ledgerLocalPath is the primary copy, outside the backup folders on purpose: backups are rotated and restored, the ledger is neither.
var ledgerLocalPath = "/var/lib/stayconnect/ledger/erasure-ledger.jsonl"

// ledgerOwnPlaces are where the appliance keeps its own data; the secondary storage must be on none of those disks.
var ledgerOwnPlaces = []string{"/opt/stayconnect", "/var/lib/stayconnect", "/var/backups/stayconnect", "/var/lib/docker"}

// ledgerIndependence is the check a folder must pass to be a second copy; tests replace it (they have no external disk).
var ledgerIndependence = func() func(string) error { return ledger.ProductionIndependence(ledgerOwnPlaces) }

const (
	ledgerSecretDomain = "ledger-destination-secret:v1"
	ledgerLockSQL      = `SELECT pg_advisory_xact_lock(hashtext('stayconnect-erasure-ledger'))`
)

type ledgerRuntime struct {
	store       *ledger.Store
	key         sealbox.Key
	reconcileMu sync.Mutex
	mu          sync.Mutex
	status      ledgerStatus
	destErr     string
}

type ledgerStatus struct {
	// ErasureAvailable: both copies healthy and in step with the database. A ledger problem stops erasure requests only, never guest internet.
	ErasureAvailable bool      `json:"erasure_available"`
	State            string    `json:"state"` // ok | not_configured | attention | recovery_needed | blocked | unavailable
	Message          string    `json:"message"`
	Problems         []string  `json:"problems,omitempty"`
	Checked          time.Time `json:"checked"`
	DB               struct {
		Entries     int64 `json:"entries"`
		Intact      bool  `json:"intact"`
		Unconfirmed int64 `json:"unconfirmed"`
	} `json:"database"`
	Local struct {
		Present bool   `json:"present"`
		Intact  bool   `json:"intact"`
		Entries int    `json:"entries"`
		Path    string `json:"path"`
		Error   string `json:"error,omitempty"`
	} `json:"primary"`
	External struct {
		Configured bool       `json:"configured"`
		Kind       string     `json:"kind,omitempty"`
		Describe   string     `json:"describe,omitempty"`
		Reachable  bool       `json:"reachable"`
		InSync     bool       `json:"in_sync"`
		Entries    int        `json:"entries"`
		Encrypted  bool       `json:"encrypted"`
		LastOK     *time.Time `json:"last_ok,omitempty"`
		Error      string     `json:"error,omitempty"`
	} `json:"secondary"`
}

func (s *server) ledgerInit(secretsDir string) {
	if raw, err := localkeys.LoadExistingKey(filepath.Join(secretsDir, notifyKeyFile)); err == nil {
		if k, err := sealbox.NewKey(ledgerSecretDomain, raw); err == nil {
			s.ledgerRT.key = k
		}
	} else {
		slog.Warn("erasure ledger: the sealing key is missing; the secondary storage credentials cannot be opened", "err", err)
	}
	s.ledgerRT.store = ledger.NewStore(ledger.Config{LocalPath: ledgerLocalPath, Cipher: s.ledgerCipher})
}

func (s *server) ledgerCipher(ctx context.Context) (*ledger.Cipher, error) {
	keys, err := s.backupRecipientKeys(ctx)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, nil
	}
	rcpt, err := backupcrypt.ParseRecipients(keys)
	if err != nil {
		return nil, err
	}
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	return &ledger.Cipher{Recipients: rcpt, Fingerprint: hex.EncodeToString(sum[:8])}, nil
}

type ledgerDestConfig struct {
	Kind        string `json:"kind"`
	Path        string `json:"path,omitempty"`
	Host        string `json:"host,omitempty"`
	Port        int    `json:"port,omitempty"`
	User        string `json:"user,omitempty"`
	Dir         string `json:"dir,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Endpoint    string `json:"endpoint,omitempty"`
	Region      string `json:"region,omitempty"`
	Bucket      string `json:"bucket,omitempty"`
	Prefix      string `json:"prefix,omitempty"`
	AccessKey   string `json:"access_key,omitempty"`
}

type ledgerDestSecret struct {
	Password   string `json:"password,omitempty"`
	PrivateKey string `json:"private_key,omitempty"`
	SecretKey  string `json:"secret_key,omitempty"`
}

func buildLedgerDestination(c ledgerDestConfig, sec ledgerDestSecret) (ledger.Destination, error) {
	switch c.Kind {
	case "dir":
		if strings.TrimSpace(c.Path) == "" {
			return nil, errors.New("the folder is required")
		}
		return &ledger.DirDest{Root: filepath.Clean(c.Path), Independent: ledgerIndependence()}, nil
	case "sftp":
		if c.Host == "" || c.User == "" || c.Dir == "" {
			return nil, errors.New("the server, the user name and the folder are required")
		}
		return &ledger.SFTPDest{Host: c.Host, Port: c.Port, User: c.User, Dir: c.Dir, Password: sec.Password,
			PrivateKey: []byte(sec.PrivateKey), Fingerprint: c.Fingerprint}, nil
	case "s3":
		return &ledger.S3Dest{Endpoint: strings.TrimRight(c.Endpoint, "/"), Region: c.Region, Bucket: c.Bucket, Prefix: c.Prefix,
			AccessKey: c.AccessKey, SecretKey: sec.SecretKey}, nil
	}
	return nil, errors.New("choose a kind of secondary storage")
}

func ledgerSecretParts() []string { return []string{"ledger", "destination"} }

// ledgerLoadDestination reads the saved destination from the database and gives it to the store. A destination whose secret
// cannot be opened (a restore onto another appliance, a replaced key file) is left unset and reported, never half-used.
func (s *server) ledgerLoadDestination(ctx context.Context) error {
	rt := &s.ledgerRT
	if rt.store == nil || s.db == nil {
		return nil
	}
	var doc string
	if err := s.db.QueryRow(ctx, `SELECT public.ledger_destination_get()::text`).Scan(&doc); err != nil {
		return err
	}
	rt.mu.Lock()
	rt.destErr = ""
	rt.mu.Unlock()
	if strings.TrimSpace(doc) == "null" {
		rt.store.SetDest(nil)
		return nil
	}
	var row struct {
		Kind   string           `json:"kind"`
		Config ledgerDestConfig `json:"config"`
	}
	if err := json.Unmarshal([]byte(doc), &row); err != nil {
		return err
	}
	row.Config.Kind = row.Kind
	var sec ledgerDestSecret
	var ct, nonce []byte
	var keyID *string
	var ver *int
	if err := s.db.QueryRow(ctx, `SELECT secret_ct, secret_nonce, secret_key_id, secret_ver FROM public.ledger_destination_secret()`).
		Scan(&ct, &nonce, &keyID, &ver); err != nil {
		return err
	}
	if len(ct) > 0 && keyID != nil && ver != nil {
		plain, err := rt.key.Open(sealbox.Sealed{Ciphertext: ct, Nonce: nonce, KeyID: *keyID, CipherVersion: *ver}, ledgerSecretParts()...)
		if err != nil {
			rt.store.SetDest(nil)
			rt.mu.Lock()
			rt.destErr = "the saved credentials of the secondary storage cannot be opened on this appliance (it was restored from another, or its key file changed): enter them again"
			rt.mu.Unlock()
			return nil
		}
		_ = json.Unmarshal(plain, &sec)
	}
	d, err := buildLedgerDestination(row.Config, sec)
	if err != nil {
		rt.store.SetDest(nil)
		rt.mu.Lock()
		rt.destErr = "the saved secondary storage is not usable: " + err.Error()
		rt.mu.Unlock()
		return nil
	}
	rt.store.SetDest(d)
	return nil
}

func parseEntries(doc []byte) ([]ledger.Entry, error) {
	var e []ledger.Entry
	if err := json.Unmarshal(doc, &e); err != nil {
		return nil, err
	}
	return e, nil
}

type ledgerTx interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
}

func dbEntries(ctx context.Context, q ledgerTx, from int64) ([]ledger.Entry, error) {
	var doc []byte
	if err := q.QueryRow(ctx, `SELECT iam_v2.ledger_export($1)::text`, from).Scan(&doc); err != nil {
		return nil, err
	}
	return parseEntries(doc)
}

func ledgerMissing(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && (pgErr.Code == "42883" || pgErr.Code == "42P01" || pgErr.Code == "3F000")
}

// ledgerReconcile holds the ledger's advisory lock for its whole run, so a file entry the database lacks is a crash's leftover, not a request in flight.
func (s *server) ledgerReconcile(ctx context.Context) ledgerStatus {
	rt := &s.ledgerRT
	rt.reconcileMu.Lock()
	defer rt.reconcileMu.Unlock()
	st := ledgerStatus{Checked: time.Now().UTC()}
	if rt.store != nil {
		st.Local.Path = rt.store.LocalPath
	}
	finish := func() ledgerStatus {
		rt.mu.Lock()
		rt.status = st
		rt.mu.Unlock()
		return st
	}
	problem := func(f string, a ...any) { st.Problems = append(st.Problems, fmt.Sprintf(f, a...)) }
	if rt.store == nil || s.db == nil {
		st.State, st.Message = "unavailable", "the erasure ledger is not available on this appliance"
		return finish()
	}
	if err := s.ledgerLoadDestination(ctx); err != nil && !ledgerMissing(err) {
		slog.Warn("erasure ledger: the secondary storage setting could not be read", "err", err)
	}
	rt.mu.Lock()
	destErr := rt.destErr
	rt.mu.Unlock()

	tx, err := s.db.Begin(ctx)
	if err != nil {
		st.State, st.Message = "unavailable", "the database could not be reached"
		return finish()
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, ledgerLockSQL); err != nil {
		st.State, st.Message = "unavailable", "the ledger is busy; try again in a moment"
		return finish()
	}
	var verify struct {
		OK      bool  `json:"ok"`
		Entries int64 `json:"entries"`
	}
	var vdoc []byte
	if err := tx.QueryRow(ctx, `SELECT iam_v2.ledger_verify()::text`).Scan(&vdoc); err != nil {
		if ledgerMissing(err) {
			st.State, st.Message = "unavailable", "this database does not have the erasure ledger yet (it is created by the next update)"
			return finish()
		}
		st.State, st.Message = "unavailable", "the ledger in the database could not be read: "+err.Error()
		return finish()
	}
	_ = json.Unmarshal(vdoc, &verify)
	st.DB.Entries, st.DB.Intact = verify.Entries, verify.OK
	var head struct {
		Ledger      string `json:"ledger"`
		Seq         int64  `json:"seq"`
		Hash        string `json:"hash"`
		Unconfirmed int64  `json:"unconfirmed"`
	}
	var hdoc []byte
	if err := tx.QueryRow(ctx, `SELECT iam_v2.ledger_head()::text`).Scan(&hdoc); err == nil {
		_ = json.Unmarshal(hdoc, &head)
	}
	st.DB.Unconfirmed = head.Unconfirmed
	if !verify.OK {
		st.State, st.Message = "blocked", "the erasure ledger in the database is damaged"
		problem("the erasure ledger in the database fails its own chain check")
		return finish()
	}
	dbE, err := dbEntries(ctx, tx, 0)
	if err != nil || ledger.Extends(0, "", dbE) != nil {
		st.State, st.Message = "blocked", "the erasure ledger in the database could not be verified"
		return finish()
	}

	local := rt.store.ReadLocal()
	if local.Reach != nil {
		st.Local.Error = local.Reach.Error()
		st.State, st.Message = "attention", "the erasure ledger on the appliance cannot be read: "+local.Reach.Error()
		return finish()
	}
	healLocal := func(why string) bool {
		if local.Present {
			_ = os.Rename(rt.store.LocalPath, rt.store.LocalPath+".damaged-"+time.Now().UTC().Format("20060102T150405Z"))
		}
		if err := rt.store.RewriteLocal(dbE); err != nil {
			problem("the copy on the appliance could not be rebuilt: %v", err)
			return false
		}
		slog.Warn("erasure ledger: the copy on the appliance was rebuilt from the database", "why", why, "entries", len(dbE))
		local = rt.store.ReadLocal()
		return true
	}
	switch {
	case !local.Present:
		if !healLocal("it was missing") {
			st.State, st.Message = "attention", "the erasure ledger on the appliance is missing and could not be rebuilt"
			return finish()
		}
	case local.Parse.Err != nil:
		if !healLocal("it was damaged: " + local.Parse.Err.Error()) {
			st.State, st.Message = "attention", "the erasure ledger on the appliance is damaged and could not be rebuilt"
			return finish()
		}
	}
	if local.Parse.Torn {
		if err := os.Truncate(rt.store.LocalPath, int64(local.Parse.ValidBytes)); err == nil {
			local = rt.store.ReadLocal()
		}
	}
	// A file holding only the first entry of ANOTHER ledger (a replacement appliance's new ledger after a restore from the old one) says nothing about erasures: the database is the truth.
	if local.Present && local.Parse.Err == nil && len(local.Parse.Entries) == 1 && local.Parse.LedgerID() != head.Ledger {
		if !healLocal("it held only the first entry of another ledger") {
			st.State, st.Message = "attention", "the erasure ledger on the appliance could not be rebuilt"
			return finish()
		}
	}
	le := local.Parse.Entries
	n := min(len(le), len(dbE))
	for i := 0; i < n; i++ {
		if le[i].Hash != dbE[i].Hash {
			st.State, st.Message = "blocked", "the copy on the appliance and the database disagree about an erasure: support is needed"
			problem("entry %d differs between the file and the database", i+1)
			st.Local.Present, st.Local.Intact, st.Local.Entries = true, true, len(le)
			return finish()
		}
	}
	switch {
	case len(dbE) > len(le):
		if err := rt.store.AppendLocal(dbE[len(le):]); err != nil {
			problem("the copy on the appliance could not be brought up to date: %v", err)
		} else {
			local = rt.store.ReadLocal()
			le = local.Parse.Entries
		}
	case len(le) > len(dbE):
		// The file holds entries the database lacks (a restore from an older backup, or a crash between file write and database commit): applied now.
		adopt := len(dbE) == 1 && le[0].Ledger != head.Ledger
		doc, _ := json.Marshal(le[len(dbE):])
		if adopt {
			doc, _ = json.Marshal(le)
		}
		var res []byte
		if err := tx.QueryRow(ctx, `SELECT public.ledger_apply($1::jsonb, $2)::text`, string(doc), adopt).Scan(&res); err != nil {
			problem("the erasures the database was missing could not be applied: %v", err)
		} else {
			var r struct {
				Applied  int             `json:"applied"`
				Failed   json.RawMessage `json:"failed"`
				Conflict json.RawMessage `json:"conflict"`
			}
			_ = json.Unmarshal(res, &r)
			if len(r.Conflict) > 0 && string(r.Conflict) != "null" {
				st.State, st.Message = "blocked", "the ledger on the appliance does not continue the one in the database: support is needed"
				problem("conflict: %s", r.Conflict)
				return finish()
			}
			if len(r.Failed) > 0 && string(r.Failed) != "null" {
				problem("an erasure could not be applied again and will be retried: %s", r.Failed)
			}
			if r.Applied > 0 {
				slog.Warn("erasure ledger: erasures the database did not have were applied", "applied", r.Applied)
			}
		}
	}
	st.Local.Present, st.Local.Intact, st.Local.Entries = true, true, len(le)

	dest := rt.store.Destination()
	switch {
	case dest == nil && destErr != "":
		st.External.Error = destErr
		st.State, st.Message = "attention", destErr
	case dest == nil:
		st.State, st.Message = "not_configured", "set up the secondary storage for the erasure ledger: erasure requests are refused until it is"
	default:
		st.External.Configured, st.External.Kind, st.External.Describe = true, dest.Kind(), dest.Describe()
		localBytes, _ := os.ReadFile(rt.store.LocalPath)
		lseq, lhash := local.Parse.Head()
		cur, _ := rt.store.CipherFingerprint(ctx)
		info, objOK, found, err := rt.store.ReadExternalHead(ctx)
		inSync := err == nil && found && objOK && info != nil && info.Seq == lseq && info.Hash == lhash &&
			info.PlainSHA256 == sha256hex(localBytes[:local.Parse.ValidBytes]) && info.CipherFP == cur
		switch {
		case err != nil:
			st.External.Error = err.Error()
			problem("the secondary storage could not be read: %v", err)
		case found && info != nil && info.Ledger != local.Parse.LedgerID() && lseq == 1 && info.Seq > 1:
			st.State, st.Message = "recovery_needed", fmt.Sprintf("this storage holds the erasure ledger of an earlier appliance (%d entries): restore its backup to continue it, or recover from it", info.Seq)
			st.External.Reachable, st.External.Entries, st.External.Encrypted = true, int(info.Seq), info.Encrypted
			problem("the secondary storage holds an earlier appliance's ledger")
		case found && info != nil && info.Ledger == local.Parse.LedgerID() && info.Seq > lseq:
			st.State, st.Message = "recovery_needed", fmt.Sprintf("the secondary storage holds erasures newer than this appliance's (up to entry %d, this appliance has %d): recover from it", info.Seq, lseq)
			st.External.Reachable, st.External.Entries, st.External.Encrypted = true, int(info.Seq), info.Encrypted
			problem("the secondary storage is ahead of the appliance")
		case inSync:
			st.External.Reachable, st.External.InSync, st.External.Entries, st.External.Encrypted = true, true, int(info.Seq), info.Encrypted
		default:
			if perr := rt.store.PushExternal(ctx); perr != nil {
				st.External.Error = perr.Error()
				problem("the secondary copy could not be brought up to date: %v", perr)
				if errors.Is(perr, ledger.ErrExternalAhead) {
					st.State, st.Message = "recovery_needed", "the secondary storage holds erasures newer than this appliance's: recover from it"
				}
				if _, _, found2, e2 := rt.store.ReadExternalHead(ctx); e2 == nil || found2 {
					st.External.Reachable = true
				}
			} else {
				st.External.Reachable, st.External.InSync, st.External.Entries = true, true, len(le)
				if i2, _, _, e := rt.store.ReadExternalHead(ctx); e == nil && i2 != nil {
					st.External.Encrypted = i2.Encrypted
				}
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		problem("the reconciliation could not be committed: %v", err)
	}
	if dest != nil {
		ok := st.External.InSync
		msg := st.External.Error
		if _, err := s.db.Exec(ctx, `SELECT public.ledger_destination_note($1, $2)`, ok, msg); err != nil {
			slog.Debug("erasure ledger: destination note not saved", "err", err)
		}
		var lastDoc string
		if err := s.db.QueryRow(ctx, `SELECT COALESCE(public.ledger_destination_get() ->> 'last_ok_at', '')`).Scan(&lastDoc); err == nil && lastDoc != "" {
			if t, err := time.Parse(time.RFC3339Nano, lastDoc); err == nil {
				st.External.LastOK = &t
			}
		}
	}
	if st.State == "" {
		switch {
		case st.External.InSync && len(st.Problems) == 0 && st.DB.Unconfirmed == 0:
			st.State, st.Message, st.ErasureAvailable = "ok", "both copies of the erasure ledger are in step", true
		default:
			st.State = "attention"
			if st.Message == "" {
				st.Message = "the erasure ledger needs attention: erasure requests are paused until both copies are in step"
			}
		}
	}
	return finish()
}

func sha256hex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func (s *server) ledgerStatusGet(w http.ResponseWriter, r *http.Request) {
	rt := &s.ledgerRT
	rt.mu.Lock()
	cached, age := rt.status, time.Since(rt.status.Checked)
	rt.mu.Unlock()
	if cached.Checked.IsZero() || age > time.Minute || r.URL.Query().Get("fresh") == "1" {
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		cached = s.ledgerReconcile(ctx)
	}
	writeJSON(w, http.StatusOK, cached)
}

func (s *server) ledgerPrepare(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	st := s.ledgerReconcile(ctx)
	code := http.StatusOK
	if !st.ErasureAvailable {
		code = http.StatusConflict
	}
	writeJSON(w, code, st)
}

type ledgerCommitReq struct {
	Entries []ledger.Entry `json:"entries"`
}

func ledgerFailure(w http.ResponseWriter, err error) {
	code, status, msg := "ledger_failed", http.StatusInternalServerError, "the erasure ledger could not be written"
	var ee *ledger.ExternalError
	var le *ledger.LocalError
	switch {
	case errors.Is(err, ledger.ErrNotConfigured):
		code, status, msg = "ledger_not_configured", http.StatusConflict, "the secondary storage for the erasure ledger is not set up"
	case errors.Is(err, ledger.ErrExternalAhead), errors.Is(err, ledger.ErrExternalFork):
		code, status, msg = "ledger_recovery_needed", http.StatusConflict, err.Error()
	case errors.Is(err, ledger.ErrConflict):
		code, status, msg = "ledger_conflict", http.StatusConflict, "the ledger changed underneath this request; try again"
	case errors.Is(err, ledger.ErrLocalDamaged):
		code, status, msg = "ledger_local_damaged", http.StatusServiceUnavailable, "the erasure ledger on the appliance is damaged; it is being repaired"
	case errors.As(err, &ee):
		code, status, msg = "ledger_external_unavailable", http.StatusServiceUnavailable, ee.Error()
	case errors.As(err, &le):
		code, status, msg = "ledger_local_failed", http.StatusInternalServerError, le.Error()
	}
	writeJSON(w, status, map[string]any{"error": code, "message": msg})
}

func (s *server) ledgerCommit(w http.ResponseWriter, r *http.Request) {
	var in ledgerCommitReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in); err != nil || len(in.Entries) == 0 || len(in.Entries) > 100 {
		httpErr(w, http.StatusBadRequest, "ledger entries are required")
		return
	}
	if s.ledgerRT.store == nil {
		httpErr(w, http.StatusServiceUnavailable, "the erasure ledger is not available")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 90*time.Second)
	defer cancel()
	if err := s.ledgerRT.store.Commit(ctx, in.Entries); err != nil {
		slog.Error("erasure ledger: commit refused", "err", err)
		if s.db != nil {
			_, _ = s.db.Exec(ctx, `SELECT public.ledger_destination_note(false, $1)`, err.Error())
		}
		ledgerFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "seq": in.Entries[len(in.Entries)-1].Seq})
}

func (s *server) ledgerAbort(w http.ResponseWriter, r *http.Request) {
	var in ledgerCommitReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&in); err != nil || len(in.Entries) == 0 {
		httpErr(w, http.StatusBadRequest, "ledger entries are required")
		return
	}
	if s.ledgerRT.store == nil {
		httpErr(w, http.StatusServiceUnavailable, "the erasure ledger is not available")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 60*time.Second)
	defer cancel()
	if err := s.ledgerRT.store.Abort(ctx, in.Entries); err != nil {
		slog.Error("erasure ledger: abort failed", "err", err)
		ledgerFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type ledgerDestReq struct {
	ledgerDestConfig
	ledgerDestSecret
	TestOnly bool   `json:"test_only"`
	By       string `json:"by"`
}

// ledgerDestinationProbe returns the SFTP server's host key fingerprint for the administrator to confirm before it is saved.
func (s *server) ledgerDestinationProbe(w http.ResponseWriter, r *http.Request) {
	var in ledgerDestReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil || in.Host == "" {
		httpErr(w, http.StatusBadRequest, "the server is required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	d := &ledger.SFTPDest{Host: in.Host, Port: in.Port}
	fp, err := d.ProbeHostKey(ctx)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "unreachable", "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"fingerprint": fp})
}

// ledgerDestinationPut tests a destination and, unless test_only, saves it as the secondary storage and makes the first copy.
func (s *server) ledgerDestinationPut(w http.ResponseWriter, r *http.Request) {
	rt := &s.ledgerRT
	var in ledgerDestReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<17)).Decode(&in); err != nil {
		httpErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	if rt.store == nil || s.db == nil {
		httpErr(w, http.StatusServiceUnavailable, "the erasure ledger is not available")
		return
	}
	dest, err := buildLedgerDestination(in.ledgerDestConfig, in.ledgerDestSecret)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid", "message": err.Error()})
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 3*time.Minute)
	defer cancel()
	if err := dest.Check(ctx); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "storage_unusable", "message": err.Error()})
		return
	}
	// Another appliance's ledger in the storage is never overwritten; this appliance's own (a recovery) is kept and recovered from.
	probe := ledger.NewStore(ledger.Config{LocalPath: filepath.Join(os.TempDir(), "unused-ledger-probe"), Dest: dest})
	info, _, found, perr := probe.ReadExternalHead(ctx)
	if perr != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": "storage_unusable", "message": perr.Error()})
		return
	}
	var ourID string
	var ourEntries int64
	var hdoc []byte
	if err := s.db.QueryRow(ctx, `SELECT iam_v2.ledger_head()::text`).Scan(&hdoc); err == nil {
		var h struct {
			Ledger string `json:"ledger"`
			Seq    int64  `json:"seq"`
		}
		_ = json.Unmarshal(hdoc, &h)
		ourID, ourEntries = h.Ledger, h.Seq
	}
	if found && info != nil && info.Ledger != ourID && ourEntries > 1 {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "belongs_to_another",
			"message": "that storage already holds the erasure ledger of a different appliance. Use a folder, bucket or prefix of its own."})
		return
	}
	existing := map[string]any{}
	if found && info != nil {
		existing = map[string]any{"entries": info.Seq, "same_appliance": info.Ledger == ourID, "encrypted": info.Encrypted, "updated_at": info.UpdatedAt}
	}
	if in.TestOnly {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "describe": dest.Describe(), "existing_ledger": existing})
		return
	}
	cfg := in.ledgerDestConfig
	cfgJSON, _ := json.Marshal(cfg)
	secJSON, _ := json.Marshal(in.ledgerDestSecret)
	var ct, nonce []byte
	var keyID *string
	var ver *int
	if string(secJSON) != "{}" {
		if !rt.key.Present() {
			httpErr(w, http.StatusServiceUnavailable, "the key that seals the storage credentials is missing on this appliance")
			return
		}
		sealed, err := rt.key.Seal(secJSON, ledgerSecretParts()...)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, "the credentials could not be sealed")
			return
		}
		ct, nonce, keyID, ver = sealed.Ciphertext, sealed.Nonce, &sealed.KeyID, &sealed.CipherVersion
	}
	by := strings.TrimSpace(in.By)
	if by == "" {
		by = "operator"
	}
	if _, err := s.db.Exec(ctx, `SELECT public.ledger_destination_set($1,$2::jsonb,$3,$4,$5,$6,$7)`, cfg.Kind, string(cfgJSON), ct, nonce, keyID, ver, by); err != nil {
		httpErr(w, http.StatusInternalServerError, "the secondary storage could not be saved: "+err.Error())
		return
	}
	rt.store.SetDest(dest)
	st := s.ledgerReconcile(ctx)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "status": st, "existing_ledger": existing})
}

type ledgerRecoverReq struct {
	Identity string `json:"identity"`
}

// ledgerRecover reads and verifies the secondary copy with a backup key holder's key, checks it against the database and primary copy witnesses, and only then applies it.
func (s *server) ledgerRecover(w http.ResponseWriter, r *http.Request) {
	var in ledgerRecoverReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil {
		httpErr(w, http.StatusBadRequest, "malformed request")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Minute)
	defer cancel()
	detail, err := s.ledgerRecoverFromExternal(ctx, in.Identity)
	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "recovery_refused", "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "detail": detail, "status": s.ledgerReconcile(ctx)})
}

func (s *server) ledgerRecoverFromExternal(ctx context.Context, identityText string) (string, error) {
	rt := &s.ledgerRT
	if rt.store == nil || rt.store.Destination() == nil {
		return "", errors.New("the secondary storage is not set up")
	}
	var ident age.Identity
	if strings.TrimSpace(identityText) != "" {
		id, err := backupcrypt.ParseIdentity(identityText)
		if err != nil {
			return "", errors.New("that is not a backup key file")
		}
		ident = id
	}
	ext := rt.store.ReadExternal(ctx, ident)
	if ext.Reach != nil {
		return "", fmt.Errorf("the secondary copy could not be read: %v", ext.Reach)
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, ledgerLockSQL); err != nil {
		return "", err
	}
	var hdoc []byte
	if err := tx.QueryRow(ctx, `SELECT iam_v2.ledger_head()::text`).Scan(&hdoc); err != nil {
		return "", err
	}
	var head struct {
		Ledger string `json:"ledger"`
		Seq    int64  `json:"seq"`
		Hash   string `json:"hash"`
	}
	_ = json.Unmarshal(hdoc, &head)
	local := rt.store.ReadLocal()
	wit := []ledger.Witness{{Source: "the database", Ledger: head.Ledger, Seq: head.Seq, Hash: head.Hash}}
	// a database that has only its own first entry (a fresh appliance) witnesses nothing about another appliance's ledger
	if head.Seq == 1 && ext.Intact() && ext.Parse.LedgerID() != head.Ledger {
		wit = nil
	}
	copies := []ledger.Copy{ext}
	if local.Intact() {
		copies = append(copies, local)
	}
	copies, wit = settleFreshLedgers(copies, wit)
	v := ledger.Assess(copies, wit)
	if v.Best == nil || (!v.Complete && !(head.Seq == 1 && ext.Intact())) || v.Conflict {
		return "", errors.New(strings.Join(v.Reasons, "; "))
	}
	entries := v.Best.Parse.Entries
	var dbs int64 = head.Seq
	adopt := head.Seq == 1 && entries[0].Ledger != head.Ledger
	feed := entries
	if !adopt && int(dbs) <= len(entries) {
		feed = entries[dbs:]
	}
	if len(feed) > 0 {
		doc, _ := json.Marshal(feed)
		var res []byte
		if err := tx.QueryRow(ctx, `SELECT public.ledger_apply($1::jsonb, $2)::text`, string(doc), adopt).Scan(&res); err != nil {
			return "", err
		}
		var r struct {
			Applied  int             `json:"applied"`
			Failed   json.RawMessage `json:"failed"`
			Conflict json.RawMessage `json:"conflict"`
		}
		_ = json.Unmarshal(res, &r)
		if string(r.Conflict) != "" && string(r.Conflict) != "null" {
			return "", fmt.Errorf("the secondary copy does not continue this appliance's ledger: %s", r.Conflict)
		}
		if string(r.Failed) != "" && string(r.Failed) != "null" {
			return "", fmt.Errorf("an erasure in the secondary copy could not be applied: %s", r.Failed)
		}
	}
	if err := rt.store.RewriteLocal(entries); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return fmt.Sprintf("the ledger was recovered from the secondary storage (%d entries)", len(entries)), nil
}

// ledgerCheckpoint appends a heartbeat entry about once a day, through both copies, so every backup records a recent head and a
// copy cut short is noticed against the most recent backup. It never runs without both copies.
func (s *server) ledgerCheckpoint(ctx context.Context) error {
	rt := &s.ledgerRT
	if rt.store == nil || rt.store.Destination() == nil || s.db == nil {
		return nil
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, ledgerLockSQL); err != nil {
		return err
	}
	var recent bool
	if err := tx.QueryRow(ctx, `SELECT COALESCE((iam_v2.ledger_head() ->> 'at_us')::bigint, 0) > (extract(epoch from now() - interval '20 hours') * 1000000)::bigint`).Scan(&recent); err != nil {
		return err
	}
	if recent {
		return nil
	}
	tenant, site := s.tenID, s.siteID
	if tenant == "" || site == "" {
		tenant, site = "00000000-0000-0000-0000-000000000000", "00000000-0000-0000-0000-000000000000"
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT iam_v2.ledger_append($1::uuid,$2::uuid,'checkpoint','{}'::jsonb)::text`, tenant, site).Scan(&raw); err != nil {
		return err
	}
	var e ledger.Entry
	if err := json.Unmarshal(raw, &e); err != nil {
		return err
	}
	if err := rt.store.Commit(ctx, []ledger.Entry{e}); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `SELECT iam_v2.ledger_confirm($1)`, []string{e.Hash}); err != nil {
		_ = rt.store.Abort(ctx, []ledger.Entry{e})
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		_ = rt.store.Abort(ctx, []ledger.Entry{e})
		return err
	}
	return nil
}

func (s *server) runLedger(ctx context.Context) {
	// Never while a restore is swapping databases: the restore reconciles the ledger itself, in order, at the right moments.
	if restoreInFlight.Load() || readMaintenance().Active {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	st := s.ledgerReconcile(cctx)
	switch st.State {
	case "ok":
		if err := s.ledgerCheckpoint(cctx); err != nil && ctx.Err() == nil {
			slog.Warn("erasure ledger: the daily checkpoint could not be written", "err", err)
		}
	case "not_configured":
		slog.Warn("erasure ledger: the secondary storage is not set up; erasure requests are refused until it is")
	default:
		slog.Error("erasure ledger needs attention", "state", st.State, "message", st.Message, "problems", st.Problems)
	}
}

// ledgerLoop reconciles shortly after start-up and then hourly, whether or not the appliance has a site yet.
func (s *server) ledgerLoop(ctx context.Context) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(45 * time.Second):
	}
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		s.runLedger(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// ledgerForgetPrevious removes the previous customer's ledger from the appliance after a reassignment: the primary file (and any
// copy set aside), the secondary-storage setting, the cached status. The next reconciliation builds the new ledger's files.
func (s *server) ledgerForgetPrevious() {
	rt := &s.ledgerRT
	if rt.store == nil {
		return
	}
	rt.store.SetDest(nil)
	_ = os.Remove(rt.store.LocalPath)
	if m, _ := filepath.Glob(rt.store.LocalPath + ".*"); len(m) > 0 {
		for _, f := range m {
			_ = os.Remove(f)
		}
	}
	rt.mu.Lock()
	rt.status = ledgerStatus{}
	rt.destErr = ""
	rt.mu.Unlock()
}
