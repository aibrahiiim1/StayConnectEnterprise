// Package pmsloader reads pms_providers rows from the control-plane DB and
// turns them into configured, registered (and possibly started) provider
// instances. scd calls Load on boot; Phase 5 will add a NATS-driven Reload
// hook so admin UI changes apply without an scd restart.
package pmsloader

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/pms"
)

// Load fetches every enabled pms_providers row reachable from this
// appliance (tenant-wide rows + site-scoped rows for siteID), resolves
// name collisions by preferring site-scoped rows, constructs the matching
// provider implementation, applies its config, and registers it.
//
// Pass siteID="" on dev/single-site deployments: the loader then treats
// all rows as tenant-wide (site-scoped rows are ignored — they'd be
// unreachable without a site to anchor them).
//
// Returns the populated registry and the list of constructed providers so
// callers can run dev-only side effects (e.g. seeding the Stub).
func Load(ctx context.Context, db *pgxpool.Pool, tenantID, siteID string) (*pms.Registry, []pms.Provider, error) {
	// Fetch BOTH tenant-wide and site-scoped rows; we'll resolve collisions
	// in Go (prefer site-scoped). Deterministic ORDER BY so the "first
	// seen wins"/"site wins" decision is repeatable for tests.
	var siteArg any
	if siteID != "" {
		siteArg = siteID
	}
	rows, err := db.Query(ctx, `
        SELECT name, kind,
               COALESCE(host, ''), COALESCE(port, 0),
               use_tls, COALESCE(auth_key, ''),
               COALESCE(base_url, ''), COALESCE(api_key, ''), COALESCE(property_id, ''),
               extra::text, field_map::text, normalization::text, stay_window::text,
               site_id IS NOT NULL AS is_site_scoped
          FROM pms_providers
         WHERE tenant_id = $1
           AND enabled = true
           AND (site_id IS NULL OR site_id = $2)
         ORDER BY name, (site_id IS NOT NULL) DESC
    `, tenantID, siteArg)
	if err != nil {
		return nil, nil, fmt.Errorf("pmsloader: query: %w", err)
	}
	defer rows.Close()

	reg := pms.NewRegistry()
	var built []pms.Provider
	seen := map[string]bool{} // tracks names already registered (site-scoped won via ORDER BY)

	for rows.Next() {
		var (
			name, kind, host, authKey, baseURL, apiKey, propertyID string
			extraJSON, fieldMapJSON, normJSON, stayJSON            string
			port                                                   int
			useTLS, siteScoped                                     bool
		)
		if err := rows.Scan(&name, &kind, &host, &port, &useTLS, &authKey,
			&baseURL, &apiKey, &propertyID,
			&extraJSON, &fieldMapJSON, &normJSON, &stayJSON, &siteScoped); err != nil {
			return nil, nil, fmt.Errorf("pmsloader: scan: %w", err)
		}
		if seen[name] {
			// A site-scoped row already won for this name; skip the
			// tenant-wide duplicate.
			slog.Info("pmsloader: overridden by site-scoped row", "name", name)
			continue
		}

		cfg := pms.ProviderConfig{
			Name: name,
			Kind: kind,
			Connection: pms.ConnectionConfig{
				Host:       host,
				Port:       port,
				UseTLS:     useTLS,
				AuthKey:    authKey,
				BaseURL:    baseURL,
				APIKey:     apiKey,
				PropertyID: propertyID,
				Extra:      decodeJSONMap(extraJSON),
			},
			FieldMap:      decodeFieldMap(fieldMapJSON),
			Normalization: decodeNormalization(normJSON),
			StayPolicy:    decodeStayPolicy(stayJSON),
		}

		prov, err := buildByKind(kind, name)
		if err != nil {
			slog.Warn("pmsloader: skipping unknown kind", "name", name, "kind", kind, "err", err)
			continue
		}
		if err := prov.Configure(cfg); err != nil {
			slog.Warn("pmsloader: configure failed", "name", name, "kind", kind, "err", err)
			continue
		}
		reg.Register(prov)
		built = append(built, prov)
		seen[name] = true
		scope := "tenant"
		if siteScoped {
			scope = "site"
		}
		slog.Info("pmsloader: registered", "name", name, "kind", kind, "scope", scope)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("pmsloader: rows: %w", err)
	}
	return reg, built, nil
}

// StartAll calls Start on every provider that implements pms.Starter.
// scd typically calls this immediately after Load.
func StartAll(ctx context.Context, providers []pms.Provider) {
	for _, p := range providers {
		if s, ok := p.(pms.Starter); ok {
			s.Start(ctx)
		}
	}
}

// StopAll calls Stop on every provider that implements pms.Stopper. Used
// during live reload to tear down the previous generation cleanly.
func StopAll(providers []pms.Provider) {
	for _, p := range providers {
		if s, ok := p.(pms.Stopper); ok {
			s.Stop()
		}
	}
}

// ---- internals ------------------------------------------------------------

func buildByKind(kind, name string) (pms.Provider, error) {
	switch kind {
	case "stub":
		return pms.NewStub(name), nil
	case "protel-fias", "opera-fias", "fidelio-fias":
		// All three speak FIAS. For now they share the same client; in 4.5.5b
		// kind-specific quirks (e.g. Suite8 longer RN) tune the field map.
		return pms.NewProtelFIAS(name), nil
	case "mews":
		return pms.NewMews(name), nil
	case "apaleo":
		return pms.NewApaleo(name), nil
	}
	return nil, fmt.Errorf("unsupported kind %q", kind)
}

func decodeJSONMap(raw string) map[string]any {
	if raw == "" || raw == "{}" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	return m
}

func decodeFieldMap(raw string) pms.FieldMap {
	if raw == "" || raw == "{}" {
		return nil
	}
	var m pms.FieldMap
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil
	}
	return m
}

func decodeNormalization(raw string) pms.Normalization {
	var n pms.Normalization
	if raw == "" || raw == "{}" {
		return n
	}
	_ = json.Unmarshal([]byte(raw), &n)
	return n
}

func decodeStayPolicy(raw string) pms.StayPolicy {
	var s pms.StayPolicy
	if raw == "" || raw == "{}" {
		return s
	}
	_ = json.Unmarshal([]byte(raw), &s)
	return s
}
