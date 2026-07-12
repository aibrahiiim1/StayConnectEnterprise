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

## Enrollment via the unix socket (installer/support fallback)

Only when the browser wizard is unavailable. Runs as root on the appliance:
```
curl --unix-socket /run/stayconnect/scd.sock -X POST \
  -H 'Content-Type: application/json' \
  -d '{"token":"<enrollment-token>","serial":"<serial>"}' \
  http://localhost/v1/setup/enroll
```
The token is still single-use, expiring, site-scoped and serial-locked at Central —
the socket path only skips the browser, not the security checks.

## Provision / reset a Hotel Admin operator

```
/opt/stayconnect/bin/edged seed-admin --email <user> --password <pass> [--allow-weak]
```
`--allow-weak` permits a <10-char password (management-network-only boxes). This is a
deliberate per-appliance provisioning action, never a shipped default.

## Factory-clean the appliance identity/enrollment state

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

Normal path: **Central → Appliances → Delete** (cascades certs, assignment,
lifecycle, telemetry). Revoke its license under **Licenses**. Only fall back to
direct DB deletion under an incident when the API is unavailable.

## Certificate lifecycle (support)

The Hotel Admin TLS cert self-manages (`docs/HOTEL_ADMIN_CERT_LIFECYCLE.md`). Manual
mint/renew helper (support only): `/etc/caddy/hotel-admin/mint-cert.sh`, then
`systemctl reload stayconnect-caddy`.
