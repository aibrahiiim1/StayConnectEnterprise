package whatsapp

// Twilio Programmable Messaging with a WhatsApp sender.
//
// Endpoint: POST https://api.twilio.com/2010-04-01/Accounts/{AccountSID}/Messages.json
// Auth:     HTTP Basic, username = Account SID, password = Auth Token
// Body:     form: From=whatsapp:+E164, To=whatsapp:+E164, ContentSid = the approved authentication template
//           (Content API), ContentVariables = {"1": code}. There is no Body field: the code is a template
//           variable, never free text.
//
// 201 Created on acceptance. An error carries {"code","message","more_info","status"}.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// TwilioDefaultBaseURL is Twilio's REST root.
const TwilioDefaultBaseURL = "https://api.twilio.com"

// Twilio sends through Twilio's WhatsApp sender.
type Twilio struct {
	AccountSID string
	AuthToken  string // secret; never logged or returned
	FromNumber string // the WhatsApp sender, E.164
	ContentSID string // approved authentication template (HX...)
	BaseURL    string // override for tests; "" = TwilioDefaultBaseURL
	HTTPClient *http.Client
}

// NewTwilio builds a Twilio WhatsApp adapter. Every field is required; the sender must be E.164.
func NewTwilio(accountSID, authToken, fromNumber, contentSID string) (*Twilio, error) {
	var missing []string
	if strings.TrimSpace(accountSID) == "" {
		missing = append(missing, "account SID")
	}
	if strings.TrimSpace(authToken) == "" {
		missing = append(missing, "auth token")
	}
	if strings.TrimSpace(fromNumber) == "" {
		missing = append(missing, "WhatsApp sender number")
	}
	if strings.TrimSpace(contentSID) == "" {
		missing = append(missing, "content SID")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: twilio needs %s", ErrConfig, strings.Join(missing, ", "))
	}
	from := strings.TrimPrefix(strings.TrimSpace(fromNumber), "whatsapp:")
	if !validE164(from) {
		return nil, fmt.Errorf("%w: twilio WhatsApp sender must be an E.164 number", ErrConfig)
	}
	return &Twilio{
		AccountSID: strings.TrimSpace(accountSID),
		AuthToken:  authToken,
		FromNumber: from,
		ContentSID: strings.TrimSpace(contentSID),
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
	}, nil
}

type twilioErrorBody struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Send delivers the code as the approved content template.
func (t *Twilio) Send(ctx context.Context, msg Message) error {
	if err := checkMessage(msg); err != nil {
		return err
	}
	vars, err := json.Marshal(map[string]string{"1": msg.Code})
	if err != nil {
		return err
	}
	form := url.Values{}
	form.Set("From", "whatsapp:"+t.FromNumber)
	form.Set("To", "whatsapp:"+msg.To)
	form.Set("ContentSid", t.ContentSID)
	form.Set("ContentVariables", string(vars))

	base := t.BaseURL
	if base == "" {
		base = TwilioDefaultBaseURL
	}
	endpoint := strings.TrimRight(base, "/") + "/2010-04-01/Accounts/" + url.PathEscape(t.AccountSID) + "/Messages.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.SetBasicAuth(t.AccountSID, t.AuthToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := t.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("whatsapp send failed", "provider", "twilio", "to", Suffix(msg.To), "err", "transport")
		return &ProviderError{Provider: "twilio", Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
		slog.Info("whatsapp sent", "provider", "twilio", "to", Suffix(msg.To), "status", resp.StatusCode)
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	pe := &ProviderError{Provider: "twilio", Status: resp.StatusCode}
	var e twilioErrorBody
	if json.Unmarshal(b, &e) == nil && e.Message != "" {
		pe.Message = e.Message
		if e.Code != 0 {
			pe.Code = strconv.Itoa(e.Code)
		}
	} else {
		pe.Message = http.StatusText(resp.StatusCode)
	}
	slog.Warn("whatsapp send refused", "provider", "twilio", "to", Suffix(msg.To), "status", resp.StatusCode, "code", pe.Code)
	return pe
}
