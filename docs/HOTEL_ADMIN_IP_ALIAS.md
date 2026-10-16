# Admin Console — Management IP Alias

The Admin Console is reachable at its canonical name **and** at the appliance
management IP, both fronted by Caddy, from one app instance:

| URL | |
|-----|--|
| `https://hotel.stayconnect.local` | canonical |
| `the retired development reference appliance's address` | management-IP alias |

Both terminate on the same Caddy vhost → the same Next.js instance on `127.0.0.1:3100`
→ edged on `127.0.0.1:8090`. There is **no** second application or database, and
port 3100/8090 are loopback-only (never exposed).

## TLS

One leaf certificate carries **both** SANs and is signed by Caddy's own local CA
(the chain a management workstation already trusts — the existing chain is
preserved, nothing new to distribute):

```
Issuer : Caddy Local Authority - ECC Intermediate
SAN    : DNS:hotel.stayconnect.local, IP Address:the retired development reference appliance
Validity: 2 years
```

Caddy serves it explicitly (`tls <fullchain> <key>` in the vhost) instead of
letting `local_certs` auto-issue two separate single-SAN certs. Files live in
`/etc/caddy/hotel-admin/` (key `0600`, owned by `caddy`).

**Renewal is now fully automated** — a systemd-timed manager renews at 45 days, on
management-IP change, or on SAN drift, with staged validation, atomic swap and
rollback. See **`docs/HOTEL_ADMIN_CERT_LIFECYCLE.md`**. The vhost (site address +
`tls` + proxy) lives in `/etc/caddy/hotel-admin/vhost.caddy`, imported by the main
Caddyfile and rewritten by the manager. `deploy/scripts/hotel-admin-mint-cert.sh`
remains only as an internal helper; production operation does not depend on anyone
running it.

**No TLS warnings:** a workstation that trusts the Caddy local root
(`/var/lib/caddy/.local/share/caddy/pki/authorities/local/root.crt`, distributed as
the StayConnect CA) validates **both** URLs cleanly — verified with
`openssl verify` (`-verify_hostname` for the name, `-verify_ip` for the IP) and
`curl --cacert`, both `return code 0 (ok)`.

## Access restriction (management only — enforced at the firewall)

Reachability is controlled by the ruleset netd renders (`internal/netcfg/render_nft.go`),
not by a Caddy bind. The appliance has exactly two NICs and **the WAN is also the
management interface** ([DEPLOYMENT_APPLIANCE.md](DEPLOYMENT_APPLIANCE.md) §1), so:

- `:443` is accepted **only** on the WAN/management interface (`NETD_MGMT_IFACE`, `ens160`)
  **to the WAN's own address**: `iifname "ens160" ip daddr <WAN address> tcp dport 443 accept`.
  The address comes from `NETD_MGMT_ADDR`; provisioning sets it to `auto`, which netd resolves to
  the interface's current primary IPv4 at every render and re-asserts when System network changes
  it (`cmd/netd/nft_watch.go`). A literal address is still accepted, but System network then
  refuses to move the WAN away from it (it would lock the console out).
- An unauthenticated client's `:443` to any other host is dropped (no redirect: the appliance holds
  no certificate for other people's names); only the portal address's `:443` reaches portald.
  Nothing a client network sends reaches Caddy.
- Client→management ranges (`172.16/12`, `192.168/16`) are dropped.

So the Admin Console — DNS name or IP — is reachable on the WAN address (the management path)
and **not** through a client network. Who can reach the WAN address is decided by the hotel's
upstream network. Client services (portal, DNS, DHCP) are unaffected.

## Why the IP alias "just works" at the app layer

- **Cookies:** the `sc_edge_session` cookie sets **no `Domain`** (host-only), so a
  session established via the DNS name or the IP is scoped to that host and neither
  breaks the other. `SameSite=Lax`, `HttpOnly`, `Secure`.
- **Origins / CSRF:** edged does not gate on `Host`/`Origin`; step-up reauth keys off
  the session + a re-entered password. Nothing is host-specific.
- **Redirects:** host-relative (`Location: /login?next=…`); the vhost also strips any
  absolute `localhost:3100` Location Next might emit.
- **allowed hosts:** the Next standalone server enforces no host allowlist.

## Verified from BOTH URLs

TLS trust (no warning), login, whoami, Setup/Enrollment (`/setup/status`), step-up
reauth gate (`reauth_required` on wrong password, nothing applied), API proxy to
edged (`/license`), favicon/`/icon.svg` + `/_next` static, page refresh + relative
redirect, logout (+ post-logout `whoami` 401). Persistence confirmed across **Caddy
reload/restart, edged + hotel-admin restart, and a full appliance reboot** — both
URLs kept serving the same dual-SAN cert (identical serial) and the full flow, with
client services up throughout.
