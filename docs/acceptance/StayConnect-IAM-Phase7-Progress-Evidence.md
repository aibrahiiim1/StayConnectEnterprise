# Phase 7 — full-system re-acceptance: progress evidence

**Phase 7 is `ACCEPTED_AND_CLOSED`** at **VERIFIED FULL-SYSTEM LIVE-DARK** maturity — Product-Owner decision
**D27** (2026-08-17), transition **T0064**, delivery head `16819aa027633b84486999451e8b689a191a15d2`. PR #15 was **MERGED** to master under
Product-Owner merge decision **D28** (transition **T0065**), merge commit
`9c57c2b5a29eb886cf317912a9eb6a6da8ccb603`; the merge introduced no content and deployed nothing. Originally authorized by **D26** (2026-08-16).
Branch `phase/7-full-system-reacceptance`, from post-Phase-6 master `9cb25b8`.

Scope reconciled from the FINAL contract §18 (Phase 7 = *cleanup, final docs/ops manual, full-system
re-acceptance*, gate = *complete matrix*) and its §19 A–G Acceptance & Failure-Drill Matrix. **Not** from
`MIGRATION_RUNBOOK.md` §"Phase 7 — Start edged" (a deployment step) or
`COMMERCIAL_ONBOARDING_EXECUTION_STATUS.md` (a separate commercial numbering).

---

## What is proven

### M1 — identity and acquisition, composed · **20/20**

`iam_v2_scratch/phase7_m1_identity_and_acquisition.sh`, run repeatedly and re-runnably against the Phase-6
scratch database. Discharges **D1** (two PMS namespaces, colliding room 101, no selector), **C3** (once per
stay), **C4** (a pinned revision is immutable under a live entitlement), **C** (one entitlement per purchase),
**A1/A2/A3** (shared window, third device refused against a limit of two, re-authorization burns no slot),
**A7** (no exit from TERMINATED), plus the two seams no phase gate can assert alone: the entitlement's stay,
interface and purchase agree, and the plan it is accounted against is the one the package revision sold.

**Mutation-checked.** Dropping `ent_live_stay` fails exactly C3 and nothing else; restoring it returns 20/20.

### M2 — the stay, end to end · **22/22**

`iam_v2_scratch/phase7_m2_the_stay_end_to_end.sh`. **F1** room move, **F2** stale events refused, **F3**
checkout ending the pre-checkout entitlement at the boundary with the reason recorded, **F5** grace granted as
a new entitlement with once-per-stay still holding, and Phase 6 composed: an existing entitlement keeps the
`VALIDITY_WINDOW` revision it pinned while the same plan publishes an `AGGREGATE_ONLINE_TIME` revision;
budgets come from the pinned revision; exhaustion terminates for `TIME`. The cross-namespace seam is checked
last: after a full checkout/grace cycle on stay A, stay B — same room number, other namespace — is untouched.

### M3 — the boundaries hold · **34/0**

`iam_v2_scratch/phase7_m3_boundaries.sh`, every privilege measured **as the real service role**. Financial
DARK expressed as privilege rather than as a flag; `E4b`'s `UNSET` confirmed as both a real value and the
default; no runtime role able to write an entitlement or session, delete an accounting record or device,
execute the boundary termination or apply an arbitrary transition; append-only checked by *attempting* the
write; the mixed-version catalog probe's premise verified; all 47 migrations confirmed to have a down
migration, counted from the filesystem.

**Mutation-checked.** Granting `svc_acctd` UPDATE on entitlements fails exactly that assertion; revoking it
returns the gate to green.

The gate was 31 cases when first recorded. It is **34/0** now, and the three added cases are the reason the
earlier number should not be quoted: one line used to read `ok "NOT PROVEN here: ..."`, incrementing the pass
count while its own text said nothing had been proved. The gate now seeds its own tenant, site, appliance and
operator — it promises to be fixture-free, and the scratch database has no such rows — seeds a real
product-setting change row, proves the row exists, and only then attempts the edit that must be refused.

### The reboot drill · **5/5**, from a real reboot with no operator action

`deploy/scripts/phase7-reboot-drill.sh`, on the development appliance. All six services converged to serving
in **10 seconds**, no unit latched failed, every unit enabled at boot, flag coherence green, and the guest
device route **ABSENT** in the `scd` that came back.

### The appliance, dark

Verified through the authoritative gate after every reboot in this phase: flag coherence 6/6, guest route
absent, exact dark Hotel Admin release by path and content hash, no synthetic state, no appliance with the
capability enabled.

---

## What is NOT proven, and why

**The complete cross-phase matrix is now GREEN, in strict mode.** This section previously recorded that it had
never run to completion; that is no longer true and the older text is not preserved as if it were.

```
PHASE7_FULL_MATRIX = PASS (strict)
gates_run=20  skipped=0  unverdicted_or_crashed=0  pass=1262  fail=0
```

Strict mode counts skips, missing gates, crashes and unparsable verdicts as failures, and the roster names
every gate that must run — so "nothing failed" cannot quietly mean "nothing ran".

It took five complete runs to get there. The first reported 24 failures and 8 gates with no verdict, and
**almost none of them were product defects**; each was fixed at its cause rather than tolerated:

- the matrix ran against a scratch database nobody could recreate, missing the deterministic fixture entirely,
  so six Phase-6 cases failed on foreign keys to an appliance and an operator that did not exist. The gate
  environment is now built from repository sources by `phase7_build_environment.sh`, proved equal to the
  appliance before anything runs against it, and is itself a roster gate;
- `pg_restore` failed on a foreign key nobody had touched: several Phase-6 gates seed under
  `session_replication_role = replica`, leaving 21 entitlements that violate four validated constraints, so
  that database cannot be restored from its own dump. That is a property of the DATA, and the gate now says so
  before dumping;
- nine Phase-4 failures were `TRANSPORT_HEARTBEAT_STALE` and its cascade — a twenty-minute gate outrunning its
  own fixture heartbeat;
- `phase4 db invariants` never ran at all (`rc=90`: the runner never passed `SCRATCH_PORT_ALLOW`) and was
  first-run-only, because its fixed UUIDs, idempotency keys and P numbers are each unique by design;
- M1/M2/M3 collapsed because the runner passed a database name but not a container.

**And the schema can be rebuilt from the repository alone.** `phase7_reconstruct_from_sources.sh` applies the
accepted history into a fresh private cluster and reaches semantic digest
`e7216a988642c9d5e44ca22478d4972d parts=2686` — identical to the DEVELOPMENT appliance across columns,
constraints with grouping preserved, indexes, triggers, function bodies and configuration, object ownership,
the complete role-security surface, memberships, and every grant and function privilege with no allowlist.

**Appliance-side M4 has now been run.** `deploy/scripts/phase7-appliance-m4.sh` is **70/0 with 3 NOT PROVEN**
against the real services, roles, listeners and schema: the DARK baseline captured before anything ran and
proved unchanged afterwards; the PUBLIC-executable definer finding attempted as the real `svc_scd` role and
shown inert; runtime-role boundaries; the financial core dark with zero postings, outbox rows, payments and
attempts; `scd` not mounting its Phase-6 endpoints at all while the portal returns the uniform non-success;
Hotel Admin on the expected release and closed to the unauthenticated; the appliance refusing the Central-only
names; guest and admin over one database; accounting, shaping and enforcement live; and a real
`pg_dump`/`pg_restore` into a fresh database that reproduces the same table and row counts and then removes
itself.

The three NOT PROVEN lines are counted as neither pass nor failure: a deliberate Central outage drill, a live
rollback of the appliance schema, and a real purge/archive with external receipt authority.

**The final reboot was real.** `deploy/scripts/phase7-final-reboot.sh` is **24/0**, and the kernel boot id
changed (`291095eb…` → `05461c40…`) because a service restart is not reboot evidence. Everything came back
with no operator action, and everything that was dark is still dark.

Phase 7 was subsequently **ACCEPTED AND CLOSED** on this evidence (D27 / T0064). The three NOT PROVEN items
were **not promoted** by that acceptance.

### A privilege observation, resolved

While investigating the corrupted database, `iam_v2.p6_data_crossing` — a `SECURITY DEFINER` function —
appeared executable by `PUBLIC`. Both authoritative sources were checked:

* **the migration set is correct**: `0041` does `REVOKE ALL ... FROM PUBLIC` and grants `svc_acctd`;
* **the appliance is correct**: its ACL is `{stayconnect=X/stayconnect, svc_acctd=X/stayconnect}`, and PUBLIC
  holds no EXECUTE.

The exposure existed only in the damaged scratch database, whose function had been re-created without its
grants. Recorded because it was checked rather than assumed, and because the Phase-6 foundation gate detecting
it is the gate working.

## D32 — the Hotel-Admin grace path, proven from HTTP to the checkout validator

The policy-driven publication path was implemented before it was wired: `PUT /edge/v1/commercial-packages/grace`
still called the raw writer, so in the running product a grace save recorded no actor, no reason code and no
version, appended no audit row, and pinned a package the operator had chosen rather than one derived to satisfy
`iam_v2.grace_package_mismatch_reason`. The guarantees existed in tests and not in the product.

Verified on the DEVELOPMENT appliance (172.21.60.23, DB `stayconnect_site`) against the running `edged`:

| Case | Result |
|---|---|
| `expected_version` omitted | `400 bad_request` — publication requires the version last read |
| stale `expected_version` (1 behind) | `409 GRACE_VERSION_CONFLICT: current version is 1 (caller expected 0)` |
| future `expected_version` (+7) | `409 GRACE_VERSION_CONFLICT` |
| `grace_duration_seconds: 0` | `400 validation` — duration must be within 1..604800 |
| `grace_device_limit_policy: ALLOW_ANY` | `400 validation` — only `REJECT_NEW_DEVICE` is implemented |
| free-text `reason_code` | `400 validation` — bounded machine code `^[A-Z][A-Z0-9_]{0,63}$` required |
| operator supplies `grace_package_revision_id` | `400 validation` — the field is retired; the system derives the package |
| valid publish at the current version | `200` → `config_version` 1→2, derived revision returned |
| the same request replayed | `409 GRACE_VERSION_CONFLICT` — the version moved |

The published result satisfies the exact validator, and the boundary recorded who changed it:

* `iam_v2.grace_package_mismatch_reason(...) IS NULL` for the live config (**ACCEPTED**);
* package `__system_checkout_grace`, `is_system = true`, `active`, and the pinned revision **is** its current
  one; revision `CHECKOUT_GRACE`, price 0, settlement exactly `{NOT_REQUIRED}`, duration policy
  `{end_mode: GRACE_AFTER_CHECKOUT, grace_duration_seconds: 1800, policy_version: CHECKOUT_GRACE_V1}`;
* the pinned plan revision carries exactly the published scalars with `time_accounting_mode = VALIDITY_WINDOW`;
* `iam_v2.checkout_grace_policy_publications` holds the row: `config_version`, `actor` = the **session**
  operator (never a body-supplied value), `reason_code`, the derived revision and a full `policy_snapshot`.

**Every legacy path is closed in code and in privilege.** As `svc_edged`: the raw writer returns
`permission denied for function publish_checkout_grace_config`, and a direct table write returns
`permission denied for table site_checkout_grace_config`. All four reserved catalogue codes
(`__system_checkout_grace`, `__system_checkout_grace_plan`, `__sys_emergency_grace_pkg__`,
`__sys_emergency_grace_plan__`) are refused by the operator publisher with one message, before any row is
touched, while an ordinary code publishes normally. Neither catalogue listing exposes a system object.

Startup now verifies the boundary itself. Granting `PUBLIC` EXECUTE on `publish_checkout_grace_policy` makes
`edged` refuse the Phase-3 surface — `phase3 writer boundary: PUBLIC holds EXECUTE on
iam_v2.publish_checkout_grace_policy(...)` — and revoking it restores service. A database where that function
had been re-owned or opened up would previously have started normally and gone on writing an audit trail that
looked complete.

After restarting all five services at this head, the published policy is unchanged (`config_version` 3,
validator **ACCEPTED**, exactly one system package): startup provisioning does not overwrite what an operator
published. Journal-derived restart counts for this boot are 0 for every service.

**Not proven here, and not fabricated:** the checkout→grace supersession step still requires a PMS checkout
event. It remains proven at integration level against a real database and **ENVIRONMENT-BLOCKED** on the
appliance. No byte-accounting or ARP/device-presence evidence is claimed.

### The closure was one service wide, not the system

The D32 retirement closed edged's raw path in code and in privilege. It did not close anyone else's. `svc_netd`
still held `EXECUTE` on `iam_v2.publish_checkout_grace_config`, granted under the rationale that grace
publication and offer recording were "both reached by the same Phase-3 surface" — they are not; publishing
grace policy is a Hotel-Admin commercial action performed by edged, and netd has never called that function.
Because netd never called it, the over-grant produced no error, no log line and no failing test: the design
said the raw path was unreachable and the database disagreed, silently.

Effective privileges now, over **every** login non-superuser role on the appliance:

| Role | raw writer EXECUTE | audited boundary EXECUTE | direct INSERT/UPDATE/DELETE |
|---|---|---|---|
| `svc_scd` | false | false | false |
| `svc_edged` | false | **true** | false |
| `svc_netd` | false | false | false |
| `svc_acctd` | false | false | false |
| `phase2_runner`, probe roles | false | false | false |
| `PUBLIC` | false | false | false |

Probed at runtime as each real role, with literal arguments so no table read could mask the result: all four
are refused with `permission denied for function publish_checkout_grace_config`. Only `svc_edged` enters the
audited boundary, and is then refused by the function's **own** actor validation — privilege and domain
validation are visibly separate layers.

**The Gate-P assertion is not vacuous.** The canonical bootstrap now ends in a fail-closed check that
enumerates roles from `pg_roles` rather than a hand-maintained list, so a runtime role added later is covered
the day it is created. It was proven against five deliberately violating states:

| Violation | Result |
|---|---|
| a NEW runtime role holds the raw writer | `GATE-P BLOCKER (D32): ... d32_probe:EXECUTE-on-raw-writer` |
| that role holds direct table DML instead | `... d32_probe:direct-DML-on-grace-config` |
| `PUBLIC` holds the audited boundary | `... PUBLIC:EXECUTE-on-audited-boundary` |
| every raw path closed **and** `svc_edged` cannot publish either | `... svc_edged cannot EXECUTE the audited boundary either, so grace policy cannot be published at all` |
| clean state, and again after re-applying the bootstrap | passes |

The fourth case is the one worth keeping: an everything-revoked database satisfies "no raw path is reachable"
perfectly while being unable to publish grace at all. Asserting only the first half would make a totally broken
system look maximally secure.

After the privilege change the canonical path still works end to end: `PUT /grace` published `config_version`
3 → 4 and `iam_v2.grace_package_mismatch_reason` returns NULL (**ACCEPTED**) for the result.

### A governance validator that refused a true sentence

Recording the trial state surfaced a false positive in the parity gate. `#?6` matches the "6" **inside**
"#16", so an accurate statement about the open PR #16 was reported as a stale claim about merged PR #6. That is
not harmless: the gate fails on correct text, and the cheapest way to make it pass again is to make the text
vaguer — the opposite of what the gate exists to enforce. The number boundary is fixed, a real "#6 is not
merged" is still caught, and an inverted case now pins both directions (72 negative cases, 0 failures).

## D33/T0072 — post-acceptance DEVELOPMENT hardening and Hotel-Admin usability sweep

Carried out on 172.21.60.23 as a real operator would, in a real browser, after the trial was accepted and
closed. Everything below was found by opening screens and using them, not by reading code.

### Guest Accounts crashed, and the reason was worse than the crash

The row renderer dereferenced `template_id`, which IAM-v2 accounts do not have, so the first real account
white-screened the page: `Cannot read properties of undefined (reading 'slice')`. The API was healthy
throughout, which is why it presented as a backend fault.

Underneath it, the create form still demanded a **Guest access plan** that IAM-v2 discards entirely — under
IAM-v2 what a guest may acquire is decided by package eligibility rules, not by a plan on the credential. An
operator choosing one believed they had decided what the guest gets. The screen could not tell which contract
it was talking to on a site with no accounts, so both list endpoints now state the authority on the envelope.

The complete supported lifecycle was then exercised against the real API: create with one-time password
reveal, read-back, patch, disable, read-back, set-password, disconnect, re-enable, duplicate username refused
`409`, delete, `404` after delete.

### The navigation "jumping back to the top" was the window scrolling

`min-h-screen` is a minimum, so a long page grew the layout past the viewport, the sidebar stretched with it,
and its `overflow-y-auto` never engaged. Reaching **WAN / LAN settings** meant scrolling the whole window;
clicking it navigated, Next.js reset the window to the top as it should, and the menu appeared to snap back.

Separately, `path.startsWith(href)` lit up more than one item: these are siblings, not parent and child —
`/network` is the Guest networks leaf — so `/network/dhcp` highlighted both it and **DHCP & leases**.

Verified live: **exactly one active item on all 38 screens**, sidebar scroll position `1082` before and after
navigating, window `scrollY` 0, content pane reset to 0.

### Seven screens answered 500, and one query could never have worked

| Screen | Cause |
|---|---|
| PMS interfaces, PMS routing, Source conflicts, Online time, Device self-service | `svc_edged` had no privilege on the Phase-3/4/5 `iam_v2` tables — the same missing-grant shape as the Phase-2 commerce surface |
| Stays | the listing ordered by `s.updated_at`, a column `iam_v2.stays` does not have |
| Licence | the screen read `/license/status` and posted to `/license/install`; edged serves `/license` for both |

The Stays defect survived because the route was **mounted** in the integration harness but never **called**.
A test now GETs every read-only collection, so the next projection that drifts from the schema fails in CI.

The licence read was wrapped in `.catch(() => null)`, so it failed silently and the page rendered as though
the appliance had no licence — the one thing a licensing screen must never say incorrectly.

### Financial screens that said "Loading…" forever

edged carries no `STAYCONNECT_PHASE4_*` configuration, so it mounts no financial routes, while the bundle is
built with `NEXT_PUBLIC_PHASE4_ADMIN=1`. Three of the four rendered their loading guard **before** their
error, so a 404 left the state unset and the screen claimed to still be working. They now say the capability
is not enabled here and that enabling it is a deployment decision. **Nothing was enabled.**

### PMS interfaces had no configuration surface at all

The screen could publish an existing revision and rotate a credential. It could not create an interface and
could not author a revision, so the **endpoint the connector dials (host:port)** and every timeout, bound,
mode, timezone and folio strategy were unreachable from the product. Every interface on this appliance exists
because a seed script wrote it into the database.

Two endpoints now exist, and the field list is read off the implementation — `pmsd.Revision.Validate()` and
`pgRepo.LoadInterface()` — not designed: endpoint, source timezone, folio identity strategy, normalization
version, credential mode, resync support, four timeouts, two heartbeat bounds, feed freshness, complete-sync
bound, optional currency pair. `read_only` is sent as true and is not offered as a choice, because pmsd
refuses any revision whose read-only capability is absent or false. An authored revision is a **draft**;
publishing remains the separate password-confirmed action it already was.

Driven in a real browser: create → confirmation, a Configure form exposing all **15** fields, `pms.local`
refused with *"endpoint must be host:port"*, and a valid save confirmed as a draft. Interfaces created while
testing are left **DECOMMISSIONED** and labelled as DEV artifacts, because revisions are immutable by design.

### Guest Accounts still depended on a concept IAM-v2 does not have

Two dependencies remained after the first pass, and the second made the screen unusable rather than merely
fragile:

* the plans call sat inside the same `Promise.all` as the accounts, so any failure of that legacy endpoint —
  a site with none, a role that cannot read it, a 500 — rendered the **whole** page as "Failed to load" with
  the accounts already fetched and sitting unused;
* the **New account** button was disabled whenever `activePlans` was empty, *whatever the authority*. Under
  IAM-v2 a credential carries no plan, so on a plan-less site an operator could never create an account at
  all. The only symptom was a dead button with no explanation.

Removing the plans fetch under IAM-v2 exposed the second immediately — the new regressions failed on their
first run, which is what they are for. The button is now gated on nothing. Where a plan is a genuine
prerequisite (a legacy site with none) the **form** names it and refuses to submit, because a disabled button
explains nothing and a `title` only explains it to someone who already suspected the button and hovered.

Proven live on the appliance: the IAM-v2 screen issues `/auth/whoami`, `/guest-accounts` and
`/guest-accounts/portal` — and **does not request `/guest-access-plans` at all**. Three regressions pin it:
zero plans under IAM-v2 with the legacy endpoint never called, a failing plans endpoint degrading the plan
column rather than the screen, and legacy-with-no-plans naming the prerequisite.

### The wording still belonged to the other authority

Cutting the screen loose from the legacy resource left its **prose** behind. Two sentences carried no
authority test at all:

* the page description — *"each account is bound to a Guest Access Plan (duration, data cap, speed and max
  devices)"* — which under IAM-v2 describes a binding that does not exist and points at a screen that cannot
  affect anything the operator is looking at;
* a standing warning — *"No active guest access plans — create one first"* — rendered whenever `activePlans`
  was empty. Under IAM-v2 the plans resource is never fetched, so that list is **permanently** empty and the
  warning was **permanently on screen**, instructing the operator to create a prerequisite their site does not
  have and cannot use.

A hostile reread of the same screen found a third, narrower instance of the same assumption: `authority` is
`null` until the first list returns, so anything keyed on the structural default rendered as **legacy** during
every load. An IAM-v2 operator saw both plan-bound sentences flash on screen on each visit. Structure still
defaults to the legacy answer — that is the fail-safe direction, asking for a plan IAM-v2 ignores rather than
omitting one a legacy site needs — but prose is now keyed on a *known* authority and claims nothing until it
has one.

Verified on the appliance, form closed and form open: no plan-bound wording of any kind, the screen states
what actually decides access (*package eligibility rules*), and `/guest-access-plans` is never requested.

Five regressions pin both directions, because asserting only the IAM-v2 side would let someone "fix" this by
deleting guidance a legacy site genuinely needs: IAM-v2 shows none of the four plan-bound phrases (closed and
open), legacy keeps its description and — with zero plans — keeps its warning, and nothing plan-bound appears
while the authority is still unknown.

### Closure pass: the Guest Accounts lifecycle as one model, not four patches

The screen had been fixed symptom by symptom, so the remaining defects were all in the states nobody had
opened. Reviewed as a single model — unknown, loading, success-empty, success-nonempty, failure — three more
appeared:

* `rows === null` was doing double duty as *"still loading"* and *"the load failed"*, so a failed load showed
  the error banner **and** "Loading…" underneath it, forever. The same never-finishes shape the Phase-4
  screens had.
* the form went on saying *"Checking how this site decides guest access…"* when the check had already failed
  and nothing was pending — a second false statement on top of the first.
* `load()` never cleared the previous error, so a successful retry would have left the old failure banner
  sitting above fresh, correct data.

The list now has four distinct outcomes, and the failure one offers the only action that helps (**Try
again**). The form states the failure instead of a phantom check.

| State | List | Form access cell | Submit |
|---|---|---|---|
| loading | "Loading…" | "Checking how this site decides guest access…" | waits |
| failed | "Could not load guest accounts" + **Try again** | "Could not determine…" | waits |
| IAM-v2 | accounts, no Plan column | package eligibility rules | enabled |
| legacy, no plans | accounts | names the prerequisite | refused |
| legacy, with plans | accounts + Plan column | the picker | enabled |

### Correction: the legacy plans request also has four outcomes

The previous closure pass modelled the ACCOUNTS fetch four ways and left the PLANS fetch binary.
`setPlans(pl?.data ?? [])` collapsed *still loading*, *returned nothing* and *failed* into one empty array, and
every consumer read that array as "no plans exist". So a plans request that **failed** — or one still in
flight, which happens on every legacy load because plans are fetched after the authority resolves — told the
operator **"No active guest access plans — create one first"**: an instruction to create something that may
already exist, and a wrong diagnosis of their own system.

Only a **confirmed successful empty result** may say that. The four outcomes are now distinct, and IAM-v2
still makes no request at all.

| Plans request | Page guidance | Form access cell | Picker | Submit |
|---|---|---|---|---|
| loading | — | "Loading guest access plans…" | no | waits |
| failed | "Could not load … cannot tell whether any exist" + retry | "Could not load … This does not mean none exist." | no | refused |
| confirmed empty | "No active guest access plans — create one first" | names the prerequisite | no | refused |
| confirmed non-empty | — | the picker | yes | enabled |
| IAM-v2 (never requested) | — | package eligibility rules | no | enabled |

Verified against the deployed bundle by holding the response, then failing it, then returning empty, then
returning a plan:

```
LOADING (held)        loading=true  failed=false says-create-one=false picker=0 submit-disabled=true
FAILED (500)          loading=false failed=true  says-create-one=false picker=0 submit-disabled=true
CONFIRMED EMPTY       loading=false failed=false says-create-one=true  picker=0 submit-disabled=true
CONFIRMED NON-EMPTY   loading=false failed=false says-create-one=false picker=1 submit-disabled=false
```

Five regressions pin the four legacy states and the IAM-v2 no-request case, asserting the visible guidance and
the form state rather than merely that the page rendered. The loading and failure cases were checked against a
deliberate re-collapse of the state (both forced to "ok") and both fail there, so they test the distinction
rather than describe it.

### Closure pass: a terminal PMS interface accepted configuration

The authoring endpoint selected `connector_kind` purely to prove the interface existed and then threw the
value away. Existence was never the question that mattered: **DECOMMISSIONED is terminal**, so a revision
authored against it can never be published or dialled, and accepting it silently is how an operator ends up
believing a retired interface was reconfigured. The backend now refuses with `409` and the UI does not offer
the button. Verified live: `409` on a decommissioned interface, `404` on an unknown one, `201` on a live one,
and the refusal leaves no revision behind.

### PMS revision authoring was a race that surfaced as HTTP 500

`revision_no` came from `(SELECT COALESCE(MAX(revision_no),0)+1 …)` inside the INSERT. Under READ COMMITTED
that subquery snapshots at statement start and takes no lock. Hammering the endpoint did **not** reveal it —
the window is sub-millisecond and 8 concurrent requests all succeeded — so it was proven deterministically by
overlapping two real transactions:

```
session 1:  9        INSERT 0 1   COMMIT
session 2:  ERROR: duplicate key value violates unique constraint
            "pms_interface_revisions_pms_interface_id_revision_no_key"
            DETAIL: Key (pms_interface_id, revision_no)=(…, 9) already exists.
```

Through the handler that was an HTTP 500 that leaked the index name. A transaction-scoped advisory lock keyed
on the interface fixes the allocation — but that alone **moved** the 500 rather than removing it: holding the
same lock externally for six seconds made the request block for 5017 ms and then fail on `dbCtx`'s ten-second
cap. The wait is now bounded with `SET LOCAL lock_timeout`, and `55P03` is reported as the conflict it is.

| Scenario (real PostgreSQL, on the appliance) | Result |
|---|---|
| 12 concurrent authorings on one interface | 12 × `201`, revision numbers 1–12, no duplicates, **no 500** |
| the lock held externally for 6 s | `409` after 3021 ms — *"try again in a moment"* |
| the lock held externally for 1 s | the request waits and returns `201` |

Integration tests cover both properties: a `site_viewer` cannot create an interface or author a revision
(`403` on each), and eight concurrent authorings yield eight distinct contiguous numbers with no `500`.

### DHCP on the guest VLAN: an appliance fault, and an external one

**The appliance fault, found and fixed.** Kea's on-disk config was the pre-guest-network default — `br-lan`,
`10.10.0.0/24`, no VLAN subnet at all. netd applies the real config over the control socket and then calls
`config-write`, which was failing:

```
Error during write-config: Unable to open file /etc/kea/kea-dhcp4.conf for writing
```

Kea runs as `_kea`; `/etc/kea` and the file are root-owned. So every network apply was recorded as **FAILED**
even when the live `config-set` had succeeded — the "failed" rows in Config history beside changes that
visibly took effect — and every cold start reverted DHCP to the wrong network.

Reproduced exactly: restarting Kea brought it back on `10.10.0.1:67` serving `10.10.0.0/24`, with **no
listener on the guest bridge at all**. Restarting netd reconciled it back, which is why the fault looked
intermittent and always seemed fine when anyone checked after touching the network.

After the fix, proven at the strongest level available — a **full reboot**:

| After reboot | Observed |
|---|---|
| DHCP listener | `10.20.0.1:67` on `br-g90` |
| on-disk config | `br-g90/10.20.0.1`, subnet `10.20.0.0/22` |
| services | scd, portald, netd, acctd, edged, hotel-admin, stayconnect-caddy all active |
| restart jobs this boot | 0 for every service |

**The external fault, which is not the appliance's and is not fixed here.** `ens192.90` has received **zero
packets** since it was created (RX 0 bytes / 0 packets; TX 41), and the lease file holds nothing but its
header. No 802.1Q VLAN-90 tagged frame has ever reached this appliance. The upstream switch port feeding the
LAN NIC must trunk VLAN 90; until it does, no device on that VLAN can reach DHCP whatever the appliance is
configured to do. Stated from interface counters, not assumed — **no device test is claimed and none was
performed.**

The two-NIC model is untouched: `ens160` WAN and management, `ens192` LAN.

---

## Boundaries observed

No Production contact or mutation · no Phase-4/5/6 capability enabled on any environment · no IAM-v2 cutover ·
no production data migration · no dual read/write · no legacy IAM removal · no real guest, PMS, provider or
financial traffic · no paid access · no per-property financial enablement · no programmatic reversal.
