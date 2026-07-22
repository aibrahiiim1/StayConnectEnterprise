// Package writerguard verifies, at startup, that the Phase-3 controlled-writer boundary is actually in force
// for THIS process — before the process is allowed to serve anything.
//
// The boundary is built from two halves that only work together: SECURITY DEFINER operations that own the
// authoritative writes, and table triggers that refuse anyone else. Either half alone is worthless. A database
// where the triggers were never applied (an older migration, a hand-repaired schema, a restored dump) accepts
// raw writes silently — every Phase-3 write still "succeeds", and nothing anywhere says the guarantees are
// gone. Worse is a process connected as the operations' OWNER: every guard passes trivially, so the boundary
// exists on paper and constrains nothing. That is the mixed legacy/raw writer mode this package refuses.
//
// It is a startup check rather than a per-write one deliberately. The property being verified is a property of
// the schema and the connection, not of any individual statement, and a service that cannot uphold it should
// not run at all — a partially-enforced boundary is the state nobody can reason about afterwards.
package writerguard

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Querier is the narrow surface this package needs — satisfied by *pgxpool.Pool, a pgx.Conn or a transaction,
// so the check can run wherever the caller already has a connection.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Requirement is one controlled-writer family: the approved operation, and the tables whose authoritative
// writes only it may perform.
type Requirement struct {
	Family   string
	Function string // exact regprocedure signature — never a bare name, which overloads make ambiguous
	Tables   []string
}

// Phase3Requirements is the complete set. It is deliberately checked in full by every Phase-3 service rather
// than per-service: the boundary is a property of the schema, and a service that verified only "its own"
// families would happily start on a database where another family's guards had been dropped.
func Phase3Requirements() []Requirement {
	return []Requirement{
		{
			Family:   "entitlement",
			Function: "iam_v2.apply_entitlement_transition(uuid,text,timestamptz,text)",
			Tables:   []string{"entitlements", "entitlement_state_transitions"},
		},
		{
			Family:   "grace_config",
			Function: "iam_v2.publish_checkout_grace_config(uuid,uuid,uuid,int,int,int,bigint,int,text,int)",
			Tables:   []string{"site_checkout_grace_config"},
		},
		{
			Family:   "accounting",
			Function: "iam_v2.ingest_absolute_counters(uuid,uuid,uuid,uuid,text,int,bigint,bigint,bigint,timestamptz)",
			Tables: []string{
				"accounting_records", "accounting_checkpoints", "delayed_accounting_records", "sessions",
			},
		},
	}
}

// Verify returns nil only when every requirement holds. The errors name the exact defect, because "Phase 3
// failed to start" without a reason is the kind of message that gets worked around at 3am.
func Verify(ctx context.Context, q Querier, reqs []Requirement) error {
	for _, r := range reqs {
		if err := verifyFunction(ctx, q, r); err != nil {
			return err
		}
		for _, tbl := range r.Tables {
			if err := verifyTrigger(ctx, q, r, tbl); err != nil {
				return err
			}
		}
	}
	return nil
}

func verifyFunction(ctx context.Context, q Querier, r Requirement) error {
	var exists, secdef, publicExec, callerIsWriter bool
	var config string
	var owner string
	err := q.QueryRow(ctx, `
		SELECT p.oid IS NOT NULL,
		       COALESCE(p.prosecdef,false),
		       COALESCE(array_to_string(p.proconfig,','),''),
		       COALESCE(pg_get_userbyid(p.proowner),''),
		       COALESCE(has_function_privilege('public', p.oid, 'EXECUTE'), false),
		       COALESCE(pg_has_role(current_user, p.proowner, 'USAGE'), false)
		  FROM (SELECT to_regprocedure($1)::oid AS oid) t
		  LEFT JOIN pg_proc p ON p.oid = t.oid
		`, r.Function).Scan(&exists, &secdef, &config, &owner, &publicExec, &callerIsWriter)
	if err != nil {
		return fmt.Errorf("phase3 writer boundary: cannot inspect %s: %w", r.Function, err)
	}
	switch {
	case !exists:
		return fmt.Errorf("phase3 writer boundary: the approved %s operation %s does not exist", r.Family, r.Function)
	case !secdef:
		// Without SECURITY DEFINER the operation runs as the caller, so the table guards it is supposed to
		// satisfy would reject its own writes — and every Phase-3 write would fail in production.
		return fmt.Errorf("phase3 writer boundary: %s is not SECURITY DEFINER", r.Function)
	case !strings.Contains(config, "search_path="):
		// A SECURITY DEFINER function with a caller-controlled search_path is a privilege-escalation
		// primitive: the caller decides which schema its unqualified names resolve to.
		return fmt.Errorf("phase3 writer boundary: %s does not pin its search_path", r.Function)
	case publicExec:
		return fmt.Errorf("phase3 writer boundary: PUBLIC holds EXECUTE on %s", r.Function)
	case callerIsWriter:
		// THE MIXED-MODE REFUSAL. This process is (or can become) the operation's owner, so every guard on
		// every table it protects passes trivially for anything it does. The boundary would be decoration.
		return fmt.Errorf(
			"phase3 writer boundary: this process's database role is a member of %q, the owner of %s — "+
				"it could bypass every controlled operation, so the boundary is not in force",
			owner, r.Function)
	}
	return nil
}

func verifyTrigger(ctx context.Context, q Querier, r Requirement, table string) error {
	var enabled bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM pg_trigger t
		    JOIN pg_class c ON c.oid = t.tgrelid
		    JOIN pg_namespace n ON n.oid = c.relnamespace
		   WHERE n.nspname = 'iam_v2' AND c.relname = $1
		     AND NOT t.tgisinternal
		     AND t.tgfoid = to_regprocedure('iam_v2.p3_controlled_writer_only()')::oid
		     AND t.tgenabled <> 'D')`, table).Scan(&enabled)
	if err != nil {
		return fmt.Errorf("phase3 writer boundary: cannot inspect iam_v2.%s: %w", table, err)
	}
	if !enabled {
		// A disabled trigger is the dangerous case: it is present, so an inventory would list it, but it
		// refuses nothing.
		return fmt.Errorf(
			"phase3 writer boundary: iam_v2.%s has no ENABLED controlled-writer guard, so raw writes to %s state would be accepted",
			table, r.Family)
	}
	return nil
}
