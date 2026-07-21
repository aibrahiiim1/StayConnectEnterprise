# Phase-3 Controlled-Writer Privilege Manifest — PREPARED, **NOT APPLIED**

> **Status: PREPARED ONLY.** Nothing in this document is executed by migration 0010 or by any Phase-3 code.
> Phase 3 is DARK and holds **zero runtime `iam_v2` privileges** (table, sequence and function EXECUTE) — an
> invariant asserted by `iam_v2_scratch/phase3_0010_lifecycle.sh`. Every grant below is a **separately gated
> Gate-P/cutover action requiring exact Product-Owner authorization**. Do not apply any of it during Phase 3.

## 1. Why a controlled writer exists

Authoritative Phase-3 state (Entitlement status + its append-only history, the site Grace policy, the system
Emergency catalog) must never be writable by a runtime role holding ordinary table DML. The controlled writers
are narrow `SECURITY DEFINER` functions, so inside them `current_user` is the schema owner while outside it is
the caller — an authorization boundary a caller **cannot** forge (unlike a session GUC, which was removed).

Guards enforcing it: `p3_controlled_writer_only` on `entitlements(status)`,
`entitlement_state_transitions(INSERT)` and `site_checkout_grace_config(INSERT OR UPDATE OR DELETE)` — there is
no approved ordinary DELETE of the authoritative grace-config row, and DELETE is deliberately **not** silently
downgraded into a "disable" (a future reset/disable must be its own audited, PO-approved API). Each protected
family resolves the **exact owner of its own approved controlled function**, looked up inline from the catalog
by unambiguous `regprocedure` signature (never a bare name, and never a caller-controlled GUC /
`application_name` / role string / payload flag), failing **closed** if the function or its owner cannot be
resolved. Behind that boundary, the deferred `p3_entitlement_status_coherent` constraint is defense-in-depth.

## 1a. Proof status (disposable PG16; nothing persisted)

| Property | Status |
|---|---|
| Per-family controlled-function **owner mapping** | **PROVEN** — each family follows its own function's owner; cross-family writes refused *even after temporarily granting the wrong owner raw table DML*, in both directions |
| **EXECUTE-only caller path** (CONNECT + schema USAGE + EXECUTE only, **zero** direct table privileges) | **PROVEN** — LOGIN callers that cannot even `SELECT` the tables drive both controlled operations end-to-end; positive callers were not `postgres`, a superuser, or the function owner |
| **COMMIT-time deferred checker** compatibility under least privilege | **PROVEN** — `p3_entitlement_status_coherent` runs read-only as its own narrow `SECURITY DEFINER` ownership, so an EXECUTE-only caller needs no table SELECT; an incoherent/forged transaction still fails at COMMIT |
| Exact **column-level** owner privileges | **PROVEN** — owners cannot mutate pinned columns (`stay_id`, `package_revision_id`) |
| **Persistent Gate-P / runtime grants** | **PREPARED BUT NOT APPLIED** — baseline ownership restored, every temporary owner/caller/probe role and grant destroyed, service roles re-verified at zero `iam_v2` table **and** function-EXECUTE privileges |

**SECURITY DEFINER allowlist.** The only allowlisted `p3_*` trigger function is the **read-only**
`p3_entitlement_status_coherent`, with a fixed `search_path` and PUBLIC EXECUTE revoked. Every other `p3_*` guard
remains SECURITY INVOKER, and the gate additionally asserts that **every** `SECURITY DEFINER` function in
`iam_v2` pins a fixed `search_path`. (The controlled writers themselves —
`apply_entitlement_transition`, `publish_checkout_grace_config`, `bootstrap_emergency_grace` — are the separate,
non-`p3_*` definer functions this manifest governs.)

## 2. Current DARK ownership — an explicit implementation foundation

Today the controlled writers are owned by the broad schema owner `iam_v2_owner` (NOSUPERUSER). **This is a DARK
implementation foundation, not the final capability model.** Because a `SECURITY DEFINER` function runs with its
owner's authority, granting EXECUTE while the owner is the broad schema owner would expose more capability than
each operation needs.

**Before any EXECUTE grant, Gate-P MUST do one of:**

- **Preferred —** create a dedicated **`NOLOGIN` controlled-writer owner** per function family, holding only the
  minimum underlying table/sequence privileges that family needs (e.g. an entitlement-transition owner with
  `UPDATE(status, activated_at, terminal_reason, terminated_at)` on `entitlements` + `INSERT` on
  `entitlement_state_transitions`, and nothing else); reassign the functions to it.
- **Or —** a formally reviewed equivalent capability-owner design proving the schema owner's broader powers
  cannot be reached through any callable function.

Constraints that hold in both cases: fixed `search_path`, no dynamic SQL, `PUBLIC` EXECUTE revoked, owner
NOSUPERUSER, no default-ACL leakage, and **no conversion of the migration/apply role to SUPERUSER or to broad
public-schema rights**.

## 3. Prepared grant manifest (do NOT apply in Phase 3)

| Operation | Eventual caller capability | Never callable by |
|---|---|---|
| `apply_entitlement_transition` | the Entitlement-lifecycle capability (Commerce/Checkout/expiry writer) | Hotel-Admin UI roles; `pmsd`; any read-only role |
| `publish_checkout_grace_config` | Hotel-Admin **Grace-policy publication** capability only | `scd`, `portald`, `acctd`, `pmsd` |
| `bootstrap_emergency_grace` | **Deployment/System-admin capability only** | `scd`, `portald`, `acctd`, `pmsd`, **and all Hotel-Admin runtime roles** |
| alert-action writer (when added) | Hotel-Admin alert-management capability (RBAC + step-up) | all service roles |
| device authorization/deauthorization writer (when added) | the session/enforcement capability | Hotel-Admin UI roles |

## 4. Capability validation the final APIs must perform — BINDING PRE-GRANT REQUIREMENTS

Running as the owner is **not** authorization. The owner-identity boundary and the EXECUTE-only caller path are
now proven (§1a), but that only establishes *who may call*; it does **not** establish *what the call may do*.
Each requirement below is a **precondition on the grant itself** — no EXECUTE grant may be applied at Gate-P
until the corresponding API enforces it:

**Entitlement transition** — expected Tenant/Site; the target Entitlement belongs to that scope; expected current
state/version (optimistic); the transition is permitted for the calling capability; cross-scope UUID use is
rejected; bounded machine reason only (no free-form text or PII); exactly one concurrent winner.

**Grace-config publication** — a Hotel-Admin actor in the same Tenant/Site; RBAC + step-up; an optimistic
expected `config_version`; complete Package/Plan graph validation; immutable publication audit (actor + reason).

**Emergency bootstrap** — Deployment/System-admin capability only; never reachable from ordinary runtime or
Hotel-Admin roles.

## 5. Gate-P test obligations (future)

Gate-P must prove **both** directions for every grant: positive authorized execution, and negative
cross-service / cross-scope / cross-tenant execution refusal — plus a re-assertion that no unintended runtime
privilege was introduced.
