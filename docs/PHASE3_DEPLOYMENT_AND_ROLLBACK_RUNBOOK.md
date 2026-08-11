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
| `NETD_PHASE3_PLAN_STATE` | netd | Where the last **admitted** plan generation is persisted (default `/var/lib/stayconnect/netd-phase3-plan.json`). It is what stops a restarted netd from accepting a plan it had already superseded. |
| `NETD_PHASE3_CLASS_STATE` | netd | The managed-class inventory — session, device, bridge, minor, generation, boot (default `/var/lib/stayconnect/netd-phase3-classes.json`). Written with file **and directory** fsynced before rename. |
| `NETD_BOOT_ID_FILE` | netd | Where the kernel's boot id is read from (default `/proc/sys/kernel/random/boot_id`). Used as a first filter only — continuity is proved by reading the kernel. |
| `ACCTD_PHASE3_PLAN_STATE` | acctd | Where the monotonic plan generation is persisted (default `/var/lib/stayconnect/acctd-phase3-plan.json`). A producer that restarted at generation 1 would have every plan correctly refused as stale — and enforcement would freeze with nothing appearing broken. |

**With the flags ON and no producer uid configured, netd refuses to start.** Live enforcement that cannot
authenticate its producer is not a degraded mode; it is an unenforceable one, and starting anyway would mean
any local process could shape the guest network.

### What survives a reboot, and what deliberately does not

After a reboot every `tc` class is gone. Recovery is one desired-state submission, and three things make it
safe:

* the **admitted** plan generation survives, so a delayed or replayed older plan is still refused;
* the **managed-class inventory** survives, but every entry is re-proved against the kernel — anything not
  actually installed is dropped;
* every recreated class gets a **strictly newer generation** from the database allocator, so the accounting
  checkpoints `acctd` still holds treat it as a new counter series (a trustworthy reset) rather than a
  counter that appears to have gone backwards.

Until that first submission converges, `/v1/health` reports `phase3_shaping.state = ACTIVE_NO_PLAN` with
`degraded: true`. That is correct and expected: the appliance is supposed to be enforcing and is not yet.

Expect `state` to be one of `DARK`, `ACTIVE_NO_PLAN`, `ACTIVE_FRESH_CONVERGED`, `ACTIVE_STALE` or
`ACTIVE_DEGRADED`. `ACTIVE_STALE` means the producer went quiet — what is installed may still be right, but
nothing is confirming it.

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

1. **Take a backup first, and record who is authorized right now.** Use the existing appliance backup path;
   a Phase-3 deployment is not special, and the rollback in §5 assumes a restorable point exists.

   Capture the legacy authorization set *before* anything is deployed. This is the "before" half of the
   legacy-parity evidence, and it is the only moment it can be taken honestly:

   ```bash
   nft -j list set inet stayconnect auth_ipv4 > /var/backups/stayconnect/legacy-before.json
   nft list ruleset > /var/backups/stayconnect/ruleset-before.txt
   ```

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

4a. **Install the DARK `pmsd` service.** Shipping the binary is not deploying the daemon — Live Increment 9
   found `/opt/stayconnect/bin/pmsd` present with no unit, which is a daemon that exists as a file and is
   absent as a service.

   ```bash
   sudo bash scripts/install-pmsd-dark.sh
   ```

   It creates the dedicated `stayconnect-pmsd` system account (the unit is **never** weakened to run as root),
   installs `/etc/stayconnect/pmsd.env` from the reviewed `deploy/env/pmsd.env.dark` — which deliberately sets
   **no** Phase-3 flag, so every flag resolves OFF — installs and enables the reviewed unit, and then verifies
   the dark contract it just installed.

   **DARK does not mean "stopped".** The unit is installed and enabled; on start it discovers every flag it
   owns is OFF, opens no PMS socket, touches no database, logs `connector and ingest flags OFF` and exits 0.
   `Restart=on-failure` (not `always`) means a clean exit stays a clean exit, so it cannot restart-loop.
   Expect `active=inactive`, `result=success`, `ExecMainStatus=0`, `NRestarts` at 0 or 1, and no `pmsd`
   process or socket. Re-verify at any time with:

   ```bash
   bash scripts/install-pmsd-dark.sh --verify-only
   ```

5. **Confirm netd converged the ruleset by itself — there is no foundation step to run any more.**

   `netd` reconciles the live ruleset against a **fresh render of the running binary** on every start
   (ADR-0003). The Phase-3 set, its forward rule and the captive exclusions are part of that render, so they
   arrive with the service and survive every subsequent restart and reboot. Nothing needs to be installed by
   hand, and the appliance's stored bundle is no longer the authority for structure.

   Two outcomes, and both are correct:

   - **the live ruleset already matched** — netd executed *no* nft command at all. This is the steady state,
     and it is what makes a routine restart incapable of disturbing guest authorization;
   - **the live ruleset differed** (the usual case for the first deployment onto a pre-Phase-3 appliance) —
     netd applied the current render as ONE atomic transaction, carrying every live authorization across
     inside that same transaction, with the remaining lease on each timed element and no lease invented for
     the permanent ones legacy `scd` writes.

   Confirm it, rather than assuming it:

   ```bash
   # what netd decided, and whether it had to change anything
   journalctl -u stayconnect-netd -b | grep -E 'nft structure converged|boot_reconcile' | tail -5

   # the Phase-3 set exists and authorizes nobody
   nft list set inet stayconnect phase3_auth_ipv4

   # legacy authorization is exactly as it was
   nft -j list set inet stayconnect auth_ipv4 > /var/backups/stayconnect/legacy-after.json
   ```

   Record `legacy-before.json` (taken in step 1) and `legacy-after.json`. Element-for-element equality across
   the deployment is the legacy-parity evidence for this step.

   > **`phase3-foundation` is retired from this runbook.** It still exists as a diagnostic — `inspect` is
   > read-only and useful for reading a live ruleset — but `install` and `rollback` are no longer part of any
   > deployment or rollback procedure, because the renderer now owns this structure. If you do run a
   > structural `install`/`rollback`, it deliberately **invalidates netd's render marker**, so the next netd
   > start rebuilds the ruleset from the current render. Do not use it to "fix" a ruleset: restart `netd`.

     report.

   Then confirm from the appliance's own side that nothing changed for anyone:

   ```bash
   nft list set inet stayconnect auth_ipv4 | head -40          # the same guests as in the "before" file
   nft list set inet stayconnect phase3_auth_ipv4              # MUST be empty
   ```

   An empty `phase3_auth_ipv4` matches nothing, so the forward rule it feeds can never match and the captive
   exclusion can never exclude. **The appliance forwards exactly what it forwarded before.** That is what
   makes this safe to do while dark — and it is also what makes the later cutover a flag flip rather than a
   ruleset regeneration.

   > **Until this step has actually been performed and verified on the unit, the cutover is NOT "flag-only".**
   > No document may describe it as flag-only on the strength of the software alone.

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

```bash
# 7. the Phase-3 authorization set exists and authorizes NOBODY
nft list set inet stayconnect phase3_auth_ipv4
#    expect the set to be present (netd renders it; see §2 step 5) with NO elements. An empty set matches nothing, so the forward
#    rule that reads it cannot match and the captive exclusion cannot exclude.

# 8. legacy authorization is exactly as it was
nft list set inet stayconnect auth_ipv4 | head -40
#    expect the same guests as in legacy-before.json (captured in §2 step 1)
```

A 404 rather than a "feature disabled" response is intentional: an unmounted route cannot leak the shape of a
schema that is not live yet. Check 6 is the one worth doing even when you are confident: it is the only check
that asks the process that would actually mutate the network whether it believes Phase 3 is live, rather than
asking the process that would ask it to. Checks 7 and 8 are the network half of the same question: "dark"
must mean the packet path is unchanged, not merely that a flag reads false.

---

## 4. Reboot drill

**Phase 3 DOES add boot-time behaviour, and the drill exists to exercise it.** An earlier version of this
runbook said the opposite; that statement was wrong, and Live Increment 9 failed its reboot check because of
the behaviour it denied. On every start `netd` reconciles the live ruleset against a fresh render of the
running binary, and reconstructs it if the kernel is holding something else — which is exactly the state a
reboot leaves behind, since nftables loads only the static `/etc/nftables.conf`.

What it reconstructs is the **confirmed active network revision's own intent snapshot**, never the editable
`guest_networks` rows. An unapplied Hotel-Admin draft therefore cannot become the running network at a reboot;
only an explicit Apply that is then Confirmed changes what the appliance runs.

1. `reboot` the appliance.
2. After it comes back, re-run **all four** checks in §3. They must produce identical results, and
   `phase3_auth_ipv4` must be **present and empty**.
3. Confirm the reconstruction happened and settled:

   ```bash
   journalctl -u stayconnect-netd -b | grep 'nft structure converged'   # expect ONE line for this boot
   systemctl restart stayconnect-netd
   journalctl -u stayconnect-netd -b | grep 'nft structure converged'   # expect NO new line: steady state
   ```

   The second command is the important one. A correct appliance issues **no nft command at all** on an
   ordinary restart; a second "converged" line means the render and the live ruleset disagree every time,
   which must be investigated before the appliance is left in service.

4. Reboot once more and repeat step 2. Idempotence across two cycles is the property being drilled.
5. Confirm guest service is unaffected: an existing guest session survives, and a new guest can authenticate
   through the *existing* (non-Phase-3) methods exactly as before.

If anything in §3 differs after the reboot, stop and roll back.

---

## 5. Rollback

Rollback is two independent steps, and they can be done separately. In most cases **restoring the previous
release is enough**: the schema is additive and inert while dark, so leaving it in place is harmless.

**5a. Restore the previous release** (binaries + Hotel-Admin bundle) using `scripts/binary-rollback.sh`, then
re-run §3.

> **THE PRE-`nftconverge` COMPATIBILITY BOUNDARY — read this before rolling back.**
>
> It is **not** true that every previous release carries live authorization across the transition. A `netd`
> built before ADR-0003 does not reconcile: it re-asserts the stored bundle, and that bundle begins with
> `delete table inet stayconnect`. Starting one therefore recreates the authorization sets **EMPTY**, and every
> guest currently online loses access in the same instant.
>
> Whether that matters depends entirely on who is authorized at that moment, so the tool reads the live set and
> decides:
>
> | rollback target | live `auth_ipv4` | result |
> | --- | --- | --- |
> | convergence-capable (carries the render marker) | anything | proceeds — authorization is carried across |
> | predates convergence | **empty** | proceeds — there is nothing to lose |
> | predates convergence | **populated** | **REFUSED.** Nothing is replaced, nothing is restarted |
> | predates convergence | unreadable | **REFUSED.** "Cannot prove empty" is not "is empty" |
>
> There is deliberately **no `--force`**. Deauthorizing a whole property is a separate, deliberate decision, so
> the refusal names the operator action instead of offering a way past itself:
> roll back to a convergence-capable release instead; or schedule a window, let the sessions end, confirm
> `nft list set inet stayconnect auth_ipv4` is empty and re-run; or deauthorize deliberately and visibly first.
>
> The Increment-9 rehearsal ran in the empty-set case, which is why it was safe. The populated case is proven in
> the disposable real-kernel suite (`internal/kerneltest/rollback_boundary_kernel_test.go`) with real packets —
> it is never rehearsed against a live property.

**5a-bis. There is no separate nft rollback step, and that is deliberate.**

Restoring the previous release restores the previous renderer, and the next `netd` start reconciles the live
ruleset to whatever that renderer produces — carrying live authorization across the change. The ruleset
follows the binaries automatically; there is nothing to undo by hand.

Do **not** run `phase3-foundation rollback` as part of a release rollback. It is a diagnostic tool, not a
deployment step, and running it on a converged appliance removes structure the running renderer will simply
rebuild on the next restart.

If a **network configuration** apply fails, netd's own rollback restores the previous *confirmed* revision by
rendering its stored intent. If that cannot be done safely, it **stops and records a blocker** rather than
falling back to executing an old stored `stayconnect.nft` — that file begins with `delete table` and would
deauthorize every guest on the appliance. An unfinished rollback needs operator attention; look for:

```bash
journalctl -u stayconnect-netd | grep rollback_nft
```

```bash
nft list set inet stayconnect auth_ipv4 | head -40   # unchanged, same guests
nft list set inet stayconnect phase3_auth_ipv4       # expect: No such file or directory
```

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

- **Turning the flags on.** Cutover is a separate, explicitly authorized step with its own gate. The ruleset
  the flags depend on is produced by `netd`'s own render (ADR-0003), so it is present as soon as the corrected
  software is deployed and `netd` has started — there is no manual installation step to perform first. Confirm
  it with §2 step 5 before treating cutover as a flag flip.
- **Per-service `iam_v2` privilege grants (Gate-P).** While dark, every runtime service role holds **zero**
  `iam_v2` table and function privileges, and the gate asserts it. The prepared grants live in
  `docs/architecture/Phase3-Controlled-Writer-Privilege-Manifest.md` and are **not applied**.
- **Further live PMS traffic.** Read-only protocol verification against the approved live interface is
  operator-executed under explicit authorization. It **has been performed**: on 2026-08-10, against Hotel ID 3
  (`150.0.0.18:5003`), 149 frames were parsed with zero parse errors across two byte-identical runs, using the
  reviewed read-only builders behind an outbound allowlist. That result is recorded truthfully in the Phase-3
  report §6b and in `governance/project-state.json`. What remains outside this runbook is any *further* PMS
  contact, and any write of any kind — no `PS`, no `PA`, no posting, no folio change, ever, from here.

---

## 7. If something goes wrong

| Symptom | Most likely cause | Action |
|---|---|---|
| `edged` exits at startup with a phase3 config error | a child flag set while master is OFF | unset the child flag; this is the guard working |
| `/edge/v1/pms-stays` answers 200 | the admin flags are ON | this is not a dark deployment — unset them and restart |
| Migration 0010 fails midway | it runs in one transaction | nothing was applied; fix the reported cause and re-run |
| Hotel Admin shows Stays/Grace nav items | the bundle was built with `NEXT_PUBLIC_PHASE3_ADMIN=1` | rebuild the bundle without it and redeploy |
| Anything unexplained | — | roll back per §5a first, investigate afterwards |
