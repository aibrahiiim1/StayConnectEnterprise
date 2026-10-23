package notifyloader

// Provider errors often quote the guest's address or number; scrubbing here, where all three channels pass, keeps
// contacts out of logs, last_error and the provider screen.

import (
	"errors"
	"regexp"
)

var (
	emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)
	// A phone number is a run of 8 or more digits (optional +, spaces, dots, dashes, brackets); shorter runs such as
	// error codes, SMTP replies and ports stay visible.
	phonePattern = regexp.MustCompile(`\+?\d[\d\s().\-]{6,}\d`)
)

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
