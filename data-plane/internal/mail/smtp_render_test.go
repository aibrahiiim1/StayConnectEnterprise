package mail

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/http"
	"net/http/httptest"
	netmail "net/mail"
	"strings"
	"testing"
)

// Audit P5 L8: the boundary is random, bodies are quoted-printable, a Message-ID is set and the From name cannot
// change how the sender reads.
func TestTheRenderedMessageIsWellFormedWhateverTheBodyHolds(t *testing.T) {
	s, err := NewSMTP(SMTPConfig{Host: "smtp.example.com", Port: 587, Security: SMTPStartTLS,
		FromAddress: "wifi@example.com", FromName: `Front Desk" <ceo@evil.example>`})
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("x", 2000)
	text := "Code: 123456\n--=_onegate_alt_boundary_\nContent-Type: text/html\n\n<b>forged</b>\n" + long
	raw := s.render(Message{To: "alice@example.com", Subject: "Your code", Text: text, HTML: "<p>" + long + "</p>"})

	for _, line := range strings.Split(raw, "\r\n") {
		if len(line) > 998 {
			t.Fatalf("a line of %d octets exceeds RFC 5322", len(line))
		}
	}
	msg, err := netmail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	from, err := netmail.ParseAddress(msg.Header.Get("From"))
	if err != nil || from.Address != "wifi@example.com" {
		t.Fatalf("From reads as %v (%v)", from, err)
	}
	if id := msg.Header.Get("Message-ID"); !strings.HasPrefix(id, "<") || !strings.HasSuffix(id, "@example.com>") {
		t.Fatalf("Message-ID %q", id)
	}
	mt, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/alternative" || params["boundary"] == "=_onegate_alt_boundary_" {
		t.Fatalf("content type %q %v", mt, params)
	}
	mr := multipart.NewReader(msg.Body, params["boundary"])
	var parts []string
	for {
		p, err := mr.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if p.Header.Get("Content-Transfer-Encoding") != "quoted-printable" {
			t.Fatalf("part without quoted-printable: %v", p.Header)
		}
		b, _ := io.ReadAll(quotedprintable.NewReader(p))
		parts = append(parts, string(b))
	}
	if len(parts) != 2 || !strings.Contains(parts[0], "<b>forged</b>") || !strings.Contains(parts[0], long) {
		t.Fatalf("a body line reshaped the message: %d parts", len(parts))
	}
	if again := s.render(Message{To: "a@example.com", Subject: "s", Text: "t", HTML: "h"}); strings.Contains(again, params["boundary"]) {
		t.Fatal("the boundary must differ per message")
	}
}

// Audit P5 L9: a malformed address is refused before any provider call.
func TestSendGridRefusesAMalformedAddressWithoutCallingTheProvider(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer srv.Close()
	sg, _ := NewSendGrid("SG.testkey", "noreply@hotel.com", "")
	sg.Endpoint = srv.URL + "/v3/mail/send"
	for _, to := range []string{"not an address", "guest@", "a@b@c"} {
		if err := sg.Send(context.Background(), Message{To: to, Subject: "s", Text: "t"}); err == nil {
			t.Errorf("%q was accepted", to)
		}
	}
	if called {
		t.Fatal("the provider was called for a malformed address")
	}
}
