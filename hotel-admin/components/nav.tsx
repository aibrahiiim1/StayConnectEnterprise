"use client";
import Link from "next/link";
import { useEffect, useMemo, useRef, useState } from "react";
import { usePathname } from "next/navigation";
import { cn } from "@/lib/utils";
import { canRead } from "@/lib/roles";
import { ROLE_LABELS, type SiteRole } from "@/lib/roles";
import {
  LayoutDashboard, Users, LogOut, Monitor, Shield, ScrollText, Hotel, Send, KeyRound,
  Wallet, BadgeCheck, Paintbrush, Archive, Network, Wifi, History, Router, Cloud,
  ServerCog, Lock, Activity, Package, Gauge, Smartphone, LogIn, Search, X,
  PanelLeftClose, PanelLeftOpen,
} from "lucide-react";
import { Tooltip } from "@/components/ui/tooltip";

// DEPLOYMENT GATES, NOT PRODUCT VOCABULARY.
//
// These env vars keep their historical names because they are a deployment contract: renaming one would
// silently un-gate a surface on every appliance already configured under the old name. What changed is that
// they are now read as the CAPABILITY each gates, so nobody has to know which release shipped what in order
// to follow this file — and nothing here reaches the operator, who sees capabilities in the sidebar and has
// no reason to learn the project's release history to find a screen.
//
// Each is a convenience only. edged is the authority and does not mount the routes behind a dark surface at
// all, so hiding a link prevents a dead end rather than enforcing anything.
const CAP_INTERNET_OFFERING = process.env.NEXT_PUBLIC_PHASE2_ADMIN === "1"; // packages + service plans
const CAP_PMS = process.env.NEXT_PUBLIC_PHASE3_ADMIN === "1";              // PMS connection, stays, routing
const CAP_CHARGES = process.env.NEXT_PUBLIC_PHASE4_ADMIN === "1";          // charges, settlements, recovery
const CAP_POST_STAY = process.env.NEXT_PUBLIC_PHASE5_ADMIN === "1";        // post-stay access, transfers
const CAP_GUEST_DEVICES = process.env.NEXT_PUBLIC_PHASE6_ADMIN === "1";    // guest device self-service

// Each item names the edged resource that gates its visibility. Items the operator's roles cannot read are
// hidden (edged still enforces server-side). `enabled: false` hides an item behind a deployment gate
// regardless of role.
//
// `keywords` feeds the filter box only. An operator looking for "wifi speed" should find Service plans, and
// one looking for "room sign in" should find the PMS resolution screen, without having to know what this
// product decided to call either.
type Item = {
  href: string;
  label: string;
  icon: any;
  resource: string;
  enabled?: boolean;
  keywords?: string;
};
type Section = { title: string; items: Item[] };

// THE SIDEBAR IS AN INFORMATION ARCHITECTURE, NOT A LIST OF BACKEND RESOURCES.
//
// It is grouped by the job an operator is doing. The previous grouping had a 16-item "Integrations" section
// holding the PMS, the financial screens, post-stay, notifications and social login at one flat level, and an
// "Access" section that mixed what a guest can buy with who is currently online. Both are jobs; neither was
// findable.
const SECTIONS: Section[] = [
  {
    title: "Overview",
    items: [
      { href: "/dashboard", label: "Dashboard", icon: LayoutDashboard, resource: "reports", keywords: "home tonight summary stats" },
    ],
  },
  {
    // WHAT A GUEST CAN BE GIVEN, AND THE SERVICE BEHIND IT. These are one job in two steps — a Service plan
    // defines the technical service (speed, devices, duration/quota); an Internet package is the guest-facing
    // offer that uses it — so they sit adjacent instead of being buried in one tabbed screen.
    title: "Internet offering",
    items: [
      { href: "/internet-packages", label: "Internet packages", icon: Package, resource: "commercial-packages", enabled: CAP_INTERNET_OFFERING, keywords: "offer tariff price free paid" },
      { href: "/service-plans",     label: "Service plans",     icon: Gauge,   resource: "commercial-packages", enabled: CAP_INTERNET_OFFERING, keywords: "speed bandwidth quota devices mbps" },
      { href: "/checkout-grace",    label: "Checkout grace",    icon: Shield,  resource: "checkout-grace",      enabled: CAP_PMS, keywords: "after checkout late departure" },
    ],
  },
  {
    title: "Guests",
    items: [
      { href: "/stays",          label: "Stays",           icon: Hotel,    resource: "pms-stays", enabled: CAP_PMS, keywords: "rooms reservations in house occupancy guest list" },
      { href: "/guest-accounts", label: "Guest accounts",  icon: KeyRound, resource: "guest-accounts", keywords: "username password login credentials voucher" },
      { href: "/sessions",       label: "Active sessions", icon: Monitor,  resource: "sessions", keywords: "online now devices connected who is on wifi disconnect" },
      { href: "/guest-device-self-service", label: "Guest devices", icon: Smartphone, resource: "guest-device-self-service", enabled: CAP_GUEST_DEVICES, keywords: "phone laptop remove device" },
      { href: "/online-time",    label: "Online-time budgets", icon: Activity, resource: "sessions", enabled: CAP_GUEST_DEVICES, keywords: "time remaining allowance hours" },
      { href: "/post-stay",      label: "Post-stay access", icon: KeyRound, resource: "post-stay-profiles", enabled: CAP_POST_STAY, keywords: "after departure loyalty" },
    ],
  },
  {
    // THE PMS, AS ONE SUBJECT.
    //
    // "PMS providers" is GONE rather than hidden. It was a second, older configuration model for the same
    // property management system the PMS connection now owns, and offering an operator two ways to configure
    // one PMS is worse than offering one imperfect way — whichever they fill in, they cannot tell whether it
    // is the one the appliance actually dials.
    //
    // Ordered connection-first: when guests cannot get online, "is the PMS connected" is the question and the
    // stays are the symptom.
    title: "Property management system",
    items: [
      { href: "/pms-interfaces",       label: "PMS connection",       icon: Hotel,  resource: "pms-interfaces",       enabled: CAP_PMS, keywords: "protel fias connect sync resync opera status" },
      { href: "/pms-routing",          label: "Network routing",      icon: Router, resource: "pms-routing",          enabled: CAP_PMS, keywords: "which pms per network vlan mapping" },
      { href: "/stay-events",          label: "PMS activity",         icon: Send,   resource: "pms-events",           enabled: CAP_PMS, keywords: "feed messages check in out log" },
      { href: "/pms-resolutions",      label: "Guest sign-in checks", icon: Send,   resource: "pms-resolutions",      enabled: CAP_PMS, keywords: "room verification failures evidence" },
    { href: "/guest-signin-attempts", label: "Guest sign-in attempts", icon: KeyRound, resource: "guest-signin-attempts", enabled: CAP_PMS, keywords: "attempt failed reason room typed credential mismatch why cannot connect" },
      { href: "/pms-source-conflicts", label: "Duplicate sources",    icon: Shield, resource: "pms-source-conflicts", enabled: CAP_PMS, keywords: "conflict two interfaces same room" },
      { href: "/stay-transfers",       label: "Cross-PMS transfer",   icon: Send,   resource: "stay-transfers",       enabled: CAP_POST_STAY, keywords: "move stay between systems" },
    ],
  },
  {
    title: "Charges",
    items: [
      { href: "/financial-health",      label: "Charge health", icon: Wallet, resource: "financial-review", enabled: CAP_CHARGES, keywords: "posting queue outbox money" },
      { href: "/financial-review",      label: "Manual review", icon: Shield, resource: "financial-review", enabled: CAP_CHARGES, keywords: "failed posting decide" },
      { href: "/financial-settlements", label: "Settlements",   icon: Wallet, resource: "financial-review", enabled: CAP_CHARGES, keywords: "payment room charge card" },
      { href: "/financial-recovery",    label: "Recovery",      icon: Shield, resource: "financial-review", enabled: CAP_CHARGES, keywords: "held restore epoch" },
    ],
  },
  {
    title: "Guest portal",
    items: [
      // Sign-in methods leads the group: which ways a guest may prove who they are is the first thing an
      // operator sets up on the portal, and it was previously not settable anywhere in the product.
      { href: "/sign-in-methods",  label: "Sign-in methods", icon: LogIn,    resource: "auth-methods", keywords: "room number voucher otp sms email social" },
      { href: "/portal-branding",  label: "Branding",        icon: Paintbrush, resource: "portal-branding", keywords: "logo colours terms languages" },
      { href: "/walled-garden",    label: "Allowed sites", icon: Shield,     resource: "walled-garden", keywords: "whitelist domains before login" },
      { href: "/social-providers", label: "Social login",  icon: KeyRound,   resource: "social-providers", keywords: "google apple facebook microsoft oauth" },
      { href: "/notifications",    label: "Email & SMS",   icon: Send,       resource: "notification-providers", keywords: "sendgrid twilio ses otp delivery" },
    ],
  },
  {
    title: "Networking",
    items: [
      { href: "/network",             label: "Guest networks",     icon: Network,   resource: "network", keywords: "vlan ssid subnet bridge captive portal" },
      { href: "/network/dhcp",        label: "DHCP & leases",      icon: Wifi,      resource: "network", keywords: "ip address pool lease kea reservation" },
      { href: "/network/system",      label: "WAN / LAN settings", icon: Router,    resource: "network", keywords: "uplink gateway dns static management" },
      { href: "/network/revisions",   label: "Config history",     icon: History,   resource: "network", keywords: "rollback revision applied" },
      { href: "/network/cloud",       label: "Cloud connection",   icon: Cloud,     resource: "network", keywords: "central outbox sync enrolment nats" },
      { href: "/network/certificate", label: "TLS certificate",    icon: Lock,      resource: "network", keywords: "https ssl rotate expiry" },
      { href: "/setup/enrollment",    label: "Setup / Activation", icon: ServerCog, resource: "network", keywords: "enrol claim licence serial activate" },
    ],
  },
  {
    title: "System",
    items: [
      { href: "/health",             label: "Diagnostics", icon: Activity,   resource: "diagnostics", keywords: "services health checks scd netd kea" },
      { href: "/operational-alerts", label: "Alerts",      icon: Shield,     resource: "operational-alerts", enabled: CAP_PMS, keywords: "warnings acknowledge" },
      { href: "/operators",          label: "Operators",   icon: Users,      resource: "operators", keywords: "staff users roles password" },
      { href: "/license",            label: "License",     icon: BadgeCheck, resource: "license", keywords: "activation capacity expiry plan" },
      { href: "/backups",            label: "Backups",     icon: Archive,    resource: "backups", keywords: "restore snapshot database" },
      { href: "/audit",              label: "Audit log",   icon: ScrollText, resource: "audit", keywords: "who did what history trail" },
    ],
  },
];

/** Flattened once, for the breadcrumb/title lookup the top bar does on every navigation. */
export const NAV_ITEMS: Item[] = SECTIONS.flatMap((s) => s.items);
export const NAV_SECTION_OF: Record<string, string> = Object.fromEntries(
  SECTIONS.flatMap((s) => s.items.map((i) => [i.href, s.title])),
);

/**
 * activeNavHref — the LONGEST href this path matches, not every href it starts with.
 *
 * `path.startsWith(it.href)` marked several items active at once, because these are siblings rather than a
 * parent and its children: "/network" is the Guest networks LEAF, so standing on "/network/dhcp" lit up both
 * "DHCP & leases" and "Guest networks". Two highlighted rows in a menu is not a cosmetic issue — the
 * highlight is the only thing telling an operator which screen they are on.
 *
 * The "/" in the child test matters too: plain startsWith would make a hypothetical "/networkfoo" light up
 * "/network", matching on a shared prefix that is not a path boundary at all.
 */
export function activeNavHref(path: string): string | null {
  const matches = NAV_ITEMS.map((it) => it.href).filter(
    (href) => path === href || path.startsWith(href + "/"),
  );
  return matches.sort((a, b) => b.length - a.length)[0] ?? null;
}

export function Nav({
  onLogout, email, roles, onNavigate, collapsed = false, onToggleCollapsed,
}: {
  onLogout: () => void;
  email?: string;
  roles: string[];
  /** Called after a link is followed, so the mobile drawer can close itself. */
  onNavigate?: () => void;
  /**
   * Render as an icon rail. THE DRAWER NEVER PASSES THIS. Below `lg` the navigation is a full-width overlay
   * with room for labels, and these labels ("Duplicate sources", "Checkout grace") are not guessable from an
   * icon — so the phone gets the labelled list, exactly as it did before.
   */
  collapsed?: boolean;
  /** Absent in the drawer, which has no width to reclaim and therefore no control to offer. */
  onToggleCollapsed?: () => void;
}) {
  const path = usePathname();
  const [query, setQuery] = useState("");
  const searchRef = useRef<HTMLInputElement | null>(null);
  // Set when the operator opens the filter FROM the rail, so focus lands in the input only once the expanded
  // input actually exists. Focusing during the same render would target an element that is not mounted yet.
  const focusFilterAfterExpand = useRef(false);

  const activeHref = useMemo(() => activeNavHref(path), [path]);

  // A FILTER, NOT A SEARCH ENGINE. Thirty-four screens in eight groups is past the point where scanning is
  // reliable, and the operator who needs "the page with the resync button" should not have to remember that it
  // lives under Property management system.
  const visibleSections = useMemo(() => {
    const q = query.trim().toLowerCase();
    return SECTIONS.map((sec) => ({
      title: sec.title,
      items: sec.items.filter((it) => {
        if (it.enabled === false || !canRead(it.resource, roles)) return false;
        if (!q) return true;
        return (
          it.label.toLowerCase().includes(q) ||
          sec.title.toLowerCase().includes(q) ||
          (it.keywords ?? "").includes(q)
        );
      }),
    })).filter((sec) => sec.items.length > 0);
  }, [query, roles]);

  // Opening the filter is also a request to SEE it. From the rail there is no input to focus, so the column
  // expands first and the focus is deferred to the render in which the input exists.
  const openFilter = () => {
    if (collapsed && onToggleCollapsed) {
      focusFilterAfterExpand.current = true;
      onToggleCollapsed();
      return;
    }
    searchRef.current?.focus();
  };

  useEffect(() => {
    if (!collapsed && focusFilterAfterExpand.current) {
      focusFilterAfterExpand.current = false;
      searchRef.current?.focus();
    }
  }, [collapsed]);

  // "/" focuses the filter, the one shortcut worth having on a screen whose primary cost is finding a page.
  // Guarded so it does not steal the key while the operator is typing into a form.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "/" || e.metaKey || e.ctrlKey || e.altKey) return;
      const t = e.target as HTMLElement | null;
      if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.tagName === "SELECT" || t.isContentEditable)) return;
      e.preventDefault();
      openFilter();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });

  const roleLabel = roles
    .map((r) => ROLE_LABELS[r as SiteRole])
    .filter(Boolean)
    .join(", ");

  return (
    <aside
      data-collapsed={collapsed ? "true" : undefined}
      className={cn(
        "sidebar-motion flex h-full shrink-0 flex-col border-r border-sidebar-border bg-sidebar text-sidebar-foreground",
        // The DRAWER is always a full 16rem overlay; only the desktop column follows the width token.
        onToggleCollapsed ? "w-[var(--sidebar-width)] transition-[width] duration-200 ease-out" : "w-64",
      )}
    >
      <div
        className={cn(
          "flex items-center gap-2.5 border-b border-sidebar-border py-3.5",
          collapsed ? "flex-col gap-2 px-2" : "px-4",
        )}
      >
        {/* The mark is drawn rather than loaded: one fewer asset to ship to an appliance, and it inherits the
            brand token so it is never out of step with the rest of the product. */}
        <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-primary text-primary-foreground">
          <Wifi className="size-4" />
        </span>
        {!collapsed && (
          <div className="min-w-0 flex-1">
            <div className="truncate text-sm font-semibold leading-tight text-white">StayConnect</div>
            <div className="truncate text-2xs uppercase tracking-widest text-sidebar-muted">Hotel Admin</div>
          </div>
        )}
        {onToggleCollapsed && (
          // ONE control, and its accessible name states what activating it will DO, which is what a screen
          // reader user needs — not what the current state is. aria-expanded carries the state.
          <Tooltip content={collapsed ? "Expand sidebar" : "Collapse sidebar"} side="right">
            <button
              type="button"
              onClick={onToggleCollapsed}
              aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
              aria-expanded={!collapsed}
              aria-controls="sidebar-nav"
              className={cn(
                "flex size-8 shrink-0 items-center justify-center rounded-md text-sidebar-muted",
                "transition-colors hover:bg-sidebar-accent/60 hover:text-white",
                "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50",
              )}
            >
              {collapsed ? <PanelLeftOpen className="size-4" /> : <PanelLeftClose className="size-4" />}
            </button>
          </Tooltip>
        )}
      </div>

      <div className={cn("border-b border-sidebar-border py-2.5", collapsed ? "px-2" : "px-3")}>
        {collapsed ? (
          // The rail keeps the filter REACHABLE rather than hiding it: finding a screen is the sidebar's main
          // job, and losing it would be the strongest argument against ever collapsing.
          <Tooltip content="Find a screen" side="right">
            <button
              type="button"
              onClick={openFilter}
              aria-label="Find a screen"
              className={cn(
                "flex size-10 w-full items-center justify-center rounded-md text-sidebar-muted",
                "transition-colors hover:bg-sidebar-accent/60 hover:text-white",
                "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50",
              )}
            >
              <Search className="size-4" />
            </button>
          </Tooltip>
        ) : (
          <div className="relative">
            <Search className="pointer-events-none absolute left-2.5 top-1/2 size-3.5 -translate-y-1/2 text-sidebar-muted" />
            <input
              ref={searchRef}
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => e.key === "Escape" && setQuery("")}
              placeholder="Find a screen…"
              aria-label="Filter navigation"
              className={cn(
                "h-8 w-full rounded-md border border-sidebar-border bg-sidebar-accent/60 pl-8 pr-7 text-sm",
                "text-sidebar-foreground placeholder:text-sidebar-muted",
                "focus:border-primary/60 focus:outline-none focus:ring-2 focus:ring-primary/25",
              )}
            />
            {query && (
              <button
                type="button"
                onClick={() => setQuery("")}
                aria-label="Clear filter"
                className="absolute right-1.5 top-1/2 -translate-y-1/2 rounded p-0.5 text-sidebar-muted hover:text-white"
              >
                <X className="size-3.5" />
              </button>
            )}
          </div>
        )}
      </div>

      <nav
        id="sidebar-nav"
        className={cn("nav-scroll flex-1 overflow-y-auto py-2.5", collapsed ? "px-2" : "px-2")}
        aria-label="Main"
      >
        {visibleSections.length === 0 ? (
          <p className="px-3 py-6 text-center text-xs text-sidebar-muted">
            Nothing matches “{query}”.
          </p>
        ) : (
          visibleSections.map((sec) => (
            <div key={sec.title} className="mb-3 last:mb-0">
              {collapsed ? (
                // The heading text goes, but the GROUPING stays: a hairline keeps the eight groups legible as
                // groups, which is most of what the headings were doing for someone who already knows the
                // product. It is decorative, so it is hidden from assistive tech — the accessible grouping
                // still comes from each item's own label.
                <div className="mx-2 mb-1.5 mt-1 border-t border-sidebar-border/70 first:mt-0 first:border-t-0" aria-hidden />
              ) : (
                <div className="px-2.5 pb-1 pt-1.5 text-2xs font-semibold uppercase tracking-widest text-sidebar-muted">
                  {sec.title}
                </div>
              )}
              <ul className="space-y-0.5">
                {sec.items.map((it) => {
                  const active = it.href === activeHref;
                  const Icon = it.icon;
                  const link = (
                    <Link
                      href={it.href}
                      onClick={onNavigate}
                      aria-current={active ? "page" : undefined}
                      // WITHOUT A VISIBLE LABEL, THE ACCESSIBLE NAME MUST COME FROM SOMEWHERE. In the rail the
                      // only text in the anchor is the sr-only span below, kept as REAL TEXT rather than
                      // replaced by aria-label. That keeps the accessible name identical in both modes, so
                      // getByRole("link", { name }) and aria-current continue to work unchanged — the
                      // collapse is a visual change, not a semantic one.
                      className={cn(
                        "group relative flex items-center rounded-md text-sm transition-colors",
                        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50",
                        collapsed ? "h-10 justify-center px-0" : "gap-2.5 px-2.5 py-1.5",
                        active
                          ? "bg-sidebar-accent font-medium text-sidebar-accent-foreground"
                          : "text-sidebar-foreground/85 hover:bg-sidebar-accent/60 hover:text-white",
                      )}
                    >
                      {/* The active rail. A background tint alone was the only active signal and it is two
                          steps off the surrounding colour — easy to miss on a dim front-desk monitor. In the
                          collapsed rail it matters MORE, because the tint is the only other cue and there is
                          no label to read. */}
                      <span
                        className={cn(
                          "absolute left-0 top-1/2 -translate-y-1/2 rounded-r-full bg-primary transition-opacity",
                          collapsed ? "h-6 w-[3px]" : "h-4 w-0.5",
                          active ? "opacity-100" : "opacity-0",
                        )}
                        aria-hidden
                      />
                      <Icon
                        className={cn(
                          "size-4 shrink-0 transition-colors",
                          active ? "text-primary" : "text-sidebar-muted group-hover:text-sidebar-foreground",
                        )}
                      />
                      <span className={cn(collapsed ? "sr-only" : "truncate")}>{it.label}</span>
                    </Link>
                  );
                  return (
                    <li key={it.href}>
                      {collapsed ? (
                        // Radix opens this on hover AND on keyboard focus, which is the requirement: a rail
                        // whose labels are mouse-only would be unusable from the keyboard.
                        <Tooltip content={it.label} side="right">{link}</Tooltip>
                      ) : (
                        link
                      )}
                    </li>
                  );
                })}
              </ul>
            </div>
          ))
        )}
      </nav>

      {/* PROFILE AND SIGN OUT SURVIVE THE COLLAPSE. Who you are signed in as, and the way out, are the two
          things an operator must never have to expand a menu to find. */}
      <div className={cn("border-t border-sidebar-border", collapsed ? "p-2" : "p-2.5")}>
        {collapsed ? (
          <Tooltip
            side="right"
            content={
              <span className="block">
                <span className="block font-medium">{email ?? "—"}</span>
                <span className="block text-muted-foreground">{roleLabel || "No site role"}</span>
              </span>
            }
          >
            {/* Not a button: it does nothing when activated. tabIndex makes it focusable so the tooltip is
                reachable from the keyboard, and the role/label make it announce as the status it is. */}
            <div
              tabIndex={0}
              role="img"
              aria-label={`Signed in as ${email ?? "unknown"}${roleLabel ? `, ${roleLabel}` : ""}`}
              className={cn(
                "mx-auto flex size-8 items-center justify-center rounded-full bg-sidebar-accent",
                "text-2xs font-semibold uppercase text-white",
                "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50",
              )}
            >
              {(email ?? "?").slice(0, 2)}
            </div>
          </Tooltip>
        ) : (
          <div className="flex items-center gap-2.5 rounded-md px-2 py-1.5">
            <span
              className="flex size-7 shrink-0 items-center justify-center rounded-full bg-sidebar-accent text-2xs font-semibold uppercase text-white"
              aria-hidden
            >
              {(email ?? "?").slice(0, 2)}
            </span>
            <div className="min-w-0 flex-1">
              <div className="truncate text-xs font-medium text-sidebar-foreground" title={email}>
                {email ?? "—"}
              </div>
              {/* The ROLE, named. An operator refused a button needs to know which hat they are wearing; the
                  product knew and did not say. */}
              <div className="truncate text-2xs text-sidebar-muted" title={roleLabel}>
                {roleLabel || "No site role"}
              </div>
            </div>
          </div>
        )}

        {(() => {
          const signOut = (
            <button
              type="button"
              onClick={onLogout}
              aria-label={collapsed ? "Sign out" : undefined}
              className={cn(
                "mt-1 flex w-full items-center rounded-md text-sm text-sidebar-foreground/85",
                "transition-colors hover:bg-sidebar-accent/60 hover:text-white",
                "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/50",
                collapsed ? "h-10 justify-center px-0" : "gap-2.5 px-2.5 py-1.5",
              )}
            >
              <LogOut className="size-4 shrink-0 text-sidebar-muted" />
              <span className={cn(collapsed ? "sr-only" : undefined)}>Sign out</span>
            </button>
          );
          return collapsed ? <Tooltip content="Sign out" side="right">{signOut}</Tooltip> : signOut;
        })()}
      </div>
    </aside>
  );
}
