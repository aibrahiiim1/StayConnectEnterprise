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

## Factory-clean the appliance identity/activation state

Wipes identity + credentials; **preserves** WAN/LAN + guest config, trust anchors,
Central URL and the Hotel Admin operator:
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
licence and credentials), then **Delete record** (allowed only for a waiting or retired
appliance; audit history is kept). Only fall back to
direct DB deletion under an incident when the API is unavailable.

## Certificate lifecycle (support)

The Hotel Admin TLS cert self-manages (`docs/HOTEL_ADMIN_CERT_LIFECYCLE.md`). Manual
mint/renew helper (support only): `/etc/caddy/hotel-admin/mint-cert.sh`, then
`systemctl reload stayconnect-caddy`.
