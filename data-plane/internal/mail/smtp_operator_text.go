package mail

import "strings"

// OperatorSMTPText turns an SMTP delivery error into text safe to show an operator (audit AD-13). The error from
// Send carries the server's own words (its greeting, its refusal reason); on a mailer pointed at a LAN service that
// would let an operator read that service's banner back. The operator sees which step failed and a generic
// instruction; the full text belongs in the journal, written by the caller. It returns ok=false for an error that
// is not a server-derived SMTP failure (a fixed text of our own, or another channel), which the caller shows as it is.
func OperatorSMTPText(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "smtp: ") {
		return "", false
	}
	rest := strings.TrimPrefix(msg, "smtp: ")
	// Our own fixed texts carry no server words.
	if rest == "invalid recipient" || rest == plainSMTPRefused ||
		strings.HasPrefix(rest, "the server does not offer STARTTLS") || isOwnValidation(rest) {
		return "", false
	}
	switch {
	case strings.HasPrefix(rest, "connect to "):
		return "The mail server could not be reached. Check the host, the port and that the server is running.", true
	case strings.HasPrefix(rest, "greeting:"):
		return "The mail server did not answer like an SMTP server. Check the host and the port.", true
	case strings.HasPrefix(rest, "STARTTLS:"):
		return "A secure connection to the mail server could not be set up. Check the security setting and the server's certificate.", true
	case strings.HasPrefix(rest, "authentication refused"):
		return "The mail server refused the user name or password.", true
	case strings.HasPrefix(rest, "MAIL FROM:"):
		return "The mail server refused the from address. Check that the account may send from it.", true
	case strings.HasPrefix(rest, "RCPT TO:"):
		return "The mail server refused the recipient.", true
	}
	// DATA, body, the end of the message, and anything unlisted: the server's words stay in the journal.
	return "The mail server did not accept the message.", true
}

func isOwnValidation(rest string) bool {
	for _, p := range []string{"SMTP host", "SMTP port", "SMTP security", "a password is required", "a username is required", "the from address", "the timeout"} {
		if strings.HasPrefix(rest, p) {
			return true
		}
	}
	return false
}
