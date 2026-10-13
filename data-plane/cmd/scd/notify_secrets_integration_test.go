package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/mail"
	"github.com/stayconnect/enterprise/data-plane/internal/notifyloader"
	"github.com/stayconnect/enterprise/data-plane/internal/sealbox"
	"github.com/stayconnect/enterprise/data-plane/internal/sms"
	"github.com/stayconnect/enterprise/data-plane/internal/whatsapp"
)

// NOTIFICATION SECRETS ARE SEALED, OPENED BY SCD ALONE, AND THE PLAINTEXT COLUMN IS RETIRED (contract §7.3).
// Needs PHASE3_TEST_DSN; skipped otherwise.

func TestIntegration_NotificationSecretIsSealedAndPreferredByTheLoader(t *testing.T) {
	dsn := os.Getenv("PHASE3_TEST_DSN")
	if dsn == "" {
		t.Skip("PHASE3_TEST_DSN not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	const tenant = "11111111-1111-1111-1111-111111111111"
	_, _ = pool.Exec(ctx, `DELETE FROM iam_v2.notification_provider_secret_generations WHERE tenant_id=$1`, tenant)
	_, _ = pool.Exec(ctx, `DELETE FROM public.notification_providers WHERE tenant_id=$1`, tenant)
	if _, err := pool.Exec(ctx, `INSERT INTO public.tenants (id, slug, name) VALUES ($1,'t-notify','t') ON CONFLICT (id) DO NOTHING`, tenant); err != nil {
		t.Fatal(err)
	}
	// An SMTP sender written the way edged now writes it: no api_key, settings in extra.
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO public.notification_providers (tenant_id, channel, kind, enabled, from_address, from_name, extra)
		VALUES ($1,'email','smtp',true,'wifi@example.com','Wi-Fi','{"host":"smtp.example.com","port":"587","security":"starttls","username":"wifi@example.com"}'::jsonb)
		RETURNING id::text`, tenant).Scan(&id); err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	key, _ := sealbox.NewKey(notifyDomain, raw)
	s := &server{db: pool, tenID: tenant, notifyKey: key,
		notifyFallback: struct {
			mail     mail.Mailer
			sms      sms.Sender
			whatsapp whatsapp.Sender
		}{mail: mail.NewStub(t.TempDir() + "/mail.log"), sms: sms.NewStub(t.TempDir() + "/sms.log"), whatsapp: whatsapp.NewStub(t.TempDir() + "/wa.log")}}

	store := func(secret string) int {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"secret": secret})
		r := httptest.NewRequest(http.MethodPost, "/v1/admin/notify/providers/"+id+"/secret", bytes.NewReader(body))
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", id)
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
		rec := httptest.NewRecorder()
		s.notifySecretStore(rec, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("store: %d %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Generation int `json:"generation"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Generation
	}
	if g := store("hunter2"); g != 1 {
		t.Fatalf("first generation must be 1, got %d", g)
	}
	// Nothing readable is stored, and the column is NULL.
	var apiKey *string
	var ct []byte
	if err := pool.QueryRow(ctx, `SELECT p.api_key, g.ciphertext FROM public.notification_providers p
		JOIN iam_v2.notification_provider_secret_generations g ON g.provider_id=p.id AND g.superseded_at IS NULL WHERE p.id=$1::uuid`, id).Scan(&apiKey, &ct); err != nil {
		t.Fatal(err)
	}
	if apiKey != nil || bytes.Contains(ct, []byte("hunter2")) {
		t.Fatal("the secret must be sealed and the plaintext column NULL")
	}
	// scd opens it; another key does not.
	if got, ok := s.openNotifySecret(ctx, id); !ok || got != "hunter2" {
		t.Fatalf("open: %q %v", got, ok)
	}
	other, _ := sealbox.NewKey(notifyDomain, append([]byte(nil), raw[:31]...))
	_ = other
	wrong := make([]byte, 32)
	_, _ = rand.Read(wrong)
	wk, _ := sealbox.NewKey(notifyDomain, wrong)
	if _, ok := (&server{db: pool, tenID: tenant, notifyKey: wk}).openNotifySecret(ctx, id); ok {
		t.Fatal("a different key must not open the secret")
	}
	// A new secret supersedes the old generation; exactly one is current.
	if g := store("hunter3"); g != 2 {
		t.Fatalf("second generation must be 2, got %d", g)
	}
	var current int
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM iam_v2.notification_provider_secret_generations WHERE provider_id=$1::uuid AND superseded_at IS NULL`, id).Scan(&current)
	if current != 1 {
		t.Fatalf("exactly one current generation, got %d", current)
	}
	if got, _ := s.openNotifySecret(ctx, id); got != "hunter3" {
		t.Fatalf("the current generation must be the newest, got %q", got)
	}
	// The loader builds the SMTP sender from the sealed secret (not the stub).
	res, err := notifyloader.LoadWithSecrets(ctx, pool, tenant, s.openNotifySecret, s.notifyFallback.mail, s.notifyFallback.sms, s.notifyFallback.whatsapp)
	if err != nil || res.MailerKind != notifyloader.KindSMTP {
		t.Fatalf("the loader must build smtp from the sealed secret: kind=%q err=%v", res.MailerKind, err)
	}
	// Without the key (a row sealed, no key on this appliance): the method falls back to the stub, nothing breaks.
	res, _ = notifyloader.LoadWithSecrets(ctx, pool, tenant, (&server{db: pool, tenID: tenant}).openNotifySecret, s.notifyFallback.mail, s.notifyFallback.sms, s.notifyFallback.whatsapp)
	if res.MailerKind != "stub" {
		t.Fatalf("an unopenable secret must fall back to the stub, got %q", res.MailerKind)
	}
}
