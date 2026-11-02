# Operator requests of 2026-11-02 — PR #287 (D89, T0280)

Merged as `f08c3413634d` (head `69ff907e`, Codex review clean). Installed on the test appliance `172.21.96.97`
(`onegate-appliance-f08c3413634d`) and on Central `172.21.96.96` (`onegate-central-f08c3413634d`). Go-Live is not
authorised.

## What changed

| Request | Result |
|---|---|
| Portal name on Central | The Client Portal address is shown first; the per-appliance technical name second. |
| VLAN 3 got an address but no sign-in page | Tagged-VLAN bridges take the parent NIC's MAC live, not only at reboot. **Accepted by the Product Owner.** |
| /19 for every client network | Single, trunk and factory networks default to /19; suggestions and validation use CIDR overlap (legacy /24 and /20, native networks, other rows). An existing /20 is widened only through the replace flow. |
| Session time with Never | Session limits accept Never (0, migration 0204). The session store has no fixed lifetime of its own and the cookie carries the browser's 400-day bound, so Never is real. |
| Portal templates | portald serves the admin loopback over HTTP, so previews do not depend on the captive-portal address. **Accepted by the Product Owner.** |
| Overview and page noise | Descriptions are an info icon beside the title; warnings are one slim attention bar; Default Network card is one line. |
| Reason and password on every change | Two step-up levels, below. |

## Step-up levels

* **Confirm-only** (`reauth`): ordinary settings and routine actions. The popup is the consent: a signed-in,
  CSRF-guarded session passes, a password that is supplied is still verified, an empty reason is recorded as
  "Confirmed in the console", a typed reason must still be 4–500 characters.
* **Strict** (`reauthStrict`, `stepUpStrict`, `stepUpActorStrict`): the password is always verified. Own password
  and two-step; backup download, restore, settings, backup now, key holders and the erasure ledger; guest-data
  export and erasure; payment accounts and every financial decision; posting lane; commercial terms; certificates
  and portal HTTPS; PMS configuration, decisions and resync; notification and social provider secrets; code and
  credential reveal; voucher key rotation; operator management; WAN/LAN; enforcement policy; guest-device setting.
  The list is pinned by `data-plane/cmd/edged/stepup_strict_test.go`; the dialogs of these actions keep the
  password field (`strictPassword`).

## Verification

* Tests: data-plane unit suite on Linux (71 packages), control-plane (19 packages), the whole edged integration
  package against a factory-clean full schema with the production role DSNs (759 passed), Admin Console and Central
  console suites.
* Live, test appliance: all eight binaries embed `f08c3413634d`, production tag, unmodified, hashes equal to
  `DEPLOYED_BINARIES.sha256`; ledger head `0204`; Never/Never saved without password or reason and restored to
  30 min / 12 h; a missing or wrong password is refused with 401 for backup-key add and WAN/LAN apply and creates
  nothing; a client-network confirm passes step-up without a password; `br-g3` and `br-g90` carry the parent MAC.
* Live, Central: `DEPLOYED.json` and ctrlapi name `f08c3413634d`; `central-deploy.sh` smoke 9/9 PASS.
* Not verified: a phone joining on this exact release (the PO's acceptance was on `591ad79f5140`, whose network and
  portal code is unchanged in `f08c3413634d`), a real Google round trip.
