package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

type store struct{ db *pgxpool.Pool }

// uuidOrNil returns s only if it is a valid UUID, so operator id stays NULL for non-operator actors.
func uuidOrNil(s string) any {
	if len(s) == 36 && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-' {
		return s
	}
	return nil
}

func (s *store) LoadIntent(ctx context.Context) ([]netcfg.GuestNetwork, error) {
	rows, err := s.db.Query(ctx, `
        SELECT id::text, name, COALESCE(ssid_label,''), enabled, network_type,
               parent_interface, COALESCE(vlan_id,0), bridge_name,
               host(gateway_ip), text(subnet_cidr), masklen(subnet_cidr),
               dhcp_mode, dns_mode, dns_servers, domain_name,
               lease_default_seconds, lease_min_seconds, lease_max_seconds,
               relay_targets, captive_portal_enabled, internet_access_enabled,
               nat_enabled, client_isolation_enabled
          FROM guest_networks ORDER BY COALESCE(vlan_id,0), name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var nets []netcfg.GuestNetwork
	for rows.Next() {
		var n netcfg.GuestNetwork
		var dnsRaw, relayRaw []byte
		if err := rows.Scan(&n.ID, &n.Name, &n.SSIDLabel, &n.Enabled, &n.NetworkType,
			&n.ParentInterface, &n.VLANID, &n.BridgeName,
			&n.GatewayIP, &n.SubnetCIDR, &n.PrefixLen,
			&n.DHCPMode, &n.DNSMode, &dnsRaw, &n.DomainName,
			&n.LeaseDefault, &n.LeaseMin, &n.LeaseMax,
			&relayRaw, &n.CaptiveEnabled, &n.InternetEnabled,
			&n.NATEnabled, &n.ClientIsolation); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(dnsRaw, &n.DNSServers)
		_ = json.Unmarshal(relayRaw, &n.RelayTargets)
		nets = append(nets, n)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range nets {
		if err := s.loadPools(ctx, &nets[i]); err != nil {
			return nil, err
		}
		if err := s.loadReservations(ctx, &nets[i]); err != nil {
			return nil, err
		}
	}
	return nets, nil
}

func (s *store) loadPools(ctx context.Context, n *netcfg.GuestNetwork) error {
	rows, err := s.db.Query(ctx,
		`SELECT host(start_ip), host(end_ip) FROM dhcp_pools WHERE guest_network_id=$1 ORDER BY sort_order, start_ip`, n.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var p netcfg.Pool
		if err := rows.Scan(&p.StartIP, &p.EndIP); err != nil {
			return err
		}
		n.Pools = append(n.Pools, p)
	}
	return rows.Err()
}

func (s *store) loadReservations(ctx context.Context, n *netcfg.GuestNetwork) error {
	rows, err := s.db.Query(ctx,
		`SELECT text(mac), host(reserved_ip), COALESCE(hostname,''), enabled FROM dhcp_reservations WHERE guest_network_id=$1`, n.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r netcfg.Reservation
		if err := rows.Scan(&r.MAC, &r.ReservedIP, &r.Hostname, &r.Enabled); err != nil {
			return err
		}
		n.Reservations = append(n.Reservations, r)
	}
	return rows.Err()
}

// SyncInterfaces refreshes the interface inventory, which is not a presence oracle: validation observes presence live (see applier.presenceSets) because this table is only as current as the last sweep.
// One timestamp per sweep keeps the inventory coherent.
func (s *store) SyncInterfaces(ctx context.Context, ifaces []Interface, mgmt, wan string) error {
	sweptAt := time.Now().UTC()
	for _, f := range ifaces {
		role := "unused"
		protected := false
		if f.Name == mgmt {
			role, protected = "management", true
		} else if f.Name == wan {
			role, protected = "wan", true
		}
		ipsRaw, _ := json.Marshal(f.IPs)
		_, err := s.db.Exec(ctx, `
            INSERT INTO network_interfaces (name, mac, role, mode, parent, link_state, mtu, ip_addresses, is_protected, last_seen_at)
            VALUES ($1, NULLIF($2,'')::macaddr, $3, 'auto', NULLIF($4,''), $5, $6, $7::jsonb, $8, $9)
            ON CONFLICT (name) DO UPDATE SET
              mac = EXCLUDED.mac, link_state = EXCLUDED.link_state, mtu = EXCLUDED.mtu,
              ip_addresses = EXCLUDED.ip_addresses, last_seen_at = EXCLUDED.last_seen_at,
              -- keep an operator-assigned role; only seed protected flag/role
              role = CASE WHEN network_interfaces.role = 'unused' THEN EXCLUDED.role ELSE network_interfaces.role END,
              is_protected = network_interfaces.is_protected OR EXCLUDED.is_protected
        `, f.Name, f.MAC, role, f.Parent, f.LinkState, f.MTU, string(ipsRaw), protected, sweptAt)
		if err != nil {
			return err
		}
	}
	return nil
}

// KnownIfaceSet returns every interface ever recorded, to tell "no such interface" from "had one, now gone".
func (s *store) KnownIfaceSet(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.Query(ctx, `SELECT name FROM network_interfaces`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out[n] = true
	}
	return out, rows.Err()
}

type IfaceMeta struct {
	Role      string
	Protected bool
}

func (s *store) InterfaceMeta(ctx context.Context) (map[string]IfaceMeta, error) {
	rows, err := s.db.Query(ctx, `SELECT name, COALESCE(role,'unused'), is_protected FROM network_interfaces`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]IfaceMeta{}
	for rows.Next() {
		var n, role string
		var protected bool
		if err := rows.Scan(&n, &role, &protected); err != nil {
			return nil, err
		}
		out[n] = IfaceMeta{Role: role, Protected: protected}
	}
	return out, rows.Err()
}

func (s *store) CreateRevision(ctx context.Context, summary string, intent []netcfg.GuestNetwork, createdBy string) (string, int64, error) {
	intentRaw, _ := json.Marshal(intent)
	var id string
	var seq int64
	by := uuidOrNil(createdBy)
	err := s.db.QueryRow(ctx, `
        INSERT INTO network_config_revisions (state, summary, intent, created_by)
        VALUES ('draft', $1, $2::jsonb, $3)
        RETURNING id::text, seq`, summary, string(intentRaw), by).Scan(&id, &seq)
	return id, seq, err
}

// RevisionState feeds the rollback guard: mid-flight revisions still have the previous revision active, confirmed ones have no stored target. See applier.Rollback.
func (s *store) RevisionState(ctx context.Context, id string) (string, error) {
	var state string
	err := s.db.QueryRow(ctx,
		`SELECT state FROM network_config_revisions WHERE id=$1`, id).Scan(&state)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return state, nil
}

func (s *store) SetRevisionState(ctx context.Context, id, state string) error {
	_, err := s.db.Exec(ctx, `UPDATE network_config_revisions SET state=$2 WHERE id=$1`, id, state)
	return err
}

func (s *store) SetRevisionValidation(ctx context.Context, id string, v netcfg.ValidationResult) error {
	raw, _ := json.Marshal(v)
	_, err := s.db.Exec(ctx,
		`UPDATE network_config_revisions SET validation=$2::jsonb, validated_at=now(), state='validated' WHERE id=$1`, id, string(raw))
	return err
}

// MarkApplying takes the single in-flight slot and records the confirm deadline; without it an apply that dies mid-flight stays 'applying' forever, the watchdog never reclaims it and every later apply fails on the unique index.
func (s *store) MarkApplying(ctx context.Context, id, bundlePath, appliedBy string, confirmDeadline any) error {
	by := uuidOrNil(appliedBy)
	_, err := s.db.Exec(ctx, `
        UPDATE network_config_revisions
           SET state='applying', bundle_path=$2, applied_by=$3, applied_at=now(), confirm_deadline=$4
         WHERE id=$1`, id, bundlePath, by, confirmDeadline)
	return err
}

func (s *store) MarkPending(ctx context.Context, id string, deadline any) error {
	_, err := s.db.Exec(ctx,
		`UPDATE network_config_revisions SET state='pending_confirmation', confirm_deadline=$2 WHERE id=$1`, id, deadline)
	return err
}

var errNotPending = errors.New("revision is not pending confirmation")

// MarkActive is the only way a revision becomes active and refuses anything not pending_confirmation, so the active revision was applied and health-checked; boot reconcile trusts its intent as the truth about what to forward.
// There is deliberately no unverified adopt variant: an unapplied draft must never become the appliance's reality.
func (s *store) MarkActive(ctx context.Context, id, confirmedBy string) error {
	by := uuidOrNil(confirmedBy)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var state string
	if err := tx.QueryRow(ctx, `SELECT state FROM network_config_revisions WHERE id=$1 FOR UPDATE`, id).Scan(&state); err != nil {
		return err
	}
	if state != "pending_confirmation" {
		return errNotPending
	}
	if _, err := tx.Exec(ctx, `UPDATE network_config_revisions SET state='superseded' WHERE state='active' AND id<>$1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE network_config_revisions SET state='active', confirmed_by=$2, confirmed_at=now() WHERE id=$1`, id, by); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *store) MarkFailed(ctx context.Context, id, reason string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE network_config_revisions SET state='failed', failure_reason=$2 WHERE id=$1`, id, reason)
	return err
}

// errNotRollbackable means the revision is no longer in a state a rollback may finish against.
var errNotRollbackable = errors.New("revision is no longer rollbackable")

// MarkRolledBack moves only an in-flight revision. An unguarded update let the watchdog's rollback overwrite a just-confirmed 'active' row, leaving no active revision so boot reconcile reconciled nothing.
// Whichever racer commits first wins; on errNotRollbackable the caller must stop, not retry.
func (s *store) MarkRolledBack(ctx context.Context, id, reason string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var state string
	if err := tx.QueryRow(ctx,
		`SELECT state FROM network_config_revisions WHERE id=$1 FOR UPDATE`, id).Scan(&state); err != nil {
		return err
	}
	if state != "applying" && state != "pending_confirmation" {
		return fmt.Errorf("%w: it is %s", errNotRollbackable, state)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE network_config_revisions SET state='rolled_back', failure_reason=$2 WHERE id=$1`, id, reason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *store) CurrentActive(ctx context.Context) (id, bundlePath string, err error) {
	var p *string
	err = s.db.QueryRow(ctx, `
        SELECT id::text, bundle_path FROM network_config_revisions
         WHERE state='active' ORDER BY seq DESC LIMIT 1`).Scan(&id, &p)
	if err == pgx.ErrNoRows {
		return "", "", nil
	}
	if p != nil {
		bundlePath = *p
	}
	return id, bundlePath, err
}

// ErrNoConfirmedActiveRevision means nothing was ever applied and confirmed, so there is nothing to reconcile to.
var ErrNoConfirmedActiveRevision = errors.New("no confirmed active network revision")

// CurrentActiveIntent returns the confirmed revision's own immutable intent, never guest_networks: those rows are UI drafts, and reconciling from them on boot would apply unapplied or rolled-back edits with no apply record, health check or confirmation.
func (s *store) CurrentActiveIntent(ctx context.Context) (id, bundlePath string, intent []netcfg.GuestNetwork, err error) {
	var p *string
	var raw []byte
	err = s.db.QueryRow(ctx, `
        SELECT id::text, bundle_path, intent FROM network_config_revisions
         WHERE state='active' ORDER BY seq DESC LIMIT 1`).Scan(&id, &p, &raw)
	if err == pgx.ErrNoRows {
		return "", "", nil, ErrNoConfirmedActiveRevision
	}
	if err != nil {
		return "", "", nil, err
	}
	if p != nil {
		bundlePath = *p
	}
	if len(raw) == 0 {
		// An active revision with no recorded intent is an error; guessing from the mutable tables is what this avoids.
		return id, bundlePath, nil, fmt.Errorf("active revision %s has no stored intent", id)
	}
	if err := json.Unmarshal(raw, &intent); err != nil {
		return id, bundlePath, nil, fmt.Errorf("active revision %s has unreadable intent: %w", id, err)
	}
	return id, bundlePath, intent, nil
}

func (s *store) ActiveBundlePath(ctx context.Context, exceptID string) (string, error) {
	var p *string
	err := s.db.QueryRow(ctx, `
        SELECT bundle_path FROM network_config_revisions
         WHERE state='active' AND id<>$1 ORDER BY seq DESC LIMIT 1`, exceptID).Scan(&p)
	if err == pgx.ErrNoRows {
		return "", nil
	}
	if err != nil || p == nil {
		return "", err
	}
	return *p, nil
}

// ActiveIntent returns the stored intent of the active revision. Rollback needs the intent, not the rendered file, because the file embeds the ruleset structure of whichever binary applied it.
func (s *store) ActiveIntent(ctx context.Context, exceptID string) ([]netcfg.GuestNetwork, error) {
	var raw []byte
	err := s.db.QueryRow(ctx, `
        SELECT intent FROM network_config_revisions
         WHERE state='active' AND id<>$1 ORDER BY seq DESC LIMIT 1`, exceptID).Scan(&raw)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil || len(raw) == 0 {
		return nil, err
	}
	var nets []netcfg.GuestNetwork
	if err := json.Unmarshal(raw, &nets); err != nil {
		return nil, err
	}
	return nets, nil
}

func (s *store) PendingRevision(ctx context.Context) (id, bundlePath string, deadlinePassed bool, err error) {
	var p *string
	err = s.db.QueryRow(ctx, `
        SELECT id::text, bundle_path, (confirm_deadline IS NOT NULL AND confirm_deadline < now())
          FROM network_config_revisions WHERE state='pending_confirmation' ORDER BY seq DESC LIMIT 1
    `).Scan(&id, &p, &deadlinePassed)
	if err == pgx.ErrNoRows {
		return "", "", false, nil
	}
	if p != nil {
		bundlePath = *p
	}
	return id, bundlePath, deadlinePassed, err
}

// undefinedTable: a schema without the portal tables has no portal address, which is not a fault.
func undefinedTable(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "42P01"
}

func (s *store) LoadPortalIP(ctx context.Context) (string, error) {
	var ip *string
	err := s.db.QueryRow(ctx, `SELECT host(portal_ip) FROM public.portal_https_settings LIMIT 1`).Scan(&ip)
	if errors.Is(err, pgx.ErrNoRows) || undefinedTable(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if ip == nil {
		return "", nil
	}
	return *ip, nil
}

// The hotel's domain is returned only while VERIFIED: an unverified name must never be answered by the resolver.
func (s *store) LoadPortalTLS(ctx context.Context) (netcfg.PortalTLS, error) {
	var p netcfg.PortalTLS
	err := s.db.QueryRow(ctx, `
        SELECT https_active, COALESCE(canonical_hostname,''), COALESCE(onegate_hostname,''),
               CASE WHEN custom_domain_status = 'VERIFIED' THEN COALESCE(custom_domain,'') ELSE '' END
          FROM public.portal_tls_state LIMIT 1`).Scan(&p.HTTPSActive, &p.CanonicalHostname, &p.OneGateHostname, &p.CustomDomain)
	if errors.Is(err, pgx.ErrNoRows) || undefinedTable(err) {
		return netcfg.PortalTLS{}, nil
	}
	return p, err
}

func (s *store) Event(ctx context.Context, revID, phase string, ok bool, detail map[string]any) {
	raw, _ := json.Marshal(detail)
	_, _ = s.db.Exec(ctx,
		`INSERT INTO network_apply_events (revision_id, phase, ok, detail) VALUES ($1,$2,$3,$4::jsonb)`,
		revID, phase, ok, string(raw))
}

func (s *store) Health(ctx context.Context, revID, name string, ok bool, detail string) {
	_, _ = s.db.Exec(ctx,
		`INSERT INTO network_health_checks (revision_id, check_name, ok, detail) VALUES ($1,$2,$3,$4)`,
		revID, name, ok, detail)
}

// InFlightRevision returns the revision that holds the single in-flight slot (applying or awaiting confirmation), or "" when none does.
func (s *store) InFlightRevision(ctx context.Context) (id, state string, err error) {
	err = s.db.QueryRow(ctx, `
        SELECT id::text, state FROM network_config_revisions
         WHERE state IN ('applying','pending_confirmation') ORDER BY seq DESC LIMIT 1`).Scan(&id, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", nil
	}
	return id, state, err
}

// ApplyingRevisions lists revisions stuck in 'applying'. Called at netd start, when no apply of this process can be running, so any
// such row was interrupted: nothing else would ever move it, and it holds the unique in-flight slot against every later apply.
func (s *store) ApplyingRevisions(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text FROM network_config_revisions WHERE state='applying' ORDER BY seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// RecordedBridges returns the bridges named by revisions netd actually applied (active, superseded, applying, awaiting confirmation, rolled
// back), with the member interface each was built on. A bridge in this record is one netd created, which is how a bridge made before the
// ownership mark existed is recognised. A name recorded with two different members is returned with no member, so nothing is deleted
// on the strength of an ambiguous record.
func (s *store) RecordedBridges(ctx context.Context) (map[string]recordedBridge, error) {
	rows, err := s.db.Query(ctx, `
        SELECT DISTINCT e->>'bridge_name', COALESCE(e->>'network_type',''), COALESCE(e->>'parent_interface',''),
                        COALESCE(NULLIF(e->>'vlan_id','')::int, 0)
          FROM network_config_revisions r, jsonb_array_elements(r.intent) e
         WHERE r.state IN ('active','superseded','pending_confirmation','applying','rolled_back')
           AND jsonb_typeof(r.intent) = 'array' AND COALESCE(e->>'bridge_name','') <> ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]recordedBridge{}
	for rows.Next() {
		var br, typ, parent string
		var vid int
		if err := rows.Scan(&br, &typ, &parent, &vid); err != nil {
			return nil, err
		}
		member := parent
		if typ == "vlan" {
			member = netcfg.VLANIfaceName(parent, vid)
		}
		if prev, seen := out[br]; seen && prev.Member != member {
			member = ""
		}
		out[br] = recordedBridge{Member: member}
	}
	return out, rows.Err()
}
