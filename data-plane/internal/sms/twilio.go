package sms

// Twilio Messages REST API: HTTP Basic (AccountSID:AuthToken), form-encoded To/From/Body. Error bodies
// ({message, code, more_info}) are surfaced verbatim.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/phone"
)

type Twilio struct {
	AccountSID string
	AuthToken  string
	FromNumber string
	BaseURL    string // override for tests; empty = "https://api.twilio.com"
	HTTPClient *http.Client
}

func NewTwilio(accountSID, authToken, fromNumber string) (*Twilio, error) {
	if accountSID == "" || authToken == "" || fromNumber == "" {
		return nil, errors.New("twilio: account_sid, auth_token, and from_number are required")
	}
	return &Twilio{
		AccountSID: accountSID,
		AuthToken:  authToken,
		FromNumber: fromNumber,
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (t *Twilio) Send(ctx context.Context, m Message) error {
	if m.To == "" {
		return errors.New("twilio: empty recipient")
	}
	// Checked here as WhatsApp does, so a malformed number costs no provider call (audit P5 L9); sent as given.
	if _, err := phone.Normalize(m.To); err != nil {
		return errors.New("twilio: invalid recipient")
	}
	form := url.Values{}
	form.Set("To", m.To)
	form.Set("From", t.FromNumber)
	form.Set("Body", m.Text)

	base := t.BaseURL
	if base == "" {
		base = "https://api.twilio.com"
	}
	endpoint := base + "/2010-04-01/Accounts/" + url.PathEscape(t.AccountSID) + "/Messages.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.SetBasicAuth(t.AccountSID, t.AuthToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := t.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("twilio: %w", err)
	}
	defer resp.Body.Close()
	// 201 on accept; 200 on some legacy paths.
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e twilioError
	if json.Unmarshal(b, &e) == nil && e.Message != "" {
		return fmt.Errorf("twilio status=%d code=%d: %s", resp.StatusCode, e.Code, e.Message)
	}
	return fmt.Errorf("twilio status=%d body=%s", resp.StatusCode, string(b))
}

type twilioError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
