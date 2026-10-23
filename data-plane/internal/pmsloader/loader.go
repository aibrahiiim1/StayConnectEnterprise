// Package pmsloader turns pms_providers rows into registered provider instances; scd reloads them on boot,
// on /v1/admin/pms/reload and periodically.
package pmsloader

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/pms"
)

// Load prefers site-scoped rows on a name collision. With siteID="" site-scoped rows are ignored.
func Load(ctx context.Context, db *pgxpool.Pool, tenantID, siteID string) (*pms.Registry, []pms.Provider, error) {
	// The ORDER BY makes "site-scoped first, first seen wins" deterministic.
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
	seen := map[string]bool{}

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
			// A retired socket-owning kind is logged at ERROR (an enabled legacy FIAS row is trying to create a
			// second PMS owner); the row is skipped and the rest load.
			if errors.Is(err, ErrLegacySocketOwnerRetired) {
				slog.Error("pmsloader: REFUSED an enabled legacy FIAS provider row; pmsd owns the PMS socket",
					"name", name, "kind", kind)
				continue
			}
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

// StartAll starts every pms.Starter except a FIAS socket owner. This is the second guard of the single-owner
// rule (buildByKind is the first): it stops a FIAS owner that reached this slice by any route, so scd can
// never hold a long-lived PMS socket. It is scoped to *pms.ProtelFIAS by type; Mews and Apaleo are HTTP
// pollers that contend for no socket and must keep starting. A refusal is logged loudly since the invariant
// was already broken upstream.
func StartAll(ctx context.Context, providers []pms.Provider) {
	for _, p := range providers {
		if _, isFIAS := p.(*pms.ProtelFIAS); isFIAS {
			slog.Error("pmsloader: refusing to start a legacy FIAS socket owner in scd; pmsd is the sole "+
				"PMS Interface socket owner", "provider", fmt.Sprintf("%T", p))
			continue
		}
		if s, ok := p.(pms.Starter); ok {
			s.Start(ctx)
		}
	}
}

func StopAll(providers []pms.Provider) {
	for _, p := range providers {
		if s, ok := p.(pms.Stopper); ok {
			s.Stop()
		}
	}
}

// socketOwningLegacyKinds are the legacy kinds that open a long-lived PMS socket (all built by pms.NewProtelFIAS).
var socketOwningLegacyKinds = map[string]bool{
	"protel-fias":  true,
	"opera-fias":   true,
	"fidelio-fias": true,
}

// ErrLegacySocketOwnerRetired: pmsd is the sole owner of a PMS Interface socket, and that must hold regardless
// of a database row. An enabled legacy FIAS row would otherwise make scd a permanent second owner at its next
// restart (StartAll gets a process-lifetime context), so the kind is refused at construction, covering
// startup and all reload paths. Load skips the row and carries on. Rolling back to the legacy connector
// requires deploying a build without this guard, never an UPDATE.
var ErrLegacySocketOwnerRetired = errors.New(
	"pmsloader: legacy FIAS provider kinds are retired in this build — pmsd is the sole PMS Interface " +
		"socket owner; configure the PMS through a PMS Interface instead")

func buildByKind(kind, name string) (pms.Provider, error) {
	// Checked before the switch so re-adding a FIAS case cannot silently re-open the path.
	if socketOwningLegacyKinds[kind] {
		return nil, fmt.Errorf("%w (kind %q, provider %q)", ErrLegacySocketOwnerRetired, kind, name)
	}
	switch kind {
	case "stub":
		return pms.NewStub(name), nil
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
