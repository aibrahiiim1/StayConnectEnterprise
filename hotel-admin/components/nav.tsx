"use client";
import Link from "next/link";
import { useEffect, useId, useMemo, useRef, useState } from "react";
import { usePathname } from "next/navigation";
import { cn } from "@/lib/utils";
import { canRead } from "@/lib/roles";
import { useCapabilities, surfaceAvailable, moduleLicensed } from "@/lib/capabilities";
import { ROLE_LABELS, type SiteRole } from "@/lib/roles";
import {
  LayoutDashboard, Users, LogOut, Monitor, Shield, ScrollText, Hotel, Send, KeyRound,
  Wallet, BadgeCheck, Paintbrush, Archive, Network, Wifi, History, Router, Cloud,
  ServerCog, Lock, Activity, Package, Gauge, Smartphone, LogIn, Search, X,
  PanelLeftClose, PanelLeftOpen, ClipboardCheck, Ticket,
  DoorOpen, DoorClosed, BedDouble, Plug, Route, Inbox, ShieldCheck, UserX, Layers, ArrowLeftRight, HeartPulse, Receipt,
  LifeBuoy, Globe, AtSign, MessageSquare, Stethoscope, Bell, Hourglass, CalendarClock, ChartColumn,
  CreditCard, BedSingle, Blocks, UsersRound, UserSearch, ChevronRight,
} from "lucide-react";
import { BySemantics, OneGateLockup } from "@/components/brand";
import { Tooltip } from "@/components/ui/tooltip";

// Build-time flags can hide mounted routes; use edged capabilities and role permissions for navigation.
// edged enforces authorization even when a route is opened directly.
type Item = {
  href: string;
  label: string;
  icon: any;
  resource: string;
  /** Some destinations need a narrower capability than their always-mounted resource. */
  capability?: string;
  /** Hide optional provider screens unless a matching module is licensed. */
  modules?: string[];
  keywords?: string;
};
type Section = { title: string; items: Item[] };

const SECTIONS: Section[] = [
  {
    title: "Overview",
    items: [
      { href: "/dashboard", label: "Overview", icon: LayoutDashboard, resource: "reports", keywords: "home tonight summary stats" },
    ],
  },
  {
    title: "Internet offering",
    items: [
      { href: "/internet-packages", label: "Internet packages", icon: Package, resource: "commercial-packages", keywords: "offer tariff price free paid" },
      { href: "/service-plans",     label: "Service plans",     icon: Gauge,   resource: "commercial-packages", keywords: "speed bandwidth quota devices mbps" },
      { href: "/vouchers",          label: "Vouchers",          icon: Ticket,  resource: "vouchers", keywords: "code card print batch redeem scratch prepaid" },
      // Keep this page under modules so it can explain unavailable payment methods too.
      { href: "/payment-methods",   label: "Payment methods",   icon: CreditCard, resource: "modules", keywords: "card stripe paymob provider price paid free voucher room charge checkout acquisition" },
    ],
  },
  {
    title: "Clients",
    items: [
      { href: "/guest-accounts", label: "Client accounts",  icon: KeyRound, resource: "guest-accounts", keywords: "username password login credentials voucher guest accounts self-service email verification resend" },
      { href: "/sessions",       label: "Active sessions", icon: Monitor,  resource: "sessions", keywords: "online now devices connected who is on wifi disconnect guests" },
      { href: "/devices",        label: "Devices",         icon: Smartphone, resource: "sessions", keywords: "device mac phone laptop place slot release replace disconnect sign in again test reset remembered" },
      { href: "/usage",          label: "Usage explorer",  icon: ChartColumn, resource: "usage", keywords: "data used quota dispute how much room device mac gigabytes consumption investigate guests" },
      { href: "/online-time",    label: "Online-time budgets", icon: Hourglass, resource: "sessions", capability: "sessions.aggregate-time", keywords: "time remaining allowance hours guests" },
    ],
  },
  {
    title: "Client access",
    items: [
      { href: "/sign-in-methods",  label: "Sign-in methods", icon: LogIn,    resource: "auth-methods", keywords: "room number voucher otp sms email account register verification suspend grace social primary method remember device client portal guest portal" },
      // Groups remain available without an identity-provider module.
      { href: "/client-groups",    label: "Client groups",   icon: UsersRound, resource: "client-groups", keywords: "employees partners organisation domain microsoft google workspace vip group audience client portal" },
      { href: "/social-providers", label: "Identity providers", icon: AtSign, resource: "social-providers", modules: ["social_login"], keywords: "google apple facebook microsoft oauth social login client portal guest portal" },
      { href: "/notifications",    label: "Delivery",        icon: MessageSquare, resource: "notification-providers", modules: ["email_otp", "sms_otp", "whatsapp_otp"], keywords: "sendgrid twilio otp whatsapp smtp mail server email sms delivery client portal guest portal" },
      { href: "/portal-branding",  label: "Portal settings", icon: Paintbrush, resource: "portal-branding", keywords: "branding logo background colours terms languages translations client portal guest portal" },
      { href: "/walled-garden",    label: "Allowed sites",   icon: Globe,     resource: "walled-garden", keywords: "whitelist domains before login client portal guest portal" },
    ],
  },
  {
    title: "Hotel",
    items: [
      { href: "/pms-interfaces",       label: "PMS connection",       icon: Plug,  resource: "pms-interfaces", keywords: "protel fias connect sync resync opera status property management system hotel" },
      { href: "/room-sign-in",         label: "Room sign-in",         icon: DoorClosed, resource: "auth-methods", capability: "pms-interfaces", keywords: "room number surname first name reservation what the guest types credential pms sign-in" },
      { href: "/stays",                label: "Stays",                icon: BedDouble,    resource: "pms-stays", keywords: "rooms reservations in house occupancy guest list guests hotel" },
      { href: "/stay-events",          label: "PMS activity",         icon: Inbox,   resource: "pms-events", keywords: "feed messages check in out log property management system hotel" },
      { href: "/pms-resolutions",      label: "Guest sign-in checks", icon: ShieldCheck,   resource: "pms-resolutions", keywords: "room verification failures evidence client sign-in checks hotel" },
      { href: "/guest-signin-attempts", label: "Guest sign-in attempts", icon: UserX, resource: "guest-signin-attempts", keywords: "attempt failed reason room typed credential mismatch why cannot connect client sign-in attempts hotel" },
      { href: "/checkout-grace",       label: "Grace Period",         icon: DoorOpen,  resource: "checkout-grace", keywords: "checkout grace after checkout late departure hotel" },
      { href: "/post-stay",            label: "Post-stay access",     icon: CalendarClock, resource: "post-stay-profiles", keywords: "after departure loyalty guests hotel" },
      { href: "/room-charge",           label: "Room charge",   icon: BedSingle, resource: "pms-financial-onboarding", keywords: "charge to room reservation folio posting fias onboarding approve currency answer meanings hotel" },
      { href: "/financial-health",      label: "Charge health", icon: HeartPulse, resource: "financial-review", keywords: "charges posting queue outbox money hotel" },
      { href: "/financial-review",      label: "Manual review", icon: ClipboardCheck, resource: "financial-review", keywords: "charges failed posting decide hotel" },
      { href: "/financial-settlements", label: "Package payments", icon: Receipt, resource: "financial-review", keywords: "settlements charges payment room charge card voucher free hotel" },
      { href: "/financial-recovery",    label: "Recovery",      icon: LifeBuoy, resource: "financial-review", keywords: "charges held restore epoch hotel" },
      { href: "/pms-routing",          label: "PMS routing",          icon: Route, resource: "pms-routing", keywords: "network routing which pms per network vlan mapping hotel" },
      { href: "/pms-source-conflicts", label: "Duplicate sources",    icon: Layers, resource: "pms-source-conflicts", keywords: "conflict two interfaces same room hotel" },
      { href: "/stay-transfers",       label: "Cross-PMS transfer",   icon: ArrowLeftRight,   resource: "stay-transfers", keywords: "move stay between systems hotel" },
    ],
  },
  {
    title: "Networking",
    items: [
      { href: "/network",             label: "Client networks",     icon: Network,   resource: "network", keywords: "vlan ssid subnet bridge captive portal guest networks" },
      { href: "/network/dhcp",        label: "DHCP & leases",      icon: Wifi,      resource: "network", keywords: "ip address pool lease kea reservation" },
      { href: "/network/system",      label: "WAN / LAN settings", icon: Router,    resource: "network", keywords: "uplink gateway dns static management" },
      { href: "/network/revisions",   label: "Config history",     icon: History,   resource: "network", keywords: "rollback revision applied" },
      { href: "/portal-https",        label: "Portal HTTPS",       icon: ShieldCheck, resource: "portal-https", keywords: "https ssl certificate captive portal address domain hostname letsencrypt client portal guest portal fallback" },
      // Hotel Admin TLS secures local HTTPS independently of Central reachability.
      { href: "/network/certificate", label: "TLS certificate",    icon: Lock,      resource: "network", keywords: "https ssl rotate expiry" },
    ],
  },
  {
    title: "System",
    items: [
      { href: "/health",             label: "Diagnostics", icon: Stethoscope,   resource: "diagnostics", keywords: "services health checks scd netd kea" },
      { href: "/operational-alerts", label: "Alerts",      icon: Bell,     resource: "operational-alerts", keywords: "warnings acknowledge" },
      { href: "/audit",              label: "Activity",    icon: ScrollText, resource: "audit", keywords: "audit log who did what history trail security changes" },
      { href: "/appliance",          label: "Appliance & licence", icon: ServerCog, resource: "license", keywords: "enrol claim serial activate setup cloud connection central licence capacity expiry plan offline renewal first-time" },
      { href: "/modules",            label: "Modules",     icon: Blocks,     resource: "modules", keywords: "licence licensed features hospitality paid access card payment room charge switch on off site type" },
      { href: "/backups",            label: "Backups",     icon: Archive,    resource: "backups", keywords: "restore snapshot database retention schedule" },
      { href: "/guest-data",         label: "Guest data requests", icon: UserSearch, resource: "guest-data-rights", keywords: "gdpr privacy personal data erase delete forget export copy access request subject" },
      { href: "/operators",          label: "Operators",   icon: Users,      resource: "operators", keywords: "staff users roles password" },
    ],
  },
];

export const NAV_ITEMS: Item[] = SECTIONS.flatMap((s) => s.items);
export const NAV_SECTION_OF: Record<string, string> = Object.fromEntries(
  SECTIONS.flatMap((s) => s.items.map((i) => [i.href, s.title])),
);

/** Match the longest route at a path boundary so nested pages highlight only their own item. */
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
  onNavigate?: () => void;
  /** Keep labels visible in the mobile drawer; icons alone cannot identify every destination. */
  collapsed?: boolean;
  onToggleCollapsed?: () => void;
}) {
  const path = usePathname();
  const [query, setQuery] = useState("");
  const searchRef = useRef<HTMLInputElement | null>(null);
  // Defer filter focus until expansion mounts the input.
  const focusFilterAfterExpand = useRef(false);

  const activeHref = useMemo(() => activeNavHref(path), [path]);
  const activeSection = activeHref ? NAV_SECTION_OF[activeHref] : undefined;
  const [openSections, setOpenSections] = useState<Set<string>>(
    () => new Set(activeSection ? [activeSection] : []),
  );
  useEffect(() => {
    setOpenSections(new Set(activeSection ? [activeSection] : []));
  }, [activeSection]);
  const toggleSection = (title: string) =>
    setOpenSections((prev) => {
      const next = new Set(prev);
      if (next.has(title)) next.delete(title);
      else next.add(title);
      return next;
    });
  const filtering = query.trim().length > 0;
  // Prefix heading IDs because desktop and mobile Nav instances coexist.
  const navId = useId();
  const sectionId = (title: string) => `${navId}-section-${title.toLowerCase().replace(/[^a-z0-9]+/g, "-")}`;

  const caps = useCapabilities();

  const visibleSections = useMemo(() => {
    const q = query.trim().toLowerCase();
    // Keep historical Hotel records reachable through Modules when Hospitality is unavailable.
    const hotel = moduleLicensed(caps, "hospitality");
    return SECTIONS.map((sec) => ({
      title: sec.title,
      items: sec.items.filter((it) => {
        if (sec.title === "Hotel" && !hotel) return false;
        if (!canRead(it.resource, roles)) return false;
        if (!surfaceAvailable(caps, it.capability ?? it.resource)) return false;
        if (it.modules && !it.modules.some((m) => moduleLicensed(caps, m))) return false;
        if (!q) return true;
        return (
          it.label.toLowerCase().includes(q) ||
          sec.title.toLowerCase().includes(q) ||
          (it.keywords ?? "").includes(q)
        );
      }),
    })).filter((sec) => sec.items.length > 0);
  }, [query, roles, caps]);

  // Expand the rail before focusing its filter input.
  const openFilter = () => {
    if (collapsed && onToggleCollapsed) {
      focusFilterAfterExpand.current = true;
      onToggleCollapsed();
      return;
    }
    searchRef.current?.focus();
  };
  const openFilterRef = useRef(openFilter);
  useEffect(() => {
    openFilterRef.current = openFilter;
  });

  useEffect(() => {
    if (!collapsed && focusFilterAfterExpand.current) {
      focusFilterAfterExpand.current = false;
      searchRef.current?.focus();
    }
  }, [collapsed]);

  // Use a modifier shortcut so speech input cannot trigger navigation by typing a slash.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key.toLowerCase() !== "k" || !(e.metaKey || e.ctrlKey) || e.altKey || e.shiftKey) return;
      e.preventDefault();
      openFilterRef.current();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const roleLabel = roles
    .map((r) => ROLE_LABELS[r as SiteRole])
    .filter(Boolean)
    .join(", ");

  return (
    <aside
      data-collapsed={collapsed ? "true" : undefined}
      className={cn(
        "sidebar-motion flex h-full shrink-0 flex-col border-r border-sidebar-border bg-sidebar text-sidebar-foreground",
        onToggleCollapsed
          ? "w-[var(--sidebar-width)] transition-[width] duration-base ease-out motion-reduce:transition-none"
          : "w-64",
      )}
    >
      <div
        className={cn(
          "flex min-h-[var(--topbar-height)] items-center gap-2.5 border-b border-sidebar-border py-2.5",
          collapsed ? "flex-col gap-2 px-2" : "px-4",
        )}
      >
        <OneGateLockup product="Admin Console" collapsed={collapsed} inverse className={collapsed ? undefined : "flex-1"} />
        {onToggleCollapsed && (
          // Name the action; aria-expanded reports the current state separately.
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
                "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-active",
              )}
            >
              {collapsed ? <PanelLeftOpen className="size-4" /> : <PanelLeftClose className="size-4" />}
            </button>
          </Tooltip>
        )}
      </div>

      <div className={cn("border-b border-sidebar-border py-2.5", collapsed ? "px-2" : "px-3")}>
        {collapsed ? (
          // Keep the filter reachable in the collapsed rail.
          <Tooltip content="Find a screen" side="right">
            <button
              type="button"
              onClick={openFilter}
              aria-label="Find a screen"
              className={cn(
                "flex size-10 w-full items-center justify-center rounded-md text-sidebar-muted",
                "transition-colors hover:bg-sidebar-accent/60 hover:text-white",
                "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-active",
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
              aria-keyshortcuts="Control+K Meta+K"
              title="Find a screen (Ctrl+K)"
              className={cn(
                "h-9 w-full rounded-md border border-sidebar-border bg-sidebar-accent/70 pl-8 pr-8 text-[0.8125rem] pointer-coarse:pr-11",
                "text-sidebar-foreground placeholder:text-sidebar-muted",
                "focus:border-sidebar-active focus:outline-none focus:ring-2 focus:ring-sidebar-active/30",
              )}
            />
            {query && (
              <button
                type="button"
                onClick={() => setQuery("")}
                aria-label="Clear filter"
                className={cn(
                  "absolute right-1 top-1/2 inline-flex size-6 -translate-y-1/2 items-center justify-center rounded",
                  "text-sidebar-muted hover:text-white pointer-coarse:right-0 pointer-coarse:size-11",
                  "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-active/60",
                )}
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
          visibleSections.map((sec) => {
            const open = collapsed || filtering || openSections.has(sec.title);
            const listId = `${sectionId(sec.title)}-list`;
            return (
            <div key={sec.title} className={cn(collapsed ? "mb-3 last:mb-0" : "mb-0.5")}>
              {collapsed ? (
                // Keep hidden heading text for assistive technology when the rail collapses.
                <>
                  <div className="mx-2 mb-1.5 mt-1 border-t border-sidebar-border/70 first:mt-0 first:border-t-0" aria-hidden />
                  <h2 id={sectionId(sec.title)} className="sr-only">{sec.title}</h2>
                </>
              ) : (
                <h2 id={sectionId(sec.title)}>
                  <button
                    type="button"
                    onClick={() => toggleSection(sec.title)}
                    disabled={filtering}
                    aria-expanded={open}
                    aria-controls={listId}
                    className={cn(
                      "group flex min-h-8 w-full items-center gap-2 rounded-md px-2.5 py-1.5 text-left pointer-coarse:min-h-11",
                      "text-nano font-normal uppercase tracking-[0.12em] text-sidebar-muted transition-colors",
                      "hover:text-sidebar-foreground disabled:cursor-default disabled:hover:text-sidebar-muted",
                      "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-active",
                    )}
                  >
                    <span className="flex-1 truncate">{sec.title}</span>
                    {!filtering && (
                      <ChevronRight
                        className={cn(
                          "size-3.5 shrink-0 transition-transform duration-base ease-onegate motion-reduce:transition-none",
                          open ? "rotate-90" : "rtl:rotate-180",
                        )}
                        aria-hidden
                      />
                    )}
                  </button>
                </h2>
              )}
              <ul id={listId} hidden={!open} className={cn("space-y-0.5", !collapsed && "pb-2")} aria-labelledby={sectionId(sec.title)}>
                {sec.items.map((it) => {
                  const active = it.href === activeHref;
                  const Icon = it.icon;
                  const link = (
                    <Link
                      href={it.href}
                      onClick={onNavigate}
                      aria-current={active ? "page" : undefined}
                      // Keep real hidden text so collapsed links retain the same accessible name.
                      className={cn(
                        "group relative flex items-center rounded-md text-[0.8125rem] transition-colors duration-press",
                        "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-active",
                        collapsed ? "h-10 justify-center px-0" : "min-h-9 gap-2.5 px-2.5 py-1.5",
                        active
                          ? "bg-sidebar-accent font-semibold text-sidebar-accent-foreground"
                          : "text-sidebar-foreground/85 hover:bg-sidebar-accent/60 hover:text-white",
                      )}
                    >
                      {/* Keep an active marker visible when labels are hidden. */}
                      <span
                        className={cn(
                          "absolute left-0 top-1/2 -translate-y-1/2 rounded-r-full bg-sidebar-active transition-opacity",
                          collapsed ? "h-6 w-[3px]" : "h-5 w-[3px]",
                          active ? "opacity-100" : "opacity-0",
                        )}
                        aria-hidden
                      />
                      <Icon
                        className={cn(
                          "size-4 shrink-0 transition-colors",
                          active ? "text-sidebar-active" : "text-sidebar-muted group-hover:text-sidebar-foreground",
                        )}
                      />
                      <span className={cn(collapsed ? "sr-only" : "truncate")}>{it.label}</span>
                    </Link>
                  );
                  return (
                    <li key={it.href}>
                      {collapsed ? (
                        // Tooltips must open on keyboard focus as well as hover.
                        <Tooltip content={it.label} side="right">{link}</Tooltip>
                      ) : (
                        link
                      )}
                    </li>
                  );
                })}
              </ul>
            </div>
            );
          })
        )}
      </nav>

      {/* Keep identity and sign-out controls visible in the collapsed rail. */}
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
            {/* Keep the status tooltip focusable without presenting an inactive button. */}
            <div
              // Focusable tooltip triggers must remain reachable without a mouse.
              // eslint-disable-next-line jsx-a11y/no-noninteractive-tabindex
              tabIndex={0}
              role="img"
              aria-label={`Signed in as ${email ?? "unknown"}${roleLabel ? `, ${roleLabel}` : ""}`}
              className={cn(
                "mx-auto flex size-8 items-center justify-center rounded-full bg-sidebar-accent",
                "text-2xs font-semibold uppercase text-white",
                "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-active",
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
              {/* Name the active role so denied controls have context. */}
              <div className="truncate text-2xs text-sidebar-muted" title={roleLabel}>
                {roleLabel || "No site role"}
              </div>
            </div>
          </div>
        )}

        {(() => {
          const change = (
            <Link
              href="/account/password"
              onClick={onNavigate}
              aria-label={collapsed ? "Change my password" : undefined}
              className={cn(
                "mt-1 flex w-full items-center rounded-md text-sm text-sidebar-foreground/85",
                "transition-colors hover:bg-sidebar-accent/60 hover:text-white",
                "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-active",
                collapsed ? "h-10 justify-center px-0" : "gap-2.5 px-2.5 py-1.5",
              )}
            >
              <KeyRound className="size-4 shrink-0 text-sidebar-muted" />
              <span className={cn(collapsed ? "sr-only" : undefined)}>Change my password</span>
            </Link>
          );
          return collapsed ? <Tooltip content="Change my password" side="right">{change}</Tooltip> : change;
        })()}

        {(() => {
          const twoStep = (
            <Link
              href="/account/security"
              onClick={onNavigate}
              aria-label={collapsed ? "Two-step sign-in" : undefined}
              className={cn(
                "mt-1 flex w-full items-center rounded-md text-sm text-sidebar-foreground/85",
                "transition-colors hover:bg-sidebar-accent/60 hover:text-white",
                "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-active",
                collapsed ? "h-10 justify-center px-0" : "gap-2.5 px-2.5 py-1.5",
              )}
            >
              <Smartphone className="size-4 shrink-0 text-sidebar-muted" />
              <span className={cn(collapsed ? "sr-only" : undefined)}>Two-step sign-in</span>
            </Link>
          );
          return collapsed ? <Tooltip content="Two-step sign-in" side="right">{twoStep}</Tooltip> : twoStep;
        })()}

        {(() => {
          const signOut = (
            <button
              type="button"
              onClick={onLogout}
              aria-label={collapsed ? "Sign out" : undefined}
              className={cn(
                "mt-1 flex w-full items-center rounded-md text-sm text-sidebar-foreground/85",
                "transition-colors hover:bg-sidebar-accent/60 hover:text-white",
                "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-active",
                collapsed ? "h-10 justify-center px-0" : "gap-2.5 px-2.5 py-1.5",
              )}
            >
              <LogOut className="size-4 shrink-0 text-sidebar-muted" />
              <span className={cn(collapsed ? "sr-only" : undefined)}>Sign out</span>
            </button>
          );
          return collapsed ? <Tooltip content="Sign out" side="right">{signOut}</Tooltip> : signOut;
        })()}

        {!collapsed && (
          <div className="mt-2 border-t border-sidebar-border/70 px-2.5 pt-2">
            <BySemantics className="text-2xs text-sidebar-muted" />
          </div>
        )}
      </div>

    </aside>
  );
}
