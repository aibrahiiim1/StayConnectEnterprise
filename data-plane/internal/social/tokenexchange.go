package social

// Shared authorization-code token exchange. The form carries the client secret, so neither it nor the
// request is ever put into an error or log; only the IdP's own error code and description are surfaced.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type tokenResp struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

func postTokenForm(ctx context.Context, hc *http.Client, provider, tokenURL string, form url.Values) (*tokenResp, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s: token exchange: %w", provider, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode != http.StatusOK {
		// Either RFC 6749 {error, error_description} or Facebook's nested {error:{message,type,code}}.
		var flat struct {
			Error       any    `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(b, &flat) == nil {
			switch e := flat.Error.(type) {
			case string:
				if e != "" {
					return nil, fmt.Errorf("%s token: %s — %s", provider, e, flat.Description)
				}
			case map[string]any:
				msg, _ := e["message"].(string)
				typ, _ := e["type"].(string)
				return nil, fmt.Errorf("%s token: %s — %s", provider, typ, msg)
			}
		}
		return nil, fmt.Errorf("%s token: status=%d", provider, resp.StatusCode)
	}
	var tk tokenResp
	if err := json.Unmarshal(b, &tk); err != nil {
		return nil, fmt.Errorf("%s token decode: %w", provider, err)
	}
	return &tk, nil
}

// looksLikeEmail is a deliberately loose shape check (e.g. preferred_username can be a phone number); it
// never decides whether an address is verified.
func looksLikeEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	if at <= 0 || at != strings.LastIndexByte(s, '@') || strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	dom := s[at+1:]
	return strings.Contains(dom, ".") && !strings.HasPrefix(dom, ".") && !strings.HasSuffix(dom, ".")
}
