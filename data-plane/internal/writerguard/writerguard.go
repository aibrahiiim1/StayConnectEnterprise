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
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier is the narrow surface this package needs — satisfied by *pgxpool.Pool, a pgx.Conn or a transaction,
// so the check can run wherever the caller already has a connection.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Tier is HOW a family's boundary is enforced. The two are not equivalent, and the difference is recorded
// here so that a family cannot be quietly moved from the stronger form to the weaker one: downgrading one is
// a visible edit to this list, not an invisible change to a trigger body.
type Tier int

const (
	// TierOperationOwned: the authoritative write is performed BY the SECURITY DEFINER operation, so the
	// operation's owner is the only role that ever appears as the writer. Nothing else can write the row at
	// all — not even the service that legitimately calls the operation.
	TierOperationOwned Tier = iota
	// TierCapabilityScoped: the authoritative operation is multi-statement service logic, so the write is
	// performed by the service under a scope only an approved opener can create. This proves the write came
	// from a role holding EXECUTE on that family's opener, inside a declared operation for that family. It
	// does NOT prove the write is the exact statement the operation intended — a service that may write
	// Stays can, inside its own declared Stay operation, write a wrong Stay.
	TierCapabilityScoped
)

func (t Tier) String() string {
	if t == TierCapabilityScoped {
		return "capability-scoped"
	}
	return "operation-owned"
}

// Requirement is one controlled-writer family: the approved operation, and the tables whose authoritative
// writes only it may perform.
type Requirement struct {
	Family   string
	Function string // exact regprocedure signature — never a bare name, which overloads make ambiguous
	Tables   []string
	Tier     Tier
	// Capability is the family name passed to iam_v2.begin_controlled_operation. Set only for
	// TierCapabilityScoped requirements.
	Capability string
}

// Phase3Requirements is the complete set. It is deliberately checked in full by every Phase-3 service rather
// than per-service: the boundary is a property of the schema, and a service that verified only "its own"
// families would happily start on a database where another family's guards had been dropped.
func Phase3Requirements() []Requirement {
	return []Requirement{
		// ---- operation-owned: the function performs the write itself -------------------------------------
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
		// ---- capability-scoped: the operation is service logic, the scope is the boundary ----------------
		// Every one of these is a family whose authoritative operation spans several statements with their own
		// invariants — a Stay lifecycle transition, a checkout conversion, a quote/purchase pair, consuming an
		// Auth Context, closing a device-authorization interval. Reimplementing them in PL/pgSQL purely to move
		// the writer identity would create a second implementation of rules that already exist, tested, in the
		// service — and two implementations of one rule is how the rules start to differ.
		//
		// The SQL operations that write these families (issue_or_return_pms_context, rebind_session_entitlement,
		// record_alert_action, …) declare their own scope as their first statement, so they keep working when
		// Gate-P gives each function its own owner.
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
		// The scope table itself is operation-owned, not capability-scoped — a capability that could
		// authorise writing its own token would authorise nothing.
		{
			Family:   "operation_scope",
			Function: "iam_v2.begin_controlled_operation(text)",
			Tables:   []string{"controlled_operation_scope"},
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
		if r.Tier == TierCapabilityScoped {
			if err := verifyCapability(ctx, q, r); err != nil {
				return err
			}
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

// verifyCapability checks the half of a capability-scoped family that verifyFunction cannot: this process must
// be ABLE to open a scope for it.
//
// Without this the service starts happily and fails at the first authoritative write — which for the Stay
// family means the first PMS event of the day, and for the commerce family means the first guest trying to
// connect. A boundary that is discovered to be misconfigured by a guest is a boundary that gets disabled.
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

// Exec is the narrow surface Open needs. *pgxpool.Pool, a pgx.Conn and a pgx.Tx all satisfy it.
type Exec interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Capability names, so a caller cannot open a scope by typing a string that only looks right. A typo would
// otherwise open no scope at all, and the family's first write would be refused at run time, on the path that
// matters, rather than here.
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

// Open declares that the current transaction is performing the named family's authoritative operation.
//
// It MUST be called on the transaction that will do the writing: the scope is transaction-local, so opening
// it on the pool and writing on a transaction (or the reverse) declares a scope nothing will ever see. That
// is also why it takes an Exec rather than a pool — the type makes the mistake harder to write.
func Open(ctx context.Context, db Exec, capability string) error {
	if _, err := db.Exec(ctx, `SELECT iam_v2.begin_controlled_operation($1)`, capability); err != nil {
		return fmt.Errorf("phase3 writer boundary: cannot open a %q operation: %w", capability, err)
	}
	return nil
}
