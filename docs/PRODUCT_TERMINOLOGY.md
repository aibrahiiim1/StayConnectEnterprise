# OneGate product terminology

**Status: current.** Product-Owner decision, 2026-09-27: OneGate's product-facing language is
industry-neutral. The platform serves hotels, and also offices, clinics, campuses, venues and any other
site that offers managed Wi-Fi, so the product no longer presents itself as hotel-only.

This file is the single reference for which word to use where. It governs **product-facing** text only:
screen titles, navigation, labels, messages, validation text, help and tips, portal strings and their
translations, and the current operator documentation. It does **not** rename code identifiers, API paths,
database objects, service names, directories, permission keys or translation keys.

## The three product terms

| Use | Instead of | Meaning |
|---|---|---|
| **Client** / **clients** | Guest / guests | The person who connects to the site's Wi-Fi through the portal. |
| **Admin Console** | Hotel Admin, Hotel Admin Console | The on-appliance administration application (served from the appliance's management address). |
| **Client Portal** | Guest Portal, guest portal | The captive portal an end user signs in on. |

Derived labels follow the same rule: *Client accounts*, *Client devices*, *Client sign-in attempts*,
*Client sign-in checks*, *Clients online*, *Client Login* (the portal's sign-in tab).

**Site** replaces *hotel* and *property* where the text means "the location this appliance serves" —
*this site's data*, *the appliance at the site*. A Central **Site** is still one physical location; its
definition is unchanged ([`ownership hierarchy`](CENTRAL_CONTROL_PLANE.md)).

### Translations of the portal terms

| Key (unchanged) | en | ar | de | fr | it | ru |
|---|---|---|---|---|---|---|
| `tab.guest` | Client Login | دخول العملاء | Kunden-Anmeldung | Connexion client | Accesso clienti | Вход для клиентов |

Keys are identifiers: a site's saved translation overrides are keyed by them, so a key is never renamed to
follow a wording change. A site that has already overridden a string keeps its own text.

## Where "guest" and "hotel" deliberately remain

These are not product wording. They carry a protocol, integration, network or domain meaning, and changing
them would change what the text says.

| Retained | Why |
|---|---|
| **Guest network(s)**, guest VLAN, guest bridge, "not on the guest network", **Guest Wi-Fi** | Standard network-engineering terms for the isolated end-user network, used the same way in every industry. |
| **PMS guest** semantics: guest name, G#, in-house guest, guest record, guest list/roster as the PMS holds it | The Property Management System's own vocabulary (FIAS and the other PMS integrations). The text describes a PMS record, not the Wi-Fi user. |
| **Stay**, **Room**, **RN**, **reservation**, **check-in/checkout**, **post-stay**, checkout grace | PMS stay semantics, unchanged by decision. |
| **Property management system** | The industry name of the system class (PMS). |
| **Hotel ID** and other PMS/FIAS field names | Integration fields defined by the PMS. |
| Code, API, database and deployment identifiers: `hotel-admin/`, `guest_accounts`, `/guest-accounts`, `guest-signin-attempts`, `check-hotel-admin-integrity.sh`, `stayconnect-*` units, `hotel.stayconnect.local`, the `@layer hotel` CSS layer, translation keys such as `tab.guest` | Renaming them is an API/deployment change with no product-facing benefit; routes keep working and existing bookmarks and integrations are unaffected. |
| Historical records: `governance/`, `docs/manifests/`, `docs/reports/`, `docs/evidence/`, `docs/acceptance/`, phase plans and closed reports | A record of what was true then is not rewritten. |

## Open decisions (not part of this change)

These are hotel-shaped by product design rather than by wording, and changing them needs its own
Product-Owner decision:

* **The PMS-centred information architecture.** The *Property management system*, *Charges* (room posting)
  and *Stays* areas exist because the product integrates with hotel PMSs. They are shown only where the
  appliance serves them, but the model itself is hospitality-specific.
* **"Reception"** in portal help text ("contact reception"). It reads acceptably in most industries; a
  configurable "help contact" would be a product feature, not a wording fix.
* **Central's Site definition example** and licence capacity ("clients online at once") are wording only and
  were changed; whether licensing should count something other than concurrent end-user devices is a
  licensing decision.
