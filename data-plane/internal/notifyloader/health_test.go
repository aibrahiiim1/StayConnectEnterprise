package notifyloader

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/mail"
)

type failingMailer struct{ err error }

func (f failingMailer) Send(context.Context, mail.Message) error { return f.err }

// captureHealth records, per health write, the context's error at the moment of the write.
func captureHealth(t *testing.T) *[]error {
	t.Helper()
	var calls []error
	old := healthExec
	healthExec = func(ctx context.Context, _ *pgxpool.Pool, _ string, _ ...any) error {
		calls = append(calls, ctx.Err())
		return ctx.Err()
	}
	t.Cleanup(func() { healthExec = old })
	return &calls
}

// P5 M3: a send through the stub never marks the enabled provider as working.
func TestStubSendRecordsNoHealth(t *testing.T) {
	calls := captureHealth(t)
	db := &pgxpool.Pool{}
	m := WrapMailer(mail.NewStub(""), kindStub, nil, db, "t1")
	_ = m.Send(context.Background(), mail.Message{To: "a@example.com"})
	if len(*calls) != 0 {
		t.Fatal("a stub send recorded provider health")
	}
	m = WrapMailer(failingMailer{}, "sendgrid", nil, db, "t1")
	_ = m.Send(context.Background(), mail.Message{To: "a@example.com"})
	if len(*calls) != 1 {
		t.Fatalf("a real provider send must record health, got %d writes", len(*calls))
	}
}

// P5 L6: a send that failed because its own context ended still records its health.
func TestHealthRecordedAfterCancelledSend(t *testing.T) {
	calls := captureHealth(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	m := WrapMailer(failingMailer{err: context.Canceled}, "sendgrid", nil, &pgxpool.Pool{}, "t1")
	_ = m.Send(ctx, mail.Message{To: "a@example.com"})
	if len(*calls) != 1 || (*calls)[0] != nil {
		t.Fatal("the health write must not inherit the cancelled send context")
	}
}

// P5 L6: truncation never splits a UTF-8 character.
func TestTruncateIsRuneSafe(t *testing.T) {
	s := strings.Repeat("é", 300) // 600 bytes
	got := Truncate(s, 501)
	if !utf8.ValidString(got) {
		t.Fatal("truncate produced invalid UTF-8")
	}
	if len(strings.TrimSuffix(got, "…")) > 501 {
		t.Fatalf("truncated to %d bytes", len(got))
	}
	if Truncate("short", 500) != "short" {
		t.Fatal("short strings must be unchanged")
	}
}

// P5 M3: a provider that cannot be built is reported by id so its last_error can be recorded.
func TestBuildFailureIsReported(t *testing.T) {
	m, s, w := fallbacks()
	r := resolve([]ProviderRow{{ID: "p-wa", Channel: "whatsapp", Kind: KindMetaWhatsApp, Secret: "tok", APIUser: "1"}}, m, s, w)
	if r.WhatsAppKind != kindStub {
		t.Fatalf("kind %s", r.WhatsAppKind)
	}
	if r.BuildErrors["p-wa"] == nil {
		t.Fatal("the construction failure was not reported")
	}
	r = resolve([]ProviderRow{{ID: "p-x", Channel: "sms", Kind: "carrier-pigeon"}}, m, s, w)
	if !errors.Is(r.BuildErrors["p-x"], errUnknownKind) {
		t.Fatalf("unknown kind not reported: %v", r.BuildErrors)
	}
}

// P5 L11: with two enabled rows for a channel, the first (newest) row wins, not whichever was scanned last.
func TestFirstRowPerChannelWins(t *testing.T) {
	m, s, w := fallbacks()
	r := resolve([]ProviderRow{
		{ID: "new", Channel: "sms", Kind: "twilio", APIUser: "ACnew", Secret: "tok", FromAddress: "+15550000001"},
		{ID: "old", Channel: "sms", Kind: kindStub},
	}, m, s, w)
	if r.SenderKind != "twilio" {
		t.Fatalf("newest row lost to an older one: %s", r.SenderKind)
	}
}
