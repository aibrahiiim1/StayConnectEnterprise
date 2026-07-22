# Phase 3 — deployment, verification, rollback and reboot runbook

**Status: DARK.** Everything in Phase 3 ships with every feature flag OFF. A dark deployment changes no guest
behaviour: no PMS socket is opened, no Phase-3 SQL is issued, and none of the Phase-3 admin routes exist.
This runbook covers deploying that dark build, proving it is dark, rolling it back, and surviving a reboot.

It is written for the operator who will actually type the commands, with the reasoning included so a step can
be adapted safely rather than followed blindly.

---

## 0. Before you touch anything

Run the offline preflight and the evidence collector on the build machine:

```bash
bash scripts/phase3-preflight.sh          # must end PASS
bash scripts/phase3-evidence.sh           # writes evidence/phase3/phase3-evidence-<UTC>.md
```

The preflight refuses a build that would not be safe to deploy dark: it proves the module builds and vets
clean, that the Phase-3 flag **defaults** are OFF in code (not merely unset in someone's shell), that an
incoherent flag set is a *loud startup failure* rather than a silent "off anyway", that no deployment file
enables the Phase-3 admin bundle, that **every** function and table migration 0010 creates is dropped by its
down script, and that 0010 grants no runtime role any `iam_v2` privilege.

Neither script contacts an appliance, a production database or a PMS. Nothing they produce is live evidence.

**Do not proceed if either script fails.** A failing preflight is the cheapest possible outcome.

---

## 1. What "dark" means on the running unit

| Surface | Flag | Deployed value |
|---|---|---|
| everything Phase-3 | `STAYCONNECT_PHASE3_MASTER` | *(unset / false)* |
| PMS connector runtime (pmsd) | `STAYCONNECT_PHASE3_PMS_CONNECTOR` | *(unset / false)* |
| Stay/Event ingestion | `STAYCONNECT_PHASE3_PMS_INGEST` | *(unset / false)* |
| resolver + Auth Context | `STAYCONNECT_PHASE3_PMS_AUTH` | *(unset / false)* |
| checkout-grace execution | `STAYCONNECT_PHASE3_CHECKOUT_GRACE` | *(unset / false)* |
| Hotel-Admin PMS surface | `STAYCONNECT_PHASE3_ADMIN` | *(unset / false)* |
| Hotel-Admin **bundle** | `NEXT_PUBLIC_PHASE3_ADMIN` | *(absent at build time)* |

A child flag set while the master flag is OFF makes `edged` **exit at startup**. That is deliberate: a
deployment mistake must be visible immediately, not lie dormant until someone flips the master flag and
discovers a half-configured surface.

### Two settings that are not flags, and one refusal to expect

These exist because turning Phase 3 on is not only a matter of flags. They are listed here rather than in the
cutover section because getting them wrong looks like a *dark* problem — a service that will not start — and
the first instinct is to blame the deployment.

| Setting | Owner | What it does |
|---|---|---|
| `NETD_PHASE3_PRODUCER_UID` | netd | The uid of the ONE local process (`acctd`) allowed to submit shaping plans. Authentication is `SO_PEERCRED` on the socket — the kernel's statement about the caller, not a header the caller writes. |
| `NETD_PHASE3_PLAN_STATE` | netd | Where the last accepted plan generation is persisted (default `/var/lib/stayconnect/netd-phase3-plan.json`). It is what stops a restarted netd from accepting a plan it had already superseded. |
| `ACCTD_PHASE3_PLAN_STATE` | acctd | Where the monotonic plan generation is persisted (default `/var/lib/stayconnect/acctd-phase3-plan.json`). A producer that restarted at generation 1 would have every plan correctly refused as stale — and enforcement would freeze with nothing appearing broken. |

**With the flags ON and no producer uid configured, netd refuses to start.** Live enforcement that cannot
authenticate its producer is not a degraded mode; it is an unenforceable one, and starting anyway would mean
any local process could shape the guest network.

**With the flags ON, every Phase-3 writing service (`acctd`, `edged`, `scd`, `pmsd`) verifies the
controlled-writer boundary before it serves anything**, and exits if it does not hold. It refuses in two
cases:

* the schema's controlled-writer guards are missing or disabled — the service would be writing Phase-3 state
  raw while believing it was protected;
* the service's own database role **is** (or can become) the controlled operations' owner — every guard would
  pass trivially for it, so the boundary would exist on paper and constrain nothing.

The second case is the one to expect on a unit that has not been through the Gate-P role separation. It is
not a bug in the deployment; it is the check telling you the runtime role is still too privileged for Phase 3
to be turned on.

---

## 2. Deploy (dark)

1. **Take a backup first.** Use the existing appliance backup path; a Phase-3 deployment is not special, and
   the rollback in §5 assumes a restorable point exists.

2. **Ship the binaries and the Hotel-Admin bundle.** Build the Hotel-Admin bundle with
   `NEXT_PUBLIC_PHASE3_ADMIN` **absent**. The nav items and pages are then not in the bundle at all — the
   operator cannot navigate to a surface whose backend routes do not exist.

3. **Apply migration 0010** through the authoritative runner, never by hand:

   ```bash
   bash scripts/edge-migrate.sh --only 0010_phase3_stay_resolution \
     --expect-db <site-db> --target-kind <kind> --ack-target <ack> \
     --expect-sha256 "$(sha256sum data-plane/migrations/0010_phase3_stay_resolution.up.sql | awk '{print $1}')"
   ```

   The SHA pin matters: it proves the file applied is the file reviewed. 0010 is **additive** — it creates
   new tables, columns, triggers and controlled functions, and grants **no runtime role any privilege**. The
   schema exists; nothing uses it yet.

4. **Restart the services** in the usual order and confirm they came up.

---

## 3. Prove it is dark

Do not assume. Confirm all four:

```bash
# 1. the flags the process actually loaded (log line, no secrets)
journalctl -u stayconnect-edged  | grep 'phase3 dark pms admin surface'   # expects master=false ... admin=false

# 2. the Phase-3 admin routes do not exist (404, NOT "disabled")
curl -sk -o /dev/null -w '%{http_code}\n' https://<mgmt-ip>/edge/v1/pms-stays          # expect 404

# 3. no PMS socket is open
ss -tanp | grep -i pmsd || echo 'no pmsd sockets — correct while dark'

# 4. no Phase-3 SQL is being issued
#    (on the site DB, with an ordinary read-only session)
psql -c "SELECT count(*) FROM pg_stat_statements WHERE query ILIKE '%iam_v2.stay_events%'"   # expect 0
```

```bash
# 5. the guest Stay-resolution endpoint does not exist either (scd mounts it only with the auth flag on)
curl -s -o /dev/null -w '%{http_code}
' --unix-socket /run/stayconnect/scd.sock   -X POST http://scd/v1/phase3/auth/pms/resolve -d '{}'                                  # expect 404

# 6. netd refuses to shape, on its own authority — even if something submitted a plan
curl -s --unix-socket /run/stayconnect/netd.sock http://netd/v1/health |   python3 -c 'import json,sys; print(json.load(sys.stdin)["phase3_shaping"])'
#    expect: active=false. A dark netd also returns 409 phase3_dark for the class-generation read.
```

A 404 rather than a "feature disabled" response is intentional: an unmounted route cannot leak the shape of a
schema that is not live yet. Check 6 is the one worth doing even when you are confident: it is the only check
that asks the process that would actually mutate the network whether it believes Phase 3 is live, rather than
asking the process that would ask it to.

---

## 4. Reboot drill

Phase 3 adds no boot-time behaviour, so the drill is a confirmation, not a migration:

1. `reboot` the appliance.
2. After it comes back, re-run **all four** checks in §3. They must produce identical results.
3. Confirm guest service is unaffected: an existing guest session survives, and a new guest can authenticate
   through the *existing* (non-Phase-3) methods exactly as before.

If anything in §3 differs after the reboot, stop and roll back — a difference means something is reading
Phase-3 state that should not be.

---

## 5. Rollback

Rollback is two independent steps, and they can be done separately. In most cases **restoring the previous
release is enough**: the schema is additive and inert while dark, so leaving it in place is harmless.

**5a. Restore the previous release** (binaries + Hotel-Admin bundle) using the standard rollback path, then
re-run §3.

**5b. Remove the schema** — only if a clean slate is required:

```bash
bash scripts/edge-migrate.sh --down --only 0010_phase3_stay_resolution \
  --expect-db <site-db> --target-kind <kind> --ack-target <ack>
```

The down script drops every table, trigger and controlled function 0010 created and removes its ledger row;
the preflight asserts that coverage on every build, so a rollback cannot silently leave executable functions
behind. **The lifecycle gate proves apply → behaviour → down → re-apply on a disposable PostgreSQL 16 on
every change**, which is why this step is rehearsed rather than hoped for.

Afterwards, confirm the schema is gone:

```bash
psql -c "SELECT count(*) FROM information_schema.tables WHERE table_schema='iam_v2' AND table_name='stay_events'"  # expect 0
psql -c "SELECT count(*) FROM public.schema_migrations WHERE version='0010_phase3_stay_resolution'"               # expect 0
```

---

## 6. What is deliberately NOT in this runbook

- **Turning the flags on.** Cutover is a separate, explicitly authorized step with its own gate.
- **Per-service `iam_v2` privilege grants (Gate-P).** While dark, every runtime service role holds **zero**
  `iam_v2` table and function privileges, and the gate asserts it. The prepared grants live in
  `docs/architecture/Phase3-Controlled-Writer-Privilege-Manifest.md` and are **not applied**.
- **Live PMS verification.** Read-only protocol verification against a real interface is operator-executed
  and its evidence is recorded separately. Nothing in this repository may claim it happened.

---

## 7. If something goes wrong

| Symptom | Most likely cause | Action |
|---|---|---|
| `edged` exits at startup with a phase3 config error | a child flag set while master is OFF | unset the child flag; this is the guard working |
| `/edge/v1/pms-stays` answers 200 | the admin flags are ON | this is not a dark deployment — unset them and restart |
| Migration 0010 fails midway | it runs in one transaction | nothing was applied; fix the reported cause and re-run |
| Hotel Admin shows Stays/Grace nav items | the bundle was built with `NEXT_PUBLIC_PHASE3_ADMIN=1` | rebuild the bundle without it and redeploy |
| Anything unexplained | — | roll back per §5a first, investigate afterwards |
