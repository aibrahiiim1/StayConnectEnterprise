# Roles & Scope Matrix

> Who can do what, at which level of the Platform → Tenant/Group → Site →
> Appliance hierarchy. Cloud roles live in the cloud DB and authenticate
> against ctrlapi; site roles live in each site's local DB and authenticate
> against that site's edged. **These are separate account systems** — a cloud
> credential opens nothing on an appliance, and vice versa.

## 1. Platform level (StayConnect staff, cloud)

| Role | Status | Scope |
|---|---|---|
| `platform_admin` | **Implemented** | Global super-operator: all tenants (scoped per request via `?tenant_id=`), issues/revokes licenses, manages CommercialPlans, sees the whole fleet |
| `platform_operations` | Roadmap — not yet implemented | fleet health + update orchestration, no commercial writes |
| `platform_support` | Roadmap — not yet implemented | read fleet/telemetry, open support sessions |
| `platform_sales` | Roadmap — not yet implemented | tenants/subscriptions CRUD, no fleet |
| `platform_billing` | Roadmap — not yet implemented | invoices/subscriptions when billing automation lands |
| `platform_auditor` | Roadmap — not yet implemented | read-only everything incl. platform audit |

Until the sub-roles land, every platform action requires `platform_admin`.

## 2. Tenant / hotel-group level (cloud)

| Role | Scope (within own tenant only) |
|---|---|
| `tenant_admin` | full group control: sites, appliances, bootstrap tokens, group operators, subscription changes, license read |
| `tenant_operator` | day-to-day fleet ops: sites/appliances read-write, no staff or plan changes |
| `viewer` | read-only across the group's cloud resources |
| `billing` | read + change subscription plan only |

Group roles see **fleet telemetry and license status** for their sites — never
guest data (it isn't in the cloud; see [DATA_OWNERSHIP.md](DATA_OWNERSHIP.md)).

## 3. Site level (edge, per-hotel `operators`/`operator_roles`)

Seven roles, enforced by edged per `/edge/v1` resource. Legend:
**W** = read-write, **R** = read-only, **–** = no access.

| /edge/v1 resource | site_admin | hotel_it_manager | front_office_operator | guest_relations_operator | voucher_operator | payments_operator | site_viewer |
|---|---|---|---|---|---|---|---|
| operators (staff) | W | **–** | – | – | – | – | – |
| license (view/upload) | W | W | R | R | – | R | R |
| guest-access-plans | W | W | R | R | – | R | R |
| voucher-batches / vouchers | W | W | **W** | **W** | **W** | R | R |
| sessions (incl. disconnect) | W | W | **W** | **W** | – | R | R |
| guests | W | W | **W** | **W** | – | R | R |
| pms-providers (+test/cache/health) | W | W | R | R | – | R | R |
| auth-methods | W | W | R | R | – | R | R |
| walled-garden | W | W | R | R | – | R | R |
| portal-branding | W | W | R | R | – | R | R |
| payments (view) | W | W | R | R | – | **W** | R |
| payments **refunds** | W | **–** | – | – | – | **W** | – |
| stripe-accounts | W | W | R | R | – | R | R |
| notification-providers | W | W | R | R | – | R | R |
| social-providers | W | W | R | R | – | R | R |
| audit | W(R) | R | R | R | – | R | R |
| reports | R | R | R | R | – | R | R |
| backups (view/trigger) | W | W | R | R | – | R | R |

Summary of intent:

- **site_admin** — everything, including staff accounts and refunds.
- **hotel_it_manager** — everything technical, but **not** staff management and
  **not** payment refunds (money and people stay with site_admin/payments).
- **front_office_operator / guest_relations_operator** — the reception desk:
  read-write on vouchers, sessions and guests; read-only elsewhere. (Same
  permissions today; kept as two roles for audit attribution and future
  divergence.)
- **voucher_operator** — vouchers only (kiosk/print station accounts). No other
  read access.
- **payments_operator** — payments read-write (incl. refunds), read-only on
  everything else.
- **site_viewer** — read-only everywhere.

License-state gates apply **on top of** roles: e.g. in Restricted state even
site_admin cannot create GuestAccessPlans or voucher batches
([LICENSING_AND_ENTITLEMENTS.md](LICENSING_AND_ENTITLEMENTS.md) §5).

## 4. Isolation guarantees

1. **Credential isolation** — site operators exist only in that site's DB;
   there is no cloud record of them and no cross-site login. A leaked hotel
   password compromises one hotel.
2. **Data isolation** — a site role can only ever see its own site's data,
   because the API it talks to is physically connected to only one database.
   No `tenant_id` filter bugs can leak across sites — there is no other
   site's data in the process.
3. **Cloud/edge separation** — platform and group roles cannot read guest
   data (not present in the cloud); site roles cannot touch commercial data
   (subscriptions/licenses are cloud-writable only; the edge holds a signed,
   read-only entitlement).
4. **Appliance identity** — appliances authenticate to the cloud with their
   own Ed25519 keys and can only speak for themselves (subject-identity check
   in telemetry ingest, JWT identity on license fetch).
5. **Audit locality** — hotel-staff actions land in the site's local
   `audit_log`; platform/group actions in the cloud `audit_log`. Neither log
   syncs to the other side.

## 5. Legacy-role mapping (compatibility window)

The edge `operator_roles` check constraint temporarily also admits the legacy
tenant-wide roles (`tenant_admin`, `tenant_operator`, `viewer`, `billing`) so
`sitemigrate` can copy existing operator rows verbatim. Post-cutover mapping
applied at the site:

| Legacy row (central) | Site role after cutover |
|---|---|
| tenant_admin | site_admin |
| tenant_operator | front_office_operator |
| viewer | site_viewer |
| billing | payments_operator |

Group-level duties of former tenant_admins move to cloud accounts under
`/cloud/v1/operators`. The legacy values are removed from the edge check
constraint when the compatibility window closes
([API_DEPRECATIONS.md](API_DEPRECATIONS.md)).

## 6. Phase 19 — Networking permissions

Guest-network management ([EDGE_NETWORKING.md](EDGE_NETWORKING.md)) adds a
`network.*` permission family, gated by edged on the `/edge/v1/network/*`,
`/edge/v1/guest-networks/*` and `/edge/v1/dhcp/*` routes (all writes proxy to
`netd`). Because a bad apply can affect connectivity, only the two technical
roles get write/apply; everyone else is read-only or none.

| Permission | Guards | site_admin | hotel_it_manager | site_viewer | front_office / guest_relations / voucher / payments |
|---|---|---|---|---|---|
| `network.interfaces.read` | list/read interfaces | R | R | R | – |
| `network.interfaces.assign` | assign guest_access/guest_trunk/unused role | W | W | – | – |
| `network.guest.read` | read guest networks + revisions | R | R | R | – |
| `network.guest.write` | create/edit/delete guest networks, pools, reservations (draft) | W | W | – | – |
| `network.guest.apply` | validate + apply + confirm a revision | W | W | – | – |
| `network.guest.rollback` | roll back to a prior revision | W | W | – | – |
| `network.dhcp.read` | read pools/reservations/leases | R | R | R | – |
| `network.dhcp.write` | edit pools/reservations | W | W | – | – |

Summary: **site_admin** and **hotel_it_manager** have full networking control
(read/write/apply/rollback); **site_viewer** is read-only across networking; the
reception/kiosk/payments roles (`front_office_operator`, `guest_relations_operator`,
`voucher_operator`, `payments_operator`) have **no** networking access.
Management/WAN interfaces are `is_protected` and refuse role edits regardless of
permission. As elsewhere, license-state gates apply on top of roles.
