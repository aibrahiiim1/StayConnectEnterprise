# Archive index — files removed from the current tree

Removed on 2026-10-28 by the integrated cleanup mission (D79, T0265) and the retired-targets cleanup (D78, T0264). **Nothing
is lost:** Git history is never rewritten, so every file below can be read or restored from the commit given:

```bash
git show <commit>:<path>            # read it
git checkout <commit> -- <path>     # restore it
```

Kept on purpose: every decision, transition and the project state; every file a transition cites as evidence
(`evidence_files`); documents cited by a decision or by `project-state.json`; files the governance tooling and CI read;
the phase reports, acceptance records and evidence; the current handbook, installation manual and operational documents.

Moved, not removed: `docs/EDGE_NETWORKING.md`, `docs/NETWORK_APPLY_AND_ROLLBACK.md`, `docs/NETWORK_TROUBLESHOOTING.md` → `docs/networking/`;
`docs/HOTEL_ADMIN_IP_ALIAS.md` → `docs/networking/ADMIN_CONSOLE_IP_ALIAS.md`.

## Backend and performance audits — consolidated into [AUDIT_BACKLOG.md](AUDIT_BACKLOG.md) (every finding, with status)

| Path | Commit |
|---|---|
| `docs/SECURITY_HARDENING.md` | `1d69508a8472` |
| `docs/audit/FINAL_REPORT.md` | `1d69508a8472` |
| `docs/audit/PHASE0_BASELINE.md` | `1d69508a8472` |
| `docs/audit/PHASE10_CROSS_CUTTING.md` | `1d69508a8472` |
| `docs/audit/PHASE1_CENTRAL.md` | `1d69508a8472` |
| `docs/audit/PHASE2_APPLIANCE_FOUNDATIONS.md` | `1d69508a8472` |
| `docs/audit/PHASE3_DATABASE_AND_MIGRATIONS.md` | `1d69508a8472` |
| `docs/audit/PHASE4_GUEST_SIGN_IN.md` | `1d69508a8472` |
| `docs/audit/PHASE5_CLIENT_PORTAL.md` | `1d69508a8472` |
| `docs/audit/PHASE6_PAYMENTS_AND_POSTING.md` | `1d69508a8472` |
| `docs/audit/PHASE7_PMS.md` | `1d69508a8472` |
| `docs/audit/PHASE8_NETWORKING.md` | `1d69508a8472` |
| `docs/audit/PHASE9_ADMIN_CONSOLE_API.md` | `1d69508a8472` |
| `docs/audit/README.md` | `1d69508a8472` |
| `docs/performance-audit/FINAL_REPORT.md` | `1d69508a8472` |
| `docs/performance-audit/PHASE0_BASELINE.md` | `1d69508a8472` |
| `docs/performance-audit/PHASE10_RESOURCE_BUDGET.md` | `1d69508a8472` |
| `docs/performance-audit/PHASE11_LOAD_TEST_PLAN.md` | `1d69508a8472` |
| `docs/performance-audit/PHASE1_DATABASE.md` | `1d69508a8472` |
| `docs/performance-audit/PHASE2_GUEST_SIGN_IN.md` | `1d69508a8472` |
| `docs/performance-audit/PHASE3_CLIENT_PORTAL.md` | `1d69508a8472` |
| `docs/performance-audit/PHASE4_ENFORCEMENT.md` | `1d69508a8472` |
| `docs/performance-audit/PHASE5_BACKGROUND_JOBS.md` | `1d69508a8472` |
| `docs/performance-audit/PHASE6_PMS.md` | `1d69508a8472` |
| `docs/performance-audit/PHASE7_ADMIN_API.md` | `1d69508a8472` |
| `docs/performance-audit/PHASE8_FRONT_ENDS.md` | `1d69508a8472` |
| `docs/performance-audit/PHASE9_CENTRAL.md` | `1d69508a8472` |
| `docs/performance-audit/README.md` | `1d69508a8472` |

## Historical delivery manifests — generated path lists, reproducible from Git history with `tools/generate-change-manifest.py <base>..<head>`; the manifests still used by tooling, cited by decisions or by `project-state.json` are kept

| Path | Commit |
|---|---|
| `docs/manifests/AdminConsolesRedesign-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/AuditRemediationMerge-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/AuthenticatorBranding-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/ClientAccessIdentity-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/ClientNetworking-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/ClientNetworkingSourceDeviceClosure-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/EmailAccountsSelfService-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/EnrollmentStaleCsr-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/FinancialModulesConfigurable-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/GdprBackupConsolidationMerge-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/GuestAccess-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/HttpsPortalProductionSwitch-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/HttpsPortalProductionSwitchMerge-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/HttpsPortalSocialLogin-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/HttpsPortalSocialLoginClosure-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/InstallerAcceptance-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/MasterGatesRepair-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/ModulesLicence-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/MultiIndustryTerminology-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/MultiPmsNetworkAdmin-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/MultiPmsNetworkAdmin-closure-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/NeutralTerminology-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/OneGate-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/PR202AcceptanceCorrection-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/Phase6-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/PmsResilienceAndBranding-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/PoLedDelivery-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/PostClosure-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/PreliveCentralAndPolicy-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/RepositoryCleanup-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/RestoreRetentionFailsafe-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/RestoreRetentionFailsafeMerge-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/RetiredAddressRedaction-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/SameVendorMultiPMS-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/SameVendorMultiPMS-closure-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/SiteModulesAcquisition-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/SiteModulesAcquisitionClosure-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/StaysPagingGuestAttributes-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/StaysPagingGuestAttributesClosure-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/VelonetClosure-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/VelonetPreLive-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/VelonetRedesign-change-manifest.md` | `1d69508a8472` |
| `docs/manifests/central-licensing-removal-and-pms-departures.md` | `1d69508a8472` |

## Merged into consolidated documents (content carried over, unchanged in substance)

| Path | Commit | Now |
|---|---|---|
| `docs/ARUBA_SSID_VLAN_MAPPING.md` | `1d69508a8472` | [networking/CLIENT_VLANS_AND_SSIDS.md](networking/CLIENT_VLANS_AND_SSIDS.md) |
| `docs/DHCP_MANAGEMENT.md` | `1d69508a8472` | [networking/DHCP.md](networking/DHCP.md) |
| `docs/DHCP_OPTION_114.md` | `1d69508a8472` | [networking/DHCP.md](networking/DHCP.md) |
| `docs/EXTERNAL_DHCP_MODE.md` | `1d69508a8472` | [networking/DHCP.md](networking/DHCP.md) |
| `docs/GUEST_VLAN_CONFIGURATION.md` | `1d69508a8472` | [networking/CLIENT_VLANS_AND_SSIDS.md](networking/CLIENT_VLANS_AND_SSIDS.md) |
| `docs/user-guide/platform-admin.md` | `1d69508a8472` | [user-guide/central-roles.md](user-guide/central-roles.md) |
| `docs/user-guide/tenant-admin.md` | `1d69508a8472` | [user-guide/central-roles.md](user-guide/central-roles.md) |
| `docs/user-guide/tenant-operator.md` | `1d69508a8472` | [user-guide/central-roles.md](user-guide/central-roles.md) |
| `docs/user-guide/viewer-and-billing.md` | `1d69508a8472` | [user-guide/central-roles.md](user-guide/central-roles.md) |

## Superseded working documents and unexecuted plans (pre-split baselines, migration logs, records of retired hosts)

| Path | Commit |
|---|---|
| `docs/COMMERCIAL_ONBOARDING_EXECUTION_STATUS.md` | `1d69508a8472` |
| `docs/CONTROL_PLANE_MIGRATION.md` | `1d69508a8472` |
| `docs/CURRENT_STATE_ASSESSMENT.md` | `1d69508a8472` |
| `docs/DEPLOYED_STATE.md` | `1d69508a8472` |
| `docs/PHASE19_ASSESSMENT.md` | `1d69508a8472` |
| `docs/PHASE19_FINAL_REPORT.md` | `1d69508a8472` |
| `docs/SYNC_PROTOCOL.md` | `1d69508a8472` |
| `docs/acceptance/ClientAccessIdentity-Real-Phone-Acceptance-Checklist.md` | `1d69508a8472` |
| `docs/architecture/Phase3-Controlled-Writer-Privilege-Manifest.md` | `1d69508a8472` |
| `docs/operations/STANDING_PMS_EXCEPTION_GO_UNKNOWN_STAY.md` | `1d69508a8472` |
| `docs/runbooks/Guest-Access-End-To-End-Acceptance.md` | `3679e67455c5` |

## Retired Phase-1B Planning Pack — a HISTORICAL subset of the Project Pack, no longer built

| Path | Commit |
|---|---|
| `exports/chatgpt/StayConnectEnterprise-Phase1B-Planning-Pack.zip` | `1d69508a8472` |
| `exports/chatgpt/phase-evidence/GIT_STAT_36fc3d1d.txt` | `3679e67455c5` |
| `exports/chatgpt/phase1b-planning/IMPLEMENTATION_BLUEPRINT.md` | `1d69508a8472` |
| `exports/chatgpt/phase1b-planning/MANIFEST.md` | `1d69508a8472` |
| `exports/chatgpt/phase1b-planning/PACK_SHA256SUMS.txt` | `1d69508a8472` |
| `exports/chatgpt/phase1b-planning/Phase1B-Privilege-Matrix.md` | `1d69508a8472` |
| `exports/chatgpt/phase1b-planning/README.md` | `1d69508a8472` |
| `exports/chatgpt/phase1b-planning/REPOSITORY_ARTIFACT_SHA256SUMS.txt` | `1d69508a8472` |
| `exports/chatgpt/phase1b-planning/StayConnect-IAM-Phase1B-Plan.md` | `1d69508a8472` |
| `exports/chatgpt/phase1b-planning/inventory/AUTH_ENTRY_POINTS.md` | `1d69508a8472` |
| `exports/chatgpt/phase1b-planning/inventory/DB_ACCESS_MAP.md` | `1d69508a8472` |
| `exports/chatgpt/phase1b-planning/inventory/IAM_V2_OBJECTS.md` | `1d69508a8472` |

## Obsolete test scripts — written for the pre-split monolith; they call removed endpoints or depend on the never-run dev bootstrap

| Path | Commit |
|---|---|
| `scripts/phase1-bootstrap.sh` | `1d69508a8472` |
| `scripts/phase1-test-client.sh` | `1d69508a8472` |
| `scripts/phase11-apaleo-test.sh` | `1d69508a8472` |
| `scripts/phase2-quota-test.sh` | `1d69508a8472` |
| `scripts/phase3-bootstrap.sh` | `1d69508a8472` |
| `scripts/phase4-pms-5b-test.sh` | `1d69508a8472` |
| `scripts/phase4-pms-test.sh` | `1d69508a8472` |
| `scripts/phase5-heartbeat-test.sh` | `1d69508a8472` |
| `scripts/phase5-overrides-test.sh` | `1d69508a8472` |
| `scripts/phase5-polish-test.sh` | `1d69508a8472` |
