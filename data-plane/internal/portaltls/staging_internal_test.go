package portaltls

import (
	"os"
	"testing"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/portaltls/portaltlstest"
)

// A staging chain is named as such in the refusal, but still refused because its roots are in no trust store.
// testdata holds public staging intermediates only (no key material).
func TestAStagingChainIsRecognisedButNeverTrusted(t *testing.T) {
	raw, err := os.ReadFile("testdata/letsencrypt-staging-intermediates.pem")
	if err != nil {
		t.Fatal(err)
	}
	stg, err := ParseChain(raw)
	if err != nil || !isStagingChain(stg) {
		t.Fatalf("the staging intermediates must be recognised: %v", err)
	}
	ca, err := portaltlstest.New(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	own, err := ParseChain(ca.RootPEM())
	if err != nil || isStagingChain(own) {
		t.Fatalf("an ordinary chain is not a staging chain: %v", err)
	}
}
