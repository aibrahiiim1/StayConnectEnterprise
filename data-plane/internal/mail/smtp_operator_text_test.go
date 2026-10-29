package mail

import (
	"errors"
	"strings"
	"testing"
)

// AD-13: the operator never reads the server's words back.
func TestOperatorSMTPTextHidesServerWords(t *testing.T) {
	banner := "220 internal-db-01 PostgreSQL proxy v9.1 ready"
	for _, e := range []error{
		errors.New("smtp: greeting: " + banner),
		errors.New("smtp: MAIL FROM: 550 " + banner),
		errors.New("smtp: RCPT TO: 550 " + banner),
		errors.New("smtp: authentication refused: 535 " + banner),
		errors.New("smtp: STARTTLS: " + banner),
		errors.New("smtp: DATA: 554 " + banner),
		errors.New("smtp: 554 " + banner),
		errors.New("smtp: connect to 10.0.0.9:25: dial tcp: " + banner),
	} {
		got, ok := OperatorSMTPText(e)
		if !ok || got == "" {
			t.Errorf("%v: expected generic text", e)
		}
		if strings.Contains(got, "PostgreSQL") || strings.Contains(got, "internal-db") || strings.Contains(got, "10.0.0.9") {
			t.Errorf("%v: server words leaked: %q", e, got)
		}
	}
	for _, e := range []error{
		errors.New("smtp: invalid recipient"),
		errors.New("smtp: " + plainSMTPRefused),
		errors.New("smtp: SMTP host must be a host name"),
		errors.New("something else"),
	} {
		if _, ok := OperatorSMTPText(e); ok {
			t.Errorf("%v: our own text must pass through", e)
		}
	}
}
