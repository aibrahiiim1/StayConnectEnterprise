# StayConnect — active/passive HA runbook

Phase 5.5 adds active/passive HA for a **single site**. The pair shares a
virtual IP (the guest gateway) managed by VRRP; keepalived decides which
box owns the VIP. nf_conntrack entries are replicated by conntrackd so
existing TCP/UDP flows survive a failover. The scd-owned nft `auth_ipv4`
set is replicated via NATS (`nft.{siteID}`) — both scds keep a synchronised
view of who's authenticated.

## Topology

```
        ┌──────────┐  VRRP adverts (multicast 224.0.0.18)
        │  LAN     │◀─────────────────────┐
        │          │                      │
        │ guests   │       ┌──────────────┼──────────────┐
        └────┬─────┘       │              │              │
             │             │              │              │
        VIP  │             │              │              │
       10.10.0.1           │    sync      │              │
             ┌─────────────┼──────────────┼──────────────┐
             │             │              │              │
        ┌────▼────┐    ┌───▼────┐    ┌────▼───┐     ┌────▼───┐
        │  scd A  │    │portald │    │conntrd │     │kd'alvd │
        │ (MASTER)│    │  A     │    │  A     │     │   A    │
        └─────────┘    └────────┘    └────────┘     └────────┘
             │              │              │              │
            Shared NATS + Postgres (control-plane side)
             │              │              │              │
        ┌────▼────┐    ┌────▼───┐    ┌────▼───┐     ┌────▼───┐
        │  scd B  │    │portald │    │conntrd │     │kd'alvd │
        │ (BACKUP)│    │  B     │    │  B     │     │   B    │
        └─────────┘    └────────┘    └────────┘     └────────┘
```

## What replicates, and how

| State                 | Path                                        |
|-----------------------|---------------------------------------------|
| Guest gateway traffic | VRRP VIP — only master answers              |
| Live TCP/UDP flows    | conntrackd (multicast on sync VLAN)         |
| `sessions` DB rows    | Shared Postgres (already HA at DB tier)     |
| `auth_ipv4` nft set   | scd → NATS `nft.{siteID}` → peer scd        |
| PMS provider config   | ctrlapi → NATS `config.{tenantID}.pms` (5.3)|
| Liveness              | scd → NATS `hb.{applianceID}` (5.4)         |

## Setup

1. **Enroll both boxes** through the admin UI (phase 5.1 flow). Both must
   be in the same `site` row. Record their appliance IDs.

2. **Install keepalived + conntrackd** on both boxes:
   ```sh
   apt install keepalived conntrackd
   ```

3. **Drop in the config templates**:
   - `keepalived.conf` → `/etc/keepalived/keepalived.conf`
   - `conntrackd.conf` → `/etc/conntrackd/conntrackd.conf`
   - `stayconnect-ha-notify.sh` → `/usr/local/bin/stayconnect-ha-notify` (chmod 755)

   Fill in the placeholders:
   - `<guest-lan-iface>` — interface serving the guest LAN (e.g. `eth1`)
   - `<guest-gateway-vip>/<prefix>` — the VIP guests already use as gateway (e.g. `10.10.0.1/24`)
   - `<shared-16char-secret>` — identical on both boxes
   - `<sync-iface>` — a dedicated sync VLAN interface (e.g. `eth2`)

   On the **backup** box:
   - `state BACKUP`
   - `priority 100` (master stays at 150)

4. **Enable the services**:
   ```sh
   systemctl enable --now conntrackd keepalived
   ```

5. **Verify**:
   - Master: `ip -4 a show dev <guest-lan-iface>` shows the VIP.
   - Backup: VIP NOT present.
   - Both: `conntrackd -s` prints matched state counters.
   - Both: `journalctl -u stayconnect-scd | grep "nft sync"` shows the
     subject subscription, and auth events on master generate `nft sync`
     mirror log lines on backup.

## Failover behaviour

- **Power loss on master**: VIP shifts in ~3s (3 missed adverts).
  conntrackd resync finishes within ~200ms. Existing guest TCP flows keep
  their ACK numbers and don't need to retransmit more than a handful of
  segments.
- **scd crash on master**: the `chk_scd` VRRP script fails within 4s
  (2 intervals × fall=2). Keepalived demotes the master, VIP shifts.
- **NATS partition**: nft sync queues up on the master's side; no guest
  impact until a failover — at which point the new master's set may lag
  by a few seconds. Auth events on the new master rebuild the set for
  incoming guests.

## Known limitations (to address in later phases)

- **No split-brain detection**: if the sync VLAN fails, both boxes may
  claim master. Mitigate by running keepalived adverts on **two**
  interfaces (add a second `vrrp_instance` with `virtual_ipaddress`
  commented out, tracking the main one).
- **NATS is a single point of failure for nft sync**. Production should
  run NATS in a 3-node cluster (straightforward — NATS ships with
  clustering built in).
- **Boot reconcile** rebuilds from the `sessions` table — fast for
  hundreds of rows, scales to tens of thousands; beyond that, consider
  a shorter replay window via `last_activity_at`.
