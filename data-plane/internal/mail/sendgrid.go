package mail

// SendGrid v3 Mail API — REST-only, no SDK needed.
//
// Endpoint: POST https://api.sendgrid.com/v3/mail/send
// Auth:     Authorization: Bearer <api_key>
// Body:     application/json (per https://docs.sendgrid.com/api-reference/mail-send/mail-send)
//
// Successful sends respond with 202 Accepted and an empty body. Errors
// arrive as 4xx/5xx with a {errors:[{message,field}]} JSON body — we
// surface the first message verbatim so operators don't need to parse.
//
// Tracking is switched off on every message (GDPR plan F4): otherwise the SendGrid account's defaults decide,
// and click tracking rewrites the verification and reset links while open tracking reports when a guest read
// the mail.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const sendGridEndpoint = "https://api.sendgrid.com/v3/mail/send"

type SendGrid struct {
	APIKey      string
	FromAddress string
	FromName    string
	Endpoint    string       // override for tests; empty = production URL
	HTTPClient  *http.Client // optional override; defaults to a 10s-timeout client
}

// NewSendGrid wires a sender. apiKey, fromAddress are required; fromName
// is optional (SendGrid renders just the address otherwise).
func NewSendGrid(apiKey, fromAddress, fromName string) (*SendGrid, error) {
	if apiKey == "" || fromAddress == "" {
		return nil, errors.New("sendgrid: api_key and from_address are required")
	}
	return &SendGrid{
		APIKey:      apiKey,
		FromAddress: fromAddress,
		FromName:    fromName,
		HTTPClient:  &http.Client{Timeout: 10 * time.Second},
	}, nil
}

func (s *SendGrid) Send(ctx context.Context, m Message) error {
	if m.To == "" {
		return errors.New("sendgrid: empty recipient")
	}
	body := sgRequest{
		Personalizations: []sgPerson{{To: []sgEmail{{Email: m.To}}}},
		From:             sgEmail{Email: s.FromAddress, Name: s.FromName},
		Subject:          m.Subject,
		TrackingSettings: noTracking,
	}
	body.Content = append(body.Content, sgContent{Type: "text/plain", Value: m.Text})
	if m.HTML != "" {
		body.Content = append(body.Content, sgContent{Type: "text/html", Value: m.HTML})
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("sendgrid: marshal: %w", err)
	}
	endpoint := s.Endpoint
	if endpoint == "" {
		endpoint = sendGridEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("sendgrid: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted {
		return nil
	}
	// Best-effort error extraction; cap body read to avoid OOM on huge
	// responses (shouldn't happen but defensive).
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var e sgError
	if json.Unmarshal(b, &e) == nil && len(e.Errors) > 0 {
		return fmt.Errorf("sendgrid status=%d: %s", resp.StatusCode, e.Errors[0].Message)
	}
	return fmt.Errorf("sendgrid status=%d body=%s", resp.StatusCode, string(b))
}

type sgRequest struct {
	Personalizations []sgPerson  `json:"personalizations"`
	From             sgEmail     `json:"from"`
	Subject          string      `json:"subject"`
	Content          []sgContent `json:"content"`
	TrackingSettings sgTracking  `json:"tracking_settings"`
}

// sgTracking is the v3 tracking_settings block. The flags carry no omitempty: an explicit false is what
// overrides an account that has tracking switched on.
type sgTracking struct {
	ClickTracking        sgClickTracking `json:"click_tracking"`
	OpenTracking         sgEnable        `json:"open_tracking"`
	SubscriptionTracking sgEnable        `json:"subscription_tracking"`
	Ganalytics           sgEnable        `json:"ganalytics"`
}
type sgClickTracking struct {
	Enable     bool `json:"enable"`
	EnableText bool `json:"enable_text"`
}
type sgEnable struct {
	Enable bool `json:"enable"`
}

var noTracking = sgTracking{}

type sgPerson struct {
	To []sgEmail `json:"to"`
}
type sgEmail struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}
type sgContent struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}
type sgError struct {
	Errors []struct {
		Message string `json:"message"`
		Field   string `json:"field"`
	} `json:"errors"`
}
