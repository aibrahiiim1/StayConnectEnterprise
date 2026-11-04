package main

import (
	"strings"
	"testing"
	"time"
)

// The link opens the property's own portal, which answers only on its Wi-Fi: both emails say so before the link.
func TestAccountEmailsSayTheLinkNeedsTheSiteWiFi(t *testing.T) {
	d := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	for _, purpose := range []string{tokenPurposeVerify, tokenPurposeReset} {
		m := accountEmailMessage(purpose, "a@example.com", "https://portal.example/account/x?t=1", &d)
		wifi, link := strings.Index(m.Text, "connected to this property's Wi-Fi"), strings.Index(m.Text, "https://portal.example")
		if wifi < 0 || link < 0 || wifi > link {
			t.Fatalf("%s: the Wi-Fi requirement must come before the link:\n%s", purpose, m.Text)
		}
		if !strings.Contains(m.Text, "not on mobile data") {
			t.Fatalf("%s: the email must say mobile data will not work:\n%s", purpose, m.Text)
		}
	}
}
