# ADR-0001 — Dedicated `pmsd` daemon owns each PMS Interface connection

- **Status:** ACCEPTED (Phase 3, D14 / T0015, 2026-07-18)
- **Context phase:** Phase 3 (PMS Stay Domain, STRICT resolution, Checkout Grace)
- **Supersedes:** none. Does not change the legacy production PMS-auth path.

## Context

The Phase-0 Contract §8/§9 and the Phase-3 authorization §9 require a specific PMS connector architecture:

- long-lived PMS sockets are **not** owned by guest HTTP handlers;
- `portald`, `scd` and `edged` must **not** each create their own PMS connection;
- **one dedicated local connector owner** manages each PMS Interface, with **one independent worker/state-machine per Interface** and **failure isolation** between Interfaces;
- all operation is **local-first** (no cloud dependency for authentication, event processing or Grace);
- interface ownership **survives service restart** safely;
- **only one client may own a PMS Interface connection at a time.**

A read-only audit established the as-built reality:

- A proven, working FIAS TCP client already exists: `data-plane/internal/pms/protel_fias.go` (472 lines) — STX/ETX framing, `LS/LD/LR` handshake (`recordLS`/`recordLD`/`recordLRs`), `GI/GC/GO/LA/LE` handling (`handleRecord`), read-only `DR` resync, an in-memory room cache, and a `runLoop`/`connectAndServe` reconnect loop.
- **The long-lived socket is owned by `scd`** — the guest-facing session daemon — through the provider goroutine started at boot (`data-plane/cmd/scd/main.go:622` → `pmsloader.StartAll` → `ProtelFIAS.Start` → `go p.runLoop`). scd is the daemon that owns the nftables `auth_ipv4` set, the `sessions` table, and the unix socket that portald calls.
- There is **no cross-process single-owner lock**: the only de-duplication is a NATS reload queue group (`scd/nats.go`), which arbitrates *reload events*, not *socket ownership*. If two scd instances ran, both would dial the PMS.

This directly conflicts with the required architecture: the long-running PMS socket currently lives inside a guest-facing daemon, and there is no single-owner guarantee.

## Decision

Introduce a **dedicated local daemon `data-plane/cmd/pmsd`** that owns each PMS Interface connection, gated by `STAYCONNECT_PHASE3_PMS_CONNECTOR` (child of `STAYCONNECT_PHASE3_MASTER`, default OFF).

`pmsd`:

1. **Reuses the proven protocol layer** (`data-plane/internal/pms`) — it does **not** create a second FIAS stack. The protocol/framing/handshake/resync logic in `protel_fias.go` is the single source of PMS wire behavior.
2. Runs **one independent supervised worker/state-machine per Interface** — bounded exponential reconnect backoff, deterministic connection state, keepalive/heartbeat observation, full-resync state, SIGTERM drain, restart/reboot recovery. One Interface failure does not stop another (failure isolation).
3. Acquires a **DB advisory single-owner lock per `(tenant_id, site_id, pms_interface_id)`** before opening a socket; a competing owner is rejected. The FIAS spike's orphan-slot reap (verify the PMS single-client slot is free before connecting) is honored at start.
4. **Persists connector freshness/cursor** to the new `pms_interface_runtime` table (migration 0010) so that `scd`, `edged` and `portald` read freshness/health from the DB and never own a PMS socket themselves.
5. **Pins** the current Interface Revision + secret generation when opening a connection; never logs credential material; redacts record framing payload before any persistence or evidence.
6. Contains **no `PS` sender** and generates **no financial record** — Phase 3 is read-only (financial posting is Phase 4).
7. Is **local-first**: no cloud dependency for authentication, event processing or Grace.
8. Runs under a dedicated least-privilege role `svc_pmsd` with **zero iam_v2 runtime grants while dark** (see `Phase3-Privilege-Matrix.md`).

A new systemd unit `deploy/systemd/stayconnect-pmsd.service` runs it. While `STAYCONNECT_PHASE3_PMS_CONNECTOR` is OFF (the delivered/deployed state) `pmsd` opens **no** socket and constructs **no** Phase-3 repository.

## Alternatives considered

- **Extend `scd` to own the connector (keep the socket in scd).** Rejected: scd is a guest-facing HTTP/unix-socket daemon; §9 forbids the long-running PMS socket living inside a guest daemon, and scd's lifecycle is coupled to guest-session control. The single-owner and failure-isolation requirements are cleaner in a dedicated daemon.
- **Per-daemon connectors in scd/portald/edged.** Rejected outright by §9 (each must not create its own PMS connection).
- **A cloud-hosted connector.** Rejected: violates the local-first requirement (no cloud dependency for auth/events/Grace).

## Consequences

- The **legacy** production PMS-auth path (scd's embedded FIAS ownership, `pms_providers`/`pms_attempts`) is **unchanged and remains authoritative today**. The Phase-3 `pmsd` path is DARK and separate until a future, separately-authorized cutover.
- A future increment may migrate the legacy path onto `pmsd` under its own authorization; that cutover is **out of Phase-3 scope**.
- Single-owner locking and freshness persistence become first-class, satisfying restart/reboot recovery and the "exactly one client owns the Interface" invariant.
