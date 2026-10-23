package main

// Under the "backups" key: readers see the encryption state; changing holders or running the test needs backups W.
//
// The private key is generated here, returned once and forgotten: never stored, logged or audited (the database
// keeps the public key and a hash of the check code). A key supplied for the restore test goes to scd for that
// one request and is dropped the same way.

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/stayconnect/enterprise/data-plane/internal/backupcrypt"
)

const restoreTestDefaultMonths = 6

func (s *server) backupKeysRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.getBackupKeys)
	r.Post("/holders", s.addBackupKeyHolder)
	r.Post("/holders/{id}/confirm", s.confirmBackupKeyHolder)
	r.Post("/holders/{id}/remove", s.removeBackupKeyHolder)
	r.Post("/test", s.testBackupKey)
	return r
}

type backupKeyHolder struct {
	ID           string     `json:"id"`
	Label        string     `json:"label"`
	PublicKey    string     `json:"public_key"`
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	CreatedBy    string     `json:"created_by"`
	ConfirmedAt  *time.Time `json:"confirmed_at"`
	RemovedAt    *time.Time `json:"removed_at"`
	RemovedBy    *string    `json:"removed_by"`
	RemoveReason *string    `json:"remove_reason"`
	LastTestedAt *time.Time `json:"last_tested_at"`
}

func (s *server) restoreTestMonths(r *http.Request) int {
	code, body, err := s.scd.call(r.Context(), http.MethodGet, "/v1/backup/settings", nil)
	if err != nil || code != http.StatusOK {
		return restoreTestDefaultMonths
	}
	var doc struct {
		Retention []struct {
			Key   string `json:"key"`
			Value int    `json:"value"`
		} `json:"retention"`
	}
	if json.Unmarshal(body, &doc) == nil {
		for _, x := range doc.Retention {
			if x.Key == "RESTORE_TEST_MONTHS" && x.Value > 0 {
				return x.Value
			}
		}
	}
	return restoreTestDefaultMonths
}

func (s *server) getBackupKeys(w http.ResponseWriter, r *http.Request) {
	if s.tenantID == "" || s.siteID == "" {
		writeAwaitingAssignment(w, "backup keys")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	// Pending holders past their hour to confirm can never become active, so they are hidden.
	rows, err := s.db.Query(ctx, `SELECT id::text, label, public_key, status, created_at, created_by, confirmed_at,
	        removed_at, removed_by, remove_reason, last_tested_at
	   FROM public.backup_key_holders
	  WHERE tenant_id=$1 AND site_id=$2 AND (status <> 'PENDING' OR created_at > now() - interval '1 hour')
	  ORDER BY status = 'REMOVED', created_at`, s.tenantID, s.siteID)
	if err != nil {
		if isUndefinedObject(err) {
			writeJSON(w, http.StatusOK, map[string]any{"available": false, "encrypted": false, "holders": []any{}})
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "the backup keys could not be read")
		return
	}
	defer rows.Close()
	holders := []backupKeyHolder{}
	active := 0
	var firstActive, lastTest *time.Time
	for rows.Next() {
		var h backupKeyHolder
		if err := rows.Scan(&h.ID, &h.Label, &h.PublicKey, &h.Status, &h.CreatedAt, &h.CreatedBy, &h.ConfirmedAt,
			&h.RemovedAt, &h.RemovedBy, &h.RemoveReason, &h.LastTestedAt); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "the backup keys could not be read")
			return
		}
		if h.Status == "ACTIVE" {
			active++
			if h.ConfirmedAt != nil && (firstActive == nil || h.ConfirmedAt.Before(*firstActive)) {
				firstActive = h.ConfirmedAt
			}
		}
		if h.LastTestedAt != nil && (lastTest == nil || h.LastTestedAt.After(*lastTest)) {
			lastTest = h.LastTestedAt
		}
		holders = append(holders, h)
	}
	if rows.Err() != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the backup keys could not be read")
		return
	}

	// The reminder clock starts at the last successful test, or when encryption was switched on, so a new setup is not already late.
	months := s.restoreTestMonths(r)
	test := map[string]any{"interval_months": months, "last_tested_at": lastTest, "overdue": false, "due_at": nil}
	if active > 0 {
		since := firstActive
		if lastTest != nil {
			since = lastTest
		}
		if since != nil {
			due := since.AddDate(0, months, 0)
			test["due_at"] = due
			test["overdue"] = time.Now().After(due)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"available": true, "encrypted": active > 0, "active_holders": active,
		"holders": holders, "restore_test": test,
	})
}

func (s *server) addBackupKeyHolder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Label           string `json:"label"`
		AcknowledgeLoss bool   `json:"acknowledge_loss"`
		Password        string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	in.Label = strings.Join(strings.Fields(in.Label), " ")
	if in.Label == "" || len(in.Label) > 80 {
		jsonErr(w, http.StatusBadRequest, "invalid_label", "name the key holder (up to 80 characters), for example \"IT manager\" or \"Hotel safe\"")
		return
	}
	if !in.AcknowledgeLoss {
		jsonErr(w, http.StatusBadRequest, "acknowledgement_required",
			"confirm that you understand: if every key is lost, the backups cannot be read by anyone")
		return
	}
	if s.tenantID == "" || s.siteID == "" {
		jsonErr(w, http.StatusConflict, "awaiting_assignment", "this appliance has no site yet")
		return
	}
	actor, ok := s.portalStepUp(w, r, in.Password)
	if !ok {
		return
	}
	secret, public, err := backupcrypt.GenerateKey()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "a key could not be generated")
		return
	}
	check, err := backupcrypt.NewCheckCode()
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "a check code could not be generated")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var id string
	if err := s.db.QueryRow(ctx, `SELECT public.backup_key_holder_add($1::uuid,$2::uuid,$3,$4,$5,true,$6)::text`,
		s.tenantID, s.siteID, in.Label, public, backupcrypt.CheckCodeHash(check), actor).Scan(&id); err != nil {
		backupKeyDBError(w, err, "the key holder could not be added")
		return
	}
	created := time.Now()
	s.audit(r, "backup_key.added", "backup_key_holder", id, map[string]any{"label": in.Label, "public_key": public})
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "label": in.Label, "public_key": public, "created_at": created,
		"private_key": secret, "check_code": check,
		"key_file":      backupcrypt.KeyFile(in.Label, s.siteID, secret, public, check, created),
		"key_file_name": "stayconnect-backup-key-" + fileSafe(in.Label) + ".txt",
	})
}

func (s *server) confirmBackupKeyHolder(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		CheckCode string `json:"check_code"`
	}
	if err := decodeJSON(r, &in); err != nil || !uuidRe.MatchString(id) || strings.TrimSpace(in.CheckCode) == "" {
		jsonErr(w, http.StatusBadRequest, "invalid", "type the check code from the key file or the recovery sheet")
		return
	}
	actor := protectionActor(sessFrom(r.Context()))
	ctx, cancel := dbCtx(r)
	defer cancel()
	if _, err := s.db.Exec(ctx, `SELECT public.backup_key_holder_confirm($1::uuid,$2::uuid,$3::uuid,$4,$5)`,
		s.tenantID, s.siteID, id, backupcrypt.CheckCodeHash(in.CheckCode), actor); err != nil {
		backupKeyDBError(w, err, "the key holder could not be confirmed")
		return
	}
	s.audit(r, "backup_key.confirmed", "backup_key_holder", id, nil)
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "ACTIVE"})
}

func (s *server) removeBackupKeyHolder(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in struct {
		Reason   string `json:"reason"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &in); err != nil || !uuidRe.MatchString(id) {
		jsonErr(w, http.StatusBadRequest, "invalid", "malformed request")
		return
	}
	in.Reason = strings.TrimSpace(in.Reason)
	if !validReason(in.Reason) {
		jsonErr(w, http.StatusBadRequest, "reason_required", "a bounded reason (4-500 characters) is required")
		return
	}
	actor, ok := s.portalStepUp(w, r, in.Password)
	if !ok {
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	if _, err := s.db.Exec(ctx, `SELECT public.backup_key_holder_remove($1::uuid,$2::uuid,$3::uuid,$4,$5)`,
		s.tenantID, s.siteID, id, actor, in.Reason); err != nil {
		backupKeyDBError(w, err, "the key holder could not be removed")
		return
	}
	s.audit(r, "backup_key.removed", "backup_key_holder", id, map[string]any{"reason": in.Reason})
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "status": "REMOVED"})
}

// testBackupKey has scd open the newest encrypted backup with the supplied key; success is recorded against the
// holder, failure is audited and changes nothing.
func (s *server) testBackupKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Identity string `json:"identity"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil || strings.TrimSpace(in.Identity) == "" {
		jsonErr(w, http.StatusBadRequest, "invalid", "paste the key file's contents, or choose the key file")
		return
	}
	code, body, err := s.scd.call(r.Context(), http.MethodPost, "/v1/backup/key-test", map[string]any{"identity": in.Identity})
	if err != nil || (code != http.StatusOK && code != http.StatusBadRequest && code != http.StatusConflict) {
		jsonErr(w, http.StatusBadGateway, "test_failed", scdDetail(body, err))
		return
	}
	if code != http.StatusOK {
		s.audit(r, "backup_key.restore_test_failed", "backup_key_holder", "", map[string]any{"detail": scdDetail(body, nil)})
		jsonErr(w, code, "test_failed", scdDetail(body, nil))
		return
	}
	var res struct {
		OK        bool   `json:"ok"`
		Backup    string `json:"backup"`
		PublicKey string `json:"public_key"`
		Detail    string `json:"detail"`
	}
	if json.Unmarshal(body, &res) != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "the test result could not be read")
		return
	}
	if !res.OK {
		s.audit(r, "backup_key.restore_test_failed", "backup", res.Backup, map[string]any{"detail": res.Detail})
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "backup": res.Backup, "detail": res.Detail})
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	var holder, label string
	// The CTE makes the recording call run once; in a WHERE clause it ran once per holder row.
	if err := s.db.QueryRow(ctx, `WITH t AS (SELECT public.backup_key_holder_tested($1::uuid,$2::uuid,$3,$4) AS id)
	     SELECT h.id::text, h.label FROM t JOIN public.backup_key_holders h ON h.id = t.id`,
		s.tenantID, s.siteID, res.PublicKey, protectionActor(sessFrom(r.Context()))).Scan(&holder, &label); err != nil {
		backupKeyDBError(w, err, "the test passed but could not be recorded")
		return
	}
	s.audit(r, "backup_key.restore_tested", "backup_key_holder", holder, map[string]any{"backup": res.Backup})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "backup": res.Backup, "holder_id": holder, "holder": label})
}

func fileSafe(label string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(label) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	if out := strings.TrimSuffix(b.String(), "-"); out != "" {
		return out
	}
	return "holder"
}

func backupKeyDBError(w http.ResponseWriter, err error, what string) {
	var pgErr *pgconn.PgError
	switch {
	case isUndefinedObject(err):
		jsonErr(w, http.StatusServiceUnavailable, "not_available", "this appliance's database does not have backup keys yet")
	case errors.As(err, &pgErr) && pgErr.Code == "P0002":
		jsonErr(w, http.StatusNotFound, "not_found", pgErr.Message)
	case errors.As(err, &pgErr) && pgErr.Code == "55006":
		jsonErr(w, http.StatusConflict, "last_holder", pgErr.Message)
	case errors.As(err, &pgErr) && (pgErr.Code == "22023" || pgErr.Code == "23514"):
		jsonErr(w, http.StatusBadRequest, "invalid", pgErr.Message)
	case errors.As(err, &pgErr) && pgErr.Code == "23505":
		jsonErr(w, http.StatusConflict, "duplicate", "this key is already registered")
	default:
		jsonErr(w, http.StatusInternalServerError, "internal", what)
	}
}
