package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const (
	testTwilioSID     = "A" + "C" + fakeHex32 // assembled so no SID-shaped literal sits in the tree
	testTwilioContent = "H" + "X" + fakeHex32
	fakeHex32         = "0123456789abcdef0123456789abcdef"
)

func TestWhatsAppIsItsOwnChannelWithItsOwnKinds(t *testing.T) {
	wa := notifyAllowedKinds["whatsapp"]
	if !wa["meta_whatsapp"] || !wa["twilio_whatsapp"] || !wa["stub"] || wa["twilio"] {
		t.Fatalf("whatsapp kinds %v", wa)
	}
	if notifyAllowedKinds["sms"]["meta_whatsapp"] || notifyAllowedKinds["sms"]["twilio_whatsapp"] {
		t.Fatal("a WhatsApp kind is accepted on the SMS channel")
	}
}

func TestValidMetaAndTwilioWhatsAppProvidersPass(t *testing.T) {
	for _, p := range []notifyProviderShape{
		{Channel: "whatsapp", Kind: "meta_whatsapp", APIUser: "109876543210", HasAPIKey: true,
			Extra: map[string]string{"template_name": "onegate_code", "language": "en_US"}},
		{Channel: "whatsapp", Kind: "meta_whatsapp", APIUser: "109876543210", HasAPIKey: true,
			Extra: map[string]string{"template_name": "onegate_code"}},
		{Channel: "whatsapp", Kind: "twilio_whatsapp", APIUser: testTwilioSID, HasAPIKey: true, FromAddress: "+14155238886",
			Extra: map[string]string{"content_sid": testTwilioContent}},
		{Channel: "whatsapp", Kind: "twilio_whatsapp", APIUser: testTwilioSID, HasAPIKey: true, FromAddress: "whatsapp:+14155238886",
			Extra: map[string]string{"content_sid": testTwilioContent}},
		{Channel: "whatsapp", Kind: "stub"},
		{Channel: "sms", Kind: "twilio", APIUser: "AC1"}, // SMS keeps its historical rules
	} {
		if msg := validateNotifyProvider(p); msg != "" {
			t.Errorf("%+v refused: %s", p, msg)
		}
	}
}

func TestIncompleteWhatsAppProvidersAreRefusedReadably(t *testing.T) {
	meta := func(mut func(*notifyProviderShape)) notifyProviderShape {
		p := notifyProviderShape{Channel: "whatsapp", Kind: "meta_whatsapp", APIUser: "109876543210", HasAPIKey: true,
			Extra: map[string]string{"template_name": "onegate_code"}}
		mut(&p)
		return p
	}
	twilio := func(mut func(*notifyProviderShape)) notifyProviderShape {
		p := notifyProviderShape{Channel: "whatsapp", Kind: "twilio_whatsapp", APIUser: testTwilioSID, HasAPIKey: true,
			FromAddress: "+14155238886", Extra: map[string]string{"content_sid": testTwilioContent}}
		mut(&p)
		return p
	}
	cases := []struct {
		p    notifyProviderShape
		want string
	}{
		{meta(func(p *notifyProviderShape) { p.APIUser = "" }), "phone number ID"},
		{meta(func(p *notifyProviderShape) { p.HasAPIKey = false }), "access token"},
		{meta(func(p *notifyProviderShape) { p.Extra = map[string]string{} }), "template_name"},
		{meta(func(p *notifyProviderShape) { p.Extra["language"] = "English" }), "language"},
		{meta(func(p *notifyProviderShape) { p.Extra["body"] = "Your code is {{1}}" }), "unknown WhatsApp setting"},
		{twilio(func(p *notifyProviderShape) { p.APIUser = "AC1" }), "account SID"},
		{twilio(func(p *notifyProviderShape) { p.HasAPIKey = false }), "auth token"},
		{twilio(func(p *notifyProviderShape) { p.FromAddress = "0100123456" }), "E.164"},
		{twilio(func(p *notifyProviderShape) { p.Extra = map[string]string{} }), "content_sid"},
		{notifyProviderShape{Channel: "whatsapp", Kind: "twilio"}, "kind not supported"},
		{notifyProviderShape{Channel: "sms", Kind: "twilio", Extra: map[string]string{"template_name": "x"}}, "only to WhatsApp"},
	}
	for _, c := range cases {
		msg := validateNotifyProvider(c.p)
		if msg == "" || !strings.Contains(msg, c.want) {
			t.Errorf("%+v: got %q, want it to mention %q", c.p, msg, c.want)
		}
	}
}

func TestMergeExtraPatchesAndRemoves(t *testing.T) {
	got := mergeExtra(map[string]string{"template_name": "a", "language": "en"}, map[string]string{"language": "", "template_name": " b "})
	if len(got) != 1 || got["template_name"] != "b" {
		t.Fatalf("%v", got)
	}
}

func TestProviderResponseNeverCarriesTheSecret(t *testing.T) {
	n := edgeNotificationProvider{ID: "x", Channel: "whatsapp", Kind: "meta_whatsapp", APIUser: "109876543210",
		Extra: publicExtra(`{"template_name":"onegate_code","api_key":"leak","token":"leak"}`), CreatedAt: time.Now()}
	b, _ := json.Marshal(n)
	if strings.Contains(string(b), "api_key") || strings.Contains(string(b), "leak") {
		t.Fatalf("response carries a secret: %s", b)
	}
	if !strings.Contains(string(b), `"template_name":"onegate_code"`) {
		t.Fatalf("template setting missing: %s", b)
	}
}
