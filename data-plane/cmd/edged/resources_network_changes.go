package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// Staged switch-on / switch-off of client networks (migration 0151). Activating a prepared network, converting a port between Access and Trunk, and the recovery
// operations enable and disable networks TOGETHER or not at all. The request is recorded here and public.guest_networks.enabled moves only when the operator
// applies the configuration; a failed apply, a rollback or a lapsed confirmation window puts every flag back. The lifecycle mirrors the topology replacements.

type changeKind string

const (
	changeEnable   changeKind = "ENABLE"
	changeDisable  changeKind = "DISABLE"
	changeSettings changeKind = "SETTINGS"
)

type changeItem struct {
	NetworkID string
	Kind      changeKind
}

type errChangeExists struct{ network string }

func (e errChangeExists) Error() string {
	return fmt.Sprintf("client network %s already has a staged change waiting to be applied; apply or cancel it first", e.network)
}

// stageChanges records one operation's changes together; nothing in guest_networks moves.
func (s *server) stageChanges(ctx context.Context, tx pgx.Tx, groupKind string, items []changeItem) (string, error) {
	var groupID string
	if err := tx.QueryRow(ctx, `SELECT gen_random_uuid()::text`).Scan(&groupID); err != nil {
		return "", err
	}
	for _, it := range items {
		if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.guest_network_changes (tenant_id, site_id, network_id, kind, group_id, group_kind)
			VALUES ($1,$2,$3,$4,$5,$6)`, s.tenantID, s.siteID, it.NetworkID, string(it.Kind), groupID, groupKind); err != nil {
			if isUniqueViolation(err) {
				return "", errChangeExists{network: it.NetworkID}
			}
			return "", err
		}
	}
	return groupID, nil
}

// materialisePendingChanges runs inside the apply transaction. Switch-offs go first so the slot, subnet or name a switch-on needs is free; a switch-on that
// collides with an enabled network refuses the whole apply (the transaction rolls back and nothing was touched).
func (s *server) materialisePendingChanges(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, network_id::text, kind, target FROM iam_v2.guest_network_changes
		 WHERE tenant_id=$1 AND site_id=$2 AND state='PENDING'
		 ORDER BY (kind = 'DISABLE') DESC, created_at, id FOR UPDATE`, s.tenantID, s.siteID)
	if err != nil {
		return nil, err
	}
	type pending struct {
		id, network, kind string
		target            []byte
	}
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.network, &p.kind, &p.target); err != nil {
			rows.Close()
			return nil, err
		}
		todo = append(todo, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var ids []string
	for _, p := range todo {
		var cur bool
		if err := tx.QueryRow(ctx, `SELECT enabled FROM guest_networks WHERE id=$1 AND tenant_id=$2 AND site_id=$3 FOR UPDATE`,
			p.network, s.tenantID, s.siteID).Scan(&cur); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				// The network is gone: nothing to switch, and the request is closed rather than left to block the next apply.
				if _, uerr := tx.Exec(ctx, `UPDATE iam_v2.guest_network_changes SET state='CANCELLED', settled_at=now() WHERE id=$1`, p.id); uerr != nil {
					return nil, uerr
				}
				continue
			}
			return nil, err
		}
		want := cur
		if p.kind != string(changeSettings) {
			want = p.kind == string(changeEnable)
		}
		if want != cur {
			if _, err := tx.Exec(ctx, `UPDATE guest_networks SET enabled=$2, updated_at=now() WHERE id=$1`, p.network, want); err != nil {
				return nil, err
			}
		}
		var before []byte
		if len(p.target) > 0 {
			var t recoveryTarget
			if err := json.Unmarshal(p.target, &t); err != nil {
				return nil, fmt.Errorf("staged settings of %s are unreadable: %w", p.network, err)
			}
			b, err := s.applyRecoveryTarget(ctx, tx, p.id, p.network, t)
			if err != nil {
				return nil, err
			}
			before, _ = json.Marshal(b)
		}
		if want && (want != cur || p.kind == string(changeSettings)) {
			if err := s.enabledConflict(ctx, tx, p.network); err != nil {
				return nil, err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE iam_v2.guest_network_changes SET state='APPLIED', prior_enabled=$2, applied_at=now(), before=$3::jsonb WHERE id=$1`, p.id, cur, nullJSON(before)); err != nil {
			return nil, err
		}
		ids = append(ids, p.id)
	}
	return ids, nil
}

// revertChanges puts every flag back and returns each change to PENDING. One change per transaction, so a stuck row cannot block the rest.
func (s *server) revertChanges(ctx context.Context, ids []string, reason string) error {
	var errs []error
	// Back out in the reverse of the order the apply went in (switch-offs were written first): networks the apply switched ON go off before the ones it switched
	// off come back, or two untagged networks on one port would collide.
	if len(ids) > 1 {
		order := map[string]int{}
		if rows, qerr := s.db.Query(ctx, `SELECT id::text, CASE kind WHEN 'ENABLE' THEN 0 WHEN 'SETTINGS' THEN 1 ELSE 2 END FROM iam_v2.guest_network_changes WHERE id = ANY($1::uuid[])`, ids); qerr == nil {
			for rows.Next() {
				var id string
				var rank int
				if rows.Scan(&id, &rank) == nil {
					order[id] = rank
				}
			}
			rows.Close()
		}
		sort.SliceStable(ids, func(i, j int) bool { return order[ids[i]] < order[ids[j]] })
	}
	for _, id := range ids {
		if err := s.revertChange(ctx, id); err != nil {
			errs = append(errs, fmt.Errorf("change %s: %w", id, err))
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	if len(ids) > 0 {
		slog.Info("staged client-network changes were put back", "changes", ids, "reason", reason)
	}
	return nil
}

func (s *server) revertChange(ctx context.Context, id string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var network string
	var prior *bool
	var beforeRaw []byte
	if err := tx.QueryRow(ctx, `SELECT network_id::text, prior_enabled, before FROM iam_v2.guest_network_changes
		 WHERE id=$1 AND state='APPLIED' FOR UPDATE`, id).Scan(&network, &prior, &beforeRaw); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // already settled or reverted
		}
		return err
	}
	if prior != nil {
		if _, err := tx.Exec(ctx, `UPDATE guest_networks SET enabled=$2, updated_at=now() WHERE id=$1`, network, *prior); err != nil {
			return err
		}
	}
	if len(beforeRaw) > 0 {
		var b recoveryBefore
		if err := json.Unmarshal(beforeRaw, &b); err != nil {
			return fmt.Errorf("the settings this change replaced are unreadable: %w", err)
		}
		if err := s.revertRecoveryTarget(ctx, tx, network, b); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.guest_network_changes
		   SET state='PENDING', prior_enabled=NULL, applied_at=NULL, revision_id=NULL, before=NULL WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// recordChangesRevision ties the materialised changes to the revision netd minted for the apply.
func (s *server) recordChangesRevision(ctx context.Context, ids []string, revision string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.db.Exec(ctx, `UPDATE iam_v2.guest_network_changes SET revision_id=$2::uuid WHERE id = ANY($1::uuid[]) AND state='APPLIED'`, ids, revision)
	return err
}

// settleConfirmedChanges closes the changes whose apply was kept: the revision being confirmed now, or one that is active or has since been superseded.
// When the kept change was a factory reset, the networks it switched off are archived in the same transaction, so "confirmed" and "archived" are one fact: a
// failed apply, a rollback, a lapsed window or a cancelled staging never gets here and never leaves a network archived.
func (s *server) settleConfirmedChanges(ctx context.Context, revisionID string) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
		UPDATE iam_v2.guest_network_changes g SET state='CONFIRMED', settled_at=now()
		 WHERE g.tenant_id=$1 AND g.site_id=$2 AND g.state='APPLIED'
		   AND (g.revision_id = NULLIF($3,'')::uuid
		        OR EXISTS (SELECT 1 FROM network_config_revisions v WHERE v.id = g.revision_id AND v.state IN ('active','superseded')))
		RETURNING g.network_id::text, g.group_id::text, g.group_kind, g.kind`, s.tenantID, s.siteID, revisionID)
	if err != nil {
		return err
	}
	type settled struct{ network, group, groupKind, kind string }
	var done []settled
	for rows.Next() {
		var x settled
		if err := rows.Scan(&x.network, &x.group, &x.groupKind, &x.kind); err != nil {
			rows.Close()
			return err
		}
		done = append(done, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, x := range done {
		if x.groupKind == "FACTORY_RESET" || x.groupKind == "FULL_RESET" {
			if err := s.archiveRetired(ctx, tx); err != nil {
				return err
			}
			break
		}
	}
	// A network that is serving again is not archived, whatever switched it back on.
	if _, err := tx.Exec(ctx, `DELETE FROM iam_v2.network_archive a USING guest_networks g WHERE g.id = a.network_id AND g.enabled AND a.tenant_id=$1::uuid AND a.site_id=$2::uuid`, s.tenantID, s.siteID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if cerr := s.captureLayers(ctx, revisionID); cerr != nil {
		slog.Warn("the layers of the confirmed configuration could not be recorded; a later restore will not compare them", "revision", revisionID, "err", cerr)
	}
	return nil
}

func nullJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}

// reconcileStaleChanges settles the changes of a kept apply and reverts those of an apply that did not hold, by the same rules as replacements.
func (s *server) reconcileStaleChanges(ctx context.Context) error {
	rows, err := s.db.Query(ctx, `SELECT g.id::text, COALESCE(v.state,''), g.revision_id IS NULL, g.applied_at < now() - $3::interval
		  FROM iam_v2.guest_network_changes g LEFT JOIN network_config_revisions v ON v.id = g.revision_id
		 WHERE g.tenant_id=$1 AND g.site_id=$2 AND g.state='APPLIED'`, s.tenantID, s.siteID, staleReplacementGrace.String())
	if err != nil {
		return err
	}
	var stale []string
	settle := false
	for rows.Next() {
		var id, state string
		var unrecorded, pastGrace bool
		if err := rows.Scan(&id, &state, &unrecorded, &pastGrace); err != nil {
			rows.Close()
			return err
		}
		switch {
		case unrecorded:
			if pastGrace {
				stale = append(stale, id)
			}
		case state == "applying" || state == "pending_confirmation":
		case state == "active" || state == "superseded":
			settle = true
		default:
			stale = append(stale, id)
		}
	}
	rerr := rows.Err()
	rows.Close()
	if rerr != nil {
		return rerr
	}
	if len(stale) > 0 {
		if err := s.revertChanges(ctx, stale, "the apply was rolled back"); err != nil {
			return fmt.Errorf("%d staged client-network change(s) could not be put back: %w", len(stale), err)
		}
	}
	if settle {
		return s.settleConfirmedChanges(ctx, "")
	}
	return nil
}

type stagedGroup struct {
	GroupID   string          `json:"group_id"`
	GroupKind string          `json:"group_kind"`
	State     string          `json:"state"`
	CreatedAt time.Time       `json:"created_at"`
	Changes   []stagedChgItem `json:"changes"`
}

type stagedChgItem struct {
	ID        string `json:"id"`
	NetworkID string `json:"network_id"`
	Network   string `json:"network"`
	Kind      string `json:"kind"`
	State     string `json:"state"`
}

// listStagedChanges serves GET /network/changes: what is waiting to be applied or is awaiting confirmation, grouped by the operation that staged it.
func (s *server) listStagedChanges(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	_ = s.reconcileStaleChanges(ctx)
	groups, err := s.loadStagedGroups(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": groups})
}

func (s *server) loadStagedGroups(ctx context.Context) ([]stagedGroup, error) {
	out := []stagedGroup{}
	if s.tenantID == "" || s.siteID == "" {
		return out, nil
	}
	rows, err := s.db.Query(ctx, `SELECT c.id::text, c.group_id::text, c.group_kind, c.state, c.created_at, c.network_id::text,
		       COALESCE(g.name, ''), c.kind
		  FROM iam_v2.guest_network_changes c LEFT JOIN guest_networks g ON g.id = c.network_id
		 WHERE c.tenant_id=$1 AND c.site_id=$2 AND c.state IN ('PENDING','APPLIED')
		 ORDER BY c.created_at, c.id`, s.tenantID, s.siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	idx := map[string]int{}
	for rows.Next() {
		var item stagedChgItem
		var gid, gkind, gstate string
		var created time.Time
		if err := rows.Scan(&item.ID, &gid, &gkind, &gstate, &created, &item.NetworkID, &item.Network, &item.Kind); err != nil {
			return nil, err
		}
		item.State = gstate
		i, ok := idx[gid]
		if !ok {
			out = append(out, stagedGroup{GroupID: gid, GroupKind: gkind, State: gstate, CreatedAt: created})
			i = len(out) - 1
			idx[gid] = i
		}
		out[i].Changes = append(out[i].Changes, item)
		if gstate == "APPLIED" {
			out[i].State = "APPLIED"
		}
	}
	return out, rows.Err()
}

// cancelStagedGroup serves DELETE /network/changes/{group_id}: drops an operation that has not been applied. Networks the operation created stay, switched off, as
// ordinary client networks the operator can delete; nothing live changes.
func (s *server) cancelStagedGroup(w http.ResponseWriter, r *http.Request) {
	group := chi.URLParam(r, "group_id")
	if !uuidPattern.MatchString(group) {
		jsonErr(w, http.StatusBadRequest, "bad_request", "unknown change")
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	// One transaction: the group's rows are locked first, so an apply that is materialising them either finished before this saw them (and the cancel is
	// refused) or waits for this to commit (and finds nothing pending). The change is closed and the network the operation made is removed together or not at all.
	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "cancel failed")
		return
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT network_id::text, state, COALESCE(target->>'created','') = 'true' FROM iam_v2.guest_network_changes
		 WHERE group_id=$1::uuid AND tenant_id=$2 AND site_id=$3 ORDER BY id FOR UPDATE`, group, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "cancel failed")
		return
	}
	var created []string
	pending, applied := 0, false
	for rows.Next() {
		var network, state string
		var madeHere bool
		if err := rows.Scan(&network, &state, &madeHere); err != nil {
			rows.Close()
			jsonErr(w, http.StatusInternalServerError, "internal", "cancel failed")
			return
		}
		switch state {
		case "APPLIED":
			applied = true
		case "PENDING":
			pending++
			if madeHere {
				created = append(created, network)
			}
		}
	}
	rows.Close()
	if rows.Err() != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "cancel failed")
		return
	}
	if applied {
		jsonErr(w, http.StatusConflict, "not_pending", "this change has been applied and is awaiting confirmation: confirm or roll it back instead")
		return
	}
	if pending == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "no pending change with that id")
		return
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.guest_network_changes SET state='CANCELLED', settled_at=now()
		 WHERE group_id=$1::uuid AND tenant_id=$2 AND site_id=$3 AND state='PENDING'`, group, s.tenantID, s.siteID); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "cancel failed")
		return
	}
	// A network the operation itself created, never applied, goes away with it; everything else stays.
	if len(created) > 0 {
		if _, err := tx.Exec(ctx, `DELETE FROM guest_networks WHERE id = ANY($1::uuid[]) AND tenant_id=$2 AND site_id=$3 AND NOT enabled`, created, s.tenantID, s.siteID); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "cancel failed; nothing was changed")
			return
		}
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "cancel failed; nothing was changed")
		return
	}
	s.audit(r, "network.change.cancelled", "guest_network_change", group, map[string]any{"changes": pending, "networks_removed": len(created)})
	writeJSON(w, http.StatusOK, map[string]any{"status": "cancelled"})
}
