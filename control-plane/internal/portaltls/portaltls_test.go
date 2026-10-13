package portaltls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func csrPEM(t *testing.T, key any, tmpl x509.CertificateRequest) string {
	t.Helper()
	der, err := x509.CreateCertificateRequest(rand.Reader, &tmpl, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

func TestParseAndCheckCSR(t *testing.T) {
	p256, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	p384, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	rsa2048, _ := rsa.GenerateKey(rand.Reader, 2048)
	rsa1024, _ := rsa.GenerateKey(rand.Reader, 1024)
	host := "abcdefgh.gw.onegate.work"

	good := csrPEM(t, p256, x509.CertificateRequest{DNSNames: []string{host}})
	if _, name, err := ParseAndCheckCSR(good); err != nil || name != host {
		t.Fatalf("a P-256 CSR for one name must pass: %v %q", err, name)
	}
	if _, _, err := ParseAndCheckCSR(csrPEM(t, rsa2048, x509.CertificateRequest{DNSNames: []string{host}, Subject: pkix.Name{CommonName: host}})); err != nil {
		t.Fatalf("RSA 2048 with CN = the name must pass: %v", err)
	}

	bad := map[string]string{
		"P-384":           csrPEM(t, p384, x509.CertificateRequest{DNSNames: []string{host}}),
		"RSA 1024":        csrPEM(t, rsa1024, x509.CertificateRequest{DNSNames: []string{host}}),
		"two names":       csrPEM(t, p256, x509.CertificateRequest{DNSNames: []string{host, "other.example.com"}}),
		"no name":         csrPEM(t, p256, x509.CertificateRequest{Subject: pkix.Name{CommonName: host}}),
		"IP SAN":          csrPEM(t, p256, x509.CertificateRequest{DNSNames: []string{host}, IPAddresses: []net.IP{net.ParseIP("10.255.255.1")}}),
		"email SAN":       csrPEM(t, p256, x509.CertificateRequest{DNSNames: []string{host}, EmailAddresses: []string{"a@b.c"}}),
		"URI SAN":         csrPEM(t, p256, x509.CertificateRequest{DNSNames: []string{host}, URIs: []*url.URL{{Scheme: "https", Host: "x"}}}),
		"CN differs":      csrPEM(t, p256, x509.CertificateRequest{DNSNames: []string{host}, Subject: pkix.Name{CommonName: "evil.example.com"}}),
		"uppercase name":  csrPEM(t, p256, x509.CertificateRequest{DNSNames: []string{"ABCDEFGH.gw.onegate.work"}}),
		"not PEM":         "hello",
		"certificate PEM": strings.Replace(good, "CERTIFICATE REQUEST", "CERTIFICATE", -1),
	}
	// A CSR whose signature does not verify: flip a byte inside the signature.
	block, _ := pem.Decode([]byte(good))
	der := append([]byte{}, block.Bytes...)
	der[len(der)-3] ^= 0xff
	bad["bad signature"] = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
	bad["two blocks"] = good + good
	for name, pemText := range bad {
		if _, _, err := ParseAndCheckCSR(pemText); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

func TestLabelsAndTokens(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		l, err := NewLabel()
		if err != nil {
			t.Fatal(err)
		}
		if !IsLabel(l) {
			t.Fatalf("label %q does not match [a-z][a-z0-9]{7}", l)
		}
		if seen[l] {
			t.Fatalf("label %q drawn twice in 2000 draws", l)
		}
		seen[l] = true
	}
	for _, bad := range []string{"1abcdefg", "abcdefg", "abcdefghi", "Abcdefgh", "abc-efgh"} {
		if IsLabel(bad) {
			t.Errorf("IsLabel(%q) must be false", bad)
		}
	}
	cl, _ := NewChallengeLabel()
	if len(cl) != 16 || strings.Trim(cl, alphaNum) != "" {
		t.Errorf("challenge label %q must be 16 [a-z0-9]", cl)
	}
	tok, _ := NewVerifyToken()
	if len(tok) != 32 || tok != strings.ToLower(tok) || strings.Trim(tok, "abcdefghijklmnopqrstuvwxyz234567") != "" {
		t.Errorf("verify token %q must be 32 lowercase base32 characters", tok)
	}
	if HostnameFor("abcdefgh", "onegate.work") != "abcdefgh.gw.onegate.work" {
		t.Error("hostname shape")
	}
	if ChallengeNameFor(KindOneGate, "abcdefgh.gw.onegate.work", "", "onegate.work") != "_acme-challenge.abcdefgh.gw.onegate.work" {
		t.Error("ONEGATE challenge name")
	}
	if ChallengeNameFor(KindCustom, "wifi.nilehotel.com", "0123456789abcdef", "onegate.work") != "0123456789abcdef.acme.onegate.work" {
		t.Error("CUSTOM challenge name must be the delegate")
	}
}

func TestValidateCustomDomain(t *testing.T) {
	ok := map[string]string{
		"wifi.nilehotel.com":     "wifi.nilehotel.com",
		"wifi.nilehotel.com.":    "wifi.nilehotel.com",
		"portal.hotel.co.uk":     "portal.hotel.co.uk",
		"a.b.wifi.example.org":   "a.b.wifi.example.org",
		"xn--wifi-8ra.hotel.com": "xn--wifi-8ra.hotel.com",
	}
	for in, want := range ok {
		got, err := ValidateCustomDomain(in, "onegate.work")
		if err != nil || got != want {
			t.Errorf("%q: got %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{
		"nilehotel.com",            // an apex
		"hotel.co.uk",              // an apex below a multi-label suffix
		"co.uk",                    // a public suffix
		"wifi.onegate.work",        // OneGate's zone
		"abcdefgh.gw.onegate.work", // OneGate's zone
		"onegate.work",
		"WiFi.NileHotel.com", // must be lowercase
		"wifi..nilehotel.com",
		"-wifi.nilehotel.com",
		"wifi_.nilehotel.com",
		"wifi.nilehotel.123",
		"localhost",
		"foo.github.io", // eTLD+1 of a private suffix: an apex there too
		strings.Repeat("a", 64) + ".nilehotel.com",
	} {
		if got, err := ValidateCustomDomain(bad, "onegate.work"); err == nil {
			t.Errorf("%q must be refused (got %q)", bad, got)
		}
	}
}

func TestParsePortalIP(t *testing.T) {
	for _, in := range []string{"10.255.255.1", "10.255.255.1/32", "172.16.0.9", "192.168.1.1"} {
		if _, err := ParsePortalIP(in); err != nil {
			t.Errorf("%q: %v", in, err)
		}
	}
	for _, in := range []string{"192.0.2.1", "8.8.8.8", "127.0.0.1", "169.254.1.1", "10.0.0.0/8", "fd00::1", "", "x"} {
		if _, err := ParsePortalIP(in); err == nil {
			t.Errorf("%q must be refused", in)
		}
	}
}

func TestCAA(t *testing.T) {
	rec, err := ParseCAA(`0 issue "letsencrypt.org"`)
	if err != nil || rec.Tag != "issue" || rec.Value != "letsencrypt.org" || rec.Flags != 0 {
		t.Fatalf("presentation form: %+v %v", rec, err)
	}
	// RFC 3597 generic form of 0 issue "letsencrypt.org".
	raw := append([]byte{0, 5}, []byte("issueletsencrypt.org")...)
	gen := `\# ` + itoa(len(raw)) + " " + hex.EncodeToString(raw)
	rec, err = ParseCAA(gen)
	if err != nil || rec.Tag != "issue" || rec.Value != "letsencrypt.org" {
		t.Fatalf("generic form: %+v %v", rec, err)
	}

	acc := "https://acme-v02.api.letsencrypt.org/acme/acct/1"
	cases := []struct {
		name string
		set  []CAARecord
		want bool
	}{
		{"no CAA", nil, true},
		{"LE", []CAARecord{{0, "issue", "letsencrypt.org"}}, true},
		{"LE among others", []CAARecord{{0, "issue", "digicert.com"}, {0, "issue", "letsencrypt.org"}}, true},
		{"other CA only", []CAARecord{{0, "issue", "digicert.com"}}, false},
		{"issue forbidden", []CAARecord{{0, "issue", ";"}}, false},
		{"only issuewild", []CAARecord{{0, "issuewild", ";"}}, true},
		{"only iodef", []CAARecord{{0, "iodef", "mailto:x@y.z"}}, true},
		{"critical unknown", []CAARecord{{128, "tbs", "x"}, {0, "issue", "letsencrypt.org"}}, false},
		{"our account", []CAARecord{{0, "issue", "letsencrypt.org; accounturi=" + acc}}, true},
		{"other account", []CAARecord{{0, "issue", "letsencrypt.org; accounturi=https://x/acct/2"}}, false},
		{"methods without dns-01", []CAARecord{{0, "issue", "letsencrypt.org; validationmethods=http-01"}}, false},
		{"methods with dns-01", []CAARecord{{0, "issue", "letsencrypt.org; validationmethods=http-01,dns-01"}}, true},
	}
	for _, c := range cases {
		if got, why := CAAPermits(c.set, acc); got != c.want {
			t.Errorf("%s: got %v (%s), want %v", c.name, got, why, c.want)
		}
	}
	if v := txtValue(`"abc" "def"`); v != "abcdef" {
		t.Errorf("txtValue: %q", v)
	}
}

func itoa(n int) string { return big.NewInt(int64(n)).String() }

func TestWeeklyCap(t *testing.T) {
	now := time.Date(2026, 10, 13, 12, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	if ok, _ := WeeklyCap(3, []time.Time{now.Add(-day), now.Add(-2 * day)}, now); !ok {
		t.Error("2 of 3 used: one more is allowed")
	}
	counted := []time.Time{now.Add(-1 * day), now.Add(-6 * day), now.Add(-3 * day)}
	ok, at := WeeklyCap(3, counted, now)
	if ok || !at.Equal(now.Add(-6*day).Add(7*day)) {
		t.Errorf("3 of 3 used: wait until the oldest ages out; got %v %v", ok, at)
	}
	// Older than the window does not count.
	if ok, _ := WeeklyCap(1, []time.Time{now.Add(-8 * day)}, now); !ok {
		t.Error("an issuance 8 days ago no longer counts")
	}
	// Over the cap (the limit was lowered): wait for enough to age out.
	ok, at = WeeklyCap(2, []time.Time{now.Add(-1 * day), now.Add(-5 * day), now.Add(-3 * day), now.Add(-4 * day)}, now)
	if ok || !at.Equal(now.Add(-3*day).Add(7*day)) {
		t.Errorf("4 counted, limit 2: three must age out; got %v %v", ok, at)
	}
}

func TestBackoff(t *testing.T) {
	want := []time.Duration{5 * time.Minute, 15 * time.Minute, time.Hour, 4 * time.Hour, 4 * time.Hour}
	for i, w := range want {
		if got := Backoff(i + 1); got != w {
			t.Errorf("Backoff(%d) = %v, want %v", i+1, got, w)
		}
	}
	if maxAttempts != 5 {
		t.Error("an order fails for good after 5 attempts")
	}
}

// RFC 9773 §4.1 example: AKI 69:88:5B:6B:87:46:40:41:E1:B3:7B:84:7B:A0:AE:2C:DE:01:C8:D4, serial
// 00:87:65:43:21 -> "aYhba4dGQEHhs3uEe6CuLN4ByNQ.AIdlQyE".
func TestARICertIDRFCExample(t *testing.T) {
	aki, _ := hex.DecodeString("69885B6B87464041E1B37B847BA0AE2CDE01C8D4")
	serial, _ := new(big.Int).SetString("0087654321", 16)
	if got := ariCertID(aki, serial); got != "aYhba4dGQEHhs3uEe6CuLN4ByNQ.AIdlQyE" {
		t.Fatalf("got %q", got)
	}
	// A serial without the top bit set carries no leading zero.
	if got := ariCertID([]byte{1}, big.NewInt(0x0102)); got != "AQ.AQI" {
		t.Fatalf("got %q", got)
	}
	if !DefaultRenewAfter(time.Unix(0, 0), time.Unix(90, 0)).Equal(time.Unix(60, 0)) {
		t.Error("the default renewal point is two thirds of the lifetime")
	}
	s, e := time.Unix(1000, 0), time.Unix(2000, 0)
	for i := 0; i < 100; i++ {
		if p := pickInWindow(s, e); p.Before(s) || !p.Before(e) {
			t.Fatalf("%v outside the window", p)
		}
	}
}

// fakeDoH answers the DNS-over-HTTPS JSON API from a table: name|type -> answers.
func fakeDoH(t *testing.T, table map[string][]dohAnswer) *httptest.Server {
	var mu sync.Mutex
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		key := strings.TrimSuffix(r.URL.Query().Get("name"), ".") + "|" + r.URL.Query().Get("type")
		_ = json.NewEncoder(w).Encode(dohResponse{Status: 0, Answer: table[key]})
	}))
}

func TestCheckDomain(t *testing.T) {
	c := DomainCheck{FQDN: "wifi.nilehotel.com", VerifyToken: "tok", CNAMETarget: "abcdefgh.gw.onegate.work",
		ChallengeAlias: "0123456789abcdef.acme.onegate.work", AccountURL: "https://ca/acct/1"}
	good := map[string][]dohAnswer{
		"_onegate-verify.wifi.nilehotel.com|16": {{Name: "_onegate-verify.wifi.nilehotel.com.", Type: 16, Data: `"tok"`}},
		"wifi.nilehotel.com|5":                  {{Name: "wifi.nilehotel.com.", Type: 5, Data: "abcdefgh.gw.onegate.work."}},
		"_acme-challenge.wifi.nilehotel.com|5":  {{Name: "_acme-challenge.wifi.nilehotel.com.", Type: 5, Data: "0123456789abcdef.acme.onegate.work."}},
		"nilehotel.com|257":                     {{Name: "nilehotel.com.", Type: 257, Data: `0 issue "letsencrypt.org"`}},
	}
	a, b := fakeDoH(t, good), fakeDoH(t, good)
	defer a.Close()
	defer b.Close()
	if err := CheckDomainEverywhere(context.Background(), []string{a.URL, b.URL}, nil, c); err != nil {
		t.Fatalf("all records published: %v", err)
	}

	// The second resolver disagrees (a stale cache without the token): not verified.
	stale := map[string][]dohAnswer{}
	for k, v := range good {
		stale[k] = v
	}
	delete(stale, "_onegate-verify.wifi.nilehotel.com|16")
	s := fakeDoH(t, stale)
	defer s.Close()
	if err := CheckDomainEverywhere(context.Background(), []string{a.URL, s.URL}, nil, c); err == nil {
		t.Fatal("both resolvers must agree")
	}

	breaks := map[string][]dohAnswer{
		"wifi.nilehotel.com|5":                 {{Name: "wifi.nilehotel.com.", Type: 5, Data: "elsewhere.example.com."}},
		"_acme-challenge.wifi.nilehotel.com|5": nil,
		"nilehotel.com|257":                    {{Name: "nilehotel.com.", Type: 257, Data: `0 issue "digicert.com"`}},
		"wifi.nilehotel.com|257":               {{Name: "wifi.nilehotel.com.", Type: 257, Data: `0 issue ";"`}},
	}
	for k, v := range breaks {
		bad := map[string][]dohAnswer{}
		for k2, v2 := range good {
			bad[k2] = v2
		}
		bad[k] = v
		srv := fakeDoH(t, bad)
		if err := CheckDomain(context.Background(), DoHResolver{URL: srv.URL}, c); err == nil {
			t.Errorf("changing %s must fail the check", k)
		}
		srv.Close()
	}
	// A CAA on the name itself overrides its parent's (the first name with CAA decides).
	override := map[string][]dohAnswer{}
	for k, v := range good {
		override[k] = v
	}
	override["nilehotel.com|257"] = []dohAnswer{{Name: "nilehotel.com.", Type: 257, Data: `0 issue "digicert.com"`}}
	override["wifi.nilehotel.com|257"] = []dohAnswer{{Name: "wifi.nilehotel.com.", Type: 257, Data: `0 issue "letsencrypt.org"`}}
	srv := fakeDoH(t, override)
	defer srv.Close()
	if err := CheckDomain(context.Background(), DoHResolver{URL: srv.URL}, c); err != nil {
		t.Errorf("CAA on the name itself decides: %v", err)
	}
}

func TestSecrets(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "portal-tls")
	s := Secrets{Dir: dir}
	if s.TokenSet() {
		t.Fatal("no token yet")
	}
	if err := s.WriteToken("has space"); err == nil {
		t.Error("a token with a space is refused")
	}
	if err := s.WriteToken("  cf-token-123\n"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ReadToken(); got != "cf-token-123" || !s.TokenSet() {
		t.Fatalf("token round trip: %q", got)
	}
	k1, err := s.AccountKey()
	if err != nil {
		t.Fatal(err)
	}
	k2, err := s.AccountKey()
	if err != nil || !k1.Equal(k2) {
		t.Fatal("the account key is created once and then reused")
	}
	if runtime.GOOS != "windows" {
		for _, f := range []string{accountKeyFile, dnsTokenFile} {
			fi, _ := os.Stat(filepath.Join(dir, f))
			if fi.Mode().Perm() != 0o600 {
				t.Errorf("%s mode %v, want 0600", f, fi.Mode().Perm())
			}
		}
		if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
			t.Errorf("dir mode %v, want 0700", fi.Mode().Perm())
		}
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 2 {
		t.Errorf("no temp files may remain: %v", ents)
	}
}

func TestSettingsValidate(t *testing.T) {
	str := func(s string) *string { return &s }
	n := func(i int) *int { return &i }
	if err := (SettingsUpdate{ACMEDirectory: str("prod")}).Validate(); err == nil {
		t.Error("directory must be staging or production")
	}
	if err := (SettingsUpdate{ContactEmail: str("not-an-email")}).Validate(); err == nil {
		t.Error("contact must be an email")
	}
	if err := (SettingsUpdate{WeeklyNewLimit: n(51)}).Validate(); err == nil {
		t.Error("weekly limit above Let's Encrypt's 50 is refused")
	}
	if err := (SettingsUpdate{ACMEDirectory: str("production"), ContactEmail: str("ops@onegate.work"), ZoneName: str("onegate.work"), WeeklyNewLimit: n(45)}).Validate(); err != nil {
		t.Error(err)
	}
}

// A fake Cloudflare API: enough of /zones and /dns_records to prove upsert, CAA preservation and that the
// token travels only in the Authorization header.
type fakeCF struct {
	mu      sync.Mutex
	records map[string]cfRecord
	next    int
	token   string
}

func (f *fakeCF) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(code int, result any) {
		raw, _ := json.Marshal(result)
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": code/100 == 2, "errors": []any{}, "result": json.RawMessage(raw)})
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		w.WriteHeader(403)
		_ = json.NewEncoder(w).Encode(map[string]any{"success": false, "errors": []map[string]any{{"code": 9109, "message": "Invalid access token"}}})
		return
	}
	switch {
	case r.URL.Path == "/zones":
		reply(200, []cfZone{{ID: "zone1", Name: "onegate.work", NameServers: []string{"a.ns.example", "b.ns.example"}}})
	case r.URL.Path == "/zones/zone1/dns_records" && r.Method == http.MethodGet:
		var out []cfRecord
		for _, rec := range f.records {
			if rec.Type == r.URL.Query().Get("type") && rec.Name == r.URL.Query().Get("name") {
				out = append(out, rec)
			}
		}
		reply(200, out)
	case r.URL.Path == "/zones/zone1/dns_records" && r.Method == http.MethodPost:
		var rec cfRecord
		_ = json.NewDecoder(r.Body).Decode(&rec)
		f.next++
		rec.ID = "r" + itoa(f.next)
		f.records[rec.ID] = rec
		reply(200, rec)
	case strings.HasPrefix(r.URL.Path, "/zones/zone1/dns_records/"):
		id := strings.TrimPrefix(r.URL.Path, "/zones/zone1/dns_records/")
		if _, ok := f.records[id]; !ok {
			reply(404, nil)
			return
		}
		if r.Method == http.MethodDelete {
			delete(f.records, id)
			reply(200, map[string]string{"id": id})
			return
		}
		var rec cfRecord
		_ = json.NewDecoder(r.Body).Decode(&rec)
		rec.ID = id
		f.records[id] = rec
		reply(200, rec)
	default:
		reply(404, nil)
	}
}

func TestCloudflare(t *testing.T) {
	f := &fakeCF{records: map[string]cfRecord{}, token: "secret-token-value"}
	f.records["keep"] = cfRecord{ID: "keep", Type: "CAA", Name: "onegate.work", Data: map[string]any{"flags": 0, "tag": "iodef", "value": "mailto:sec@onegate.work"}}
	f.records["other"] = cfRecord{ID: "other", Type: "CAA", Name: "onegate.work", Data: map[string]any{"flags": 0, "tag": "issue", "value": "digicert.com"}}
	f.records["oldle"] = cfRecord{ID: "oldle", Type: "CAA", Name: "onegate.work", Data: map[string]any{"flags": 0, "tag": "issue", "value": "letsencrypt.org"}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	ctx := context.Background()
	cf := &Cloudflare{Token: "secret-token-value", ZoneName: "onegate.work", BaseURL: srv.URL}

	zi, err := cf.Verify(ctx)
	if err != nil || zi.ID != "zone1" || len(zi.NameServers) != 2 {
		t.Fatalf("verify: %+v %v", zi, err)
	}
	id1, err := cf.UpsertA(ctx, "abcdefgh.gw.onegate.work", "10.255.255.1")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := cf.UpsertA(ctx, "abcdefgh.gw.onegate.work", "10.254.254.1")
	if err != nil || id1 != id2 {
		t.Fatalf("an address change updates the same record: %s %s %v", id1, id2, err)
	}
	if r := f.records[id1]; r.Content != "10.254.254.1" || r.TTL != 300 || r.Proxied == nil || *r.Proxied {
		t.Errorf("A record: %+v (TTL 300, never proxied)", r)
	}
	txt, err := cf.SetTXT(ctx, "_acme-challenge.abcdefgh.gw.onegate.work", "v")
	if err != nil || f.records[txt].TTL != 60 {
		t.Fatalf("TXT: %v %+v", err, f.records[txt])
	}
	if err := cf.DeleteRecord(ctx, txt); err != nil {
		t.Fatal(err)
	}
	if err := cf.DeleteRecord(ctx, txt); err != nil {
		t.Errorf("deleting a record that is gone is not an error: %v", err)
	}

	issue := "letsencrypt.org; accounturi=https://acme/acct/9"
	others, err := cf.ApplyCAA(ctx, issue)
	if err != nil {
		t.Fatal(err)
	}
	if len(others) != 1 || others[0] != "digicert.com" {
		t.Errorf("other issuers are reported and kept: %v", others)
	}
	tags := map[string][]string{}
	for _, r := range f.records {
		if r.Type == "CAA" {
			tags[r.Data["tag"].(string)] = append(tags[r.Data["tag"].(string)], r.Data["value"].(string))
		}
	}
	if len(tags["iodef"]) != 1 || len(tags["issuewild"]) != 1 || tags["issuewild"][0] != ";" || len(tags["issue"]) != 2 {
		t.Errorf("CAA after apply: %v", tags)
	}
	if f.records["oldle"].Data["value"] != issue {
		t.Errorf("the existing letsencrypt.org record is updated in place: %v", f.records["oldle"].Data)
	}
	// Applying again changes nothing.
	before := len(f.records)
	if _, err := cf.ApplyCAA(ctx, issue); err != nil || len(f.records) != before {
		t.Errorf("ApplyCAA is idempotent: %v %d->%d", err, before, len(f.records))
	}

	bad := &Cloudflare{Token: "wrong-token", ZoneName: "onegate.work", BaseURL: srv.URL}
	_, err = bad.Verify(ctx)
	if err == nil || strings.Contains(err.Error(), "wrong-token") {
		t.Fatalf("a refused token is an error that never contains the token: %v", err)
	}
}
