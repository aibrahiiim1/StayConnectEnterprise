package main

// CLIENT GROUPS: the Admin Console's half of docs/architecture/ONEGATE_CLIENT_IDENTITY_AND_ACCESS_POLICY.md §3.
//
// A group is a site policy object -- name, priority (lower wins), enabled -- with OR-ed membership rules. The
// Admin Console owns the groups and their append-only history (iam_v2.client_group_changes); it decides nothing
// about any Client: who is in a group is evaluated by scd at sign-in from the Client's verified factors, and
// edged reads NO identity table.
//
// Every rule is validated with the SAME function scd's evaluator uses (iamv2.ValidateClientGroupRule), so a
// rule that cannot be evaluated cannot be saved. A group an Internet Package still names as its audience cannot
// be deleted: deleting it would silently turn that audience into nobody.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/stayconnect/enterprise/data-plane/internal/iamv2"
)

type clientGroupRule struct {
	ID    string         `json:"id,omitempty"`
	Type  string         `json:"type"`
	Value map[string]any `json:"value"`
}

type clientGroup struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Priority    int               `json:"priority"`
	Enabled     bool              `json:"enabled"`
	Rules       []clientGroupRule `json:"rules"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
}

type clientGroupWrite struct {
	Name        *string            `json:"name,omitempty"`
	Description *string            `json:"description,omitempty"`
	Priority    *int               `json:"priority,omitempty"`
	Enabled     *bool              `json:"enabled,omitempty"`
	Rules       *[]clientGroupRule `json:"rules,omitempty"` // replaces the whole rule set
	Reason      string             `json:"reason,omitempty"`
}

// validateClientGroupWrite returns an operator-readable refusal, or "".
func validateClientGroupWrite(in clientGroupWrite, creating bool) string {
	if creating && (in.Name == nil || strings.TrimSpace(*in.Name) == "") {
		return "a group needs a name"
	}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if n == "" || len(n) > 80 {
			return "the name must be 1 to 80 characters"
		}
	}
	if in.Description != nil && len(*in.Description) > 500 {
		return "the description must be at most 500 characters"
	}
	if in.Priority != nil && (*in.Priority < 1 || *in.Priority > 1000) {
		return "priority must be between 1 (wins) and 1000"
	}
	if in.Rules != nil {
		if len(*in.Rules) > 20 {
			return "a group may have at most 20 membership rules"
		}
		for i, r := range *in.Rules {
			if r.Value == nil {
				r.Value = map[string]any{}
			}
			if err := iamv2.ValidateClientGroupRule(strings.ToUpper(strings.TrimSpace(r.Type)), r.Value); err != nil {
				return "rule " + itoa(i+1) + ": " + err.Error()
			}
		}
	}
	return ""
}

func (s *server) clientGroupsRoutes() http.Handler {
	r := chi.NewRouter()
	r.Get("/", s.listClientGroups)
	r.Post("/", s.createClientGroup)
	r.Get("/changes", s.clientGroupChanges)
	r.Get("/{id}", s.getClientGroup)
	r.Patch("/{id}", s.patchClientGroup)
	r.Delete("/{id}", s.deleteClientGroup)
	return r
}

type rowQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

func (s *server) loadClientGroups(ctx context.Context, q rowQuerier, onlyID string) ([]clientGroup, error) {
	rows, err := q.Query(ctx, `
		SELECT g.id::text, g.name, g.description, g.priority, g.enabled,
		       to_char(g.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       to_char(g.updated_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"'),
		       r.id::text, r.rule_type, COALESCE(r.rule_value,'{}'::jsonb)::text
		  FROM iam_v2.client_groups g
		  LEFT JOIN iam_v2.client_group_rules r ON r.tenant_id=g.tenant_id AND r.site_id=g.site_id AND r.group_id=g.id
		 WHERE g.tenant_id=$1 AND g.site_id=$2 AND ($3 = '' OR g.id::text = $3)
		 ORDER BY g.priority, lower(g.name), g.id, r.created_at, r.id`, s.tenantID, s.siteID, onlyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []clientGroup
	idx := map[string]int{}
	for rows.Next() {
		var g clientGroup
		var rid, rtype, rval *string
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.Priority, &g.Enabled, &g.CreatedAt, &g.UpdatedAt, &rid, &rtype, &rval); err != nil {
			return nil, err
		}
		i, seen := idx[g.ID]
		if !seen {
			g.Rules = []clientGroupRule{}
			out = append(out, g)
			i = len(out) - 1
			idx[g.ID] = i
		}
		if rid != nil && rtype != nil {
			v := map[string]any{}
			if rval != nil {
				_ = json.Unmarshal([]byte(*rval), &v)
			}
			out[i].Rules = append(out[i].Rules, clientGroupRule{ID: *rid, Type: *rtype, Value: v})
		}
	}
	return out, rows.Err()
}

func (s *server) listClientGroups(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	out, err := s.loadClientGroups(ctx, s.db, "")
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	writeList(w, out)
}

func (s *server) getClientGroup(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	out, err := s.loadClientGroups(ctx, s.db, chi.URLParam(r, "id"))
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	if len(out) == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "group not found")
		return
	}
	writeJSON(w, http.StatusOK, out[0])
}

func (s *server) writeRules(ctx context.Context, tx pgx.Tx, groupID string, rules []clientGroupRule) error {
	if _, err := tx.Exec(ctx, `DELETE FROM iam_v2.client_group_rules WHERE tenant_id=$1 AND site_id=$2 AND group_id=$3::uuid`,
		s.tenantID, s.siteID, groupID); err != nil {
		return err
	}
	for _, rule := range rules {
		v := rule.Value
		if v == nil {
			v = map[string]any{}
		}
		b, _ := json.Marshal(v)
		if _, err := tx.Exec(ctx, `INSERT INTO iam_v2.client_group_rules (tenant_id, site_id, group_id, rule_type, rule_value)
			VALUES ($1,$2,$3::uuid,$4,$5::jsonb)`, s.tenantID, s.siteID, groupID, strings.ToUpper(strings.TrimSpace(rule.Type)), string(b)); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) recordGroupChange(ctx context.Context, tx pgx.Tx, groupID, action, actor, reason string, before, after *clientGroup) error {
	var bj, aj *string
	if before != nil {
		b, _ := json.Marshal(before)
		x := string(b)
		bj = &x
	}
	if after != nil {
		b, _ := json.Marshal(after)
		x := string(b)
		aj = &x
	}
	if strings.TrimSpace(actor) == "" {
		actor = "operator"
	}
	_, err := tx.Exec(ctx, `INSERT INTO iam_v2.client_group_changes (tenant_id, site_id, group_id, action, changed_by, change_reason, before, after)
		VALUES ($1,$2,$3::uuid,$4,$5,$6,$7::jsonb,$8::jsonb)`, s.tenantID, s.siteID, groupID, action, actor, strings.TrimSpace(reason), bj, aj)
	return err
}

func (s *server) createClientGroup(w http.ResponseWriter, r *http.Request) {
	var in clientGroupWrite
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	if msg := validateClientGroupWrite(in, true); msg != "" {
		jsonErr(w, http.StatusBadRequest, "validation", msg)
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "begin failed")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	priority, enabled, desc := 100, true, ""
	if in.Priority != nil {
		priority = *in.Priority
	}
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	if in.Description != nil {
		desc = strings.TrimSpace(*in.Description)
	}
	var id string
	err = tx.QueryRow(ctx, `INSERT INTO iam_v2.client_groups (tenant_id, site_id, name, description, priority, enabled)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id::text`, s.tenantID, s.siteID, strings.TrimSpace(*in.Name), desc, priority, enabled).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			jsonErr(w, http.StatusConflict, "conflict", "a group with that name already exists")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "insert failed")
		return
	}
	if in.Rules != nil {
		if err := s.writeRules(ctx, tx, id, *in.Rules); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "rules failed")
			return
		}
	}
	after, err := s.loadClientGroups(ctx, tx, id)
	if err != nil || len(after) == 0 {
		jsonErr(w, http.StatusInternalServerError, "internal", "read back failed")
		return
	}
	if err := s.recordGroupChange(ctx, tx, id, "CREATED", s.actor(r), in.Reason, nil, &after[0]); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "history failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	s.audit(r, "client_group.created", "client_group", id, map[string]any{"name": after[0].Name, "rules": len(after[0].Rules)})
	writeJSON(w, http.StatusCreated, after[0])
}

func (s *server) patchClientGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in clientGroupWrite
	if err := decodeJSON(r, &in); err != nil {
		jsonErr(w, http.StatusBadRequest, "bad_request", "bad body")
		return
	}
	if msg := validateClientGroupWrite(in, false); msg != "" {
		jsonErr(w, http.StatusBadRequest, "validation", msg)
		return
	}
	ctx, cancel := dbCtx(r)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "begin failed")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	before, err := s.loadClientGroups(ctx, tx, id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	if len(before) == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "group not found")
		return
	}
	var desc *string
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		desc = &d
	}
	var name *string
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		name = &n
	}
	if _, err := tx.Exec(ctx, `UPDATE iam_v2.client_groups SET
			name = COALESCE($3, name), description = COALESCE($4, description),
			priority = COALESCE($5, priority), enabled = COALESCE($6, enabled), updated_at = now()
		 WHERE tenant_id=$1 AND site_id=$2 AND id=$7::uuid`,
		s.tenantID, s.siteID, name, desc, in.Priority, in.Enabled, id); err != nil {
		if isUniqueViolation(err) {
			jsonErr(w, http.StatusConflict, "conflict", "a group with that name already exists")
			return
		}
		jsonErr(w, http.StatusInternalServerError, "internal", "update failed")
		return
	}
	if in.Rules != nil {
		if err := s.writeRules(ctx, tx, id, *in.Rules); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "rules failed")
			return
		}
	}
	after, err := s.loadClientGroups(ctx, tx, id)
	if err != nil || len(after) == 0 {
		jsonErr(w, http.StatusInternalServerError, "internal", "read back failed")
		return
	}
	if err := s.recordGroupChange(ctx, tx, id, "UPDATED", s.actor(r), in.Reason, &before[0], &after[0]); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "history failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	s.audit(r, "client_group.updated", "client_group", id, map[string]any{"name": after[0].Name, "rules": len(after[0].Rules)})
	writeJSON(w, http.StatusOK, after[0])
}

// packagesNamingGroup lists the active packages whose CURRENT revision names this group as its audience.
//
// Through the scoped reader iam_v2.p2_package_current_conditions, never a direct read of
// package_eligibility_rules: edged holds INSERT on that table for publishing and deliberately no SELECT, so a
// direct join is a permission error that would have made every group undeletable (found live).
func (s *server) packagesNamingGroup(ctx context.Context, q rowQuerier, groupID string) ([]string, error) {
	rows, err := q.Query(ctx, `
		SELECT p.code FROM iam_v2.internet_packages p
		 WHERE p.tenant_id=$1 AND p.site_id=$2 AND p.active AND COALESCE(p.is_system,false)=false
		   AND p.current_revision_id IS NOT NULL
		   AND EXISTS (SELECT 1 FROM iam_v2.p2_package_current_conditions($1,$2,p.id) c
		                WHERE c.kind='RULE' AND c.rule_type='CLIENT_GROUP' AND c.value->'group_ids' ? $3)
		 ORDER BY 1`, s.tenantID, s.siteID, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if rows.Scan(&c) == nil {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out, rows.Err()
}

func (s *server) deleteClientGroup(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var in clientGroupWrite
	_ = decodeJSON(r, &in) // an optional reason
	ctx, cancel := dbCtx(r)
	defer cancel()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "begin failed")
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	before, err := s.loadClientGroups(ctx, tx, id)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	if len(before) == 0 {
		jsonErr(w, http.StatusNotFound, "not_found", "group not found")
		return
	}
	// A group that is still somebody's audience is not deletable: the package would silently offer itself to
	// nobody. The operator changes the package first, then deletes the group.
	if named, err := s.packagesNamingGroup(ctx, tx, id); err == nil && len(named) > 0 {
		jsonErr(w, http.StatusConflict, "in_use", "this group is the audience of: "+strings.Join(named, ", ")+". Change those packages first.")
		return
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		jsonErr(w, http.StatusInternalServerError, "internal", "audience check failed")
		return
	}
	if err := s.recordGroupChange(ctx, tx, id, "DELETED", s.actor(r), in.Reason, &before[0], nil); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "history failed")
		return
	}
	if _, err := tx.Exec(ctx, `DELETE FROM iam_v2.client_groups WHERE tenant_id=$1 AND site_id=$2 AND id=$3::uuid`, s.tenantID, s.siteID, id); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "delete failed")
		return
	}
	if err := tx.Commit(ctx); err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "commit failed")
		return
	}
	s.audit(r, "client_group.deleted", "client_group", id, map[string]any{"name": before[0].Name})
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) clientGroupChanges(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := dbCtx(r)
	defer cancel()
	rows, err := s.db.Query(ctx, `
		SELECT id::text, group_id::text, action, changed_by, change_reason,
		       COALESCE(before,'null'::jsonb)::text, COALESCE(after,'null'::jsonb)::text,
		       to_char(changed_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		  FROM iam_v2.client_group_changes WHERE tenant_id=$1 AND site_id=$2
		 ORDER BY changed_at DESC LIMIT 200`, s.tenantID, s.siteID)
	if err != nil {
		jsonErr(w, http.StatusInternalServerError, "internal", "query failed")
		return
	}
	defer rows.Close()
	type change struct {
		ID        string          `json:"id"`
		GroupID   string          `json:"group_id"`
		Action    string          `json:"action"`
		ChangedBy string          `json:"changed_by"`
		Reason    string          `json:"reason"`
		Before    json.RawMessage `json:"before"`
		After     json.RawMessage `json:"after"`
		ChangedAt string          `json:"changed_at"`
	}
	out := []change{}
	for rows.Next() {
		var c change
		var b, a string
		if err := rows.Scan(&c.ID, &c.GroupID, &c.Action, &c.ChangedBy, &c.Reason, &b, &a, &c.ChangedAt); err != nil {
			jsonErr(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		c.Before, c.After = json.RawMessage(b), json.RawMessage(a)
		out = append(out, c)
	}
	writeList(w, out)
}
