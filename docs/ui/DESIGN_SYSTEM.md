# OneGate admin consoles — design system in practice

How to build a page in the **Admin Console** (`hotel-admin/`) and **Central** (`cloud-admin/`). Both consoles
share the same tokens (`design-system/tokens.css`, copied by `tools/sync-design-tokens.mjs`), the same
`tailwind.config.ts`, `app/globals.css`, `components/help.tsx` and the same `components/ui/*` files with the
same APIs. **Change a shared component in both consoles, byte for byte.** The palette, type scale and
contrast rules are in [`design-system/README.md`](../../design-system/README.md).

The look: calm and precise. Neutral layers, hairline borders, one brand colour for the single main action,
weights no heavier than 600, sentence case everywhere (no uppercase labels), and no decoration that does not
carry information.

## Tokens at a glance

| | Use |
|---|---|
| Layers | `bg-background` canvas · `bg-card` cards, inputs, dialogs · `bg-surface` table headers, inset panels · `bg-accent` hover/selected fill |
| Text | `text-foreground` · `text-muted-foreground` for secondary text (never lighter) |
| Lines | `border-border` hairlines · `border-border-strong` button edges · `border-input` control edges (≥ 3:1) |
| State | `success` · `warning` · `destructive` · `info`, each with `-subtle` / `-subtle-foreground`. State only, always with a word |
| Radius | `rounded-md` 6px controls · `rounded-lg` 10px cards · `rounded-xl` 12px dialogs, menus |
| Elevation | `shadow-card` resting · `shadow-card-hover` clickable on hover · `shadow-overlay` dialogs, drawers, menus |
| Type | `text-title` page title · `text-headline` dialog/drawer title · `text-emphasis` card title · `text-sm` body · `text-label` labels · `text-xs` meta · `text-2xs` table headers, badges · `text-metric` stat values |
| Spacing | 4px scale; page sections `space-y-5`; card padding `px-5 py-4`; table cells `px-4 py-2.5` (`compact`: `px-3 py-2`) |

Never write a literal colour (`text-red-600`, `#hex`); a unit test refuses it.

## Page anatomy

```
PageShell                       width: default | wide | narrow | form
  PageHeader                    title · help (lightbulb) · meta · ONE short description line · actions
  [Callout]                     only an essential operational / financial / security warning
  [StatGrid of StatCard]        optional summary figures
  Card
    FilterBar inset             Segmented filters · SearchInput · actions
    Table                       the list
    Pagination                  inside a CardFooter, or below the card
  SectionCard …                 further sections
DetailsDrawer                   secondary detail opened from a row or history entry
```

- **PageHeader** renders the title, the help trigger, an optional `meta` (a Badge or LiveStatus) and at most
  one short `description` sentence. `eyebrow` and `icon` are **deprecated and not rendered** — delete them
  from callers; the top-bar breadcrumb already says where you are.
- The page owns no gutter: `app/(app)/layout.tsx` supplies it. Never add `p-6 mx-auto` to a page.
- One primary (brand) button per screen. Everything else is `secondary`, `ghost` or a row action.

## Help text

| Where | What |
|---|---|
| `PageHeader help={…}` → the **HelpTip** side sheet | Everything explanatory: how the feature works, definitions, examples, edge cases. Use `HelpSection` / `HelpList`. |
| `PageHeader description` | One sentence saying what the page is for. No second line. |
| On the page, in a `Callout` | Only what the operator must see before acting: operational, financial or security consequences. |
| Beside a control (`Field hint`, `disabledReason`) | The rule for that control, or why it is disabled. One line. |
| `Explain` (tooltip) | A definition of a single figure or column. |

Do not stack explanatory paragraphs on the page. If a card needs more than one line of description, move the
rest into the page help.

## Lists and tables

- Wrap a list in `Card className="overflow-hidden"` with `FilterBar inset` on top, or use
  `SectionCard flush`.
- **Filters**: `Segmented` for 2–5 mutually exclusive views; `FilterChips` when the options carry counts or
  tones; a `Select` for long option lists. Filters, search and actions sit on **one** `FilterBar` row.
- **Search**: always `SearchInput`. It carries `type="search"`, `autoComplete="off"`, `spellCheck={false}`,
  a non-credential `name`/`id` and the password-manager opt-outs (`data-1p-ignore`, `data-lpignore`,
  `data-form-type="other"`), so a box labelled "Username…" is never offered saved credentials. Never wrap a
  search box in a `<form>` (use `role="search"` and `onSearch` for Enter). Never `autoFocus` it. A bespoke
  search field spreads `SEARCH_FIELD_PROPS`.
- **Cells**: one line per cell wherever possible.
  - `TD numeric` / `TH numeric` — counts, bytes, money, durations: right-aligned tabular figures, no wrap.
  - `TD nowrap` — short values: dates, speeds ("20 / 10 Mbps", not "20 Mbps down / 10 Mbps up"), codes, badges.
  - `TD truncate title={full}` — long names; `TD muted` for secondary columns.
  - A second line under the primary cell (`text-xs text-muted-foreground`) is allowed for the row's type or ID.
  - Hide secondary columns on small screens with `hidden md:table-cell`. The sidebar takes ~240px, so a page
    at `xl` (1280) has only ~974px for the table: columns that do not fit there move to `2xl:table-cell`, and
    what they show stays reachable from the row's details (or as a second line below `2xl`).
  - Never `nowrap` free text (names, emails, holders, reasons, descriptions): it grows to its longest real
    value. Give it `truncate` with a `max-w-[…]` and a `title`, or let it wrap. Test with long values.
- **Row actions**: `RowActions subject={rowName} primary={…} items={[…]}` in the last cell (`TD className="w-0"`).
  At most one inline action (the most frequent, never destructive); the rest go in the ⋯ menu, destructive
  ones last and red. Never plain-text action links side by side ("Edit Disable").
- **Pinned actions**: a last cell holding `RowActions` sticks to the inline end of an overflowing table
  automatically (opaque over hover/selected fills, an edge shadow only while columns scroll beneath it). Any
  other actions cell (a "Details" button) opts in with `TD pinEnd` and `TH pinEnd`.
- **Row click**: `TR onClick` opens the row's details (drawer or page). The row becomes keyboard-reachable;
  RowActions stop propagation.
- Long tables: `Table stickyHeader maxHeight="32rem"`, `density="compact"` for dense operational queues.
- States: `SkeletonRows` while loading (never a spinner on a blank page), `EmptyState` (size `sm` inside a
  card) for empty and no-match, `ErrorBanner` above the table with the last good rows still shown.

## Status

`StatusBadge tone=…` for a lifecycle state in a row (Active, Suspended, Failed…): dot + word. `Badge` for
attributes and counts. Tones: `ok` good/active, `warn` needs attention soon, `err` failed/blocked, `info`
neutral fact, `default` inactive/unknown, `accent` "current"/"selected".

## Stat cards

`StatCard label value hint` — the hint is **one short line** ("of 250 licensed"). No multi-line hints; put the
definition in `explain` (a tooltip) or the page help. `tone` only colours the value when it needs attention
(`warn`, `err`) or is confirmed good (`ok`). Group them with `StatGrid cols={4}`.

## History, versions and audit → DetailsDrawer

Revisions, versions, change history and audit entries are **secondary**. On the page they appear as a compact
`HistoryList` (title, who, when, one-line summary) — newest first, ideally limited to the latest few with a
"View all" link. Clicking an entry opens a **`DetailsDrawer`** with the full record: `eyebrow` = the record
type, `title`, status `badges`, `DetailsSection`s holding a `DescriptionList` and a `ChangeDiff` (before /
after per field), and record actions (e.g. "Roll back to this revision") in `actions`. Do not render full
diffs or long histories inline on the page, and do not open a dialog for them.

## Forms

- Every control sits in a `Field` (label, `hint` or `error`, `required` or `optional`). Mark the minority:
  if most fields are required, mark the optional ones.
- Lay fields out with `FieldGrid cols={2}` (one column on phones). Use three columns only for short numeric
  fields. Units go in `InputGroup suffix="Mbps"` or `SettingField` for operational settings.
- Settings forms live in a `SectionCard` with the save button in its `footer`. Large editors open in a
  `DialogForm` (size `md`/`lg`) or a full page; never squeeze a multi-column editor into a drawer.
- Consequential actions use `ConfirmDialog` (`consequences`, `requireReason`, `requirePassword`,
  `confirmText`, `confirmVariant="danger"`); the dialog explains a disabled submit through `disabledReason`.
- `InfoDialog` for a read-only acknowledgement; toasts (`useToast`) only confirm an action that just happened.
- `Button loading` while a request is in flight; it disables the button and keeps its label.

## Shell

Tonal sidebar with collapsible sections (sentence case), the current page marked by fill, weight and an
accent icon; a 3.75rem icon rail on desktop (remembered per device) and a labelled drawer below 1024px; a 56px
top bar with the breadcrumb "Section / Page", status and theme switch; the account block (who, role,
password, two-step sign-in, sign out) at the foot of the sidebar.

## Accessibility

Keep `data-testid`s and accessible names stable. Icon-only buttons need `aria-label`. Status is never colour
alone. Focus rings come from the components (`ring-ring`); do not remove them. Touch targets grow to 44px on
coarse pointers through the `pointer-coarse:` variant already in the components.
