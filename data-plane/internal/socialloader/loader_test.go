package socialloader

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/stayconnect/enterprise/data-plane/internal/social"
)

// The secret comes from the opener only: a row that opens is built with it, one that does not is skipped,
// and with no opener nothing is built and the base stub stays.
func TestAssembleBuildsOnlyFromOpenedSecrets(t *testing.T) {
	rows := []Row{
		{ID: "p-google", Provider: "google", ClientID: "gid", ExtraJSON: `{}`, CallbackHosts: []string{"WiFi.Hotel.example.", " "}},
		{ID: "p-facebook", Provider: "facebook", ClientID: "fid", ExtraJSON: `{}`, CallbackHosts: []string{"ab12cd34.gw.onegate.work"}},
		{ID: "p-microsoft", Provider: "microsoft", ClientID: "mid", ExtraJSON: `not json`},
	}
	var asked []string
	open := func(_ context.Context, id string) (string, bool) {
		asked = append(asked, id)
		switch id {
		case "p-google":
			return "google-secret", true
		case "p-microsoft":
			return "ms-secret", true
		}
		return "", false // facebook: no sealed generation
	}
	stub := &social.Stub{ProviderName: "facebook"}
	got := Assemble(context.Background(), rows, open, stub)

	g, err := got.Registry.Get("google")
	if err != nil {
		t.Fatal(err)
	}
	if gg, ok := g.(*social.Google); !ok || gg.ClientSecret != "google-secret" || gg.ClientID != "gid" {
		t.Fatalf("google not built from its opened secret: %#v", g)
	}
	if !reflect.DeepEqual(got.CallbackHosts["google"], []string{"wifi.hotel.example"}) {
		t.Errorf("callback hosts not normalised: %v", got.CallbackHosts["google"])
	}
	if f, _ := got.Registry.Get("facebook"); f != social.Provider(stub) || got.Real["facebook"] {
		t.Error("a provider whose secret does not open must not be built; the base entry stays")
	}
	if _, err := got.Registry.Get("microsoft"); err == nil || got.Real["microsoft"] {
		t.Error("a row with invalid extra was built")
	}
	if !got.Real["google"] || len(got.Real) != 1 {
		t.Errorf("real = %v", got.Real)
	}

	// No opener: the sealing key is missing. Nothing is built, nothing is opened.
	none := Assemble(context.Background(), rows, nil)
	if len(none.Real) != 0 || len(none.Registry.Names()) != 0 {
		t.Fatalf("built without a sealing key: %v", none.Registry.Names())
	}
}

// Each load is a fresh registry, so a provider removed from the database disappears on the next load.
func TestAssembleReturnsAFreshRegistryEachTime(t *testing.T) {
	open := func(context.Context, string) (string, bool) { return "s", true }
	first := Assemble(context.Background(), []Row{{ID: "1", Provider: "google", ClientID: "c", ExtraJSON: `{}`}}, open)
	second := Assemble(context.Background(), nil, open)
	if _, err := first.Registry.Get("google"); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Registry.Get("google"); err == nil {
		t.Fatal("a removed provider survived the reload")
	}
}

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
