package whatsapp

// WhatsApp Business Cloud API (Meta).
//
// Endpoint: POST https://graph.facebook.com/v19.0/{phone_number_id}/messages
// Auth:     Authorization: Bearer {access token}
// Body:     JSON, type=template, naming an approved AUTHENTICATION template. The code fills the body parameter
//           and the template's copy-code button parameter (button sub_type "url", index "0").
//
// 200 OK carries {"messages":[{"id":...}]}. An error carries {"error":{"message","type","code",...}}.

import (
	"bytes"
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

// MetaDefaultBaseURL is the Graph API root including its version.
const MetaDefaultBaseURL = "https://graph.facebook.com/v19.0"

// Meta sends through the WhatsApp Business Cloud API.
type Meta struct {
	AccessToken   string // secret; never logged or returned
	PhoneNumberID string
	TemplateName  string
	Language      string // template language code; "" = message locale, then "en"
	BaseURL       string // override for tests; "" = MetaDefaultBaseURL
	HTTPClient    *http.Client
}

// NewMeta builds a Meta adapter. access token, phone number id and template name are required.
func NewMeta(accessToken, phoneNumberID, templateName, language string) (*Meta, error) {
	var missing []string
	if strings.TrimSpace(accessToken) == "" {
		missing = append(missing, "access token")
	}
	if strings.TrimSpace(phoneNumberID) == "" {
		missing = append(missing, "phone number ID")
	}
	if strings.TrimSpace(templateName) == "" {
		missing = append(missing, "template name")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: meta needs %s", ErrConfig, strings.Join(missing, ", "))
	}
	return &Meta{
		AccessToken:   accessToken,
		PhoneNumberID: strings.TrimSpace(phoneNumberID),
		TemplateName:  strings.TrimSpace(templateName),
		Language:      strings.TrimSpace(language),
		HTTPClient:    &http.Client{Timeout: 10 * time.Second},
	}, nil
}

type metaParam struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type metaComponent struct {
	Type       string      `json:"type"`
	SubType    string      `json:"sub_type,omitempty"`
	Index      string      `json:"index,omitempty"`
	Parameters []metaParam `json:"parameters"`
}

type metaTemplate struct {
	Name       string            `json:"name"`
	Language   map[string]string `json:"language"`
	Components []metaComponent   `json:"components"`
}

type metaRequest struct {
	MessagingProduct string       `json:"messaging_product"`
	RecipientType    string       `json:"recipient_type"`
	To               string       `json:"to"`
	Type             string       `json:"type"`
	Template         metaTemplate `json:"template"`
}

type metaErrorBody struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    int    `json:"code"`
	} `json:"error"`
}

func (m *Meta) language(msg Message) string {
	if m.Language != "" {
		return m.Language
	}
	if l := strings.TrimSpace(msg.Locale); l != "" {
		return l
	}
	return "en"
}

// Send delivers the code as the approved authentication template.
func (m *Meta) Send(ctx context.Context, msg Message) error {
	if err := checkMessage(msg); err != nil {
		return err
	}
	body := metaRequest{
		MessagingProduct: "whatsapp",
		RecipientType:    "individual",
		To:               strings.TrimPrefix(msg.To, "+"),
		Type:             "template",
		Template: metaTemplate{
			Name:     m.TemplateName,
			Language: map[string]string{"code": m.language(msg)},
			Components: []metaComponent{
				{Type: "body", Parameters: []metaParam{{Type: "text", Text: msg.Code}}},
				{Type: "button", SubType: "url", Index: "0", Parameters: []metaParam{{Type: "text", Text: msg.Code}}},
			},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return err
	}
	base := m.BaseURL
	if base == "" {
		base = MetaDefaultBaseURL
	}
	endpoint := strings.TrimRight(base, "/") + "/" + url.PathEscape(m.PhoneNumberID) + "/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	client := m.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("whatsapp send failed", "provider", "meta", "to", Suffix(msg.To), "err", "transport")
		return &ProviderError{Provider: "meta", Err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		slog.Info("whatsapp sent", "provider", "meta", "to", Suffix(msg.To), "status", resp.StatusCode)
		return nil
	}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	pe := &ProviderError{Provider: "meta", Status: resp.StatusCode}
	var e metaErrorBody
	if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
		pe.Message = e.Error.Message
		if e.Error.Code != 0 {
			pe.Code = strconv.Itoa(e.Error.Code)
		}
	} else {
		pe.Message = http.StatusText(resp.StatusCode)
	}
	slog.Warn("whatsapp send refused", "provider", "meta", "to", Suffix(msg.To), "status", resp.StatusCode, "code", pe.Code)
	return pe
}
