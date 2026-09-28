package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"strings"
	"testing"
)

func sp(s string) *string { return &s }

func testAppleP8(t *testing.T) string {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func TestValidateSocialWrite(t *testing.T) {
	key := testAppleP8(t)
	cases := []struct {
		name     string
		provider string
		in       socialWriteReq
		creating bool
		wantErr  string // substring; "" = accepted
	}{
		{"google plain", "google", socialWriteReq{ClientSecret: sp("s")}, true, ""},
		{"facebook plain", "facebook", socialWriteReq{ClientSecret: sp("s")}, true, ""},
		{"microsoft default tenant", "microsoft", socialWriteReq{ClientSecret: sp("s")}, true, ""},
		{"microsoft guid tenant", "microsoft", socialWriteReq{Tenant: sp("11111111-2222-3333-4444-555555555555")}, true, ""},
		{"microsoft bad tenant", "microsoft", socialWriteReq{Tenant: sp("a/b")}, true, "tenant must be"},
		{"tenant on google", "google", socialWriteReq{Tenant: sp("common")}, true, "Microsoft only"},
		{"team id on facebook", "facebook", socialWriteReq{TeamID: sp("TEAM123456")}, false, "Apple only"},
		{"apple complete", "apple", socialWriteReq{ClientSecret: sp(key), TeamID: sp("TEAM123456"), KeyID: sp("KEYABC1234")}, true, ""},
		{"apple missing ids", "apple", socialWriteReq{ClientSecret: sp(key)}, true, "needs team_id"},
		{"apple bad team id", "apple", socialWriteReq{ClientSecret: sp(key), TeamID: sp("team"), KeyID: sp("KEYABC1234")}, true, "team_id must be"},
		{"apple bad key id", "apple", socialWriteReq{ClientSecret: sp(key), TeamID: sp("TEAM123456"), KeyID: sp("k")}, true, "key_id must be"},
		{"apple secret not a key", "apple", socialWriteReq{ClientSecret: sp("hunter2"), TeamID: sp("TEAM123456"), KeyID: sp("KEYABC1234")}, true, ".p8"},
		{"apple patch keeps stored", "apple", socialWriteReq{DisplayName: sp("Apple")}, false, ""},
		{"apple patch blank secret keeps stored", "apple", socialWriteReq{ClientSecret: sp("")}, false, ""},
		{"apple patch clears team id", "apple", socialWriteReq{TeamID: sp("")}, false, "team_id must be"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validateSocialWrite(tc.provider, &tc.in, tc.creating)
			if tc.wantErr == "" && got != "" {
				t.Fatalf("unexpected refusal: %s", got)
			}
			if tc.wantErr != "" && !strings.Contains(got, tc.wantErr) {
				t.Fatalf("got %q, want it to contain %q", got, tc.wantErr)
			}
			if strings.Contains(got, "hunter2") || strings.Contains(got, "PRIVATE KEY") {
				t.Fatalf("secret leaked into the message: %s", got)
			}
		})
	}
}

func TestSocialExtraPatchCarriesOnlyProvidedKeys(t *testing.T) {
	in := socialWriteReq{TeamID: sp(" TEAM123456 "), KeyID: sp("KEYABC1234")}
	var m map[string]string
	if err := json.Unmarshal([]byte(in.extraPatch()), &m); err != nil {
		t.Fatal(err)
	}
	if len(m) != 2 || m["team_id"] != "TEAM123456" || m["key_id"] != "KEYABC1234" {
		t.Errorf("patch = %v", m)
	}
	if (&socialWriteReq{}).extraPatch() != "{}" {
		t.Error("an update without extra fields must merge an empty object")
	}
}

func TestSocialProviderResponseNeverCarriesTheSecret(t *testing.T) {
	b, _ := json.Marshal(edgeSocialProvider{Provider: "apple", TeamID: "TEAM123456", KeyID: "KEYABC1234"})
	if strings.Contains(string(b), "client_secret") {
		t.Errorf("response shape exposes client_secret: %s", b)
	}
}
