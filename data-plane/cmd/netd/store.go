package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/stayconnect/enterprise/data-plane/internal/netcfg"
)

// store is netd's view of the site database: it loads guest-network intent and
// records revisions / apply events / health checks.
type store struct{ db *pgxpool.Pool }

// uuidOrNil returns s only if it is a valid UUID, else nil — so an operator id
// column stays NULL for system/CLI actors that aren't real operators.
func uuidOrNil(s string) any {
	if len(s) == 36 && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-' {
		return s
	}
	return nil
}

// LoadIntent returns all guest networks (flattened with pools + reservations)
// for rendering. onlyEnabled filters to enabled networks.
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
	// pools + reservations per network
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

// AvailableInterfaces refreshes the network_interfaces inventory from discovery
// and returns the set of assignable parent names (excludes protected mgmt/wan
// only from *new guest attachment*, not from the inventory listing).
func (s *store) SyncInterfaces(ctx context.Context, ifaces []Interface, mgmt, wan string) error {
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
            VALUES ($1, NULLIF($2,'')::macaddr, $3, 'auto', NULLIF($4,''), $5, $6, $7::jsonb, $8, now())
            ON CONFLICT (name) DO UPDATE SET
              mac = EXCLUDED.mac, link_state = EXCLUDED.link_state, mtu = EXCLUDED.mtu,
              ip_addresses = EXCLUDED.ip_addresses, last_seen_at = now(),
              -- keep an operator-assigned role; only seed protected flag/role
              role = CASE WHEN network_interfaces.role = 'unused' THEN EXCLUDED.role ELSE network_interfaces.role END,
              is_protected = network_interfaces.is_protected OR EXCLUDED.is_protected
        `, f.Name, f.MAC, role, f.Parent, f.LinkState, f.MTU, string(ipsRaw), protected)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *store) AvailableIfaceSet(ctx context.Context) (map[string]bool, error) {
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

// --- revisions ---

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

func (s *store) MarkApplying(ctx context.Context, id, bundlePath, appliedBy string, confirmDeadline any) error {
	by := uuidOrNil(appliedBy)
	_, err := s.db.Exec(ctx, `
        UPDATE network_config_revisions
           SET state='applying', bundle_path=$2, applied_by=$3, applied_at=now()
         WHERE id=$1`, id, bundlePath, by)
	return err
}

func (s *store) MarkPending(ctx context.Context, id string, deadline any) error {
	_, err := s.db.Exec(ctx,
		`UPDATE network_config_revisions SET state='pending_confirmation', confirm_deadline=$2 WHERE id=$1`, id, deadline)
	return err
}

var errNotPending = errors.New("revision is not pending confirmation")

// MarkActive confirms a revision. When requirePending is true it refuses to
// activate a revision that is not currently pending_confirmation (so a
// rolled-back/failed revision can never be forced active by a stray confirm).
func (s *store) MarkActive(ctx context.Context, id, confirmedBy string) error {
	return s.markActive(ctx, id, confirmedBy, true)
}

// markActiveAdopt is the internal path used by adopt (no pending gate).
func (s *store) markActiveAdopt(ctx context.Context, id, confirmedBy string) error {
	return s.markActive(ctx, id, confirmedBy, false)
}

func (s *store) markActive(ctx context.Context, id, confirmedBy string, requirePending bool) error {
	by := uuidOrNil(confirmedBy)
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if requirePending {
		var state string
		if err := tx.QueryRow(ctx, `SELECT state FROM network_config_revisions WHERE id=$1 FOR UPDATE`, id).Scan(&state); err != nil {
			return err
		}
		if state != "pending_confirmation" {
			return errNotPending
		}
	}
	// supersede the previous active revision
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

func (s *store) MarkRolledBack(ctx context.Context, id, reason string) error {
	_, err := s.db.Exec(ctx,
		`UPDATE network_config_revisions SET state='rolled_back', failure_reason=$2 WHERE id=$1`, id, reason)
	return err
}

// CurrentActive returns the id + bundle path of the active revision (used for
// boot reconciliation), or ("","") if none.
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

// ActiveBundlePath returns the bundle path of the current active revision (the
// known-good to roll back to), or "" if none.
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

// PendingRevision returns the single in-flight pending_confirmation revision,
// if any (used by the watchdog on netd startup and by confirm/rollback).
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
