# OneGate design system

One visual language for the three OneGate front-ends: the **guest portal** (portald), **Hotel Admin**
(the console on each appliance) and **Central** (the vendor console). Hotel Admin and Central are separate
products with separate logins; they share every token and component and differ only in their accent colour
and their product line under the OneGate mark.

| Front-end | Where the system lives |
|---|---|
| Hotel Admin | `hotel-admin/app/tokens.css` (generated), `hotel-admin/app/globals.css`, `hotel-admin/tailwind.config.ts`, `hotel-admin/components/ui/*`, `hotel-admin/components/brand.tsx` |
| Central | `cloud-admin/app/tokens.css` (generated), `cloud-admin/app/globals.css`, `cloud-admin/tailwind.config.ts`, `cloud-admin/components/ui/*`, `cloud-admin/components/brand.tsx` |
| Guest portal | `data-plane/cmd/portald/templates.go` (inline CSS: the portal cannot load a stylesheet or font from anywhere but the appliance) |

**The token values have one source:** [`tokens.css`](tokens.css). Run `node tools/sync-design-tokens.mjs`
after editing it; each console has a unit test (`test/design-tokens-sync.test.ts`) that fails when its copy
drifts, and one that checks the text contrast of every token pair in both themes.

## Principles

1. **Clarity over decoration.** Operators use these screens at a front desk, often mid-conversation with a
   guest. Every screen answers one question first; decoration never competes with state.
2. **One primary action per screen.** Brand colour is the logo, the single main action, and the active
   navigation item. If a second thing wants to be brand-coloured it is wrong.
3. **Colour carries state, never decoration** — and never *only* colour: every badge has its word.
4. **Flat where the eye scans.** Lists and queues are square and shadowless; depth comes from hairlines and
   tonal layers. Elevation is reserved for things that float (menus, dialogs, sheets) or respond (hover).
5. **Say it in the hotel's words.** The glossary in the redesign handoff is binding: *Customer*, *Site*,
   *Appliance*, *Guest network*, *Internet package*, *Service plan*, *Stay*, *Operator*, *Password
   confirmation*… Internal words (tenant, VLAN as a label, mirror, step-up, FIAS) are never the main label.
6. **Nothing silently changes behaviour.** The design system presents behaviour; it never decides it.
   Password confirmations, mandatory reasons, typed confirmations, one-time secrets and apply→confirm→
   rollback are product rules the components exist to express, not friction to remove.

## Colour

Tokens are HSL triples behind **role** names (`--primary`, `--muted-foreground`, `--border`…) consumed by
Tailwind as `hsl(var(--role) / <alpha>)`. A theme is a set of values; no component names a colour.

| Role | Light | Use |
|---|---|---|
| `primary` | `#1773bd` (hover `#125c97`, press `#0f4b7b`) | the one main action, active nav, links, the mark |
| `primary-subtle` / `-foreground` | `#e8f1f8` / `#0f4b7b` | active-nav tint, selected option cards |
| `foreground` (ink) | `#14161a` | text and headings |
| `muted-foreground` (slate) | `#5a6270` | secondary text, timestamps, quiet buttons |
| `background` (canvas) | `#f1f2f4` | the app canvas behind cards |
| `card` (surface) | `#ffffff` | cards, dialogs, inputs |
| `surface` (recessed) | `#f6f7f9` | table headers, inset panels |
| `accent` | `#eceef1` | hover and selection fills |
| `border` (hairline) | `#dcdfe4` | dividers — **never** a control boundary |
| `input` (edge) | `#7f8795` | control boundaries — ≥3:1, **never** a divider |
| `sidebar` (inverse) | `#1e1e21` | the navigation column, in both themes |
| `success` / `warning` / `destructive` / `info` | owned green, waiting amber, danger red, info blue | state only, each with a `-subtle` fill and a `-subtle-foreground` text |

Central sets `data-product="central"` on `<html>`, which swaps only the accent to **teal** (`#0e7c86`), so
the vendor console can never be mistaken for a hotel's appliance.

Both consoles ship **Light, Dark and System** themes from the same tokens (`.dark` redefines the values).
The guest portal is light only and takes each hotel's brand colour from Portal settings.

## Typography

One family: **Inter** (variable), self-hosted in each console via `@fontsource-variable/inter` — the
appliance may have no route to a font CDN. The guest portal uses the platform system font stack (no font
download before a guest is online).

| Tailwind class | Size / weight / line | Use |
|---|---|---|
| `text-title` | 1.625rem / 700 / 1.3, −0.025em | page title (one per screen) |
| `text-metric` | 1.5rem / 700 / 1.2 | numbers in stat cards |
| `text-subtitle` | 1.25rem / 700 / 1.4 | section headings, mobile page titles |
| `text-headline` | 1.125rem / 700 / 1.4 | dialog and sheet titles |
| `text-emphasis` | 0.9375rem / 650 / 1.5 | card titles, row titles |
| `text-body` / `text-sm` | 0.875rem / 400 / 1.45 | default text |
| `text-label` | 0.8125rem / 600 | form labels, button text |
| `text-caption` | 0.75rem / 400 | metadata, hints |
| `text-micro` | 0.6875rem / 700 | badges, table headers, eyebrows (uppercase) |
| `text-nano` | 0.625rem / 700 | counters, nav group headings |

Numbers that are compared use `tabular` (tabular figures).

## Space, shape, elevation, motion

- **Spacing**: the 4px scale only — 4 / 8 / 12 / 16 / 20 / 24 / 32 / 48px.
- **Radius** (by role): `rounded-none` flat (queue rows, status channels), `rounded-md` 7px controls,
  `rounded-lg` 9px cards and sheets, `rounded-xl` 12px overlays (dialogs, menus), `rounded-full` pills.
- **Elevation**: `shadow-card` (resting card), `shadow-card-hover` (clickable card on hover/focus),
  `shadow-overlay` (dialog, sheet, menu), `shadow-control-hover` / `shadow-control-press` (buttons).
- **Motion**: 80ms press feedback, 160ms state changes, easing `cubic-bezier(0.2, 0, 0, 1)` (`ease-onegate`).
  No bounce, no spring. `prefers-reduced-motion` removes transitions entirely.
- **Focus**: a 2px ink ring (light) / near-white ring (dark) offset 2px — never brand-coloured, so focus
  never reads as a second primary action.

## Logo

`components/brand.tsx` exports `OneGateMark` (two dotted chevrons around a node, drawn inline and filled
with `currentColor`) and `OneGateLockup` (the mark in its brand tile with "OneGate" and the product line).
The product name is always set in type beside the mark. No StayConnect branding appears in any redesigned
surface.

## Icons

**Lucide** (`lucide-react`) in both consoles, 16px in controls and navigation, 20px in page-header tiles,
1.5–2px stroke, `currentColor`. Each navigation destination has a distinct icon. Icons never carry meaning
alone: a status icon always has its word beside it. Directional icons (chevrons, arrows) are mirrored under
`dir="rtl"`. The guest portal uses a handful of inline SVG icons (no icon font).

## Components (Hotel Admin `components/ui/*`, mirrored in Central)

| Component | File | States it covers |
|---|---|---|
| `Button` (primary · secondary · outline · ghost/quiet · subtle · danger · link) | `button.tsx` | hover, press, focus, disabled, busy (label) |
| `Input`, `Textarea`, `Select`, `Field`, `Label`, `Hint` | `input.tsx` | label binding, hint, error (announced via `aria-describedby`), disabled, read-only |
| `Switch` | `misc.tsx` | on, off, disabled |
| `Badge`, `StatusDot` | `badge.tsx` | ok, warn, err, info, neutral, accent, live dot |
| `Card` (+ Header/Title/Body/Footer), `Section` | `card.tsx` | — |
| `PageShell`, `PageHeader`, `StatCard`, `Toolbar` | `page.tsx` | eyebrow, title, description, actions; stat tone, trend, link, loading |
| `Table`, `THead`, `TR`, `TH`, `TD` | `table.tsx` | hover, hidden columns on small screens (caller) |
| `SearchInput`, `FilterChips`, `Pagination`, `KeyValueGrid`, `MetricStrip`, `Timeline`, `Stepper`, `OptionCard` | `data.tsx` | counts, empty/no-match, selected |
| `Dialog`, `DialogForm`, `ConfirmDialog`, `DetailDialog` | `dialog.tsx` | sizes sm/md/lg/xl, busy, inline error, reason (text or choice, length limit), password confirmation, **typed confirmation**, consequence list |
| `Sheet` (+ Header/Body/Footer/Section) | `sheet.tsx` | full width on mobile |
| `Tabs`, `Segmented` | `tabs.tsx` | arrow-key navigation, `tabpanel` roles (Radix) |
| `Toast` / `useToast` | `toast.tsx` | success, error |
| `Tooltip`, `Explain` | `tooltip.tsx` | hover and keyboard focus |
| `EmptyState` | `empty-state.tsx` | icon, title, hint, first action |
| `ErrorBanner`, `Callout` | `error-banner.tsx` | info, success, warning, danger; trace id |
| Charts: `AreaChart`, `ColumnChart`, `BarList`, `SplitBar`, `Heatmap`, `Sparkline`, `Meter` | `chart.tsx`, `misc.tsx` | both themes |
| **Patterns** — `OneTimeReveal`, `PendingChangeBanner`, `LiveStatus`, `ReadOnlyNotice`, `NotAvailable`, `ConsequenceList`, `SettingField`, `CopyButton` | `patterns.tsx` | see below |
| `SurfaceNotEnabled` | `components/surface-not-enabled.tsx` | "Not enabled on this appliance" (guest internet unaffected) |

## Patterns (the states every screen must account for)

| State | How it looks |
|---|---|
| Normal | `PageHeader` + content cards; one primary button top right |
| Loading | `Skeleton` blocks in the shape of the content — never a spinner on a blank page |
| Refreshing / live | content stays, dimmed (`refreshingClass`); `LiveStatus` says "Updated x ago · every Ns" |
| Empty | `EmptyState`: icon, title, one-line hint, optional first action |
| No match | `EmptyState` naming the filter, with "Clear filters" |
| Error | `ErrorBanner` with the server's words and trace id; the last good data stays visible |
| Warning / success | `Callout` tone warning / success; transient results use `useToast` |
| Disabled | control at 50% with the reason in a tooltip or hint |
| Read-only | inputs disabled, actions hidden, one `ReadOnlyNotice` line under the header |
| Permission restricted | the menu item is hidden; a block the role may not see renders `NotAvailable` with the reason |
| Feature not enabled | `SurfaceNotEnabled` — "Not enabled on this appliance", guest internet unaffected |
| Destructive confirmation | `ConfirmDialog` with `consequences`, `requireReason`, `requirePassword`, `confirmText` (typed) and `confirmVariant="danger"` |
| One-time reveal | `OneTimeReveal`: modal, "Shown once" warning, large mono value, Copy, explicit "I have it" |
| Apply → confirm → rollback | `PendingChangeBanner` with a live countdown, Keep / Roll back, health results beneath |
| Password confirmation | the `requirePassword` field of `ConfirmDialog` (or `DialogForm`), always `type="password"`, cleared on close |

**The UI never shows a button the role cannot use** (`lib/roles.ts` `canWrite`), and never offers to show a
one-time secret again.

## Layout and navigation

- **App shell** (both consoles): inverse sidebar (16rem; collapses to a 3.75rem icon rail on desktop,
  remembered per device; a slide-in drawer below 1024px), a 56px sticky top bar with the breadcrumb
  "Group / Page", status pill and theme switch, and a content pane that owns the page gutter.
- **Hotel Admin** navigation: 8 groups — Overview · Internet offering · Guests · Property management system ·
  Charges · Guest portal · Networking · System. Menu items are hidden when the role cannot read them or the
  appliance does not serve them. The page title always equals the menu label.
- **Central** navigation: Overview · Infrastructure · Commercial · Administration, with the Customer
  context selector at the top for platform admins (a fixed "Your customer" label for tenant users).
- **Breakpoints**: phone < 640px (single column, tables hide secondary columns, sheets full width),
  tablet 640–1023px (drawer navigation), laptop ≥ 1024px (sidebar), wide ≥ 1536px.
- **RTL-ready**: logical spacing (`ps-*`, `pe-*`, `start-*`), mirrored directional icons. The consoles are
  English-only today; the guest portal is fully RTL in Arabic.

## Accessibility (WCAG 2.1 AA)

4.5:1 text contrast in both themes (tested), 3:1 control boundaries, visible focus everywhere, full
keyboard use (Radix dialogs trap focus and close on Esc; tabs move with arrow keys), form errors announced
through `aria-describedby`/`aria-live`, 44px touch targets in the guest portal, and no information by
colour alone.

## Guest portal

The portal is server-rendered HTML from portald with one inline stylesheet and a few lines of inline
script — no external font, CDN, framework or analytics, because the guest has no internet yet and the page
opens in a captive-portal mini-browser. Its default look uses the same palette, radius scale and type
roles as the consoles, expressed as CSS custom properties that each hotel's Portal settings override
(brand colour, button shade, text colour, radius, typeface, layout template, density, panel position,
hero height, surface, photo darkening). Six layout templates (Classic, Split, Immersive, Header bar,
Resort, Kiosk) are variations of one token set, so each stays coherent under any hotel's colour and photo.
Every page — sign-in, package choice, "You're online", errors — is branded and translated in the six
built-in languages, and fully mirrored in Arabic.

## Design decisions on record

- **Info stays blue.** The brand book defines `info` as `#245d92`, close to the brand blue. It is kept as
  the brand defines it; `info` is therefore never used without its word or icon, never on a button and never
  inside the active navigation item (whose tint it resembles).
- **"Licence" in Hotel Admin, "License" in Central.** Each console uses the spelling of its own menu in the
  redesign handoff (Hotel Admin: *Appliance & licence*; Central: *Licenses*), which also matches the words the
  appliance's own services send to Hotel Admin.
- **Central shows licensing only.** Appliances report nothing but licensing to Central, so Central's screens
  show licenses, sites and appliances and never guest activity; a screen that would need telemetry is not
  built rather than shown empty.
- **Controls mirror the server.** Each console hides an action its server refuses for the signed-in role
  (Hotel Admin: `lib/roles.ts`; Central: `lib/permissions.ts`, which cites the ctrlapi rule for every entry).
  The UI never widens or narrows what the server allows.
