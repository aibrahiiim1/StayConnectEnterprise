# Admin Console UX — before/after evidence (2026-11-03)

These screenshots back up PR #296 (Client access pages), PR #297 (Hotel section) and the closure PR. They were taken on the test appliance `172.21.96.97`.

**Privacy:** the images contain no guest data, no host addresses and no operator account names.
- Table rows are blurred, and so is the sidebar on the *before* images.
- IP and MAC addresses, e-mail addresses, appliance gateway host names and room or reservation numbers were masked before capture (*after*) or blurred afterwards (*before*).

## Release under test

- **Before:** releases up to `20261103-053741`; the Hotel pages were captured before PR #297.
- **After:** release `20261103-082555`, built from source commit `98196984`.

## Desktop (1440 px)

| # | Page | Before | After |
|---|---|---|---|
| 01 | Sign-in methods | [before](before/01-sign-in-methods.jpg) | [Methods](after/01-sign-in-methods.jpg) · [Protection](after/02-sign-in-methods-protection.jpg) |
| 03 | Client groups: add | [list](before/03-client-groups.jpg) · [dialog](before/03-client-groups-add.jpg) | [dialog](after/03-client-groups-add.jpg) |
| 04 | Identity providers: add | [list](before/04-identity-providers.jpg) · [dialog](before/04-identity-providers-add.jpg) | [dialog](after/04-identity-providers-add.jpg) |
| 05 | Delivery: add sender | [list](before/05-delivery.jpg) · [dialog](before/05-delivery-add.jpg) | [dialog](after/05-delivery-add.jpg) |
| 06 | After Check-out Access | [before](before/06-after-checkout-access.jpg) | [after](after/06-after-checkout-access.jpg) |
| 07 | PMS connection | [before](before/07-pms-connection.jpg) | [after](after/07-pms-connection.jpg) |
| 08 | PMS connection: Manage | [before](before/08-pms-connection-manage.jpg) | [after](after/08-pms-connection-manage.jpg) |
| 09 | Room sign-in | [before](before/09-room-sign-in.jpg) | [after](after/09-room-sign-in.jpg) |
| 10 | Stays | [before](before/10-stays.jpg) | [after](after/10-stays.jpg) |
| 11 | PMS activity | [before](before/11-pms-activity.jpg) | [after](after/11-pms-activity.jpg) |
| 12 | Guest sign-in checks | [before](before/12-guest-sign-in-checks.jpg) | [after](after/12-guest-sign-in-checks.jpg) |
| 13 | Guest sign-in attempts | [before](before/13-guest-sign-in-attempts.jpg) | [after](after/13-guest-sign-in-attempts.jpg) |
| 14 | Room charge | [before](before/14-room-charge.jpg) | [after](after/14-room-charge.jpg) |
| 15 | Charge health | [before](before/15-charge-health.jpg) | [after](after/15-charge-health.jpg) |
| 16 | Manual review | [before](before/16-manual-review.jpg) | [after](after/16-manual-review.jpg) |
| 17 | Package payments | [before](before/17-package-payments.jpg) | [after](after/17-package-payments.jpg) |
| 18 | Recovery | [before](before/18-recovery.jpg) | [after](after/18-recovery.jpg) |
| 19 | PMS routing | [before](before/19-pms-routing.jpg) | [after](after/19-pms-routing.jpg) |
| 20 | Duplicate sources | [before](before/20-duplicate-sources.jpg) | [after](after/20-duplicate-sources.jpg) |
| 21 | Cross-PMS transfer | [before](before/21-cross-pms-transfer.jpg) | [after](after/21-cross-pms-transfer.jpg) |

## Phone (390 px)

[Sign-in methods](after/m01-sign-in-methods.jpg) · [PMS connection](after/m02-pms-connection.jpg) · [Room charge](after/m03-room-charge.jpg) · [Charge health](after/m04-charge-health.jpg) · [Delivery: add sender](after/m05-delivery-add.jpg)

At 1440 px and 390 px, no page scrolls sideways.
