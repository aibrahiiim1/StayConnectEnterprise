# OneGate — Documentation Index

**Two current references win over every other document** (and where a document and the code disagree, the code wins):

- the [system handbook](architecture/ONEGATE_SYSTEM_HANDBOOK.md) — the system as built, each fact labelled with how it
  was verified;
- the [Fresh VM Installation Manual](runbooks/ONEGATE_FRESH_VM_INSTALLATION_MANUAL.md) — building, installing,
  verifying, upgrading and recovering an appliance and a Central (Appendix 3: what was validated on the test pair).

Product words (Client, Admin Console, Client Portal, Site) are defined in [PRODUCT_TERMINOLOGY.md](PRODUCT_TERMINOLOGY.md).
Current environments and retired targets: [`CLAUDE.md`](../CLAUDE.md) §0D. Removed documents, and the commit to read
each from: [ARCHIVE.md](ARCHIVE.md). Audit findings and their status: [AUDIT_BACKLOG.md](AUDIT_BACKLOG.md); the decisions they wait on:
[AUDIT_DECISIONS.md](AUDIT_DECISIONS.md).

> **Production model.** Onboarding is zero-touch: a factory-clean appliance registers itself with Central (no token)
> and waits for **Activate** (customer, site, licence terms); a site without internet uses offline activation. The
> entitlement is a signed appliance licence. Central is licensing, activation and fleet status only
> ([CENTRAL_CONTROL_PLANE.md](CENTRAL_CONTROL_PLANE.md)).

## Install and operate

| Need | Document |
|---|---|
| Install a new appliance or Central | [runbooks/ONEGATE_FRESH_VM_INSTALLATION_MANUAL.md](runbooks/ONEGATE_FRESH_VM_INSTALLATION_MANUAL.md) |
| Shortest UI path to bring a site online | [APPLIANCE_ONBOARDING_MANUAL.md](APPLIANCE_ONBOARDING_MANUAL.md) |
| Central: deploy, upgrade, move | [DEPLOYMENT_CLOUD.md](DEPLOYMENT_CLOUD.md) · [ROLLBACK_POLICY.md](ROLLBACK_POLICY.md) |
| Appliance: services, ports, security notes | [DEPLOYMENT_APPLIANCE.md](DEPLOYMENT_APPLIANCE.md) |
| Activation, licensing, entitlements | [APPLIANCE_ACTIVATION_AND_LICENSING.md](APPLIANCE_ACTIVATION_AND_LICENSING.md) · [LICENSING_AND_ENTITLEMENTS.md](LICENSING_AND_ENTITLEMENTS.md) |
| Backup, restore, disaster recovery | [BACKUP_AND_RESTORE.md](BACKUP_AND_RESTORE.md) · [BACKUP_RETENTION.md](BACKUP_RETENTION.md) · [DISASTER_RECOVERY_FACTORY_CLEAN_INSTALL.md](DISASTER_RECOVERY_FACTORY_CLEAN_INSTALL.md) |
| Migrations and the Central-outage behaviour | [MIGRATION_RUNBOOK.md](MIGRATION_RUNBOOK.md) · [OFFLINE_OPERATION.md](OFFLINE_OPERATION.md) |
| PMS interface commissioning | [runbooks/PMS-Interface-Commissioning.md](runbooks/PMS-Interface-Commissioning.md) (FIAS reference: [FIAS_2.20.24.pdf](FIAS_2.20.24.pdf)) |
| Portal domain DNS and certificates | [runbooks/ONEGATE_PORTAL_DNS_AND_CERTIFICATES.md](runbooks/ONEGATE_PORTAL_DNS_AND_CERTIFICATES.md) (the `onegate.work` runbook is retired history) |
| Testing both planes | [TESTING_RUNBOOK.md](TESTING_RUNBOOK.md) |
| API deprecations | [API_DEPRECATIONS.md](API_DEPRECATIONS.md) |
| Background (not re-verified against the current release) | [STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md](STAYCONNECT_COMPLETE_OPERATIONS_MANUAL.md) |

## Networking

[CLIENT_NETWORKING.md](CLIENT_NETWORKING.md) (current behaviour) · [networking/EDGE_NETWORKING.md](networking/EDGE_NETWORKING.md)
(the model) · [networking/CLIENT_VLANS_AND_SSIDS.md](networking/CLIENT_VLANS_AND_SSIDS.md) ·
[networking/DHCP.md](networking/DHCP.md) · [networking/NETWORK_APPLY_AND_ROLLBACK.md](networking/NETWORK_APPLY_AND_ROLLBACK.md) ·
[networking/NETWORK_TROUBLESHOOTING.md](networking/NETWORK_TROUBLESHOOTING.md) ·
[networking/ADMIN_CONSOLE_IP_ALIAS.md](networking/ADMIN_CONSOLE_IP_ALIAS.md)

## Consoles (user guides)

[user-guide/](user-guide/README.md): Central configuration and page reference, the Central role guides
([central-roles.md](user-guide/central-roles.md)), the Admin Console configuration manual and page reference, common
tasks, and what Clients see in the Client Portal.

## Architecture and product contracts

| Topic | Document |
|---|---|
| Central (authoritative) | [CENTRAL_CONTROL_PLANE.md](CENTRAL_CONTROL_PLANE.md) · [CLOUD_ARCHITECTURE.md](CLOUD_ARCHITECTURE.md) |
| Appliance | [EDGE_ARCHITECTURE.md](EDGE_ARCHITECTURE.md) · [TARGET_ARCHITECTURE.md](TARGET_ARCHITECTURE.md) |
| Client identity and access | [architecture/ONEGATE_CLIENT_IDENTITY_AND_ACCESS_POLICY.md](architecture/ONEGATE_CLIENT_IDENTITY_AND_ACCESS_POLICY.md) |
| Devices, email accounts, HTTPS portal and social login, modules | [ONEGATE_DEVICE_MANAGEMENT.md](architecture/ONEGATE_DEVICE_MANAGEMENT.md) · [ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE.md](architecture/ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE.md) · [ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md](architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md) · [ONEGATE_MODULES_AND_ACQUISITION.md](architecture/ONEGATE_MODULES_AND_ACQUISITION.md) |
| Roles, data ownership, settings | [ROLE_AND_SCOPE_MATRIX.md](ROLE_AND_SCOPE_MATRIX.md) · [DATA_OWNERSHIP.md](DATA_OWNERSHIP.md) · [OPERATIONAL_SETTINGS_POLICY.md](OPERATIONAL_SETTINGS_POLICY.md) |
| Admin UI design system | [ui/DESIGN_SYSTEM.md](ui/DESIGN_SYSTEM.md) |
| Component inventory (machine-readable) | [inventory/onegate-components.json](inventory/onegate-components.json) |
| IAM contract and phase plans (Phases 0–7, closed) | [architecture/StayConnect-IAM-Phase0-Contract.md](architecture/StayConnect-IAM-Phase0-Contract.md) and the `architecture/StayConnect-IAM-Phase*-Plan.md` files, the privilege matrices and [adr/](architecture/adr/) |
| Earlier whole-system snapshot (2026-07-10, kept as cited evidence) | [SYSTEM_OVERVIEW.md](SYSTEM_OVERVIEW.md) |

## Security, PKI and privacy

[CA_CEREMONY_RUNBOOK.md](CA_CEREMONY_RUNBOOK.md) (current trust universe and the legacy Root CA custody record) ·
[ASSIGNMENT_KEY_CUSTODY_RUNBOOK.md](ASSIGNMENT_KEY_CUSTODY_RUNBOOK.md) · [VENDOR_BREAKGLASS_RUNBOOK.md](VENDOR_BREAKGLASS_RUNBOOK.md) ·
[HOTEL_ADMIN_CERT_LIFECYCLE.md](HOTEL_ADMIN_CERT_LIFECYCLE.md) · [privacy/](privacy/README.md) (GDPR programme, DPA, breach and
erasure runbooks).

## Governance and delivery

- [PO_LED_DELIVERY_MODEL.md](PO_LED_DELIVERY_MODEL.md) — how work is delivered (D42; D67; D79 standing deployment to the
  test appliance); merge only with the Product Owner's `po-merge-approved` label on the exact head.
- [FAST_DELIVERY_AND_PARALLEL_AGENT_PROTOCOL.md](FAST_DELIVERY_AND_PARALLEL_AGENT_PROTOCOL.md) ·
  [GITHUB_EXECUTION_AND_DELIVERY_RULE.md](GITHUB_EXECUTION_AND_DELIVERY_RULE.md) · [ZERO_STALE_LEFTOVERS_RULE.md](ZERO_STALE_LEFTOVERS_RULE.md).
- Machine-readable state: `governance/project-state.json` (`python tools/project-state.py validate`), decisions in
  `governance/decision-register.json`, one receipt per transition in `governance/transitions/`.

## Records (kept as evidence; not current guidance)

Phase reports ([reports/](reports/)), acceptance records ([acceptance/](acceptance/)), evidence ([evidence/](evidence/)),
the delivery manifests still used or cited ([manifests/](manifests/)), the Protel spike ([spikes/](spikes/)), the
handoff ([context/](context/)), [templates/](templates/), [design/](design/), the security delivery evidence
([TERMINAL_DELIVERY_SECURITY_EVIDENCE.md](TERMINAL_DELIVERY_SECURITY_EVIDENCE.md)), the Phase-3/4 runbook and triage
([PHASE3_DEPLOYMENT_AND_ROLLBACK_RUNBOOK.md](PHASE3_DEPLOYMENT_AND_ROLLBACK_RUNBOOK.md),
[PHASE3_SCOPE_AMENDMENT_PROPOSAL.md](PHASE3_SCOPE_AMENDMENT_PROPOSAL.md), [PHASE4_DEPENDENCY_TRIAGE.md](PHASE4_DEPENDENCY_TRIAGE.md)),
and the superseded nightly model ([NIGHTLY_AUTHORITATIVE_DELIVERY.md](NIGHTLY_AUTHORITATIVE_DELIVERY.md)).
