package notifyloader

import (
	"errors"
	"strings"
	"testing"
)

func TestScrubContactsRemovesRecipientsButKeepsTheProvidersWords(t *testing.T) {
	cases := map[string]string{
		"smtp: RCPT TO: 550 5.1.1 <maria.okonkwo@example.com>: Recipient address rejected":    "smtp: RCPT TO: 550 5.1.1 <[email]>: Recipient address rejected",
		"twilio: 400 code 21211: The 'To' number +44 7700 900123 is not a valid phone number": "twilio: 400 code 21211: The 'To' number [phone] is not a valid phone number",
		"sendgrid: 401 unauthorized": "sendgrid: 401 unauthorized",
	}
	for in, want := range cases {
		if got := ScrubContacts(in); got != want {
			t.Errorf("ScrubContacts(%q)\n got %q\nwant %q", in, got, want)
		}
	}
}

var errSentinel = errors.New("provider says no to +201001234567")

func TestScrubErrorKeepsIdentity(t *testing.T) {
	err := scrubError(errSentinel)
	if strings.Contains(err.Error(), "1001234567") {
		t.Fatalf("number survived: %v", err)
	}
	if !errors.Is(err, errSentinel) {
		t.Fatal("errors.Is no longer finds the original")
	}
	if scrubError(nil) != nil {
		t.Fatal("nil became an error")
	}
}
