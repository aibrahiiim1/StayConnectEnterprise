// Package socialloader builds a fresh social.Registry from a tenant's provider rows.
//
// Secrets come only from the sealed generation scd opens; the loader never reads
// social_oauth_providers.client_secret, so no plaintext column is ever used and a missing key never falls
// back to it. The development Stub is passed in as a base provider (never in a production build) and a real
// provider built from a row replaces it.
package socialloader

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/social"
)

// Extra is the non-secret provider configuration in social_oauth_providers.extra; other keys are ignored.
type Extra struct {
	Tenant string `json:"tenant,omitempty"`  // microsoft: common|organizations|consumers|<tenant id>|<domain>
	TeamID string `json:"team_id,omitempty"` // apple: 10-character Team ID
	KeyID  string `json:"key_id,omitempty"`  // apple: 10-character Key ID of the .p8 key
}

// Build never puts the secret in the returned error.
func Build(provider, clientID, clientSecret, scopes string, extra Extra) (social.Provider, error) {
	switch provider {
	case "google":
		return social.NewGoogle(clientID, clientSecret, scopes)
	case "microsoft":
		return social.NewMicrosoft(clientID, clientSecret, scopes, extra.Tenant)
	case "apple":
		return social.NewApple(clientID, clientSecret, extra.TeamID, extra.KeyID, scopes)
	case "facebook":
		return social.NewFacebook(clientID, clientSecret, scopes)
	}
	return nil, social.ErrUnknownProvider
}

type Row struct {
	ID            string
	Provider      string
	ClientID      string
	Scopes        string
	ExtraJSON     string
	CallbackHosts []string
}

// SecretOpener returns a row's opened sealed secret; ok is false when there is none or it cannot be opened.
type SecretOpener func(ctx context.Context, providerID string) (secret string, ok bool)

type Loaded struct {
	Registry *social.Registry
	// CallbackHosts are the lower-case hosts the hotel registered per provider, compared against the canonical hostname.
	CallbackHosts map[string][]string
	// Real names the providers built from a row rather than a base stub.
	Real map[string]bool
}

// ReadRows selects no secret column.
func ReadRows(ctx context.Context, db *pgxpool.Pool, tenantID string) ([]Row, error) {
	rows, err := db.Query(ctx, `
        SELECT id::text, provider, COALESCE(client_id,''), COALESCE(scopes,''),
               COALESCE(extra,'{}'::jsonb)::text, COALESCE(callback_hosts, '{}'::text[])
          FROM social_oauth_providers
         WHERE tenant_id = $1 AND enabled = true
    `, tenantID)
	if err != nil {
		return nil, fmt.Errorf("socialloader: query: %w", err)
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.ID, &r.Provider, &r.ClientID, &r.Scopes, &r.ExtraJSON, &r.CallbackHosts); err != nil {
			return nil, fmt.Errorf("socialloader: scan: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Assemble registers base providers, then one real provider per row whose secret opens. A row that cannot
// be built is logged by provider name only, never the secret, and leaves any base entry in place.
// open == nil means the sealing key is absent: no row is built.
func Assemble(ctx context.Context, rows []Row, open SecretOpener, base ...social.Provider) *Loaded {
	out := &Loaded{Registry: social.NewRegistry(), CallbackHosts: map[string][]string{}, Real: map[string]bool{}}
	for _, p := range base {
		out.Registry.Register(p)
	}
	for _, r := range rows {
		if open == nil {
			slog.Warn("socialloader: social sealing key missing (run keybootstrap at deploy); provider not built", "provider", r.Provider)
			continue
		}
		var extra Extra
		if err := json.Unmarshal([]byte(r.ExtraJSON), &extra); err != nil {
			slog.Warn("socialloader: extra is not valid JSON; provider not built", "provider", r.Provider)
			continue
		}
		secret, ok := open(ctx, r.ID)
		if !ok {
			slog.Warn("socialloader: no sealed secret could be opened; provider not built", "provider", r.Provider)
			continue
		}
		p, err := Build(r.Provider, r.ClientID, secret, r.Scopes, extra)
		if err != nil {
			slog.Warn("socialloader: construct failed; provider not built", "provider", r.Provider, "err", err)
			continue
		}
		out.Registry.Register(p)
		out.Real[r.Provider] = true
		out.CallbackHosts[r.Provider] = NormaliseHosts(r.CallbackHosts)
		slog.Info("socialloader: registered", "provider", r.Provider, "kind", "real", "callback_hosts", len(r.CallbackHosts))
	}
	return out
}

func Load(ctx context.Context, db *pgxpool.Pool, tenantID string, open SecretOpener, base ...social.Provider) (*Loaded, error) {
	rows, err := ReadRows(ctx, db, tenantID)
	if err != nil {
		return Assemble(ctx, nil, open, base...), err
	}
	return Assemble(ctx, rows, open, base...), nil
}

// NormaliseHosts lower-cases and drops trailing dots and empties so "WiFi.Hotel.example." matches the canonical hostname.
func NormaliseHosts(in []string) []string {
	out := make([]string, 0, len(in))
	for _, h := range in {
		h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
		if h != "" {
			out = append(out, h)
		}
	}
	return out
}
