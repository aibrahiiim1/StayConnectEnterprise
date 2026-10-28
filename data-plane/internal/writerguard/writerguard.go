// Package writerguard verifies at startup that the controlled-writer boundary is in force for this process.
// The boundary needs both SECURITY DEFINER operations that own the writes and table triggers that refuse
// everyone else; a database missing the triggers, or a process connected as the operations' owner (every guard
// passes trivially), accepts raw writes silently. The check is a startup one because the property belongs to
// the schema and connection, and a service that cannot uphold it must not run.
package writerguard

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is satisfied by *pgxpool.Pool, a pgx.Conn or a transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Tier is how a family's boundary is enforced. The tiers are not equivalent; recording them here makes a
// downgrade a visible edit.
type Tier int

const (
	// TierOperationOwned: the SECURITY DEFINER operation performs the write, so only its owner ever writes
	// the row, not even the service that calls it.
	TierOperationOwned Tier = iota
	// TierCapabilityScoped: the operation is multi-statement service logic, so the service writes under a scope
	// only an approved opener can create. It proves the writer held EXECUTE on the opener, not that the
	// statement was the intended one.
	TierCapabilityScoped
)

func (t Tier) String() string {
	if t == TierCapabilityScoped {
		return "capability-scoped"
	}
	return "operation-owned"
}

// Requirement is one controlled-writer family: the approved operation and the tables only it may write.
type Requirement struct {
	Family   string
	Function string // exact regprocedure signature; a bare name is ambiguous under overloads
	Tables   []string
	Tier     Tier
	// Capability is the family name passed to iam_v2.begin_controlled_operation (TierCapabilityScoped only).
	Capability string
}

// Phase3Requirements is checked in full by every Phase-3 service, so none starts on a database where another
// family's guards were dropped.
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
		// The canonical audited grace publication boundary. Its actor validation, expected_version check and
		// ledger append are properties of this function, so a replaced, re-owned or PUBLIC-executable version
		// would record nothing yet look complete; checking it makes such a database refuse to run. The family
		// name is distinct from the capability-scoped grace_publication (the ledger) so one requirement is not
		// read as covering both.
		{
			Family:   "grace_policy_publication",
			Function: "iam_v2.publish_checkout_grace_policy(uuid,uuid,uuid,int,int,int,bigint,int,text,int,int,uuid,text)",
			Tables:   []string{"site_checkout_grace_config"},
		},
		{
			Family:   "accounting",
			Function: "iam_v2.ingest_absolute_counters(uuid,uuid,uuid,uuid,text,int,bigint,bigint,bigint,timestamptz)",
			Tables: []string{
				"accounting_records", "accounting_checkpoints", "delayed_accounting_records", "sessions",
			},
		},
		{
			Family:   "class_generation",
			Function: "iam_v2.allocate_class_generation(uuid,uuid,uuid)",
			Tables:   []string{"appliance_class_generation"},
		},
		{
			Family:   "auth_offers",
			Function: "iam_v2.record_auth_context_offer(uuid,uuid,uuid,uuid,int,bigint,timestamptz)",
			Tables:   []string{"auth_context_offers"},
		},
		// An `active` Session claims the kernel is enforcing now, which only the enforcement owner can know.
		// It shares the sessions table with accounting, which is guarded once by one trigger.
		{
			Family:   "session_enforcement",
			Function: "iam_v2.activate_session_enforcement(uuid,uuid,uuid,text,int,bigint)",
			Tables:   []string{"sessions"},
		},
		// Capability-scoped: multi-statement service logic whose invariants live in the service, so the scope is
		// the boundary rather than a second PL/pgSQL implementation. The SQL operations writing these families
		// declare their own scope first, so they keep working when each function gets its own owner.
		{
			Family:     "auth_context",
			Function:   "iam_v2.begin_controlled_operation(text)",
			Tables:     []string{"auth_contexts"},
			Tier:       TierCapabilityScoped,
			Capability: "auth_context",
		},
		{
			Family:     "device_auth",
			Function:   "iam_v2.begin_controlled_operation(text)",
			Tables:     []string{"entitlement_device_authorizations"},
			Tier:       TierCapabilityScoped,
			Capability: "device_auth",
		},
		{
			Family:     "session_binding",
			Function:   "iam_v2.begin_controlled_operation(text)",
			Tables:     []string{"session_entitlement_bindings"},
			Tier:       TierCapabilityScoped,
			Capability: "session_binding",
		},
		{
			Family:     "grace_publication",
			Function:   "iam_v2.begin_controlled_operation(text)",
			Tables:     []string{"checkout_grace_policy_publications"},
			Tier:       TierCapabilityScoped,
			Capability: "grace_publication",
		},
		{
			Family:     "alert",
			Function:   "iam_v2.begin_controlled_operation(text)",
			Tables:     []string{"checkout_grace_alert_actions"},
			Tier:       TierCapabilityScoped,
			Capability: "alert",
		},
		{
			Family:     "stay",
			Function:   "iam_v2.begin_controlled_operation(text)",
			Tables:     []string{"stays", "stay_events"},
			Tier:       TierCapabilityScoped,
			Capability: "stay",
		},
		{
			Family:     "auth_resolution",
			Function:   "iam_v2.begin_controlled_operation(text)",
			Tables:     []string{"auth_resolutions"},
			Tier:       TierCapabilityScoped,
			Capability: "auth_resolution",
		},
		{
			Family:     "commerce_intent",
			Function:   "iam_v2.begin_controlled_operation(text)",
			Tables:     []string{"offer_quotes", "purchases"},
			Tier:       TierCapabilityScoped,
			Capability: "commerce_intent",
		},
		{
			Family:     "checkout_conversion",
			Function:   "iam_v2.begin_controlled_operation(text)",
			Tables:     []string{"checkout_grace_audit", "entitlement_boundary_watermarks"},
			Tier:       TierCapabilityScoped,
			Capability: "checkout_conversion",
		},
		{
			Family:     "source_conflict",
			Function:   "iam_v2.begin_controlled_operation(text)",
			Tables:     []string{"pms_source_conflicts"},
			Tier:       TierCapabilityScoped,
			Capability: "source_conflict",
		},
		// The scope table is operation-owned: a capability that could write its own token would authorise nothing.
		{
			Family:   "operation_scope",
			Function: "iam_v2.begin_controlled_operation(text)",
			Tables:   []string{"controlled_operation_scope"},
		},
	}
}

// Verify returns nil only when every requirement holds; errors name the exact defect.
func Verify(ctx context.Context, q Querier, reqs []Requirement) error {
	if err := verifyRole(ctx, q); err != nil {
		return err
	}
	// Several families share one operation; its catalog shape is checked once.
	checkedFn := map[string]bool{}
	checkedCap := map[string]bool{}
	for _, r := range reqs {
		if !checkedFn[r.Function] {
			if err := verifyFunction(ctx, q, r); err != nil {
				return err
			}
			checkedFn[r.Function] = true
		}
		if r.Tier == TierCapabilityScoped && !checkedCap[r.Function+"|"+r.Capability] {
			if err := verifyCapability(ctx, q, r); err != nil {
				return err
			}
			checkedCap[r.Function+"|"+r.Capability] = true
		}
		for _, tbl := range r.Tables {
			if err := verifyTrigger(ctx, q, r, tbl); err != nil {
				return err
			}
		}
	}
	return nil
}

// verifyRole refuses a role whose attributes skip the guards: a superuser, BYPASSRLS or replication role.
func verifyRole(ctx context.Context, q Querier) error {
	var super, bypass, repl bool
	if err := q.QueryRow(ctx, `
		SELECT rolsuper, rolbypassrls, rolreplication FROM pg_roles WHERE rolname = current_user`).Scan(&super, &bypass, &repl); err != nil {
		return fmt.Errorf("phase3 writer boundary: cannot inspect this process's database role: %w", err)
	}
	if super || bypass || repl {
		return fmt.Errorf("phase3 writer boundary: this process's database role is a superuser, BYPASSRLS or replication role — " +
			"it could bypass every controlled operation, so the boundary is not in force")
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
		// Without SECURITY DEFINER the table guards would reject the operation's own writes.
		return fmt.Errorf("phase3 writer boundary: %s is not SECURITY DEFINER", r.Function)
	case !strings.Contains(config, "search_path="):
		// An unpinned search_path on SECURITY DEFINER lets the caller choose what unqualified names resolve to.
		return fmt.Errorf("phase3 writer boundary: %s does not pin its search_path", r.Function)
	case publicExec:
		return fmt.Errorf("phase3 writer boundary: PUBLIC holds EXECUTE on %s", r.Function)
	case callerIsWriter:
		// This process is or can become the owner, so every guard passes trivially for it.
		return fmt.Errorf(
			"phase3 writer boundary: this process's database role is a member of %q, the owner of %s — "+
				"it could bypass every controlled operation, so the boundary is not in force",
			owner, r.Function)
	}
	return nil
}

// verifyCapability checks this process can open a scope for the family; otherwise the service would start and
// fail at its first authoritative write (a PMS event or a guest connecting).
func verifyCapability(ctx context.Context, q Querier, r Requirement) error {
	if r.Capability == "" {
		return fmt.Errorf("phase3 writer boundary: capability-scoped family %q names no capability", r.Family)
	}
	var canOpen bool
	if err := q.QueryRow(ctx, `
		SELECT COALESCE(has_function_privilege(current_user, to_regprocedure($1)::oid, 'EXECUTE'), false)`,
		r.Function).Scan(&canOpen); err != nil {
		return fmt.Errorf("phase3 writer boundary: cannot inspect EXECUTE on %s: %w", r.Function, err)
	}
	if !canOpen {
		return fmt.Errorf(
			"phase3 writer boundary: this process cannot open a %q operation (no EXECUTE on %s), "+
				"so every authoritative write in that family would be refused at the first attempt",
			r.Capability, r.Function)
	}
	return nil
}

// verifyTrigger requires a guard that fires in normal operation ('O' origin or 'A' always; 'R' fires only in replica mode, 'D' never) as a
// BEFORE ROW trigger, and refuses a role that owns the table, since an owner can switch the trigger off.
func verifyTrigger(ctx context.Context, q Querier, r Requirement, table string) error {
	var enabled, ownsTable bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (
		  SELECT 1 FROM pg_trigger t
		    JOIN pg_class c ON c.oid = t.tgrelid
		    JOIN pg_namespace n ON n.oid = c.relnamespace
		   WHERE n.nspname = 'iam_v2' AND c.relname = $1
		     AND NOT t.tgisinternal
		     AND t.tgfoid = to_regprocedure('iam_v2.p3_controlled_writer_only()')::oid
		     AND t.tgenabled IN ('O','A')
		     AND (t.tgtype::int & 3) = 3),
		       COALESCE((SELECT pg_has_role(current_user, c.relowner, 'USAGE')
		                   FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		                  WHERE n.nspname = 'iam_v2' AND c.relname = $1), false)`, table).Scan(&enabled, &ownsTable)
	if err != nil {
		return fmt.Errorf("phase3 writer boundary: cannot inspect iam_v2.%s: %w", table, err)
	}
	if ownsTable {
		return fmt.Errorf(
			"phase3 writer boundary: this process's database role owns iam_v2.%s and could disable its guard — "+
				"it could bypass every controlled operation, so the boundary is not in force", table)
	}
	if !enabled {
		// A disabled trigger is present, so an inventory lists it, but it refuses nothing.
		return fmt.Errorf(
			"phase3 writer boundary: iam_v2.%s has no ENABLED controlled-writer guard, so raw writes to %s state would be accepted",
			table, r.Family)
	}
	return nil
}

type Exec interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Capability names, so a typo cannot silently open no scope.
const (
	CapStay               = "stay"
	CapAuthResolution     = "auth_resolution"
	CapCommerceIntent     = "commerce_intent"
	CapCheckoutConversion = "checkout_conversion"
	CapSourceConflict     = "source_conflict"
	CapAuthContext        = "auth_context"
	CapDeviceAuth         = "device_auth"
	CapSessionBinding     = "session_binding"
	CapGracePublication   = "grace_publication"
	CapAlert              = "alert"
)

// Open declares that the current transaction performs the named family's authoritative operation. The scope
// is transaction-local, so it must be opened on the transaction that writes.
func Open(ctx context.Context, db Exec, capability string) error {
	if _, err := db.Exec(ctx, `SELECT iam_v2.begin_controlled_operation($1)`, capability); err != nil {
		return fmt.Errorf("phase3 writer boundary: cannot open a %q operation: %w", capability, err)
	}
	return nil
}

// OpenPhase5 is a separate call because the two openers have separate allowlists: a caller cannot reach the
// wrong boundary by passing a different string.
func OpenPhase5(ctx context.Context, db Exec, capability string) error {
	if _, err := db.Exec(ctx, `SELECT iam_v2.p5_begin_controlled_operation($1)`, capability); err != nil {
		return fmt.Errorf("phase5 writer boundary: cannot open a %q operation: %w", capability, err)
	}
	return nil
}
