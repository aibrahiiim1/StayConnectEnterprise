package socialloader

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/social"
)

func p8(t *testing.T) string {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func TestBuildRegistersEveryRealProvider(t *testing.T) {
	key := p8(t)
	for _, tc := range []struct {
		provider, id, secret string
		extra                Extra
		want                 any
	}{
		{"google", "cid", "sec", Extra{}, &social.Google{}},
		{"microsoft", "cid", "sec", Extra{Tenant: "organizations"}, &social.Microsoft{}},
		{"apple", "com.hotel.wifi", key, Extra{TeamID: "TEAM123456", KeyID: "KEYABC1234"}, &social.Apple{}},
		{"facebook", "app", "sec", Extra{}, &social.Facebook{}},
	} {
		p, err := Build(tc.provider, tc.id, tc.secret, "", tc.extra)
		if err != nil {
			t.Fatalf("%s: %v", tc.provider, err)
		}
		if p.Name() != tc.provider {
			t.Errorf("%s: Name() = %q", tc.provider, p.Name())
		}
		reg := social.NewRegistry()
		reg.Register(&social.Stub{ProviderName: tc.provider})
		reg.Register(p)
		if got, _ := reg.Get(tc.provider); got != p {
			t.Errorf("%s: real provider did not replace the stub", tc.provider)
		}
	}
	if m, _ := Build("microsoft", "c", "s", "", Extra{Tenant: "organizations"}); m.(*social.Microsoft).Tenant != "organizations" {
		t.Error("microsoft tenant from extra not applied")
	}
}

func TestBuildRefusesIncompleteRowsWithoutLeakingSecrets(t *testing.T) {
	const secret = "super-secret-value"
	for _, tc := range []struct {
		provider string
		id       string
		extra    Extra
	}{
		{"google", "", Extra{}},
		{"microsoft", "", Extra{}},
		{"microsoft", "cid", Extra{Tenant: "a/b"}},
		{"facebook", "", Extra{}},
		{"apple", "svc", Extra{TeamID: "TEAM123456", KeyID: "KEYABC1234"}}, // secret is not a .p8 key
		{"apple", "svc", Extra{}},
	} {
		_, err := Build(tc.provider, tc.id, secret, "", tc.extra)
		if err == nil {
			t.Errorf("%s %+v: expected an error", tc.provider, tc.extra)
			continue
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%s: secret leaked into error %q", tc.provider, err)
		}
	}
	if _, err := Build("myspace", "c", "s", "", Extra{}); !errors.Is(err, social.ErrUnknownProvider) {
		t.Errorf("unknown provider: %v", err)
	}
}
