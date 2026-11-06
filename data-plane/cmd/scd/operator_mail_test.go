package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/onegate/onegate/data-plane/internal/mail"
)

type operatorMailbox struct {
	mu   sync.Mutex
	sent []mail.Message
}

func (c *operatorMailbox) Send(_ context.Context, m mail.Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, m)
	return nil
}

func TestTheResetEmailCarriesOnlyTheCodeAndItsLifetime(t *testing.T) {
	m, problem := operatorMailMessage(operatorMailRequest{To: "ann@example.com", Template: "reset_code", Code: "01234567", Minutes: 15})
	if problem != "" {
		t.Fatal(problem)
	}
	if m.To != "ann@example.com" || !strings.Contains(m.Text, "01234567") || !strings.Contains(m.Text, "15 minutes") {
		t.Fatalf("message %+v", m)
	}
	for _, leak := range []string{"http://", "https://", "password is", "username"} {
		if strings.Contains(strings.ToLower(m.Text), leak) {
			t.Fatalf("the email must not carry %q: %s", leak, m.Text)
		}
	}
	if n, _ := operatorMailMessage(operatorMailRequest{To: "ann@example.com", Template: "password_changed"}); !strings.Contains(n.Subject, "was changed") {
		t.Fatalf("notice %+v", n)
	}
}

func TestAnOperatorEmailIsRefusedWhenItIsNotWellFormed(t *testing.T) {
	for _, in := range []operatorMailRequest{
		{To: "", Template: "reset_code", Code: "01234567", Minutes: 15},
		{To: "Ann <ann@example.com>", Template: "reset_code", Code: "01234567", Minutes: 15},
		{To: "ann@example.com", Template: "reset_code", Code: "1234", Minutes: 15},
		{To: "ann@example.com", Template: "reset_code", Code: "01234567", Minutes: 90},
		{To: "ann@example.com", Template: "anything", Code: "01234567", Minutes: 15},
	} {
		if _, problem := operatorMailMessage(in); problem == "" {
			t.Fatalf("%+v was accepted", in)
		}
	}
}

func TestOperatorEmailGoesOnlyThroughTheSitesOwnSMTPServer(t *testing.T) {
	body := `{"to":"ann@example.com","template":"reset_code","code":"01234567","minutes":15}`
	for _, kind := range []string{"", "stub", "sendgrid"} {
		cap := &operatorMailbox{}
		s := &server{mail: cap}
		s.notify.mailKind = kind
		w := httptest.NewRecorder()
		s.operatorMailSend(w, httptest.NewRequest(http.MethodPost, "/v1/admin/operator-mail/send", strings.NewReader(body)))
		if w.Code != http.StatusConflict || len(cap.sent) != 0 {
			t.Fatalf("kind %q: %d, %d sent", kind, w.Code, len(cap.sent))
		}
	}
	cap := &operatorMailbox{}
	s := &server{mail: cap}
	s.notify.mailKind = "smtp"
	w := httptest.NewRecorder()
	s.operatorMailSend(w, httptest.NewRequest(http.MethodPost, "/v1/admin/operator-mail/send", strings.NewReader(body)))
	if w.Code != http.StatusOK || len(cap.sent) != 1 || cap.sent[0].To != "ann@example.com" {
		t.Fatalf("smtp: %d, %+v", w.Code, cap.sent)
	}
	st := httptest.NewRecorder()
	s.operatorMailState(st, httptest.NewRequest(http.MethodGet, "/v1/admin/operator-mail/state", nil))
	if !strings.Contains(st.Body.String(), `"ready":true`) {
		t.Fatalf("state %s", st.Body.String())
	}
}
