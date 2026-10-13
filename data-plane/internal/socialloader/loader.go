// Package socialloader resolves a tenant's social OAuth providers from the database and builds the matching
// implementation for each into a fresh social.Registry.
//
// SECRETS COME FROM ONE PLACE ONLY: the sealed generation scd opens (contract
// docs/architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md §5). The loader never reads
// social_oauth_providers.client_secret. scd seals any plaintext still in that column before it loads
// (cmd/scd/social_secrets.go), so a provider is built from a secret that has been through the sealing key or
// not at all: there is no path on which a plaintext column is used once sealing exists, and none on which a
// missing key quietly falls back to it.
//
// The development Stub (social.Stub) is passed in by the caller as a base provider; a real provider built from
// a row replaces it. scd passes no stub in a production build.
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

// Extra is the non-secret, provider-specific configuration kept in
// social_oauth_providers.extra (jsonb). Only the keys a provider reads are
// defined; anything else in the column is ignored.
type Extra struct {
	Tenant string `json:"tenant,omitempty"`  // microsoft: common|organizations|consumers|<tenant id>|<domain>
	TeamID string `json:"team_id,omitempty"` // apple: 10-character Team ID
	KeyID  string `json:"key_id,omitempty"`  // apple: 10-character Key ID of the .p8 key
}

// Build constructs the real provider for one configuration row. The secret is
// passed in and never appears in the returned error.
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

// Row is one enabled provider configuration, without its secret.
type Row struct {
	ID            string
	Provider      string
	ClientID      string
	Scopes        string
	ExtraJSON     string
	CallbackHosts []string
}

// SecretOpener returns the current sealed secret of one provider row, opened. ok is false when the row has no
// sealed generation or it cannot be opened (no key, a replaced key, a tampered row).
type SecretOpener func(ctx context.Context, providerID string) (secret string, ok bool)

// Loaded is the result of one load: the registry the handlers use, and for every provider built from a row the
// callback hosts the hotel registered in its own app (lower-case, as compared against the canonical hostname).
type Loaded struct {
	Registry      *social.Registry
	CallbackHosts map[string][]string
	// Real names the providers built from a configuration row (as opposed to a base stub).
	Real map[string]bool
}

// ReadRows reads the enabled provider rows of a tenant. It selects no secret column.
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

// Assemble builds a fresh registry: the base providers first, then one real provider per row whose secret
// opens. A row that cannot be built (no opener, no sealed secret, missing credentials, an unreadable Apple key,
// invalid extra) is logged by provider name only -- never the secret -- and leaves the base entry, if any, in
// place. open == nil means the sealing key is absent: no row is built.
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

// Load is ReadRows + Assemble.
func Load(ctx context.Context, db *pgxpool.Pool, tenantID string, open SecretOpener, base ...social.Provider) (*Loaded, error) {
	rows, err := ReadRows(ctx, db, tenantID)
	if err != nil {
		return Assemble(ctx, nil, open, base...), err
	}
	return Assemble(ctx, rows, open, base...), nil
}

// NormaliseHosts lower-cases callback hosts and drops a trailing dot and empties, so a host the operator typed
// as "WiFi.Hotel.example." compares equal to the canonical hostname.
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
