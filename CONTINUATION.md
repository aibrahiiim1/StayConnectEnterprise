# Continuation checkpoint

> **This file is a CHECKPOINT, not a state store.** The authoritative current state is
> `governance/project-state.json`, and it is the only place current operational state is defined. This file
> exists to hand a session to the next one: what was just done, what is known to be open, and the
> environment facts that cost time to rediscover. If the two ever disagree, the register wins.
>
> It went stale once, badly: it announced PR #120 as the master head for nine days and 52 merges, and
> carried an "OPEN DEFECT" that four later PRs had closed. Nothing read it, because it was in no validator's
> surface list. It now is — `tools/validate-standing-records.py` checks any stated master head here against
> `current_state_facts.master_head_at_delivery_base` and live master.

## Where the tree is

Delivery base: master `44077fb8` — the merge of PR #172, D41/T0174, which deferred all real Guest
activation until StayConnect is functionally complete. Current work is the functional-completeness closure
that deferral asks for.

## PRE-LIVE 172.21.60.25, as read on 2026-09-22

The only appliance. Onboarded, enrolled, claimed, licensed, under a pinned signed assignment. the retired development reference appliance
is RETIRED and must not be contacted.

* Schema head `0083`. All eight StayConnect units active; `kea-dhcp4-server` enabled; containers
  `stayconnect-pg` (timescaledb 2.16.1-pg16), `-redis`, `-nats`.
* **The database superuser role is `stayconnect`, not `postgres`.** `su - postgres` and `psql -U postgres`
  both fail on this appliance; every query goes through
  `docker exec stayconnect-pg psql -U stayconnect -d stayconnect_site`.
* Networking: `ens160` WAN/management `172.21.60.25/24`; `ens192` is a member of `br-g-00d1fa1a`
  (`192.168.77.1/24`), so the single guest network is **untagged**. There is no `ens192.<vid>` and **no
  `br-lan`** — an assertion about a legacy bridge will fail here, and that is the appliance, not the test.
* Counters (live read, recorded in `current_state_facts.live_counters`): 28 purchases, 28 entitlements,
  4 active, 17 sessions, 0 live, 14 310 accounting records, 8 vouchers (all REVOKED).
* **Financial traffic is ZERO** on all five counters and has never been anything else.
* Deployed runtime is MIXED, per service, and is recorded that way: `scd` 759286af,
  `edged`/`portald`/`netd` a4124ce5, `pmsd`/`acctd` 305587b6, `keybootstrap` d4e1dc76,
  `sitemigrate`/`svc-run` 29a6b21f. All five are ancestors of master. A service this delivery does not
  change is not rebuilt merely to make the record uniform.

## Verified complete in the closure so far

* **Three migrations the runner could not see.** 0081–0083 were in a root `migrations/` directory; every
  tool reads `data-plane/migrations`. A rebuild from the repository would have stopped at 0080 and called
  itself complete, three least-privilege grants short of the appliance.
  `tools/validate-migration-location.py` now refuses it, proven bidirectionally.
* **A voucher could not be redeemed once.** The single-use burn was a direct `UPDATE iam_v2.vouchers` by
  `svc_scd`, which holds no UPDATE on that table, in the same transaction as the grant — so the grant rolled
  back. Migration 0084 moves the burn into the SECURITY DEFINER grant kernel; `svc_scd` gains no privilege.
* **Voucher code format is a setting** (0085): digits-only or mixed, 6–8 characters, audited, with the
  issuance path wired to `internal/codegen` instead of its own hardcoded alphabet.
* **Guest-network update** refused topology changes silently and overwrote `dns_servers` with JSON `null`
  on any PUT that omitted it. Both fixed.
* **Operator rollback of a confirmed revision** took the whole guest network down and reported success. It
  now refuses with 409.
* Four harnesses that defaulted to or asserted the retired appliance, one of which reboots it.

## Open, and known

* The functional-completeness gap inventory is wider than what is closed above — guest networking
  multi-VLAN proof on the current architecture, the voucher admin surface (issuance has no caller at all),
  reveal/export, and the live recovery drills. See the delivery's own report for the current list.
* One Product-Owner decision remains open and is unchanged: whether master protection should additionally
  require an approving review. It needs a second reviewing account, because GitHub will not let a PR author
  approve their own PR.

## Build environment

Designated workstation, Docker 29.5.2, data disk on C: (F: is NOT achievable — Docker Desktop 4.76 on the
WSL2 backend ignores `DataFolder` and reverts a supported WSL distro move). Go builds need `-p 2` /
`GOMAXPROCS=2`; Node needs `--no-file-parallelism`. Do NOT set `MSYS_NO_PATHCONV=1` for
`clean-install-reconstruction.sh` or `generate-production-baseline.sh` — they manage path conversion
themselves and the override breaks them.

`iam_v2_scratch/run.sh` refuses `stayconnect_site`, `stayconnect` and `stayconnect_site_b` as scratch
database names — a live-database guard. Use a name like `iam_scratch`. The `timescale/timescaledb` image
initialises, shuts down and restarts, so wait on `pg_isready` rather than on a first successful query.
