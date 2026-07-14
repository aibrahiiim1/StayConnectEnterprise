# Commercial Onboarding — Execution Status (persistent state store)

_Continuously updated. Do not treat chat as the state store._

## Servers / access
- Central: `root@150.0.0.252` (ctrlapi:8080, cloud-admin:3000, mTLS:9443, NATS live :4222, NATS mTLS parallel :4223, authz svc). ufw active.
- Appliance: `root@172.21.60.23` (scd/edged/netd/portald/kea/hotel-admin). Guest plane must stay up.
- DB: `docker exec sc-central-pg psql -U stayconnect -d stayconnect`.

## Deployment versions / rollback points
- ctrlapi: `/opt/stayconnect/bin/ctrlapi` (+ `.prev`). scd: `/opt/stayconnect/bin/scd` (+ `.prev`).
- cloud-admin: `/opt/stayconnect/releases/cloud-admin/*` (symlink `cloud-admin-current`).
- NATS backups: `/opt/stayconnect/nats-migration-backup/<ts>/`.
- CA ceremony backup (encrypted root, checksum, runbook): `/opt/stayconnect/ca-ceremony-backup/`; passphrase on appliance `/root/ca-ceremony-passphrase-holding/` (de-co-located).

## COMPLETED + VERIFIED (do not redo except regression)
- API mTLS (:9443) runtime cutover (hello/license/csr/cert/rotation).
- Separate appliance identity key (`ed25519.key`) vs mTLS key (`mtls-client.key`), both 0600.
- Two-tier CA: offline-encrypted Root (plaintext removed from host) → online Intermediate; 7 distinct per-purpose keys.
- Persistent, managed server-TLS lifecycle (monitor + rotate + verify-before-switch + rollback + audit).
- Parallel NATS mTLS `:4223` + Auth Callout (`nats-authz`, non-root): URI-SAN identity, per-appliance perms, deny-by-default, fail-closed, XKey, audit.
- NATS matrix 15/15: mTLS-only, per-appliance + cross-tenant isolation, JetStream-admin denied, missing/revoked cert rejection (new conn), ACTIVE revocation (short-lived JWT TTL=600s prod, ≤10min latency).
- Telemetry idempotency EXISTS: outbox `seq` + `fleet_telemetry_dedupe (appliance_id,seq)` ON CONFLICT DO NOTHING (fleet.go). Heartbeat = presence upsert (idempotent).
- Migrations through 0025. Lifecycle 13+grace states; license 5 states driven from signed doc.

## NATS mTLS PRODUCTION CUTOVER — COMPLETE + VERIFIED (2026-07-12)
- 5A active revocation: short-lived callout JWT (AUTHZ_USER_TTL_SECONDS=600 prod), verified live conn terminated on revoke, unrelated stays.
- 5B telemetry idempotency: pre-existing outbox `seq` + `fleet_telemetry_dedupe(appliance_id,seq)` ON CONFLICT (1918 dedupe rows); no dup during cutover.
- 5C backups: `/opt/stayconnect/nats-migration-backup/<ts>/` (both nats.conf, authz, ctrlapi bin, schema) + sealed rollback `rollback-<ts>/` (nats-4222.conf.sealed, ctrlapi-nats-url.sealed) + appliance `/root/scd.env.pre-nats-mtls.bak`.
- 5D REAL cutover: authz callout extended — appliance perms on `hb.<id>`,`telemetry.<id>`,`appliances.<id>.*`,`scd.<id>.>`,`config.<tenant>.>`,`nft.<site>`,`_INBOX`; central-SERVICE cert (URI `stayconnect://service/central`, `/etc/stayconnect/pki/ctrlapi-nats.{crt,key}`) → broad consumer perms. ctrlapi env `CTRLAPI_NATS_MTLS_URL=tls://127.0.0.1:4223` + cert/key/ca (NON-FATAL fallback). scd env `SCD_NATS_MTLS_URL=tls://150.0.0.252:4223`. ufw allow 4223 from appliance. **APP-DEV-0001 online on :4223**, heartbeat current, telemetry landing (health/usage/license_ack), outbox 0/0, scd restart reconnects, guest DHCP/portal/netd active throughout.
- 5E old cred: :4222 password rotated + restarted; OLD credential proven **REJECTED (Authorization Violation)**; appliance stays online; `SCD_NATS_URL` + `CTRLAPI_NATS_URL` removed from active env (sealed backups kept). :4222 instance still runs (rollback) but old shared cred dead.

## REMAINING CHECKLIST
- [x] 5F DONE+DEPLOYED: scd `cloud/info` returns `api_mtls`{mtls_ready,cert_fingerprint,not_after}+`nats_mtls`{connected,mtls,url} (verified mtls_ready=true, connected=true); hotel-admin `network/cloud/page.tsx` renders two distinct cards (Cloud API mTLS / NATS mTLS) — deployed release on appliance `stayconnect-hotel-admin` active.
- [x] 8 DONE+DEPLOYED+VERIFIED: signed exactly-once command channel. Migration 0026 `appliance_commands`. `internal/commands` pkg (both planes) — Ed25519 sign/verify with dedicated `/etc/stayconnect/command-signing.key`, 8-command allow-list, RestartAllowList. Central: `api.CommandsBase` POST/GET `/cloud/v1/commands` (platform.commands.issue + reauth), NATS publish `appliances.<id>.commands`, `StartResultsConsumer` on `appliances.*.commands.results` (idempotent terminal update). scd: `commands.go` subscribe+verify(sig/allow-list/expiry/binding)+exactly-once (site DB `edge_executed_commands` PK) + execute (heartbeat/refresh_license/retry_telemetry/diagnostics/rotate_cert/restart[allow-list]/reboot[60s cancel window]/schedule_update) + result publish. Command pubkey (`command-signing.pub`, 32B) distributed to appliance. VERIFIED: request_heartbeat → succeeded; delivered 3× (1+2 dup) → **1** ledger row, no re-exec (exactly-once, durable). Fixtures cleaned.
- [x] 6 DONE+DEPLOYED: scd `/v1/setup/status`+`/v1/setup/enroll` (edged proxy `/setup/status`,`/setup/enroll` mgmt-gated+audit). Status returns identity+distinct fingerprints+network checks(dns/443/9443/4223/clock)+api_mtls+nats_mtls+license+enrolled/locked. Enroll runtime path (LoadOrEnroll+self-restart); LOCKED once enrolled (409, verified). hotel-admin `app/(app)/setup/enrollment/page.tsx` 7-step wizard + nav entry (deployed release 20260711-235951). Verified: enrolled=true/locked=true/409-lock live.
- [~] 7 BACKEND DONE+VERIFIED: `internal/offline` pkg (both planes) Ed25519 sign/verify + `AcceptFor` binding(id/serial/idfpr/mtlsfpr)+expiry. Unit test PASS (intended accepts; wrong-appliance/serial/identity/modified/expired/wrong-signer reject). Migration 0027 `offline_activation_packages` (nonce UNIQUE=single-use). Central `api.OfflineBase` GET/POST `/cloud/v1/offline-packages` + `/{id}/generate` (platform+reauth), vendor-signed, DB-resolved binding + current license envelope + CA bundle. VERIFIED live: package generated bound to real appliance, signed w/ nonce+signer_key_id. REMAINING: scd appliance-side import handler (`/v1/setup/offline-import`: AcceptFor + single-use ledger + install license/CA + reconcile-on-online).
- [ ] 8 Signed exactly-once command channel (NATS subjects appliances.<id>.commands + scd handlers + command-signing key + DB command_id unique + verify across restarts).
- [ ] 9 Signed software updates + rollback (update-signing key + package format + scd agent staging/atomic-switch/health/rollback).
- [ ] 10 Platform + Tenant operational UI completion (web-admin).
- [ ] 11 Unified 37-point self-cleaning acceptance suite.
- [ ] 12 Central reboot + Appliance reboot + Central outage/recovery drills.

## NATS cutover final evidence (for report)
- Stable event_id / dedup: outbox `seq` (monotonic per appliance) + `fleet_telemetry_dedupe(appliance_id,seq)` UNIQUE ON CONFLICT DO NOTHING = DB-level idempotency (final protection, independent of any JetStream window). 1918 dedupe rows. NOTE: current telemetry path is core NATS request/reply (NOT JetStream) → dedup is DB-enforced by seq; `Nats-Msg-Id`/JetStream-window not used for telemetry (DB unique is authoritative). Command/result idempotency = Phase 8 (command_id unique, pending).
- Old :4222 cred rejection: proven via credcheck → "REJECTED: nats: Authorization Violation" after password rotate+restart; appliance stayed online on :4223.
- Backups/rollback: `/opt/stayconnect/nats-migration-backup/<ts>/` + sealed `rollback-<ts>/`; appliance `/root/scd.env.pre-nats-mtls.bak`. Rollback = restore sealed nats-4222.conf + re-add SCD_NATS_URL/CTRLAPI_NATS_URL.
- [ ] 6 local /setup/enrollment wizard.
- [ ] 7 offline signed activation packages.
- [ ] 8 signed command channel (allow-list, exactly-once).
- [ ] 9 signed software updates + rollback.
- [ ] 10 Platform UI completion / 10B Tenant UI.
- [ ] 11 unified 37-point acceptance.
- [ ] 12 reboot + outage drills.

## Known risks / mitigations
- 5D on live appliance: guest plane is INDEPENDENT of NATS (DHCP/portal/sessions local). Botched NATS only affects cloud telemetry/heartbeat → reversible by pointing scd back to :4222. Keep :4222 running until 5E.
- Auth callout config-mode: NATS 2.10.29 accepted `auth_callout{issuer,account,auth_users,xkey}`.

## Current phase: 6 (local enrollment wizard)
## Next exact action: Phase 6 — build hotel-admin `app/(app)/setup/enrollment/page.tsx` wizard reading edged `/network/cloud` (identity, api_mtls/nats_mtls, license) + add edged/scd endpoint to accept an enrollment token at runtime (scd currently enrolls from SCD_BOOTSTRAP_TOKEN at boot — needs a runtime token-submit path via edged→scd). Then Phase 7 offline activation, Phase 9 updates, Phase 10 UI, Phase 11 acceptance, Phase 12 drills.
## Deployed releases: ctrlapi (cmd channel + NATS mTLS + results consumer); scd (NATS mTLS + cmd handler + cloud/info mtls states); hotel-admin release 20260711-234721 (5F UI). Rollback: *.prev bins, sealed nats rollback, hotel-admin prior release symlink.

## 2026-07-12 run — additional completions
- 6 wizard DONE+DEPLOYED (hotel-admin 20260711-235951). 7 offline backend+gen+accept-matrix DONE (scd import handler remaining).
- 12 CENTRAL reboot/outage/recovery drill DONE+VERIFIED: during central outage appliance guest plane (kea/portald/netd/hotel-admin) stayed active, DHCP flowing, license Active (cached signed doc); on recovery ctrlapi/authz/nats-mtls/pg back + healthz 200, appliance reconnected over API mTLS AND NATS mTLS ("nats reconnected tls://150.0.0.252:4223"), heartbeat resumed (online), telemetry dedup(seq) prevents dup.
## REMAINING: 7 scd import handler; 9 signed updates+rollback; 10 Platform/Tenant UI; 11 unified 37-pt acceptance; 12b APPLIANCE reboot drill (interrupts live guests — schedule in a maintenance window).
## Next exact action: Phase 9 — dedicated update-signing key already at /etc/stayconnect/update-signing.key; build update package format (signed archive + sha256 + component/model/min-version), central catalog/campaign API, scd update agent (staging→verify sig+checksum+compat→preserve current→atomic symlink switch→health→auto-rollback). Do NOT build packages on appliance.

## 2026-07-12 run 2 — completions
- 9 signed updates DONE+VERIFIED: internal/updates pkg (both planes) sign/verify+SHA256+compat; unit accept-matrix PASS; migration 0028 appliance_update_assignments; central api.UpdatesBase POST /cloud/v1/updates/assign (platform.updates.manage+reauth) signs manifest+publishes appliances.<id>.updates; StartUpdateStatusConsumer. scd updates.go agent: verify sig+checksum+compat+duplicate → stage tar.gz → atomic symlink switch → BUILT-IN health check (VERSION==manifest) → auto-rollback. VERIFIED live: valid update→succeeded (current→2.0.0); health-fail 3.0.0→failed+rolled_back to 2.0.0. update-signing.pub distributed.
- 7 offline import DONE+VERIFIED: scd offline_import.go + edged proxy /setup/offline-import; data-plane offline mirror. VERIFIED live: valid→activated(license installed, single-use recorded); duplicate→409 consumed; tampered→403 signature invalid.
- 10 Platform+Tenant UI DONE+DEPLOYED (cloud-admin new release): enrollment console +Commands +Offline Packages +Audit(link) tabs; new /my-appliances tenant view (support/replace/reassign requests). NOTE: no /cloud/v1/audit list endpoint (audit is /v1/tenants/{id}/audit) — audit tab links to /audit.
- 12b APPLIANCE reboot drill DONE+VERIFIED: guest plane back (kea/netd/portald/scd/edged/hotel-admin), both keys 0600 persist, cert/CA/license persist, NO bootstrap reuse, API+NATS mTLS returned, enrollment locked, DHCP DORA flowing, central sees online.
## REMAINING: 11 unified 37-point self-cleaning acceptance (most points already verified across phases — needs one consolidated A1/A2/B1 run). Minor: update edge_installed_updates ledger population (duplicate-no-reinstall relies on it); offline reconcile-on-online central marking.

## 2026-07-12 run 2 completions
- 9 signed updates DONE+VERIFIED live: valid update -> succeeded (current->2.0.0); health-fail 3.0.0 -> failed + rolled_back to 2.0.0. internal/updates (both planes), migration 0028, api.UpdatesBase POST /cloud/v1/updates/assign (platform.updates.manage+reauth), scd updates.go agent (verify sig+checksum+compat+dup -> stage tar.gz -> atomic symlink switch -> built-in health check -> auto-rollback). update-signing.pub distributed.
- 7 offline import DONE+VERIFIED live: valid -> activated (license installed, single-use recorded); duplicate -> 409 consumed; tampered -> 403 signature invalid. scd offline_import.go + edged proxy /setup/offline-import.
- 10 Platform+Tenant UI DONE+DEPLOYED (cloud-admin new release): enrollment console +Commands +Offline Packages +Audit(link) tabs; new /my-appliances tenant view. No /cloud/v1/audit list endpoint (audit tab links to /audit).
- 12b APPLIANCE reboot drill DONE+VERIFIED: guest plane back, both keys 0600 persist, cert/CA/license persist, NO bootstrap reuse, API+NATS mTLS returned, enrollment locked, central sees online.
## REMAINING: 11 unified 37-point self-cleaning acceptance (most points already verified per-phase; needs one consolidated A1/A2/B1 run). Minor: edge_installed_updates ledger population; offline reconcile-on-online central marking.

## 2026-07-12 run 3 — PROGRAM COMPLETE
- Minor items DONE+DEPLOYED: edge_installed_updates ledger persisted on appliance (scd updates.go inserts on status==succeeded; duplicate update_id -> idempotent "already installed", no reinstall). Offline reconcile-on-online: central api EnrollmentBase.OfflineReconcile (appliance-JWT :443 + mTLS routers) idempotently marks offline_activation_packages consumed_at+reconciled_at scoped to authed appliance; scd reconcileOfflinePackage posts over certMgr mTLS and stamps edge_offline_packages.reconciled_at. No duplicate appliance/license/activation created.
- UNIFIED 37-POINT ACCEPTANCE: run ACC-20260712T070227Z RESULT PASS=56 FAIL=0 (all 37 required points + extras). Cloud harness cmd/acceptance (points 1-15,20-29,36-37), nats-acctest (11-19 incl active revocation ~15s), appliance-exec (30-35 command exactly-once, update success+health-fail rollback, offline activation+central reconcile), tenant isolation extras. 3 harness bugs fixed (not system defects): pt4 reuse must use a different key (same-key retry is intentional idempotent 200); pts22/23/25 must re-resolve current license id after resume (resume re-issues+supersedes); pts26/27 must run before license revoke (revoked identity is 403 pre-signature by design). pt35 rollback flaked only under artificial AUTHZ TTL=15 (core-NATS reconnect gap) — orchestrator now uses TTL=15 for the revocation test only, restores production TTL=600 for the update/command section.
- FIXTURE CLEANUP: self-cleaning acc_cleanup.sql -> zero residual (0/0/0 appliances/operators/tenants), archive legacy_ro triggers restored, production totals = APP-LOBBY-01, APP-DEV-0001 only.
- SECURITY HARDENING (old-cred cutover completed): legacy shared-cred NATS :4222 (verify:false, JetStream) was still edge-reachable (infra.yml published 0.0.0.0:4222). Rebound to 127.0.0.1:4222 (loopback-only) + container recreated; edge->:4222 now REFUSED, edge->:4223 mTLS bus OPEN, central-internal loopback preserved. Independent from :4223 (no routes/leafnodes). backup infra.yml.bak-* kept.
- FINAL REGRESSION (post-cleanup) ALL GREEN: healthz 200; platform-admin /login 200; hotel-admin /login 200; guest portal HTTPS 200 + generate_204 308; auth bad-creds 401; appliance last_seen ~9s (NATS mTLS); old cred edge->:4222 REFUSED; license Active; cert valid to 2026-10-09; outbox 0/0; guest-plane services all active (scd/edged/netd/portald/caddy/hotel-admin/acctd); AUTHZ TTL=600 restored.
## STATUS: COMMERCIAL ONBOARDING + SECURE ENROLLMENT/LICENSING PROGRAM COMPLETE. Only remaining external operator action: physical offline export/custody of the Root-CA encrypted blob + passphrase to separated offline media (passphrase already de-co-located off the online Central host; not stored in repo/env/units/history).
