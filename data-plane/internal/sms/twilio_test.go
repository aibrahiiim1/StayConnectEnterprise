package sms

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func fakeTwilio(t *testing.T, status int, captured *url.Values, gotAuth *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/2010-04-01/Accounts/") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("unexpected content-type: %s", ct)
		}
		*gotAuth = r.Header.Get("Authorization")
		_ = r.ParseForm()
		*captured = r.PostForm
		if status >= 400 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"code":21211,"message":"Invalid 'To' Phone Number"}`))
			return
		}
		w.WriteHeader(status)
	}))
}

func TestTwilioSuccess(t *testing.T) {
	var captured url.Values
	var gotAuth string
	srv := fakeTwilio(t, http.StatusCreated, &captured, &gotAuth)
	defer srv.Close()

	tw, err := NewTwilio("ACtest", "secret-token", "+15555550000")
	if err != nil {
		t.Fatal(err)
	}
	tw.BaseURL = srv.URL
	tw.HTTPClient = &http.Client{Timeout: 2 * time.Second}

	if err := tw.Send(context.Background(), Message{
		To: "+15551234567", Text: "Code: 123456",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if !strings.HasPrefix(gotAuth, "Basic ") {
		t.Errorf("missing basic auth: %q", gotAuth)
	}
	if captured.Get("To") != "+15551234567" || captured.Get("From") != "+15555550000" {
		t.Errorf("form fields wrong: %+v", captured)
	}
	if captured.Get("Body") != "Code: 123456" {
		t.Errorf("body field wrong: %q", captured.Get("Body"))
	}
}

func TestTwilioErrorSurfacesMessage(t *testing.T) {
	var captured url.Values
	var gotAuth string
	srv := fakeTwilio(t, http.StatusBadRequest, &captured, &gotAuth)
	defer srv.Close()

	tw, _ := NewTwilio("ACtest", "secret", "+15555550000")
	tw.BaseURL = srv.URL

	err := tw.Send(context.Background(), Message{To: "+1xxx", Text: "x"})
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if !strings.Contains(err.Error(), "Invalid 'To' Phone Number") {
		t.Errorf("err didn't include upstream message: %v", err)
	}
}
