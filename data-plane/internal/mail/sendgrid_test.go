package mail

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeSendGrid stands in for api.sendgrid.com — verifies the request shape
// matches the v3 /mail/send contract and lets the test assert on it.
func fakeSendGrid(t *testing.T, status int, captured *sgRequest, gotAuth *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/mail/send" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("unexpected content-type: %s", ct)
		}
		*gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, captured); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if status >= 400 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"errors":[{"message":"invalid email","field":"to"}]}`))
			return
		}
		w.WriteHeader(status)
	}))
}

func TestSendGridSuccess(t *testing.T) {
	var captured sgRequest
	var gotAuth string
	srv := fakeSendGrid(t, http.StatusAccepted, &captured, &gotAuth)
	defer srv.Close()

	sg, err := NewSendGrid("SG.testkey", "noreply@hotel.com", "Hotel WiFi")
	if err != nil {
		t.Fatal(err)
	}
	sg.Endpoint = srv.URL + "/v3/mail/send"
	sg.HTTPClient = &http.Client{Timeout: 2 * time.Second}

	if err := sg.Send(context.Background(), Message{
		To: "guest@example.com", Subject: "Your code", Text: "Code: 123456",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotAuth != "Bearer SG.testkey" {
		t.Errorf("auth header = %q, want Bearer SG.testkey", gotAuth)
	}
	if captured.From.Email != "noreply@hotel.com" || captured.From.Name != "Hotel WiFi" {
		t.Errorf("from = %+v", captured.From)
	}
	if len(captured.Personalizations) != 1 || captured.Personalizations[0].To[0].Email != "guest@example.com" {
		t.Errorf("recipient = %+v", captured.Personalizations)
	}
	if captured.Subject != "Your code" {
		t.Errorf("subject = %q", captured.Subject)
	}
}

func TestSendGridErrorSurfacesMessage(t *testing.T) {
	var captured sgRequest
	var gotAuth string
	srv := fakeSendGrid(t, http.StatusBadRequest, &captured, &gotAuth)
	defer srv.Close()

	sg, _ := NewSendGrid("SG.testkey", "noreply@hotel.com", "")
	sg.Endpoint = srv.URL + "/v3/mail/send"

	err := sg.Send(context.Background(), Message{To: "x@y", Subject: "s", Text: "t"})
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if !strings.Contains(err.Error(), "invalid email") {
		t.Errorf("err didn't include upstream message: %v", err)
	}
}

func TestSendGridConstructorValidation(t *testing.T) {
	if _, err := NewSendGrid("", "from@x", ""); err == nil {
		t.Error("missing api_key should fail")
	}
	if _, err := NewSendGrid("k", "", ""); err == nil {
		t.Error("missing from_address should fail")
	}
}
