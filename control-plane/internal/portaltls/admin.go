package portaltls

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// Everything here is public material; the DNS token and ACME account key never appear in any answer.

type SettingsView struct {
	Settings
	DirectoryURL  string   `json:"directory_url"`
	TokenSet      bool     `json:"token_set"`
	DNSProvider   string   `json:"dns_provider"`
	Available     bool     `json:"available"`
	Unavailable   string   `json:"unavailable_reason,omitempty"`
	TestOverrides []string `json:"test_overrides"`
}

func (s *Service) View(ctx context.Context) (*SettingsView, error) {
	st, err := LoadSettings(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	ok, why := s.Availability(st)
	overrides := append([]string{}, s.Overrides...)
	return &SettingsView{Settings: st, DirectoryURL: s.Opts.directoryURL(st.ACMEDirectory), TokenSet: s.Secrets.TokenSet(),
		DNSProvider: s.Opts.DNSProvider, Available: ok, Unavailable: why, TestOverrides: overrides}, nil
}

func (s *Service) UpdateSettings(ctx context.Context, u SettingsUpdate, actor string) (Settings, error) {
	if err := u.Validate(); err != nil {
		return Settings{}, refuse(http.StatusBadRequest, "bad_request", "%s", err.Error())
	}
	before, err := LoadSettings(ctx, s.DB)
	if err != nil {
		return Settings{}, err
	}
	// The zone is fixed once it carries hostnames: the DNS token is scoped to one zone, so switching would
	// strand old names where Central can no longer update or delete them.
	if u.ZoneName != nil && NormalizeFQDN(*u.ZoneName) != before.ZoneName {
		var inUse int
		if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM portal_hostnames
		        WHERE released_at IS NULL OR dns_record_id IS NOT NULL`).Scan(&inUse); err != nil {
			return Settings{}, err
		}
		if inUse > 0 {
			return Settings{}, refuse(http.StatusConflict, "zone_in_use",
				"%d appliance hostname(s) live in %s; the zone cannot change while they exist", inUse, before.ZoneName)
		}
	}
	// CAA pins the production account; leaving production would make every order fail.
	if u.ACMEDirectory != nil && *u.ACMEDirectory != before.ACMEDirectory && before.CAAAppliedAt != nil &&
		before.ACMEDirectory == DirectoryProduction {
		return Settings{}, refuse(http.StatusConflict, "caa_pins_production",
			"CAA is applied and pins the production ACME account, so Central stays on production. Remove the zone's CAA records first if staging is really needed.")
	}
	after, err := SaveSettings(ctx, s.DB, u, actor)
	if err != nil {
		return Settings{}, err
	}
	// Certificates from the directory Central left (staging is trusted by no browser) become due now, so
	// appliances reorder on their next sync.
	if before.ACMEDirectory != after.ACMEDirectory {
		ct, err := s.DB.Exec(ctx, `UPDATE portal_certificate_orders SET renew_after=now()
		        WHERE status='ISSUED' AND revoked_at IS NULL AND revoke_requested_at IS NULL
		          AND COALESCE(acme_directory_url,'') <> $1`, s.Opts.directoryURL(after.ACMEDirectory))
		if err != nil {
			return Settings{}, err
		}
		AppendEvent(ctx, s.DB, "", "", EvRenewalForced, actor, map[string]any{
			"reason": "acme_directory changed", "from": before.ACMEDirectory, "to": after.ACMEDirectory,
			"certificates": ct.RowsAffected()})
	}
	changes := map[string]any{}
	if before.ACMEDirectory != after.ACMEDirectory {
		changes["acme_directory"] = map[string]string{"from": before.ACMEDirectory, "to": after.ACMEDirectory}
	}
	if before.ContactEmail != after.ContactEmail {
		changes["contact_email"] = map[string]string{"from": before.ContactEmail, "to": after.ContactEmail}
	}
	if before.ZoneName != after.ZoneName {
		changes["zone_name"] = map[string]string{"from": before.ZoneName, "to": after.ZoneName}
	}
	if before.WeeklyNewLimit != after.WeeklyNewLimit {
		changes["weekly_new_limit"] = map[string]int{"from": before.WeeklyNewLimit, "to": after.WeeklyNewLimit}
	}
	if len(changes) > 0 {
		AppendEvent(ctx, s.DB, "", "", EvSettingsChanged, actor, changes)
	}
	s.Nudge()
	return after, nil
}

// SetToken stores the DNS API token; the event records that it was set, never the value.
func (s *Service) SetToken(ctx context.Context, token, actor string) error {
	if err := s.Secrets.WriteToken(token); err != nil {
		return refuse(http.StatusBadRequest, "bad_request", "%s", err.Error())
	}
	// A new token may see another account's zone of the same name: re-prove before use.
	_, _ = s.DB.Exec(ctx, `UPDATE portal_tls_settings SET zone_id=NULL, updated_at=now(), updated_by=NULLIF($1,'') WHERE id`, actor)
	AppendEvent(ctx, s.DB, "", "", EvTokenSet, actor, nil)
	return nil
}

type TestResult struct {
	ZoneID      string   `json:"zone_id"`
	ZoneName    string   `json:"zone_name"`
	NameServers []string `json:"nameservers"`
	AccountURL  string   `json:"account_url"`
	Directory   string   `json:"directory"`
}

func (s *Service) TestAccess(ctx context.Context, actor string) (*TestResult, error) {
	st, err := LoadSettings(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	if st.ContactEmail == "" {
		return nil, refuse(http.StatusConflict, "not_configured", "set the ACME contact email first")
	}
	prov, err := s.NewProvider(st)
	if err != nil {
		return nil, refuse(http.StatusConflict, "not_configured", "%s", err.Error())
	}
	zi, err := prov.Verify(ctx)
	if err != nil {
		AppendEvent(ctx, s.DB, "", "", EvAccessTested, actor, map[string]any{"ok": false, "error": truncate(err.Error(), 500)})
		return nil, refuse(http.StatusBadGateway, "dns_access_failed", "DNS access failed: %s", err.Error())
	}
	if _, err := s.DB.Exec(ctx, `UPDATE portal_tls_settings SET zone_id=$1 WHERE id`, zi.ID); err != nil {
		return nil, err
	}
	st.ZoneID = zi.ID
	dir := s.Opts.directoryURL(st.ACMEDirectory)
	s.acmeMu.Lock()
	_, err = s.acmeClient(ctx, &st, dir)
	s.acmeMu.Unlock()
	if err != nil {
		AppendEvent(ctx, s.DB, "", "", EvAccessTested, actor, map[string]any{"ok": false, "zone_id": zi.ID, "error": truncate(err.Error(), 500)})
		return nil, refuse(http.StatusBadGateway, "acme_failed", "DNS access works, but the ACME account could not be registered: %s", err.Error())
	}
	res := &TestResult{ZoneID: zi.ID, ZoneName: zi.Name, NameServers: zi.NameServers, AccountURL: st.AccountURL, Directory: dir}
	if res.NameServers == nil {
		res.NameServers = []string{}
	}
	AppendEvent(ctx, s.DB, "", "", EvAccessTested, actor, map[string]any{"ok": true, "zone_id": zi.ID, "nameservers": zi.NameServers,
		"account_url": st.AccountURL, "directory": dir})
	s.Nudge()
	return res, nil
}

type CAAResult struct {
	Issue        string   `json:"issue"`
	IssueWild    string   `json:"issuewild"`
	OtherIssuers []string `json:"other_issuers"`
	AppliedAt    string   `json:"applied_at"`
}

// ApplyCAA pins issuance for the zone to Let's Encrypt and OneGate's ACME account and forbids wildcards.
func (s *Service) ApplyCAA(ctx context.Context, actor string) (*CAAResult, error) {
	st, err := LoadSettings(ctx, s.DB)
	if err != nil {
		return nil, err
	}
	// CAA pins one ACME account (accounturi); applied on staging it would block every production order.
	// Only the test directory override (Pebble) is exempt.
	if st.ACMEDirectory != DirectoryProduction && s.Opts.DirectoryURL == "" {
		return nil, refuse(http.StatusConflict, "caa_needs_production",
			"Switch the ACME directory to production first: CAA pins the current ACME account, and the staging account would block every production certificate.")
	}
	if ok, why := s.Availability(st); !ok {
		return nil, refuse(http.StatusConflict, "not_configured", "%s", why)
	}
	dir := s.Opts.directoryURL(st.ACMEDirectory)
	s.acmeMu.Lock()
	_, err = s.acmeClient(ctx, &st, dir)
	s.acmeMu.Unlock()
	if err != nil {
		return nil, refuse(http.StatusBadGateway, "acme_failed", "%s", err.Error())
	}
	prov, err := s.NewProvider(st)
	if err != nil {
		return nil, err
	}
	issue := "letsencrypt.org; accounturi=" + st.AccountURL
	others, err := prov.ApplyCAA(ctx, issue)
	if err != nil {
		return nil, refuse(http.StatusBadGateway, "dns_failed", "CAA could not be applied: %s", err.Error())
	}
	var at time.Time
	if err := s.DB.QueryRow(ctx, `UPDATE portal_tls_settings SET caa_applied_at=now() WHERE id RETURNING caa_applied_at`).Scan(&at); err != nil {
		return nil, err
	}
	if others == nil {
		others = []string{}
	}
	AppendEvent(ctx, s.DB, "", "", EvCAAApplied, actor, map[string]any{"issue": issue, "issuewild": ";", "other_issuers": others})
	return &CAAResult{Issue: issue, IssueWild: ";", OtherIssuers: others, AppliedAt: at.UTC().Format(time.RFC3339)}, nil
}

type FleetCert struct {
	Hostname     string     `json:"hostname"`
	Kind         string     `json:"kind"`
	OrderID      string     `json:"order_id"`
	Status       string     `json:"status"`
	Serial       *string    `json:"serial"`
	NotAfter     *time.Time `json:"not_after"`
	RenewAfter   *time.Time `json:"renew_after"`
	Revoked      bool       `json:"revoked"`
	DirectoryURL *string    `json:"acme_directory_url"`
	Directory    string     `json:"acme_directory"` // staging | production | other
	Error        *string    `json:"error"`
}

type FleetRow struct {
	ApplianceID  string      `json:"appliance_id"`
	Serial       string      `json:"serial"`
	CustomerName *string     `json:"customer_name"`
	SiteName     *string     `json:"site_name"`
	Hostname     string      `json:"hostname"`
	PortalIP     *string     `json:"portal_ip"`
	DNSState     string      `json:"dns_state"`
	DNSError     *string     `json:"dns_error"`
	Released     bool        `json:"released"`
	CustomDomain *string     `json:"custom_domain"`
	DomainStatus *string     `json:"custom_domain_status"`
	DomainError  *string     `json:"custom_domain_error"`
	Certificates []FleetCert `json:"certificates"`
	LastError    *string     `json:"last_error"`
}

func DirectoryName(u string) string {
	switch u {
	case LetsEncryptStagingURL:
		return DirectoryStaging
	case LetsEncryptProductionURL:
		return DirectoryProduction
	case "":
		return ""
	}
	return "other"
}

// Fleet lists appliances with a portal hostname and each hostname's newest certificate order, with its
// directory so staging and production certificates can be told apart.
func (s *Service) Fleet(ctx context.Context) ([]FleetRow, error) {
	rows, err := s.DB.Query(ctx, `
        SELECT h.appliance_id::text, a.serial, t.name, si.name,
               h.label, h.fqdn, COALESCE(host(h.portal_ip),''), COALESCE(h.dns_record_id,''), COALESCE(host(h.dns_synced_ip),''),
               h.dns_synced_at, COALESCE(h.dns_error,''), h.released_at, h.appliance_id::text,
               d.fqdn, d.status, d.last_error
          FROM portal_hostnames h
          JOIN appliances a ON a.id = h.appliance_id
          LEFT JOIN tenants t ON t.id = a.tenant_id
          LEFT JOIN sites si ON si.id = a.site_id
          LEFT JOIN portal_custom_domains d ON d.appliance_id = h.appliance_id AND d.status <> 'REMOVED'
         ORDER BY h.released_at IS NOT NULL, t.name NULLS LAST, a.serial`)
	if err != nil {
		return nil, err
	}
	var out []FleetRow
	for rows.Next() {
		var r FleetRow
		var appID string
		var h hostRow
		if err := rows.Scan(&appID, &r.Serial, &r.CustomerName, &r.SiteName,
			&h.Label, &h.FQDN, &h.PortalIP, &h.RecordID, &h.SyncedIP, &h.SyncedAt, &h.DNSError, &h.ReleasedAt, &h.ApplianceID,
			&r.CustomDomain, &r.DomainStatus, &r.DomainError); err != nil {
			rows.Close()
			return nil, err
		}
		r.ApplianceID, r.Hostname, r.DNSState, r.Released = appID, h.FQDN, h.DNSState(), h.ReleasedAt != nil
		if h.ReleasedAt != nil {
			r.DNSState = "RELEASED"
		}
		if h.PortalIP != "" {
			ip := h.PortalIP
			r.PortalIP = &ip
		}
		if h.DNSError != "" {
			e := h.DNSError
			r.DNSError, r.LastError = &e, &e
		}
		if r.DomainError != nil && r.LastError == nil {
			r.LastError = r.DomainError
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		certs, err := s.fleetCerts(ctx, out[i].ApplianceID)
		if err != nil {
			return nil, err
		}
		out[i].Certificates = certs
		for _, c := range certs {
			if c.Error != nil && out[i].LastError == nil {
				out[i].LastError = c.Error
			}
		}
	}
	if out == nil {
		out = []FleetRow{}
	}
	return out, nil
}

func (s *Service) fleetCerts(ctx context.Context, applianceID string) ([]FleetCert, error) {
	rows, err := s.DB.Query(ctx, `
        SELECT DISTINCT ON (hostname) hostname, kind, id::text, status, serial, not_after, renew_after,
               (revoke_requested_at IS NOT NULL OR revoked_at IS NOT NULL), acme_directory_url, error
          FROM portal_certificate_orders
         WHERE appliance_id=$1
         ORDER BY hostname, created_at DESC`, applianceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FleetCert{}
	for rows.Next() {
		var c FleetCert
		if err := rows.Scan(&c.Hostname, &c.Kind, &c.OrderID, &c.Status, &c.Serial, &c.NotAfter, &c.RenewAfter, &c.Revoked,
			&c.DirectoryURL, &c.Error); err != nil {
			return nil, err
		}
		if c.DirectoryURL != nil {
			c.Directory = DirectoryName(*c.DirectoryURL)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

type EventRow struct {
	ID          int64           `json:"id"`
	At          time.Time       `json:"at"`
	ApplianceID *string         `json:"appliance_id"`
	Hostname    *string         `json:"hostname"`
	Kind        string          `json:"kind"`
	Actor       *string         `json:"actor"`
	Detail      json.RawMessage `json:"detail"`
}

// Events returns the newest events for one appliance ("" = all).
func (s *Service) Events(ctx context.Context, applianceID string, limit int) ([]EventRow, error) {
	rows, err := s.DB.Query(ctx, `
        SELECT id, at, appliance_id::text, hostname, kind, actor, detail
          FROM portal_tls_events
         WHERE ($1='' OR appliance_id::text=$1)
         ORDER BY at DESC, id DESC LIMIT $2`, applianceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EventRow{}
	for rows.Next() {
		var e EventRow
		if err := rows.Scan(&e.ID, &e.At, &e.ApplianceID, &e.Hostname, &e.Kind, &e.Actor, &e.Detail); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// RevokeAppliance revokes an appliance's certificates; it reorders with a new key at once, so the ACME reason is "superseded".
func (s *Service) RevokeAppliance(ctx context.Context, applianceID, hostname, actor, why string) (int, error) {
	var exists bool
	if err := s.DB.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM portal_hostnames WHERE appliance_id=$1)`, applianceID).Scan(&exists); err != nil {
		return 0, err
	}
	if !exists {
		return 0, refuse(http.StatusNotFound, "not_found", "this appliance has no portal hostname")
	}
	var n int
	err := s.DB.QueryRow(ctx, `
        SELECT count(*) FROM portal_certificate_orders
         WHERE appliance_id=$1 AND ($2='' OR hostname=$2) AND status IN ('ISSUED','SUPERSEDED')
           AND revoke_requested_at IS NULL AND revoked_at IS NULL AND not_after > now()`, applianceID, hostname).Scan(&n)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	if n == 0 {
		return 0, refuse(http.StatusConflict, "nothing_to_revoke", "there is no valid certificate to revoke")
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if err := requestRevocation(ctx, tx, applianceID, hostname, RevokeSuperseded, actor, why, false); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	s.Nudge()
	return n, nil
}
