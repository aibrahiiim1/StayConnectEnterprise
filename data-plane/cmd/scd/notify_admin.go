package main

// NOTIFICATION SENDERS, OPERATED LIKE A PRODUCT: contract docs/architecture/ONEGATE_CLIENT_IDENTITY_AND_ACCESS_POLICY.md §7.
//
// Three things the boot-time loader could not do:
//   1. RELOAD. Senders were resolved once at start-up, so an SMTP change in the Admin Console took effect at
//      the next restart. edged now pokes /v1/admin/notify/reload after every provider write.
//   2. SEAL. The secret of a sender (SMTP password, API key, access token) is sealed by scd under
//      notify_dek.key and stored in iam_v2.notification_provider_secret_generations; the plaintext api_key
//      column is left NULL for every row written from now on. edged forwards the secret here once and never
//      reads it back; the loader prefers a sealed generation and falls back to api_key for rows written
//      before 0107, so nothing breaks on upgrade.
//   3. TEST. "Send a test message" builds the sender from the row as stored -- the same construction the
//      loader uses -- delivers one message to an operator-supplied address, and records the outcome on the
//      row (last_success_at / last_error) where the Delivery page shows it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/localkeys"
	"github.com/stayconnect/enterprise/data-plane/internal/mail"
	"github.com/stayconnect/enterprise/data-plane/internal/notifyloader"
	"github.com/stayconnect/enterprise/data-plane/internal/sealbox"
	"github.com/stayconnect/enterprise/data-plane/internal/sms"
	"github.com/stayconnect/enterprise/data-plane/internal/whatsapp"
)

const (
	notifyKeyFile = "notify_dek.key"
	notifyDomain  = "notify-secret-aead:v1"
)

// notifySet is the resolved sender trio, swapped atomically on reload.
type notifySet struct {
	mail     mail.Mailer
	sms      sms.Sender
	whatsapp whatsapp.Sender
	mu       sync.RWMutex
}

// initNotifyKey loads the sealing key. Without it, sealed secrets cannot be opened and their senders fall
// back to the stub (the method is then not offered); plaintext rows keep working.
func (s *server) initNotifyKey(secretsDir string) {
	raw, err := localkeys.LoadExistingKey(filepath.Join(secretsDir, notifyKeyFile))
	if err != nil {
		slog.Warn("notification secrets: sealing key missing (run keybootstrap at deploy); sealed senders unavailable", "err", err)
		return
	}
	k, err := sealbox.NewKey(notifyDomain, raw)
	if err != nil {
		slog.Warn("notification secrets: bad key", "err", err)
		return
	}
	s.notifyKey = k
}

// openNotifySecret is the loader's SecretOpener.
func (s *server) openNotifySecret(ctx context.Context, providerID string) (string, bool) {
	if !s.notifyKey.Present() || s.db == nil {
		return "", false
	}
	var gen, ver int
	var keyID string
	var nonce, ct []byte
	err := s.db.QueryRow(ctx, `SELECT generation_no, key_id, nonce, ciphertext, cipher_version
	      FROM iam_v2.notification_provider_secret_generations
	     WHERE tenant_id=$1 AND provider_id=$2::uuid AND superseded_at IS NULL`, s.tenID, providerID).
		Scan(&gen, &keyID, &nonce, &ct, &ver)
	if err != nil {
		return "", false
	}
	plain, err := s.notifyKey.Open(sealbox.Sealed{Ciphertext: ct, Nonce: nonce, KeyID: keyID, CipherVersion: ver},
		s.tenID, providerID, fmt.Sprint(gen))
	if err != nil {
		slog.Warn("notification secrets: cannot open sealed secret", "provider", providerID)
		return "", false
	}
	return string(plain), true
}

// loadNotify (re)resolves the senders from the database and swaps them in.
func (s *server) loadNotify(ctx context.Context) {
	nctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	loaded, err := notifyloader.LoadWithSecrets(nctx, s.db, s.tenID, s.openNotifySecret, s.notifyFallback.mail, s.notifyFallback.sms, s.notifyFallback.whatsapp)
	if err != nil {
		slog.Warn("notifyloader: load failed; using stubs", "err", err)
	}
	m := notifyloader.WrapMailer(loaded.Mailer, loaded.MailerKind, s.met, s.db, s.tenID)
	sm := notifyloader.WrapSender(loaded.Sender, loaded.SenderKind, s.met, s.db, s.tenID)
	wa := notifyloader.WrapWhatsApp(loaded.WhatsApp, loaded.WhatsAppKind, s.met, s.db, s.tenID)
	s.notify.mu.Lock()
	s.notify.mail, s.notify.sms, s.notify.whatsapp = m, sm, wa
	s.mail, s.sms, s.whatsapp = m, sm, wa
	s.notify.mu.Unlock()
	slog.Info("notification providers loaded", "email", loaded.MailerKind, "sms", loaded.SenderKind, "whatsapp", loaded.WhatsAppKind)
}

// senders returns the current trio. Tests that set s.mail / s.sms / s.whatsapp directly are honoured: the
// struct fields win when the swap set was never populated.
func (s *server) senders() (mail.Mailer, sms.Sender, whatsapp.Sender) {
	s.notify.mu.RLock()
	defer s.notify.mu.RUnlock()
	m, sm, wa := s.notify.mail, s.notify.sms, s.notify.whatsapp
	if m == nil {
		m = s.mail
	}
	if sm == nil {
		sm = s.sms
	}
	if wa == nil {
		wa = s.whatsapp
	}
	return m, sm, wa
}

func (s *server) notifyAdminRoutes(r chi.Router) {
	r.Post("/v1/admin/notify/reload", s.notifyReload)
	r.Post("/v1/admin/notify/providers/{id}/secret", s.notifySecretStore)
	r.Post("/v1/admin/notify/providers/{id}/test", s.notifyTest)
}

func (s *server) notifyReload(w http.ResponseWriter, r *http.Request) {
	if s.db == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false})
		return
	}
	s.loadNotify(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// notifySecretStore seals one secret for a provider row, supersedes the previous generation and clears the
// plaintext column. edged has already authorised the operator; this socket is not reachable from the network.
func (s *server) notifySecretStore(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		Secret string `json:"secret"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.Secret) == "" {
		httpErr(w, http.StatusBadRequest, "secret required")
		return
	}
	if !s.notifyKey.Present() {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": "notify_key_missing",
			"message": "The appliance has no notification sealing key; run keybootstrap at deploy."})
		return
	}
	ctx := r.Context()
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notification_providers WHERE id=$1::uuid AND tenant_id=$2)`, id, s.tenID).Scan(&exists); err != nil || !exists {
		httpErr(w, http.StatusNotFound, "provider not found")
		return
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "begin")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var next int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(generation_no),0)+1 FROM iam_v2.notification_provider_secret_generations
	     WHERE tenant_id=$1 AND provider_id=$2::uuid`, s.tenID, id).Scan(&next); err != nil {
		httpErr(w, http.StatusInternalServerError, "generation")
		return
	}
	sealed, err := s.notifyKey.Seal([]byte(req.Secret), s.tenID, id, fmt.Sprint(next))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "seal")
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.notification_provider_secret_generations SET superseded_at = now()
	     WHERE tenant_id=$1 AND provider_id=$2::uuid AND superseded_at IS NULL`, s.tenID, id); err != nil {
		httpErr(w, http.StatusInternalServerError, "supersede")
		return
	}
	if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.notification_provider_secret_generations
	     (tenant_id, provider_id, generation_no, ciphertext, nonce, key_id, cipher_version)
	     VALUES ($1,$2::uuid,$3,$4,$5,$6,$7)`, s.tenID, id, next, sealed.Ciphertext, sealed.Nonce, sealed.KeyID, sealed.CipherVersion); err != nil {
		httpErr(w, http.StatusInternalServerError, "store")
		return
	}
	// The plaintext column is retired for this row: the sealed generation is now the only secret.
	if _, err := tx.Exec(ctx, `UPDATE notification_providers SET api_key = NULL, updated_at = now() WHERE id=$1::uuid AND tenant_id=$2`, id, s.tenID); err != nil {
		httpErr(w, http.StatusInternalServerError, "clear")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		httpErr(w, http.StatusInternalServerError, "commit")
		return
	}
	s.loadNotify(ctx)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "generation": next})
}

// notifyTest delivers one test message with the provider as stored. The outcome is recorded on the row.
func (s *server) notifyTest(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var req struct {
		To string `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.To) == "" {
		httpErr(w, http.StatusBadRequest, "a recipient is required")
		return
	}
	ctx := r.Context()
	var row notifyloader.ProviderRow
	var extra string
	err := s.db.QueryRow(ctx, `SELECT id::text, channel, kind, COALESCE(api_key,''), COALESCE(api_user,''),
	        COALESCE(from_address,''), COALESCE(from_name,''), COALESCE(region,''), COALESCE(extra,'{}'::jsonb)::text
	      FROM notification_providers WHERE id=$1::uuid AND tenant_id=$2`, id, s.tenID).
		Scan(&row.ID, &row.Channel, &row.Kind, &row.Secret, &row.APIUser, &row.FromAddress, &row.FromName, &row.Region, &extra)
	if errors.Is(err, pgx.ErrNoRows) {
		httpErr(w, http.StatusNotFound, "provider not found")
		return
	}
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "read")
		return
	}
	row.Extra = map[string]string{}
	var m map[string]any
	if json.Unmarshal([]byte(extra), &m) == nil {
		for k, v := range m {
			if sv, ok := v.(string); ok {
				row.Extra[k] = sv
			}
		}
	}
	if sec, ok := s.openNotifySecret(ctx, row.ID); ok {
		row.Secret = sec
	}
	mailer, sender, wa, kind := notifyloader.BuildOne(row, nil, nil, nil)
	if kind == "stub" && row.Kind != "stub" {
		s.recordNotifyOutcome(ctx, row.ID, errors.New("the sender could not be built from its settings"))
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "not_buildable",
			"message": "The sender could not be built from its settings; check every field and the stored secret."})
		return
	}
	tctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	to := strings.TrimSpace(req.To)
	var serr error
	switch {
	case mailer != nil:
		serr = mailer.Send(tctx, mail.Message{To: to, Subject: "OneGate test message",
			Text: "This is a test message from your OneGate Client Portal.\n\nIf you can read this, email delivery is working."})
	case sender != nil:
		serr = sender.Send(tctx, sms.Message{To: to, Text: "OneGate test message: SMS delivery is working."})
	case wa != nil:
		serr = wa.Send(tctx, whatsapp.Message{To: to, Code: "000000", TTLMinutes: 1})
	default:
		serr = errors.New("no sender for this channel")
	}
	s.recordNotifyOutcome(ctx, row.ID, serr)
	if serr != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "send_failed", "message": serr.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "kind": kind})
}

func (s *server) recordNotifyOutcome(ctx context.Context, id string, serr error) {
	if serr == nil {
		_, _ = s.db.Exec(ctx, `UPDATE notification_providers SET last_success_at = now(), last_error = NULL, last_error_at = NULL WHERE id=$1::uuid AND tenant_id=$2`, id, s.tenID)
		return
	}
	msg := serr.Error()
	if len(msg) > 500 {
		msg = msg[:500]
	}
	_, _ = s.db.Exec(ctx, `UPDATE notification_providers SET last_error = $3, last_error_at = now() WHERE id=$1::uuid AND tenant_id=$2`, id, s.tenID, msg)
}
