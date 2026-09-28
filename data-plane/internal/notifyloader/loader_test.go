package notifyloader

import (
	"context"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/mail"
	"github.com/stayconnect/enterprise/data-plane/internal/sms"
	"github.com/stayconnect/enterprise/data-plane/internal/whatsapp"
)

type fakeWA struct{}

func (fakeWA) Send(context.Context, whatsapp.Message) error { return nil }

func fallbacks() (mail.Mailer, sms.Sender, whatsapp.Sender) {
	return mail.NewStub(""), sms.NewStub(""), fakeWA{}
}

func TestNoWhatsAppRowKeepsTheStub(t *testing.T) {
	m, s, w := fallbacks()
	r := resolve([]providerRow{{channel: "sms", kind: "stub"}}, m, s, w)
	if r.WhatsAppKind != "stub" || r.WhatsApp != w {
		t.Fatalf("whatsapp %v %T", r.WhatsAppKind, r.WhatsApp)
	}
}

func TestMetaWhatsAppRowBuildsTheMetaAdapter(t *testing.T) {
	m, s, w := fallbacks()
	r := resolve([]providerRow{{
		channel: "whatsapp", kind: KindMetaWhatsApp, apiKey: "tok", apiUser: "12345",
		extra: map[string]string{"template_name": "onegate_code", "language": "ar"},
	}}, m, s, w)
	meta, ok := r.WhatsApp.(*whatsapp.Meta)
	if !ok || r.WhatsAppKind != KindMetaWhatsApp {
		t.Fatalf("got %s %T", r.WhatsAppKind, r.WhatsApp)
	}
	if meta.PhoneNumberID != "12345" || meta.TemplateName != "onegate_code" || meta.Language != "ar" || meta.AccessToken != "tok" {
		t.Fatalf("meta %+v", meta)
	}
	// SMS is untouched by a WhatsApp row.
	if r.SenderKind != "stub" {
		t.Fatalf("sms kind %s", r.SenderKind)
	}
}

func TestTwilioWhatsAppRowBuildsTheTwilioAdapter(t *testing.T) {
	m, s, w := fallbacks()
	r := resolve([]providerRow{{
		channel: "whatsapp", kind: KindTwilioWhatsApp, apiKey: "auth", apiUser: "AC1", fromAddr: "+14155238886",
		extra: map[string]string{"content_sid": "HX1"},
	}}, m, s, w)
	tw, ok := r.WhatsApp.(*whatsapp.Twilio)
	if !ok || r.WhatsAppKind != KindTwilioWhatsApp {
		t.Fatalf("got %s %T", r.WhatsAppKind, r.WhatsApp)
	}
	if tw.AccountSID != "AC1" || tw.AuthToken != "auth" || tw.FromNumber != "+14155238886" || tw.ContentSID != "HX1" {
		t.Fatalf("twilio %+v", tw)
	}
}

func TestIncompleteOrUnknownWhatsAppRowFallsBackToStub(t *testing.T) {
	m, s, w := fallbacks()
	for _, row := range []providerRow{
		{channel: "whatsapp", kind: KindMetaWhatsApp, apiKey: "tok", apiUser: "1"},                      // no template
		{channel: "whatsapp", kind: KindTwilioWhatsApp, apiKey: "a", apiUser: "AC1", fromAddr: "+1415"}, // no content sid
		{channel: "whatsapp", kind: "twilio"},                                                           // an SMS kind
	} {
		r := resolve([]providerRow{row}, m, s, w)
		if r.WhatsAppKind != "stub" || r.WhatsApp != w {
			t.Fatalf("row %+v resolved to %s", row, r.WhatsAppKind)
		}
	}
}

func TestParseExtraKeepsStringsOnly(t *testing.T) {
	got := parseExtra(`{"template_name":"t","n":1,"language":"en"}`)
	if len(got) != 2 || got["template_name"] != "t" || got["language"] != "en" {
		t.Fatalf("%v", got)
	}
	if len(parseExtra("not json")) != 0 {
		t.Fatal("bad json parsed")
	}
}
