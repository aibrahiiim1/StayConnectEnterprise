package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// THE ADMIN CONSOLE'S HALF OF THE CLIENT-ACCESS CONTRACT, VALIDATED WHERE THE OPERATOR IS LOOKING.

func TestClientGroupWritesAreValidatedWithTheEvaluatorsOwnRules(t *testing.T) {
	name := "Employees"
	ok := clientGroupWrite{Name: &name, Rules: &[]clientGroupRule{
		{Type: "EMAIL_DOMAIN", Value: map[string]any{"domains": []any{"company.com"}, "include_subdomains": true}},
		{Type: "idp_tenant", Value: map[string]any{"provider": "microsoft", "tenant_ids": []any{"11111111-2222-3333-4444-555555555555"}}},
	}}
	if msg := validateClientGroupWrite(ok, true); msg != "" {
		t.Fatalf("valid group refused: %s", msg)
	}
	bad := map[string]clientGroupWrite{
		"no name on create":     {Rules: &[]clientGroupRule{}},
		"empty name":            {Name: strp("  ")},
		"priority out of range": {Name: &name, Priority: intp(0)},
		"reserved rule":         {Name: &name, Rules: &[]clientGroupRule{{Type: "IDP_GROUP", Value: map[string]any{"provider": "microsoft"}}}},
		"malformed rule":        {Name: &name, Rules: &[]clientGroupRule{{Type: "EMAIL_DOMAIN", Value: map[string]any{"domains": []any{"not a domain"}}}}},
		"too many rules":        {Name: &name, Rules: &[]clientGroupRule{}},
	}
	many := make([]clientGroupRule, 21)
	for i := range many {
		many[i] = clientGroupRule{Type: "EMAIL_DOMAIN", Value: map[string]any{"domains": []any{"a.com"}}}
	}
	bad["too many rules"] = clientGroupWrite{Name: &name, Rules: &many}
	for label, in := range bad {
		if msg := validateClientGroupWrite(in, strings.Contains(label, "create")); msg == "" {
			t.Errorf("%s must be refused", label)
		}
	}
	// A refusal names the rule by its position, in words an operator can act on.
	msg := validateClientGroupWrite(bad["malformed rule"], false)
	if !strings.HasPrefix(msg, "rule 1:") {
		t.Fatalf("refusal must name the rule: %q", msg)
	}
}

func TestSMTPProvidersAreValidatedAndSESIsNoLongerOffered(t *testing.T) {
	if notifyAllowedKinds["email"]["ses"] {
		t.Fatal("ses is accepted on create but scd has no adapter for it; it must not be offered")
	}
	if !notifyAllowedKinds["email"]["smtp"] {
		t.Fatal("smtp must be an email kind")
	}
	good := notifyProviderShape{Channel: "email", Kind: "smtp", HasAPIKey: true, FromAddress: "wifi@example.com",
		Extra: map[string]string{"host": "smtp.office365.com", "port": "587", "security": "starttls", "username": "wifi@example.com"}}
	if msg := validateNotifyProvider(good); msg != "" {
		t.Fatalf("valid SMTP refused: %s", msg)
	}
	// Port follows the transport when left blank; an internal relay needs no credentials at all.
	relay := notifyProviderShape{Channel: "email", Kind: "smtp", FromAddress: "wifi@example.com",
		Extra: map[string]string{"host": "relay.internal", "security": "none"}}
	if msg := validateNotifyProvider(relay); msg != "" {
		t.Fatalf("an internal relay without credentials must be valid: %s", msg)
	}
	cases := map[string]notifyProviderShape{
		"no host":            {Channel: "email", Kind: "smtp", HasAPIKey: true, FromAddress: "a@b.c", Extra: map[string]string{"username": "u"}},
		"username, no pass":  {Channel: "email", Kind: "smtp", FromAddress: "a@b.c", Extra: map[string]string{"host": "h", "username": "u"}},
		"bad security":       {Channel: "email", Kind: "smtp", FromAddress: "a@b.c", Extra: map[string]string{"host": "h", "security": "ssl"}},
		"bad port":           {Channel: "email", Kind: "smtp", FromAddress: "a@b.c", Extra: map[string]string{"host": "h", "port": "99999"}},
		"bad from":           {Channel: "email", Kind: "smtp", FromAddress: "nope", Extra: map[string]string{"host": "h"}},
		"unknown extra":      {Channel: "email", Kind: "smtp", FromAddress: "a@b.c", Extra: map[string]string{"host": "h", "tls": "yes"}},
		"timeout too long":   {Channel: "email", Kind: "smtp", FromAddress: "a@b.c", Extra: map[string]string{"host": "h", "timeout_seconds": "600"}},
		"extras on sendgrid": {Channel: "email", Kind: "sendgrid", HasAPIKey: true, FromAddress: "a@b.c", Extra: map[string]string{"host": "h"}},
	}
	for label, p := range cases {
		if msg := validateNotifyProvider(p); msg == "" {
			t.Errorf("%s must be refused", label)
		}
	}
	// What the API shows of an SMTP row never includes a secret, and does include the connection settings.
	pub := publicExtra(`{"host":"h","port":"587","security":"starttls","username":"u","password":"leak"}`)
	if pub["password"] != "" || pub["host"] != "h" || pub["username"] != "u" {
		t.Fatalf("public extra must show settings and never a secret: %v", pub)
	}
}

func TestThePortalJourneySettingsAreBounded(t *testing.T) {
	ok := map[string]json.RawMessage{"portal": json.RawMessage(`{"primary_method":"email","remember_device_days":30}`)}
	if err := validateAuthMethodsPatch(ok); err != nil {
		t.Fatalf("valid portal settings refused: %v", err)
	}
	off := map[string]json.RawMessage{"portal": json.RawMessage(`{"primary_method":"","remember_device_days":0}`)}
	if err := validateAuthMethodsPatch(off); err != nil {
		t.Fatalf("auto primary and never-remember must be valid: %v", err)
	}
	for label, raw := range map[string]string{
		"unknown primary": `{"primary_method":"facebook"}`,
		"negative days":   `{"remember_device_days":-1}`,
		"too many days":   `{"remember_device_days":400}`,
		"not an object":   `"email"`,
	} {
		if err := validateAuthMethodsPatch(map[string]json.RawMessage{"portal": json.RawMessage(raw)}); err == nil {
			t.Errorf("%s must be refused", label)
		}
	}
}

func strp(s string) *string { return &s }
func intp(n int) *int       { return &n }
