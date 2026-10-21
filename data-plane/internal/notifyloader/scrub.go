package notifyloader

// PROVIDER ERRORS DO NOT CARRY THE RECIPIENT (GDPR audit B12).
//
// Twilio, SendGrid and SMTP servers often quote the address or number they refused ("RCPT TO <guest@...>
// rejected", "The 'To' number +4477... is not a valid phone number"). That text is logged, stored in
// notification_providers.last_error and shown to every role that reads the provider screen, so a guest's
// contact would outlive the code that was sent to it. Every send error is scrubbed here, at the one place all
// three channels pass through, before anything stores or logs it. The provider's own words are kept otherwise:
// they are what an operator acts on.

import (
	"errors"
	"regexp"
)

var (
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	// A run of digits, optionally led by + and broken by spaces, dots, dashes or brackets; it is a phone number
	// when it holds eight or more digits. Shorter runs are left alone: provider error codes (21211), SMTP
	// replies (550 5.1.1), HTTP statuses and ports are what an operator needs to see.
	phonePattern = regexp.MustCompile(`\+?\d[\d\s().\-]{6,}\d`)
)

// ScrubContacts replaces email addresses and phone numbers in s.
func ScrubContacts(s string) string {
	s = emailPattern.ReplaceAllString(s, "[email]")
	return phonePattern.ReplaceAllStringFunc(s, func(m string) string {
		digits := 0
		for _, r := range m {
			if r >= '0' && r <= '9' {
				digits++
			}
		}
		if digits < 8 {
			return m
		}
		return "[phone]"
	})
}

type scrubbedError struct {
	msg string
	err error
}

func (e *scrubbedError) Error() string { return e.msg }
func (e *scrubbedError) Unwrap() error { return e.err }

// scrubError keeps the error's identity for errors.Is/As and changes only its text.
func scrubError(err error) error {
	if err == nil {
		return nil
	}
	msg := ScrubContacts(err.Error())
	if msg == err.Error() {
		return err
	}
	var already *scrubbedError
	if errors.As(err, &already) {
		return err
	}
	return &scrubbedError{msg: msg, err: err}
}
