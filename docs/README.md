# StayConnect Enterprise — Documentation Index

Start here. This index is organized by what you're trying to do. If you're new to
StayConnect, read the **Complete Operations Manual** first — it takes a hotel from
an unpacked appliance to live, licensed guest WiFi and covers day-2 operations.

> **Current production model (read this first):** onboarding is **zero-touch** —
> a factory-clean appliance with internet registers itself with Central (no token)
> and appears under **Appliances** as **Waiting for activation**, where one
> **Activate** step picks the customer, site and licence terms. A site without
> internet uses **offline activation** (one file each way). The entitlement is a
> **signed appliance licence** (max concurrent online guests + validity + grace).
> **Plans, subscriptions and enrollment tokens no longer exist.** Central is
> licensing, activation and fleet status only — the binding description is
> [CENTRAL_CONTROL_PLANE.md](CENTRAL_CONTROL_PLANE.md).

---

## 1. Quick start — new hotel installation

- [APPLIANCE_ONBOARDING_MANUAL.md](APPLIANCE_ONBOARDING_MANUAL.md) — the shortest
  UI-only path to bring one hotel online (zero-touch, two consoles).

## 2. Complete Operations Manual — **recommended starting point**

- [STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md](STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md)
  — end-to-end: architecture, install, WAN/LAN, registration, activation,
  convergence, the license model & states, concurrent capacity, guest VLANs (with
  worked VLAN 100 / VLAN 200 examples), DHCP/DNS/NAT/portal, auth methods, access
  plans & vouchers, integrations, renewal & anti-replay, recovery, Central-outage
  behavior, replacement & rebind, factory reset, deactivate/revoke/decommission/
  delete, safe-deletion order, security/certs, backup, audit, troubleshooting, and
  go-live / day-2 checklists.

## 3. Control Panel (Central) — configuration manual

- [CENTRAL_CONTROL_PLANE.md](CENTRAL_CONTROL_PLANE.md) — **authoritative**: what
  Central is, the console structure, appliance states, lifecycle, API contract and
  roles.
- [user-guide/control-panel-config-manual.md](user-guide/control-panel-config-manual.md)
  — how to create a **Customer → Site**, activate an **Appliance**, set its
  **Licence**, and run day-2 operations from the Central console.

## 4. Control Panel (Central) — page reference

- [user-guide/control-panel-reference.md](user-guide/control-panel-reference.md) —
  every Central console page: Overview, Customers (and the customer page), Appliances
  (and the appliance page), Licenses, and System (Security alerts, Trust & keys,
  Audit log, Team, Backup health).

## 5. Hotel Admin (Appliance) — configuration manual

- [user-guide/hotel-admin-config-manual.md](user-guide/hotel-admin-config-manual.md)
  — activate (Appliance & licence page), then fully configure the appliance: WAN/LAN, guest VLANs,
  auth methods, vouchers, integrations, branding, operators, TLS, diagnostics.

## 6. Hotel Admin (Appliance) — page reference

- [user-guide/hotel-admin-reference.md](user-guide/hotel-admin-reference.md) — every
  Hotel Admin page and what each option does.

## 7. Troubleshooting & recovery

- [NETWORK_TROUBLESHOOTING.md](NETWORK_TROUBLESHOOTING.md)
- [NETWORK_APPLY_AND_ROLLBACK.md](NETWORK_APPLY_AND_ROLLBACK.md) ·
  [ROLLBACK_POLICY.md](ROLLBACK_POLICY.md)
- [OFFLINE_OPERATION.md](OFFLINE_OPERATION.md) — behavior when Central is unreachable
- [BACKUP_AND_RESTORE.md](BACKUP_AND_RESTORE.md) ·
  [BACKUP_RETENTION.md](BACKUP_RETENTION.md)
- [MIGRATION_RUNBOOK.md](MIGRATION_RUNBOOK.md)
- Complete Operations Manual → **Troubleshooting** and **Reboot & service recovery**.

## 8. Security & PKI runbooks

- [SECURITY_HARDENING.md](SECURITY_HARDENING.md)
- [CA_CEREMONY_RUNBOOK.md](CA_CEREMONY_RUNBOOK.md)
- [ASSIGNMENT_KEY_CUSTODY_RUNBOOK.md](ASSIGNMENT_KEY_CUSTODY_RUNBOOK.md)
- [VENDOR_BREAKGLASS_RUNBOOK.md](VENDOR_BREAKGLASS_RUNBOOK.md)
- [HOTEL_ADMIN_CERT_LIFECYCLE.md](HOTEL_ADMIN_CERT_LIFECYCLE.md)
- [LICENSING_AND_ENTITLEMENTS.md](LICENSING_AND_ENTITLEMENTS.md)
- [TERMINAL_DELIVERY_SECURITY_EVIDENCE.md](TERMINAL_DELIVERY_SECURITY_EVIDENCE.md)

## 9. Governance & delivery (permanent rules)

- [GITHUB_EXECUTION_AND_DELIVERY_RULE.md](GITHUB_EXECUTION_AND_DELIVERY_RULE.md) — the
  GitHub repo `aibrahiiim1/StayConnectEnterprise` is the only authoritative source;
  one Phase per branch + one PR; every final report embeds the deterministic
  changed-file manifest.
- [ZERO_STALE_LEFTOVERS_RULE.md](ZERO_STALE_LEFTOVERS_RULE.md) — no stale/contradictory
  artifact may survive a completed task; enforced by `tools/project-state.py` and
  `tools/validate-project-state.sh`.
- [templates/PHASE_FINAL_REPORT_TEMPLATE.md](templates/PHASE_FINAL_REPORT_TEMPLATE.md) —
  mandatory 20-section final-report structure.
- Machine-readable current state: `governance/project-state.json` (validate with
  `make governance-validate`); changed-file manifest: `tools/generate-change-manifest.py`.

---

## Architecture & deep-dive references

| Topic | Document |
|---|---|
| Full system reference (historical snapshot, 2026-07-10) | [SYSTEM_OVERVIEW.md](SYSTEM_OVERVIEW.md) |
| Central (licensing, activation, fleet status) — authoritative | [CENTRAL_CONTROL_PLANE.md](CENTRAL_CONTROL_PLANE.md) |
| Cloud / control-plane architecture | [CLOUD_ARCHITECTURE.md](CLOUD_ARCHITECTURE.md) · [TARGET_ARCHITECTURE.md](TARGET_ARCHITECTURE.md) |
| Edge / appliance architecture | [EDGE_ARCHITECTURE.md](EDGE_ARCHITECTURE.md) · [EDGE_NETWORKING.md](EDGE_NETWORKING.md) |
| Guest VLANs | [GUEST_VLAN_CONFIGURATION.md](GUEST_VLAN_CONFIGURATION.md) · [ARUBA_SSID_VLAN_MAPPING.md](ARUBA_SSID_VLAN_MAPPING.md) |
| DHCP | [DHCP_MANAGEMENT.md](DHCP_MANAGEMENT.md) · [DHCP_OPTION_114.md](DHCP_OPTION_114.md) · [EXTERNAL_DHCP_MODE.md](EXTERNAL_DHCP_MODE.md) |
| Sync protocol (historical — telemetry is off, CLAUDE.md §0E) | [SYNC_PROTOCOL.md](SYNC_PROTOCOL.md) |
| Deployment | [DEPLOYMENT_CLOUD.md](DEPLOYMENT_CLOUD.md) · [DEPLOYMENT_APPLIANCE.md](DEPLOYMENT_APPLIANCE.md) |
| Roles & scope | [ROLE_AND_SCOPE_MATRIX.md](ROLE_AND_SCOPE_MATRIX.md) |
| Data ownership | [DATA_OWNERSHIP.md](DATA_OWNERSHIP.md) |
| Testing | [TESTING_RUNBOOK.md](TESTING_RUNBOOK.md) |

The role-based, task-oriented user guides live under
[user-guide/](user-guide/README.md).
