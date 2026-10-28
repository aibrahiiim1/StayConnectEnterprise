package api

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func decodeBody(body string, dst any) error {
	return DecodeJSON(httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(body)), dst)
}

// Audit Phase 1 M9: bodies are bounded and the named fields have maximum lengths, nested ones included.
func TestDecodeJSONLimits(t *testing.T) {
	var in activateReq
	big := `{"reason":"` + strings.Repeat("a", MaxJSONBody) + `"}`
	if err := decodeBody(big, &in); err == nil || !strings.Contains(badBody(err), "larger than") {
		t.Fatalf("a body over %d bytes must be refused: %v", MaxJSONBody, err)
	}

	for _, c := range []struct {
		body, field string
	}{
		{`{"reason":"` + strings.Repeat("r", 501) + `"}`, "reason"},
		{`{"new_customer":{"name":"` + strings.Repeat("n", 121) + `"}}`, "name"},
		{`{"new_site":{"name":"s","code":"` + strings.Repeat("c", 49) + `"}}`, "code"},
	} {
		var in activateReq
		err := decodeBody(c.body, &in)
		var fl *FieldLimitError
		if !errors.As(err, &fl) || fl.Field != c.field {
			t.Fatalf("%s over its limit must be refused, got %v", c.field, err)
		}
		if !strings.Contains(badBody(err), c.field+" must be at most") {
			t.Fatalf("message must name the field: %q", badBody(err))
		}
	}

	// At the limit is accepted; multi-byte characters count as characters.
	var ok activateReq
	if err := decodeBody(`{"reason":"`+strings.Repeat("é", 500)+`","new_customer":{"name":"`+strings.Repeat("x", 120)+`"}}`, &ok); err != nil {
		t.Fatalf("values at the limit must pass: %v", err)
	}

	var u userWrite
	if err := decodeBody(`{"password":"`+strings.Repeat("p", 1025)+`"}`, &u); err == nil {
		t.Fatal("a password over 1024 characters must be refused")
	}
	long := strings.Repeat("d", 121)
	if err := decodeBody(`{"display_name":"`+long+`"}`, &u); err == nil {
		t.Fatal("a *string display_name over 120 must be refused")
	}
}

// An optional body (archive, delete) still refuses an over-long reason.
func TestLimitExceededOnOptionalBody(t *testing.T) {
	var in deleteConfirm
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/x", bytes.NewReader([]byte(`{"reason":"`+strings.Repeat("r", 600)+`"}`)))
	if !limitExceeded(rec, req, DecodeJSON(req, &in)) || rec.Code != http.StatusBadRequest {
		t.Fatalf("over-long reason on an optional body: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodDelete, "/x", nil)
	if limitExceeded(rec, req, DecodeJSON(req, &in)) {
		t.Fatal("an empty optional body is not a limit error")
	}
}
