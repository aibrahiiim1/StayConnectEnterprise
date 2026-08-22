# Commissioning a PMS Interface

How a property's PMS goes from "nothing configured" to "guests authenticate against real occupancy". Written
from the live commissioning of the Coral Sea Holiday Protel interface on 2026-08-22, which is also where every
defect listed at the end was found.

The order below is not a preference. Each step is a precondition for the next, and the failure you get from
doing them out of order is usually silent — a healthy-looking connection that ingests nothing, or a screen
that says success while no socket exists.

---

## 0. What you need before you start

| Thing | Who decides it | Notes |
|---|---|---|
| Display label | Product Owner | free text, e.g. `Protel` |
| Connector kind | implementation | must be one of the implemented connectors (`protel-fias`) |
| Endpoint `host:port` | property / PMS vendor | the FIAS listener |
| Source timezone | property | IANA zone, e.g. `Africa/Cairo` — the PMS's own clock, not the appliance's |
| Base currency + exponent | property finance | FIAS carries no currency on the wire, so the revision is authoritative |
| Credential mode | PMS vendor | `NONE` when the link has no transport auth; `AUTH_KEY` otherwise |

Everything else is **derived** and must not be asked for: interface and revision IDs, `revision_no`,
`normalization_version`, `source_fingerprint`, and all seven timeout/heartbeat/freshness bounds. Timeouts come
from the Phase-0 freshness contract (§9: heartbeat ≤ 5 min, auth occupancy freshness ≤ 15 min) and the measured
Protel spike, not from taste.

Tenant and site are **never** entered. They come from the appliance's signed Central assignment and nowhere
else.

---

## 1. Database role and grants

pmsd connects as its own least-privilege role. Create it and apply the grants:

```sh
psql -f deploy/gatep/gatep-roles.sql            # creates svc_pmsd among the others
deploy/gatep/gatep-set-passwords.sh             # generates a SCRAM password on the appliance
psql -f deploy/gatep/svc-pmsd-iamv2-connector-grants.sql
```

The connector's grants live in their own per-service file, **not** in `gatep-grants.sql` — that file's
reconciler preamble revokes all `iam_v2` privilege from every `svc_*` role as its first act, so an `iam_v2`
grant written there is revoked by the next Gate-P run.

The grant file ends with an assertion that fails the deployment if `svc_pmsd` ever holds a posting, payment,
settlement or accounting privilege. Leave it there.

Guest authentication and the admin UI need their own reads:

```sh
psql -f deploy/gatep/svc-scd-iamv2-guest-auth-grants.sql      # the resolver's read surface
psql -f deploy/gatep/svc-edged-phase345-admin-grants.sql      # incl. the routing write path
```

---

## 2. Service account and binaries

```sh
useradd --system --no-create-home --shell /usr/sbin/nologin stayconnect-pmsd
install -m 0755 pmsd /opt/stayconnect/bin/
```

**There is nothing to provision for scope.** pmsd resolves tenant/site from the canonical Central-signed
assignment that the appliance already holds — the same chain scd uses, re-verified on every scope load against
the trust registry anchored by the manufacture-time registry root, and bound to this appliance's identity.

pmsd has no assignment key of its own and cannot issue a scope, only verify one. An earlier revision of this
procedure generated an appliance-local Ed25519 keypair and a pmsd-specific signed document; that was a second
assignment authority — anyone able to write that key and file could have scoped the PMS connector to any
tenant, without the canonical assignment ever being consulted. It has been removed, and
`internal/assignment.Resolve` is now the single implementation for every daemon.

Behaviour when the assignment is not usable:

| `assignment.Outcome` | Cause | pmsd |
|---|---|---|
| `absent` | no identity, or no assignment adopted | `assigned=false` — factory-clean, does no PMS work, exits cleanly |
| `unverifiable` | present but unreadable, no trust anchor, signed by a key outside the registry, or bound to another appliance | `ErrAssignmentNotGranting` — refuses to start |
| `not_granting` | verified, and its state is unassigned / revoked / decommissioned | `ErrAssignmentNotGranting` — refuses to start |
| `granted` | verified, bound to this appliance, granting | resolves tenant/site and proceeds |

**Read the outcome, not the emptiness of tenant/site.** All three non-granting cases produce an empty scope,
so a caller that infers "not enrolled yet" from empty fields reports a rejected appliance as a new one — which
is exactly what happened before `Outcome` existed: pmsd switched on an empty `State`, and a bad signature took
the factory-clean branch and exited 0.

`State` and `Version` stay empty for anything that did not verify. That is the long-standing scd contract and
it is unchanged, including scd's local-first behaviour of continuing to operate on a valid but stale
last-known-good assignment through a Central outage — expiry is not a de-authorisation.

## 3. Environment

`/etc/stayconnect/pmsd.env`, mode 0640, owned `root:stayconnect-pmsd`:

```
STAYCONNECT_PHASE3_MASTER=1
STAYCONNECT_PHASE3_PMS_CONNECTOR=1
STAYCONNECT_PHASE3_PMS_INGEST=1
STAYCONNECT_PHASE3_CHECKOUT_GRACE=1

PMSD_DB_URL=postgres://svc_pmsd:…@127.0.0.1:5432/stayconnect_site?sslmode=disable
PMSD_EVIDENCE_KEY_HEX=…            PMSD_EVIDENCE_KEY_VERSION=1
PMSD_EVENT_IDENTITY_KEY_HEX=…      PMSD_EVENT_IDENTITY_KEY_VERSION=1
```

**`STAYCONNECT_PHASE3_CHECKOUT_GRACE` is not optional if you want checkouts to apply.** A GO applies the Stay
checkout and its conversion in one transaction and the Stay engine refuses to split them, so with the flag off
the Converter is not wired and every GO is refused rather than converted. That refusal is deliberate — it is
better than inventing a second, Stay-only checkout — but it means an appliance ingesting arrivals and silently
refusing departures, which looks like everything working.

`PMSD_SECRET_KEY_ID` / `PMSD_SECRET_KEY_HEX` are needed only for `credential_mode=AUTH_KEY`. Do not set them
for a `NONE` interface; an unused keyring is a key held for no reason.

No assignment key appears here. The chain's paths (`PMSD_ASSIGNMENT_DIR`, `PMSD_ASSIGNMENT_REGISTRY`,
`PMSD_ASSIGNMENT_REGISTRY_ROOT`, `PMSD_ASSIGNMENT_TRUST`) default to the appliance's canonical locations and
are overridable only for tests and offline tooling.

Then the other two daemons:

```
# /etc/stayconnect/edged.env
STAYCONNECT_PHASE3_MASTER=1
STAYCONNECT_PHASE3_ADMIN=1

# /etc/stayconnect/scd.env
STAYCONNECT_PHASE3_MASTER=1
STAYCONNECT_PHASE3_PMS_AUTH=1
```

Enabling PMS authentication does **not** disable voucher or guest-account authentication. They are separate
methods on the portal and nothing here touches them.

---

## 4. Author, publish, activate

Three separate acts, through `/edge/v1/pms-interfaces`:

```
POST /pms-interfaces                 {connector_kind, display_label}      -> AUTH_DISABLED
POST /pms-interfaces/{id}/revisions  {endpoint, source_timezone, ...}     -> draft revision
POST /pms-interfaces/{id}/publish    {revision_id, expected_revision_id,
                                      reason_code, password}              -> current revision
POST /pms-interfaces/{id}/lifecycle  {state: ACTIVE, reason_code,
                                      password}                           -> ACTIVE
```

They are separate on purpose. Publishing decides **what** the connector would dial; activating decides
**whether** it dials at all. pmsd selects `WHERE lifecycle_state='ACTIVE'`, so a published interface that was
never activated is simply never picked up.

Revisions are immutable. Changing anything means authoring the next revision and publishing it — including
correcting a value you have just entered.

---

## 5. Hand the socket over — one owner, always

If the property was previously served by the legacy `public.pms_providers` path, disable it **before**
starting pmsd, and prove the socket is free:

```sh
psql -c "UPDATE public.pms_providers SET enabled=false WHERE kind='protel-fias';"
systemctl restart stayconnect-scd
ss -tnp | grep '<pms-host>:<port>'      # must return nothing
```

Most FIAS listeners accept one client. Two owners is not "two connections" — it is one connector silently
holding the slot the other needs.

---

## 6. Checkout-grace preflight

Before admitting live GO events, make the grace path valid, or the first real checkout of a guest who bought
a package will fail its transaction and stall the durable inbox behind an event that fails again on every
retry:

```sh
psql -c "SELECT iam_v2.bootstrap_emergency_grace('<tenant>','<site>');"
psql -c "SELECT iam_v2.emergency_grace_health('<tenant>','<site>');"   -- must be OK
```

With no `site_checkout_grace_config` row the converter falls back to the Emergency path, and the Emergency
catalog is read-only and pre-provisioned — it is not created in the checkout hot path. `bootstrap_emergency_grace`
creates a **system** package (`is_system=t`, code `__sys_emergency_grace_pkg__`) which is deliberately not
offered to guests.

---

## 7. Start, and read the four axes

```sh
systemctl enable --now stayconnect-pmsd
psql -c "SELECT transport_status, continuity_status, sync_status, last_sync_failure_code
           FROM iam_v2.pms_interface_runtime;"
```

Healthy is `CONNECTED` / `CONTINUOUS` / `IN_SYNC`. The axes are independent on purpose and none of them is a
verdict — "healthy" is computed from them at read time, never stored.

`RESYNC_IN_PROGRESS` cycling every few seconds with zero admitted events is the signature of records being
rejected. The connector logs the record type and a bounded reason (`identity_absent`,
`identity_ambiguous_or_malformed`, `event_validation_failed`) with no payload, room, name or reservation in
the line. Read those before changing anything.

---

## 8. Route the guest network

```
PUT /edge/v1/pms-routing/{guest_network_id}   {pms_interface_id, routing_mode: MAPPED}
```

Setting a route **replaces** any other interface mapped to that network. An unmapped network resolves against
nothing and is reported under `unmapped_guest_networks`, which is a legitimate configuration for a staff VLAN
— not a hole.

Verify with `GET /edge/v1/pms-routing`, and confirm authentication end to end by checking
`iam_v2.auth_resolutions` for a `VERIFIED` row. The guest-facing response is a uniform envelope by design, so
a resolver failure and a genuine non-match look identical from outside. **When authentication is unexpectedly
refused, the reason is in the daemon log and the audit table, never on screen.**

A `VERIFIED` resolution can still return `NOT_VERIFIED` to the guest with reason
`verified_but_no_eligible_package`: identity was proven, but the site has no sellable Internet Package. That
is commercial configuration, not an authentication fault.

---

## 9. Persistence

`systemctl restart stayconnect-pmsd` and a full reboot must both end with the socket re-established and the
projection intact. On reboot the connector re-dials, re-runs a DR resync and re-pins the published revision;
`iam_v2.stays`, `stay_guests` and `guest_network_pms_map` survive untouched.

---

## Defects this procedure exists because of

Every one of these was found on a live link, and each presented as something working.

| Symptom | Cause | Fix |
|---|---|---|
| Every screen succeeds, nothing connects | no product path could set `lifecycle_state='ACTIVE'` | `POST /{id}/lifecycle` |
| PMS connected, Stays ingested, nobody authenticates | `guest_network_pms_map` had a read path and test fixtures but no writer | `PUT /pms-routing/{id}` |
| Duplicate-source detection can never fire | `source_fingerprint` was read but never written, so every revision was NULL and NULL never equals NULL | derived at authoring from connector kind + endpoint; **fails closed** if the connector kind cannot be read, rather than fingerprinting a partial identity |
| Healthy link, full DS…DE resync, **zero** events admitted, resyncing forever | `Event.Validate` demanded a canonical-UUID secret generation, which a `credential_mode=NONE` interface legitimately does not have | secret generation is optional; still validated when present |
| ~10 of 365 roster records wedge the pipeline permanently | a record with no `G#` was classed MALFORMED → continuity fault → resync → same records again | absent identity distinguished from ambiguous identity; skipped and counted, not faulted |
| Arrivals ingest, departures never apply | this Protel sends every GO with `RN` and no `G#` | reservation required on GI/GC, room-only permitted on GO; the engine resolves by room and refuses when sharers make it ambiguous |
| A correctly published interface reported "not found at this site" | a query tested `pms_interface_revisions.published_at`, which does not exist; the error was indistinguishable from no-such-row | publication is `pms_interfaces.current_revision_id` |
| Real guest refused, uniform envelope, no reason on screen | `svc_scd` lacked the entire Phase-3 resolver read surface, one table at a time | grants derived from `internal/pmsresolve` + `cmd/scd/phase3_auth.go` |
| First checkout of a paying guest would stall the inbox | `emergency_grace_health` is a plain SQL function reading `service_plans` as the caller; `svc_pmsd` had no SELECT | granted; found by rehearsing the converter as `svc_pmsd` in a rolled-back transaction |

The last row is the technique worth keeping: **rehearse the converter's exact statement sequence as the
service role inside a transaction you roll back.** It exercises the grants and the controlled-writer triggers
for real, commits nothing, and finds the privilege gap before a departing guest does.

---

## Verification status — Coral Sea Holiday, 2026-08-22

Recorded separately from the procedure because two items are implemented and **not** yet proven live, and
conflating them with the verified ones would misrepresent the delivery.

### Verified live

Single pmsd ownership of the socket · read-only FIAS handshake · `DR` → `DS…DE` resync · durable
`stay_events` → `stays` (502 IN_HOUSE, 502 guests, 0 pending) · runtime axes `CONNECTED / CONTINUOUS /
IN_SYNC` · guest-network → interface routing · Hotel Admin Stays and Events showing real room and date data ·
restart and full-reboot persistence.

### PMS resolver verification — VERIFIED

The STRICT resolver returns `VERIFIED` with a `resolved_stay_id` for a real in-house Stay on the PRE-LIVE
guest network, and determinate `NO_MATCH` for a non-resident pair. Recorded in `iam_v2.auth_resolutions`.

**This is the identity decision only.** It says the guest is who they claim to be and is currently resident.

### Guest Internet authorization — BLOCKED, separately

The guest-visible flow still returns `NOT_VERIFIED` with reason `verified_but_no_eligible_package`: identity
is proven, but the site has no sellable Internet Package, so no offer can be made and no Auth Context is
consumed. This is a **package / commercial configuration decision**, not a PMS or authentication fault, and
it is outside the commissioning scope. PMS resolution will keep returning `VERIFIED` regardless of when that
decision is made.

The emergency-grace catalog created in step 6 is a **system** package (`is_system=t`) and is deliberately not
offered to guests, so it does not and must not satisfy this.

### Real GO on a known Stay — LIVE-PROVEN

Proven at 01:52 UTC on 2026-08-22 by **natural checkouts during the property's departure window**. No checkout
event was fabricated, and none should ever be.

| Evidence | Result |
|---|---|
| `stay_events` GO | 3 `APPLIED`, all resolved to a Stay; **0 PENDING** |
| `stays` | 1 `CHECKED_OUT` with `effective_checkout_at` set and `last_applied_event_id` linked; 501 `IN_HOUSE` |
| `checkout_grace_audit` | `NO_GRACE` / `NO_ACTIVE_ENTITLEMENT_AT_CHECKOUT` — correct, the Stay had bought nothing |
| Financial boundary | `purchases=0`, `entitlements=0`, `pms_postings=0`, `accounting_records=0` |
| Runtime | `CONNECTED / CONTINUOUS / IN_SYNC` |

Getting there required two privileges the pre-flight rehearsal had missed, each of which failed
**mid-transaction on a real checkout** and left the GO `PENDING` with the ordered per-interface stream stalled
behind it:

- `UPDATE` on `site_checkout_grace_config`
- `UPDATE` on `entitlements`

Both are `SELECT ... FOR UPDATE` **row locks**, not writes. PostgreSQL requires UPDATE privilege to take a
lock even when the statement modifies nothing, and the controlled-writer triggers still refuse any actual
column change from a non-owner.

**The lesson, which is the reusable part:** a privilege rehearsal must issue the statement the code issues,
**lock clauses included**. The original rehearsal ran plain `SELECT`s against both tables and passed, proving
something adjacent to what was needed. It caught the `service_plans` gap and missed these two.

The applier also logged nothing but `UNCLASSIFIED` for every failure — a missing grant, a violated domain
guard and a dropped connection were one indistinguishable line while the stream silently backed up. It now
logs the cause (database and engine text only; no PMS payload passes through it), which is what turned a
stalled queue into a two-minute diagnosis.

### How to re-check any of this

```sh
psql -c "SELECT event_type, processing_status, count(*) FROM iam_v2.stay_events GROUP BY 1,2 ORDER BY 1,2;"
psql -c "SELECT status, count(*) FROM iam_v2.stays GROUP BY 1;"
psql -c "SELECT outcome_code, count(*) FROM iam_v2.auth_resolutions GROUP BY 1;"
journalctl -u stayconnect-pmsd | grep 'application failed'
```

A `GO` at `APPLIED` with a `CHECKED_OUT` Stay is healthy. A `GO` left `PENDING` is not — it means the
conversion is failing and the inbox is stalling; read the `cause` field, do not restart and hope.
