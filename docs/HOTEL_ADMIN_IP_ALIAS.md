# Hotel Admin — Management IP Alias

The Hotel Admin is reachable at its canonical name **and** at the appliance
management IP, both fronted by Caddy, from one app instance:

| URL | |
|-----|--|
| `https://hotel.stayconnect.local` | canonical |
| `https://172.21.60.23` | management-IP alias |

Both terminate on the same Caddy vhost → the same Next.js instance on `127.0.0.1:3100`
→ edged on `127.0.0.1:8090`. There is **no** second application or database, and
port 3100/8090 are loopback-only (never exposed).

## TLS

One leaf certificate carries **both** SANs and is signed by Caddy's own local CA
(the chain a management workstation already trusts — the existing chain is
preserved, nothing new to distribute):

```
Issuer : Caddy Local Authority - ECC Intermediate
SAN    : DNS:hotel.stayconnect.local, IP Address:172.21.60.23
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

Reachability is controlled by nftables (`deploy/nftables/stayconnect.nft`), not by a
Caddy bind:

- `:443` is accepted **only** on the management interface `ens160` to the management
  IP (`iifname "ens160" ip daddr 172.21.60.23 tcp dport 443 accept`).
- Guest `:443` arriving on `br-lan` is DNAT'd to the **captive portal**
  (`10.10.0.1:8343`) — it never reaches Caddy.
- Guest→management ranges (`172.16/12`, `192.168/16`) are dropped.

So the Hotel Admin — DNS name or IP — is **not** exposed through br-lan, the guest
network, or as a public WAN service. Guest services (portal, DNS, DHCP) are
unaffected.

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
guest services up throughout.
