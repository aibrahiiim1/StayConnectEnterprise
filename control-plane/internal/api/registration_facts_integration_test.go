package api

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/control-plane/internal/activation"
)

// Audit Phase 1 M11: every self-reported fact is checked.
func TestApplianceFactsRules(t *testing.T) {
	ok := applianceFacts{Serial: "SC-ABCD-EFGH-JKMN", Hostname: "onegate.local", Model: "Dell Inc. PowerEdge R250",
		WANMAC: "00-11-22-AA-BB-CC", LANMAC: "", Fingerprint: "0123456789abcdef0123456789abcdef"}
	n, err := ok.normalize()
	if err != nil || n.WANMAC != "00:11:22:aa:bb:cc" {
		t.Fatalf("a real appliance's facts must pass and the MAC be canonical: %+v %v", n, err)
	}
	for name, f := range map[string]applianceFacts{
		"serial with space":   {Serial: "SC 1"},
		"serial too long":     {Serial: strings.Repeat("A", 65)},
		"serial with quote":   {Serial: `SC"1`},
		"hostname bad char":   {Serial: "S", Hostname: "host;rm"},
		"hostname too long":   {Serial: "S", Hostname: strings.Repeat("h", 254)},
		"model too long":      {Serial: "S", Model: strings.Repeat("m", 121)},
		"model control char":  {Serial: "S", Model: "Dell\x00"},
		"bad wan mac":         {Serial: "S", WANMAC: "00:11:22"},
		"bad lan mac":         {Serial: "S", LANMAC: "nope"},
		"fingerprint not hex": {Serial: "S", Fingerprint: "xyz"},
		"fingerprint long":    {Serial: "S", Fingerprint: strings.Repeat("a", 129)},
	} {
		if _, err := f.normalize(); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
}

func TestWriteFileEscapesTheName(t *testing.T) {
	rec := httptest.NewRecorder()
	writeFile(rec, `license-SC"; evil=1.json`, map[string]string{})
	_, params, err := mime.ParseMediaType(rec.Header().Get("Content-Disposition"))
	if err != nil || params["filename"] != `license-SC"; evil=1.json` || params["evil"] != "" {
		t.Fatalf("Content-Disposition not escaped: %q (%v)", rec.Header().Get("Content-Disposition"), err)
	}
}

func TestRegistrationRefusesBadFacts(t *testing.T) {
	f := newFixture(t)
	bx := newBox("SC-TEST-M11 bad", "hwf-m11")
	if rec := f.register(bx); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "serial") {
		t.Fatalf("a serial with a space must be refused: %d %s", rec.Code, rec.Body.String())
	}
	if n := f.count(`SELECT count(*) FROM appliances`); n != 0 {
		t.Fatal("nothing may be stored")
	}
}

func signedRequest(t *testing.T, serial string) (activation.Request, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	req := activation.Request{SchemaVersion: activation.SchemaVersion, RequestID: "req-" + serial, Serial: serial,
		PublicKey: base64.RawStdEncoding.EncodeToString(pub), WANMAC: "00:11:22:33:44:66",
		HardwareFpr: "abcdef0123456789", Hostname: "onegate", CreatedAt: time.Now().Unix(), Nonce: "n-" + serial}
	activation.SignRequest(priv, &req)
	return req, priv
}

// Audit Phase 1 M16: a request id recorded for another appliance is a 409, and one request yields one package
// even under concurrent calls.
func TestOfflineImportConflictAndSinglePackage(t *testing.T) {
	f := newFixture(t)
	f.withCA()
	vendor := f.t.TempDir() + "/vendor.key"
	_, vk, _ := ed25519.GenerateKey(rand.Reader)
	if err := os.WriteFile(vendor, vk, 0o600); err != nil {
		t.Fatal(err)
	}
	off := NewOfflineBase(f.base, vendor, "", "https://central.example.invalid")
	if off == nil {
		t.Fatal("offline base")
	}
	reqA, _ := signedRequest(t, "SC-TEST-M16-A")
	f.header = map[string]string{"X-Activation-Code": codeOfPub(t, reqA.PublicKey)}
	if rec := f.operatorCall(off.importRequest, http.MethodPost, "", reqA); rec.Code != http.StatusOK {
		t.Fatalf("import A: %d %s", rec.Code, rec.Body.String())
	}
	reqB, _ := signedRequest(t, "SC-TEST-M16-B")
	reqB.RequestID = reqA.RequestID
	_, privB, _ := ed25519.GenerateKey(rand.Reader)
	reqB.PublicKey = base64.RawStdEncoding.EncodeToString(privB.Public().(ed25519.PublicKey))
	activation.SignRequest(privB, &reqB)
	f.header = map[string]string{"X-Activation-Code": codeOfPub(t, reqB.PublicKey)}
	if rec := f.operatorCall(off.importRequest, http.MethodPost, "", reqB); rec.Code != http.StatusConflict {
		t.Fatalf("a request id of another appliance must be 409, got %d %s", rec.Code, rec.Body.String())
	}

	f.header = nil
	var appID string
	if err := f.pool.QueryRow(f.ctx, `SELECT id::text FROM appliances WHERE serial='SC-TEST-M16-A'`).Scan(&appID); err != nil {
		t.Fatal(err)
	}
	cust, site := f.customerSite("Offline")
	f.activate(appID, cust, site, 5)
	var wg sync.WaitGroup
	codes := make([]int, 4)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			rec := f.operatorCall(off.activationPackage, http.MethodPost, appID, map[string]any{})
			codes[i] = rec.Code
			if rec.Code >= 500 {
				t.Logf("package call: %d %s", rec.Code, rec.Body.String())
			}
		}(i)
	}
	wg.Wait()
	created, failed := 0, 0
	for _, c := range codes {
		if c == http.StatusCreated {
			created++
		}
		if c >= 500 {
			failed++
		}
	}
	if created != 1 || failed != 0 || f.count(`SELECT count(*) FROM offline_activation_packages WHERE appliance_id=$1`, appID) != 1 {
		t.Fatalf("one request must yield exactly one package: codes %v", codes)
	}
}
