package whatsapp

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testCode   = "482913"
	testSecret = "super-secret-token-value"
)

// captureLogs routes slog to a buffer for the test and restores the default afterwards.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

func assertNoLeak(t *testing.T, logs string) {
	t.Helper()
	if strings.Contains(logs, testCode) || strings.Contains(logs, testSecret) {
		t.Fatalf("log leaked the code or a secret: %s", logs)
	}
}

type captured struct {
	method, path, auth, ctype string
	body                      []byte
}

func fixture(t *testing.T, status int, reply string, got *captured) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path = r.Method, r.URL.Path
		got.auth, got.ctype = r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		got.body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestMetaSendsTheCodeAsAuthenticationTemplateParameters(t *testing.T) {
	logs := captureLogs(t)
	var got captured
	srv := fixture(t, http.StatusOK, `{"messages":[{"id":"wamid.X"}]}`, &got)
	m, err := NewMeta(testSecret, "1098765", "onegate_code", "")
	if err != nil {
		t.Fatal(err)
	}
	m.BaseURL = srv.URL
	if err := m.Send(context.Background(), Message{To: "+201001234567", Code: testCode, TTLMinutes: 10, Locale: "ar"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got.method != http.MethodPost || got.path != "/1098765/messages" {
		t.Fatalf("request %s %s", got.method, got.path)
	}
	if got.auth != "Bearer "+testSecret || got.ctype != "application/json" {
		t.Fatalf("headers auth=%q ctype=%q", got.auth, got.ctype)
	}
	var body map[string]any
	if err := json.Unmarshal(got.body, &body); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                "201001234567",
		"type":              "template",
		"template": map[string]any{
			"name":     "onegate_code",
			"language": map[string]any{"code": "ar"}, // no configured language: the message locale
			"components": []any{
				map[string]any{"type": "body", "parameters": []any{map[string]any{"type": "text", "text": testCode}}},
				map[string]any{"type": "button", "sub_type": "url", "index": "0",
					"parameters": []any{map[string]any{"type": "text", "text": testCode}}},
			},
		},
	}
	gotJSON, _ := json.Marshal(body)
	wantJSON, _ := json.Marshal(want)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("body\n got %s\nwant %s", gotJSON, wantJSON)
	}
	if _, free := body["text"]; free {
		t.Fatal("free-text message sent")
	}
	assertNoLeak(t, logs.String())
}

func TestMetaConfiguredLanguageWinsAndDefaultIsEnglish(t *testing.T) {
	var got captured
	srv := fixture(t, http.StatusOK, `{}`, &got)
	m, _ := NewMeta(testSecret, "1", "tpl", "en_US")
	m.BaseURL = srv.URL
	_ = m.Send(context.Background(), Message{To: "+15551234567", Code: testCode, Locale: "ar"})
	if !bytes.Contains(got.body, []byte(`"language":{"code":"en_US"}`)) {
		t.Fatalf("configured language not used: %s", got.body)
	}
	m2, _ := NewMeta(testSecret, "1", "tpl", "")
	m2.BaseURL = srv.URL
	_ = m2.Send(context.Background(), Message{To: "+15551234567", Code: testCode})
	if !bytes.Contains(got.body, []byte(`"language":{"code":"en"}`)) {
		t.Fatalf("default language not en: %s", got.body)
	}
}

func TestMetaErrorIsTypedAndReadable(t *testing.T) {
	logs := captureLogs(t)
	var got captured
	srv := fixture(t, http.StatusBadRequest,
		`{"error":{"message":"(#132001) Template name does not exist in the translation","type":"OAuthException","code":132001}}`, &got)
	m, _ := NewMeta(testSecret, "1", "missing", "en")
	m.BaseURL = srv.URL
	err := m.Send(context.Background(), Message{To: "+15551234567", Code: testCode})
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.Provider != "meta" || pe.Status != 400 || pe.Code != "132001" ||
		!strings.Contains(pe.Message, "Template name does not exist") {
		t.Fatalf("error %#v", err)
	}
	if strings.Contains(err.Error(), testSecret) || strings.Contains(err.Error(), testCode) {
		t.Fatalf("error text leaked: %v", err)
	}
	assertNoLeak(t, logs.String())
}

func TestMetaRequiresConfiguration(t *testing.T) {
	_, err := NewMeta("", "", "", "")
	if !errors.Is(err, ErrConfig) || !strings.Contains(err.Error(), "access token") ||
		!strings.Contains(err.Error(), "phone number ID") || !strings.Contains(err.Error(), "template name") {
		t.Fatalf("err %v", err)
	}
}

func TestTwilioSendsContentTemplateWithWhatsAppAddresses(t *testing.T) {
	logs := captureLogs(t)
	var got captured
	srv := fixture(t, http.StatusCreated, `{"sid":"SM1"}`, &got)
	tw, err := NewTwilio("AC123", testSecret, "+14155238886", "HXabc")
	if err != nil {
		t.Fatal(err)
	}
	tw.BaseURL = srv.URL
	if err := tw.Send(context.Background(), Message{To: "+201001234567", Code: testCode}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got.method != http.MethodPost || got.path != "/2010-04-01/Accounts/AC123/Messages.json" {
		t.Fatalf("request %s %s", got.method, got.path)
	}
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("AC123:"+testSecret))
	if got.auth != wantAuth || got.ctype != "application/x-www-form-urlencoded" {
		t.Fatalf("headers auth=%q ctype=%q", got.auth, got.ctype)
	}
	form, err := url.ParseQuery(string(got.body))
	if err != nil {
		t.Fatal(err)
	}
	if form.Get("From") != "whatsapp:+14155238886" || form.Get("To") != "whatsapp:+201001234567" ||
		form.Get("ContentSid") != "HXabc" || form.Get("ContentVariables") != `{"1":"`+testCode+`"}` {
		t.Fatalf("form %v", form)
	}
	if _, free := form["Body"]; free {
		t.Fatal("free-text Body sent")
	}
	assertNoLeak(t, logs.String())
}

func TestTwilioErrorIsTypedAndReadable(t *testing.T) {
	var got captured
	srv := fixture(t, http.StatusBadRequest, `{"code":63016,"message":"Failed to send freeform message outside the window"}`, &got)
	tw, _ := NewTwilio("AC1", testSecret, "whatsapp:+14155238886", "HX1")
	tw.BaseURL = srv.URL
	err := tw.Send(context.Background(), Message{To: "+15551234567", Code: testCode})
	var pe *ProviderError
	if !errors.As(err, &pe) || pe.Provider != "twilio" || pe.Status != 400 || pe.Code != "63016" {
		t.Fatalf("error %#v", err)
	}
	if strings.Contains(err.Error(), testSecret) {
		t.Fatalf("error text leaked: %v", err)
	}
}

func TestTwilioRequiresConfiguration(t *testing.T) {
	if _, err := NewTwilio("AC1", "tok", "", "HX1"); !errors.Is(err, ErrConfig) {
		t.Fatalf("missing sender accepted: %v", err)
	}
	if _, err := NewTwilio("AC1", "tok", "14155238886", "HX1"); !errors.Is(err, ErrConfig) {
		t.Fatalf("non-E.164 sender accepted: %v", err)
	}
	if _, err := NewTwilio("AC1", "tok", "+14155238886", ""); !errors.Is(err, ErrConfig) {
		t.Fatalf("missing content SID accepted: %v", err)
	}
}

func TestSendRefusesBadDestinationAndEmptyCodeWithoutCalling(t *testing.T) {
	var got captured
	srv := fixture(t, http.StatusOK, `{}`, &got)
	m, _ := NewMeta(testSecret, "1", "tpl", "")
	m.BaseURL = srv.URL
	if err := m.Send(context.Background(), Message{To: "0100123", Code: testCode}); !errors.Is(err, ErrInvalidDest) {
		t.Fatalf("err %v", err)
	}
	if err := m.Send(context.Background(), Message{To: "+15551234567", Code: " "}); !errors.Is(err, ErrEmptyCode) {
		t.Fatalf("err %v", err)
	}
	if got.method != "" {
		t.Fatal("provider contacted for an invalid message")
	}
}

func TestTransportFailureIsAProviderError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()
	tw, _ := NewTwilio("AC1", testSecret, "+14155238886", "HX1")
	tw.BaseURL = srv.URL
	var pe *ProviderError
	if err := tw.Send(context.Background(), Message{To: "+15551234567", Code: testCode}); !errors.As(err, &pe) || pe.Err == nil {
		t.Fatalf("err %v", err)
	}
}

func TestStubWritesItsFileAndNeverLogsTheCode(t *testing.T) {
	logs := captureLogs(t)
	p := filepath.Join(t.TempDir(), "wa.log")
	if err := NewStub(p).Send(context.Background(), Message{To: "+15551234567", Code: testCode, TTLMinutes: 10}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), testCode) {
		t.Fatalf("stub file %q", b)
	}
	assertNoLeak(t, logs.String())
	if !strings.Contains(logs.String(), "4567") {
		t.Fatalf("stub log lacks the destination suffix: %s", logs.String())
	}
}
