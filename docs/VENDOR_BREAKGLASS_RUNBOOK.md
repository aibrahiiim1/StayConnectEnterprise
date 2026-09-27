# Vendor Break-Glass / Support Runbook

Support-only procedures that bypass the normal UI onboarding. **Not part of hotel
onboarding** and **not available to Hotel-IT operators.**

Every action here:

- requires **root** on the appliance (or Central),
- requires an **incident / support-ticket reference**,
- requires a stated **reason**,
- is an **audited support action**,
- must never be used in a normal customer onboarding (which is UI-only — see
  `docs/APPLIANCE_ONBOARDING_MANUAL.md`).

Record `incident=<ref> reason=<text> operator=<you>` in the ticket before running any
command below.

## Central status via the unix socket (support fallback)

Only when Hotel Admin is unavailable. Enrollment tokens no longer exist: the appliance
registers itself. Runs as root on the appliance:
```
curl --unix-socket /run/stayconnect/scd.sock http://localhost/v1/central/status        # activation, licence, Central link
curl --unix-socket /run/stayconnect/scd.sock -X POST http://localhost/v1/central/refresh  # same as "Check now"
```
The socket path only skips the browser; activation itself still happens in Central.

## Provision / reset a Hotel Admin operator

```
/opt/stayconnect/bin/edged seed-admin --email <user> --password <pass> [--allow-weak]
```
`--allow-weak` permits a <10-char password (management-network-only boxes). This is a
deliberate per-appliance provisioning action, never a shipped default.

## Reset the appliance identity/activation state (same customer only)

Wipes identity + credentials; **preserves** WAN/LAN + guest config, trust anchors,
Central URL and the Hotel Admin operator — and therefore the current customer's
local data. It is **not** a factory reset:

- **The box stays pinned to its customer.** After the reset scd generates a new
  identity key and registers again as *Waiting for activation*, but its signed
  registration carries `holds_customer_id` — the customer whose data is still in
  its site database. Central shows that customer on the waiting appliance and
  **Activate accepts only that customer**; any other is refused
  `409 holds_other_customer_data`. Re-activating it for the same customer is the
  intended use.
- **Changing customer requires a factory reset.** Retire → a factory-clean install
  from a blank disk
  ([DISASTER_RECOVERY_FACTORY_CLEAN_INSTALL.md §7](DISASTER_RECOVERY_FACTORY_CLEAN_INSTALL.md#7-when-an-existing-appliance-must-be-factory-reset))
  → registration (it then reports no customer) → Activate. Never wipe the site
  database by hand to get round the refusal: the factory-clean install is the
  only supported way to leave a customer.
- If the site database holds **more than one** customer (an interrupted
  cross-customer transition) or cannot be read, scd does not register at all and
  Hotel Admin shows the reason as the last Central error; only a factory-clean
  install resolves the first case.
- **It does not clear the *Removed from OneGate Central* state**, and must not be
  made to: leave `/etc/stayconnect/removed-from-central.json` in place. While it
  exists scd never registers; the only way back is the blank-disk install.
```
systemctl stop stayconnect-scd stayconnect-edged
shred -u /etc/stayconnect/identity/ed25519.key
rm -f  /etc/stayconnect/identity/identity.json
shred -u /etc/stayconnect/certs/mtls-client.key
rm -f  /etc/stayconnect/certs/client.crt
rm -f  /etc/stayconnect/assignment/assignment.json /etc/stayconnect/assignment/registry.json
shred -u /etc/stayconnect/license/current.json 2>/dev/null; rm -f /etc/stayconnect/license/*.json
# PRESERVE: assignment-registry-root.pub, certs/ca.crt, netplan, scd.env (Central URL)
systemctl start stayconnect-scd stayconnect-edged
```

## Remove an appliance from the Control Panel

Normal path: **Central → Appliances → the appliance → Retire appliance** (revokes its
licence at once, and its credentials once the appliance acknowledges — or at once for
an emergency retire), then **Delete record** (allowed only for a waiting or retired
appliance; audit history is kept; the retired identity key stays refused). A running
appliance that had held a customer and whose record is deleted enters the persistent
*Removed from OneGate Central* state and needs a blank-disk install before any new
activation. Only fall back to direct DB deletion under an incident when the API is
unavailable — and never delete a `retired_appliance_identities` row to let a box back in.

## Certificate lifecycle (support)

The Hotel Admin TLS cert self-manages (`docs/HOTEL_ADMIN_CERT_LIFECYCLE.md`). Manual
mint/renew helper (support only): `/etc/caddy/hotel-admin/mint-cert.sh`, then
`systemctl reload stayconnect-caddy`.
