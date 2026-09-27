package activation

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// holds_customer_id is optional. An appliance that predates it signed exactly the eleven original fields; its
// request must still verify, so an empty value must leave the signed bytes unchanged.
func TestHoldsCustomerOmittedKeepsLegacySignedBytes(t *testing.T) {
	r, _ := newReq(t)
	legacy, _ := json.Marshal(struct {
		SchemaVersion int    `json:"schema_version"`
		RequestID     string `json:"request_id"`
		Serial        string `json:"serial"`
		PublicKey     string `json:"public_key"`
		WANMAC        string `json:"wan_mac"`
		LANMAC        string `json:"lan_mac"`
		HardwareFpr   string `json:"hardware_fingerprint"`
		Hostname      string `json:"hostname"`
		Model         string `json:"model"`
		CreatedAt     int64  `json:"created_at"`
		Nonce         string `json:"nonce"`
	}{r.SchemaVersion, r.RequestID, r.Serial, r.PublicKey, r.WANMAC, r.LANMAC, r.HardwareFpr, r.Hostname,
		r.Model, r.CreatedAt, r.Nonce})
	if string(requestSigningBytes(r)) != string(legacy) {
		t.Fatalf("an empty holds_customer_id changed the signed bytes:\n got %s\nwant %s", requestSigningBytes(r), legacy)
	}
	out, _ := json.Marshal(r)
	if strings.Contains(string(out), "holds_customer_id") {
		t.Fatal("a factory-clean request must not carry holds_customer_id")
	}
}

// When present, the field is signed: it cannot be removed or changed to point at another customer.
func TestHoldsCustomerIsCoveredBySignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	r := &Request{SchemaVersion: SchemaVersion, RequestID: "req-h", Serial: "SC-H",
		PublicKey: base64.RawStdEncoding.EncodeToString(pub), Nonce: "n", HoldsCustomerID: "cust-a"}
	SignRequest(priv, r)
	if !VerifyRequest(r) {
		t.Fatal("a signed request that holds a customer must verify")
	}
	for _, v := range []string{"", "cust-b"} {
		x := *r
		x.HoldsCustomerID = v
		if VerifyRequest(&x) {
			t.Fatalf("holds_customer_id changed to %q and the request still verified", v)
		}
	}
	// Round trip through the file the operator carries.
	b, _ := json.Marshal(r)
	var back Request
	if err := json.Unmarshal(b, &back); err != nil || back.HoldsCustomerID != "cust-a" || !VerifyRequest(&back) {
		t.Fatalf("the request file lost or broke holds_customer_id: %v %+v", err, back)
	}
}
