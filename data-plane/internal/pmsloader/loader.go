// Package pmsloader reads pms_providers rows from the control-plane DB and
// turns them into configured, registered (and possibly started) provider
// instances. scd calls Load on boot; Phase 5 will add a NATS-driven Reload
// hook so admin UI changes apply without an scd restart.
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
			// A retired socket-owning kind is a DIFFERENT event from an unrecognised one, and it is the one
			// somebody needs to act on: an enabled legacy FIAS row means a database toggle is trying to
			// create a second PMS owner. Logged at ERROR with the reason, while the row is skipped and the
			// remaining providers load normally.
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

// StartAll calls Start on every provider that implements pms.Starter, EXCEPT a FIAS socket owner.
//
// THE SECOND HALF OF THE SINGLE-OWNER GUARANTEE, and it is here rather than only in buildByKind because the
// two chokepoints fail differently. buildByKind stops the currently-known FIAS kinds from being built; this
// stops a FIAS socket owner from being started at all, including one that reached this slice by some future
// route. Together they mean scd cannot hold a long-lived PMS socket regardless of what the database says or
// which kinds the construction switch grows.
//
// IT IS SCOPED TO THE SOCKET OWNER, NOT TO EVERYTHING STARTABLE. Mews and Apaleo also implement Starter, and
// their loops are HTTP pollers — refreshSpaces / refreshUnits over REST. They hold no single-client socket
// and cannot contend with pmsd for one, so refusing them would disable working cloud-PMS providers for no
// safety gain. *pms.ProtelFIAS is the type that dials and holds the FIAS connection, and it backs all three
// FIAS kinds, so naming the type is what makes this a structural check rather than a string comparison.
//
// The refusal is loud: a socket owner reaching this point means the invariant was already broken upstream,
// and that belongs in the log rather than being silently skipped.
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

// socketOwningLegacyKinds are the legacy provider kinds that open a LONG-LIVED PMS SOCKET.
//
// All three are built by pms.NewProtelFIAS, which is the only Provider in this package implementing
// pms.Starter — so this set is exactly the set of legacy kinds that can become a PMS connection owner.
var socketOwningLegacyKinds = map[string]bool{
	"protel-fias":  true,
	"opera-fias":   true,
	"fidelio-fias": true,
}

// ErrLegacySocketOwnerRetired is returned for any legacy kind that would open a PMS socket.
//
// pmsd IS THE SOLE OWNER OF A PMS INTERFACE SOCKET. That is an architectural invariant, not a deployment
// preference, and it has to hold against the state of a database row rather than because of it.
//
// The legacy path could still reach a live socket. pmsloader.Load filtered on `enabled = true` and nothing
// else, buildByKind happily constructed a FIAS client, and scd's STARTUP called
// pmsloader.StartAll(rootCtx, ...) — a process-lifetime context. The three reload paths each pass a context
// that dies within 30 seconds, so toggling the row at runtime looked harmless; the next scd restart or
// appliance reboot was what turned it into a permanent second owner, dialling the same listener pmsd owns
// and competing for a single-client slot. A disabled row and a removed admin page are procedural
// protections, and procedure is not what should stand between a database UPDATE and two PMS owners.
//
// So the kind is refused HERE, at construction. Nothing downstream can start what was never built, which
// makes one guard cover process startup and all three reload paths at once. Load treats a build error as
// "skip this row and carry on", so a retained enabled row degrades to a logged refusal rather than
// preventing the other providers from loading.
//
// This deliberately does NOT repair the old connector's lifecycle or context handling. The goal is to make
// that path unreachable from the current runtime, not to make it work better.
//
// ROLLBACK IS A SOFTWARE DECISION. public.pms_providers is retained for history and for a possible rollback
// to the legacy connector, and that rollback now requires deploying a build without this guard — which is a
// reviewed, deliberate act. It can no longer be performed accidentally with an UPDATE statement.
var ErrLegacySocketOwnerRetired = errors.New(
	"pmsloader: legacy FIAS provider kinds are retired in this build — pmsd is the sole PMS Interface " +
		"socket owner; configure the PMS through a PMS Interface instead")

func buildByKind(kind, name string) (pms.Provider, error) {
	// Checked BEFORE the switch so that adding a FIAS kind back to the switch cannot silently re-open the
	// path: the refusal is keyed to the kind, not to the branch that would have built it.
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
