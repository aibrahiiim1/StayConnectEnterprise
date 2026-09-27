# OneGate product terminology

**Status: current.** Product-Owner decisions, 2026-09-27:

1. OneGate's product-facing language is industry-neutral. The platform serves hotels, and also offices,
   clinics, campuses, venues and any other site that offers managed Wi-Fi, so the product no longer
   presents itself as hotel-only.
2. **The core stays neutral; hospitality is a module.** Everything whose meaning depends on hospitality or
   a PMS is grouped in one **Hotel** section of the Admin Console. Inside it, the PMS's own vocabulary —
   *guest*, *stay*, *room*, *check-in/checkout* — is correct and is kept.

This file is the single reference for which word to use where. It governs **product-facing** text only:
screen titles, navigation, labels, messages, validation text, help and tips, portal strings and their
translations, and the current operator documentation. It does **not** rename code identifiers, API paths,
database objects, service names, directories, permission keys, role keys or translation keys.

## The core product terms

| Use | Instead of | Meaning |
|---|---|---|
| **Client** / **clients** | Guest / guests | The person who connects to the site's Wi-Fi through the portal. |
| **Admin Console** | Hotel Admin, Hotel Admin Console | The on-appliance administration application (served from the appliance's management address). |
| **Client Portal** | Guest Portal, guest portal | The captive portal an end user signs in on. |
| **Site** | hotel, property (meaning the location) | The location this appliance serves — *this site's data*, *the appliance at the site*. A Central **Site** is still one physical location ([`ownership hierarchy`](CENTRAL_CONTROL_PLANE.md)). |
| **the site team** | reception, front desk (in generic help) | Whoever helps end users at the site. Portal help reads *"Please contact the site team for assistance."* |

Derived labels follow the same rule: *Client accounts*, *Client devices*, *Clients online*, *Client Login*
(the portal's sign-in tab), *Client sign-in protection*.

## The Hotel module

The Admin Console's **Hotel** section holds every screen whose meaning depends on hospitality or a PMS. It
appears only when the appliance serves those screens (the navigation is capability-driven, unchanged).

| Screen | Why it is Hotel |
|---|---|
| PMS connection, PMS activity, PMS routing, Duplicate sources, Cross-PMS transfer | The PMS integration itself. |
| Stays | Reservations and in-house guests as the PMS reports them. |
| Guest sign-in checks, Guest sign-in attempts | Room sign-in, verified against the PMS guest list — the subject is the PMS guest record. |
| **Grace Period** | Internet access a guest keeps after PMS checkout (formerly *Checkout grace*). |
| Post-stay access | Access after departure, keyed to the PMS stay. |
| Charge health, Manual review, Settlements, Recovery | Posting internet charges to the guest's room bill in the PMS. |

**Inside Hotel, "guest" is right** when the subject is the PMS guest or reservation record: the guest list,
guests in house, the guest name on the reservation, G#, a departing guest, *Guest checked out*. Outside Hotel
the end user is always a **client**.

**Grace Period** is the post-checkout access grace. It is not the licence's renewal *grace period* on the
Appliance & licence screen, which keeps its own wording.

### Role labels

Labels only; role keys, permissions and behaviour are unchanged.

| Role key | Label |
|---|---|
| `hotel_it_manager` | Site IT manager |
| `front_office_operator` | Client services operator |
| `guest_relations_operator` | Client relations operator |

### Translations of the portal terms

| Key (unchanged) | en | ar | de | fr | it | ru |
|---|---|---|---|---|---|---|
| `tab.guest` | Client Login | دخول العملاء | Kunden-Anmeldung | Connexion client | Accesso clienti | Вход для клиентов |
| "the site team" (in help and notices) | the site team | فريق الموقع | das Team vor Ort | l'équipe du site | il personale della struttura | персонал площадки |

Keys are identifiers: a site's saved translation overrides are keyed by them, so a key is never renamed to
follow a wording change. A site that has already overridden a string keeps its own text.

## Where "guest" and "hotel" deliberately remain

These are not product wording. They carry a protocol, integration, network or domain meaning, and changing
them would change what the text says.

| Retained | Why |
|---|---|
| **Guest network(s)**, guest VLAN, guest bridge, "not on the guest network", **Guest Wi-Fi** | Standard network-engineering terms for the isolated end-user network, used the same way in every industry. |
| **PMS guest** semantics inside the Hotel module: guest name, G#, in-house guest, guest record, guest list/roster as the PMS holds it | The Property Management System's own vocabulary (FIAS and the other PMS integrations). The text describes a PMS record, not the Wi-Fi user. |
| **Stay**, **Room**, **RN**, **reservation**, **check-in/checkout**, **post-stay** | PMS stay semantics, unchanged by decision. |
| **Property management system**, **Hotel ID** and other PMS/FIAS field names | The industry name of the system class and integration fields defined by the PMS. |
| Code, API, database and deployment identifiers: `hotel-admin/`, `guest_accounts`, `/guest-accounts`, `/checkout-grace`, `guest-signin-attempts`, role keys, `check-hotel-admin-integrity.sh`, `stayconnect-*` units, `hotel.stayconnect.local`, the `@layer hotel` CSS layer, translation keys such as `tab.guest` | Renaming them is an API/deployment change with no product-facing benefit; routes keep working and existing bookmarks and integrations are unaffected. |
| Historical records: `governance/` history, `docs/manifests/`, `docs/reports/`, `docs/evidence/`, `docs/acceptance/`, phase plans and closed reports | A record of what was true then is not rewritten. |

## Open decisions (not part of this change)

* **Licence capacity** counts concurrent end-user devices ("clients online at once"). Whether licensing
  should count something else is a licensing decision.
* **A configurable help contact** for the portal (a named desk, phone or email instead of "the site team")
  would be a product feature, not a wording fix.
