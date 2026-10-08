package mail

import (
	"bufio"
	"context"
	"net"
	"net/smtp"
	"strings"
	"testing"
	"time"
)

func TestSMTPConfigValidation(t *testing.T) {
	good := SMTPConfig{Host: "smtp.example.com", Port: 587, Security: SMTPStartTLS, Username: "u", Password: "p", FromAddress: "wifi@example.com"}
	if msg := ValidateSMTPConfig(good); msg != "" {
		t.Fatalf("valid config refused: %s", msg)
	}
	bad := []SMTPConfig{
		{Host: "", Port: 587, Security: SMTPStartTLS, FromAddress: "a@b.c"},
		{Host: "smtp host", Port: 587, Security: SMTPStartTLS, FromAddress: "a@b.c"},
		{Host: "h", Port: 0, Security: SMTPStartTLS, FromAddress: "a@b.c"},
		{Host: "h", Port: 70000, Security: SMTPStartTLS, FromAddress: "a@b.c"},
		{Host: "h", Port: 587, Security: "ssl", FromAddress: "a@b.c"},
		{Host: "h", Port: 587, Security: SMTPStartTLS, Username: "u", FromAddress: "a@b.c"},
		{Host: "h", Port: 587, Security: SMTPStartTLS, Password: "p", FromAddress: "a@b.c"},
		{Host: "h", Port: 587, Security: SMTPStartTLS, FromAddress: "not an address"},
	}
	for i, c := range bad {
		if ValidateSMTPConfig(c) == "" {
			t.Errorf("case %d must be refused: %+v", i, c)
		}
	}
	if DefaultSMTPPort(SMTPTLS) != 465 || DefaultSMTPPort(SMTPStartTLS) != 587 || DefaultSMTPPort(SMTPNone) != 25 {
		t.Fatal("default ports")
	}
}

// fakeSMTP is a plain-text SMTP server good enough to receive one message (security=none path).
func fakeSMTP(t *testing.T) (addr string, got chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	got = make(chan string, 1)
	go func() {
		defer ln.Close()
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		w := func(s string) { _, _ = c.Write([]byte(s + "\r\n")) }
		w("220 fake ESMTP")
		var data strings.Builder
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\r\n")
			if inData {
				if line == "." {
					inData = false
					w("250 queued")
					got <- data.String()
					continue
				}
				data.WriteString(line + "\n")
				continue
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				w("250-fake")
				w("250 8BITMIME")
			case strings.HasPrefix(line, "MAIL FROM"), strings.HasPrefix(line, "RCPT TO"):
				w("250 ok")
			case line == "DATA":
				w("354 go")
				inData = true
			case line == "QUIT":
				w("221 bye")
				return
			default:
				w("500 what")
			}
		}
	}()
	return ln.Addr().String(), got
}

func TestSMTPSendsOneMessageInTheClearWhenToldTo(t *testing.T) {
	addr, got := fakeSMTP(t)
	host, port, _ := net.SplitHostPort(addr)
	var p int
	for _, c := range port {
		p = p*10 + int(c-'0')
	}
	m, err := NewSMTP(SMTPConfig{Host: host, Port: p, Security: SMTPNone, FromAddress: "wifi@example.com", FromName: "Café Wi-Fi", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Send(context.Background(), Message{To: "alice@example.com", Subject: "Your code", Text: "Your one-time code is: 123456"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	select {
	case body := <-got:
		for _, want := range []string{"From: =?utf-8?q?Caf=C3=A9_Wi-Fi?= <wifi@example.com>", "To: alice@example.com", "Subject: =?utf-8?q?Your_code?=", "123456"} {
			if !strings.Contains(body, want) {
				t.Errorf("message lacks %q:\n%s", want, body)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no message received")
	}
}

func TestPasswordAuthRefusesAPlaintextHop(t *testing.T) {
	a := &loginOrPlain{user: "u", pass: "p", host: "h"}
	if _, _, err := a.Start(&smtp.ServerInfo{Name: "h", TLS: false, Auth: []string{"PLAIN"}}); err == nil {
		t.Fatal("PLAIN without TLS must be refused")
	}
	mech, resp, err := a.Start(&smtp.ServerInfo{Name: "h", TLS: true, Auth: []string{"LOGIN", "PLAIN"}})
	if err != nil || mech != "PLAIN" || string(resp) != "\x00u\x00p" {
		t.Fatalf("PLAIN preferred under TLS: %q %q %v", mech, resp, err)
	}
	mech, _, err = a.Start(&smtp.ServerInfo{Name: "h", TLS: true, Auth: []string{"LOGIN"}})
	if err != nil || mech != "LOGIN" {
		t.Fatalf("LOGIN fallback: %q %v", mech, err)
	}
	if r, _ := a.Next([]byte("Username:"), true); string(r) != "u" {
		t.Fatal("LOGIN username step")
	}
	if r, _ := a.Next([]byte("Password:"), true); string(r) != "p" {
		t.Fatal("LOGIN password step")
	}
}
