package main

import (
	"context"
	"crypto/rand"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/mail"
	"github.com/stayconnect/enterprise/data-plane/internal/notifyloader"
	"github.com/stayconnect/enterprise/data-plane/internal/sealbox"
	"github.com/stayconnect/enterprise/data-plane/internal/sms"
	"github.com/stayconnect/enterprise/data-plane/internal/whatsapp"
)

// P5 M3 and P5 L5: a provider that cannot be built, or whose sealed secret cannot be opened, says so in last_error and is never marked working.
// Needs PHASE3_TEST_DSN; skipped otherwise.
func TestIntegration_BrokenNotificationProviderRecordsItsError(t *testing.T) {
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
	const tenant = "11111111-1111-1111-1111-111111111112"
	_, _ = pool.Exec(ctx, `DELETE FROM iam_v2.notification_provider_secret_generations WHERE tenant_id=$1`, tenant)
	_, _ = pool.Exec(ctx, `DELETE FROM public.notification_providers WHERE tenant_id=$1`, tenant)
	if _, err := pool.Exec(ctx, `INSERT INTO public.tenants (id, slug, name) VALUES ($1,'t-notify-health','t') ON CONFLICT (id) DO NOTHING`, tenant); err != nil {
		t.Fatal(err)
	}
	// A WhatsApp row with no template name cannot be built.
	var waID string
	if err := pool.QueryRow(ctx, `INSERT INTO public.notification_providers (tenant_id, channel, kind, enabled, api_key, api_user)
		VALUES ($1,'whatsapp','meta_whatsapp',true,'tok','12345') RETURNING id::text`, tenant).Scan(&waID); err != nil {
		t.Fatal(err)
	}
	fb := struct {
		mail     mail.Mailer
		sms      sms.Sender
		whatsapp whatsapp.Sender
	}{mail: mail.NewStub(t.TempDir() + "/mail.log"), sms: sms.NewStub(t.TempDir() + "/sms.log"), whatsapp: whatsapp.NewStub(t.TempDir() + "/wa.log")}
	res, err := notifyloader.LoadWithSecrets(ctx, pool, tenant, nil, fb.mail, fb.sms, fb.whatsapp)
	if err != nil || res.WhatsAppKind != "stub" {
		t.Fatalf("kind=%q err=%v", res.WhatsAppKind, err)
	}
	var lastErr *string
	if err := pool.QueryRow(ctx, `SELECT last_error FROM public.notification_providers WHERE id=$1::uuid`, waID).Scan(&lastErr); err != nil {
		t.Fatal(err)
	}
	if lastErr == nil || !strings.Contains(*lastErr, "could not be built") {
		t.Fatalf("construction failure not recorded: %v", lastErr)
	}
	// A send through the stub fallback does not clear that error or mark the provider working.
	wa := notifyloader.WrapWhatsApp(res.WhatsApp, res.WhatsAppKind, nil, pool, tenant)
	_ = wa.Send(ctx, whatsapp.Message{To: "+15550000000", Code: "123456"})
	var success *string
	if err := pool.QueryRow(ctx, `SELECT last_error, last_success_at::text FROM public.notification_providers WHERE id=$1::uuid`, waID).Scan(&lastErr, &success); err != nil {
		t.Fatal(err)
	}
	if lastErr == nil || success != nil {
		t.Fatalf("a stub send marked the broken provider working: last_error=%v last_success_at=%v", lastErr, success)
	}

	// A sealed secret this appliance's key cannot open is logged and recorded.
	raw := make([]byte, 32)
	_, _ = rand.Read(raw)
	key, _ := sealbox.NewKey(notifyDomain, raw)
	var mailID string
	if err := pool.QueryRow(ctx, `INSERT INTO public.notification_providers (tenant_id, channel, kind, enabled, from_address, from_name, extra)
		VALUES ($1,'email','smtp',true,'wifi@example.com','Wi-Fi','{"host":"smtp.example.com"}'::jsonb) RETURNING id::text`, tenant).Scan(&mailID); err != nil {
		t.Fatal(err)
	}
	sealed, err := key.Seal([]byte("hunter2"), tenant, mailID, "1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO iam_v2.notification_provider_secret_generations
		(tenant_id, provider_id, generation_no, key_id, nonce, ciphertext, cipher_version) VALUES ($1,$2::uuid,1,$3,$4,$5,$6)`,
		tenant, mailID, sealed.KeyID, sealed.Nonce, sealed.Ciphertext, sealed.CipherVersion); err != nil {
		t.Fatal(err)
	}
	wrong := make([]byte, 32)
	_, _ = rand.Read(wrong)
	wk, _ := sealbox.NewKey(notifyDomain, wrong)
	if _, ok := (&server{db: pool, tenID: tenant, notifyKey: wk}).openNotifySecret(ctx, mailID); ok {
		t.Fatal("a wrong key opened the secret")
	}
	if err := pool.QueryRow(ctx, `SELECT last_error FROM public.notification_providers WHERE id=$1::uuid`, mailID).Scan(&lastErr); err != nil {
		t.Fatal(err)
	}
	if lastErr == nil || !strings.Contains(*lastErr, "could not be unsealed") {
		t.Fatalf("unsealable secret not recorded: %v", lastErr)
	}
}
