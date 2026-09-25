"use client";
import Link from "next/link";
import { useMemo } from "react";
import { usePathname } from "next/navigation";
import { cn } from "@/lib/utils";
import {
  LayoutDashboard, MapPin, Server, Users, LogOut,
  BadgeCheck, ScrollText, Building2, PlugZap,
  ShieldAlert, FileBadge, KeyRound, HardDrive,
  PanelLeftClose, PanelLeftOpen,
} from "lucide-react";
import { VelonetLockup } from "@/components/brand";
import { CustomerSelector } from "@/components/customer-selector";
import { Tooltip } from "@/components/ui/tooltip";
import { PAGE_READ, usePermissions } from "@/lib/permissions";

export type NavItem = { href: string; label: string; icon: React.ComponentType<{ className?: string }> };
export type NavSection = { title: string; items: NavItem[] };

// CENTRAL'S FOUR GROUPS (handoff §7, "App shell"). The page title of every screen equals its menu label.
//
// "Fleet" is gone: its page was deleted, and a menu item that opens a 404 is worse than no item. The retired
// /commercial and /subscription pages stay reachable by URL for history but are deliberately NOT listed —
// plans and subscriptions are not part of the product.
export const NAV_SECTIONS: NavSection[] = [
  {
    title: "Overview",
    items: [{ href: "/dashboard", label: "Dashboard", icon: LayoutDashboard }],
  },
  {
    title: "Infrastructure",
    items: [
      { href: "/sites", label: "Sites", icon: MapPin },
      { href: "/onboarding", label: "Onboarding", icon: PlugZap },
      { href: "/appliances", label: "Appliances", icon: Server },
    ],
  },
  {
    title: "Commercial",
    items: [
      { href: "/tenants", label: "Customers", icon: Building2 },
      { href: "/licenses", label: "Licenses", icon: BadgeCheck },
    ],
  },
  {
    title: "Administration",
    items: [
      { href: "/operators", label: "Operators", icon: Users },
      { href: "/security", label: "Security alerts", icon: ShieldAlert },
      { href: "/certificates", label: "Certificates", icon: FileBadge },
      { href: "/assignment-keys", label: "Assignment keys", icon: KeyRound },
      { href: "/backup-health", label: "Backup health", icon: HardDrive },
      { href: "/audit", label: "Audit log", icon: ScrollText },
    ],
  },
];

export const NAV_ITEMS: NavItem[] = NAV_SECTIONS.flatMap((s) => s.items);
export const NAV_SECTION_OF: Record<string, string> = Object.fromEntries(
  NAV_SECTIONS.flatMap((s) => s.items.map((i) => [i.href, s.title])),
);

/** The longest nav href this path is (or is under). */
export function activeNavHref(path: string): string | null {
  const matches = NAV_ITEMS.map((it) => it.href).filter(
    (href) => path === href || path.startsWith(href + "/"),
  );
  return matches.sort((a, b) => b.length - a.length)[0] ?? null;
}

export function Nav({
  onLogout, email, onNavigate, collapsed = false, onToggleCollapsed,
}: {
  onLogout: () => void;
  email?: string;
  /** Called after a link is followed, so the mobile drawer can close itself. */
  onNavigate?: () => void;
  /** Icon rail (desktop only; the drawer never passes it). */
  collapsed?: boolean;
  onToggleCollapsed?: () => void;
}) {
  const path = usePathname() ?? "";
  const activeHref = useMemo(() => activeNavHref(path), [path]);
  // A menu item is shown only when the server lets this role read that page (lib/permissions.ts PAGE_READ).
  const { can } = usePermissions();
  const sections = useMemo(
    () =>
      NAV_SECTIONS.map((sec) => ({
        ...sec,
        items: sec.items.filter((it) => {
          const need = PAGE_READ[it.href];
          return !need || can[need];
        }),
      })).filter((sec) => sec.items.length > 0),
    [can],
  );

  return (
    <aside
      data-collapsed={collapsed ? "true" : undefined}
      className={cn(
        "sidebar-motion flex h-full shrink-0 flex-col border-e border-sidebar-border bg-sidebar text-sidebar-foreground",
        onToggleCollapsed ? "w-[var(--sidebar-width)] transition-[width] duration-200 ease-out" : "w-64",
      )}
    >
      <div
        className={cn(
          "flex min-h-[var(--topbar-height)] items-center gap-2.5 border-b border-sidebar-border py-2.5",
          collapsed ? "flex-col gap-2 px-2" : "px-4",
        )}
      >
        <VelonetLockup product="Central" collapsed={collapsed} inverse className={collapsed ? undefined : "flex-1"} />
        {onToggleCollapsed && (
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
              {collapsed ? <PanelLeftOpen className="size-4 rtl:-scale-x-100" /> : <PanelLeftClose className="size-4 rtl:-scale-x-100" />}
            </button>
          </Tooltip>
        )}
      </div>

      {/* THE CUSTOMER CONTEXT sits above the menu because it scopes what every page below it shows. */}
      <div className={cn("border-b border-sidebar-border py-2.5", collapsed ? "px-2" : "px-3")}>
        <CustomerSelector collapsed={collapsed} onExpand={onToggleCollapsed} />
      </div>

      <nav id="sidebar-nav" className="nav-scroll flex-1 overflow-y-auto px-2 py-2.5" aria-label="Main">
        {sections.map((sec) => (
          <div key={sec.title} className="mb-3 last:mb-0">
            {collapsed ? (
              <div className="mx-2 mb-1.5 mt-1 border-t border-sidebar-border/70 first:mt-0 first:border-t-0" aria-hidden />
            ) : (
              <div className="px-2.5 pb-1.5 pt-2 text-nano uppercase tracking-[0.12em] text-sidebar-muted">
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
                    className={cn(
                      "group relative flex items-center rounded-md text-[0.8125rem] transition-colors duration-press",
                      "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-active",
                      collapsed ? "h-10 justify-center px-0" : "min-h-9 gap-2.5 px-2.5 py-1.5",
                      active
                        ? "bg-sidebar-accent font-semibold text-sidebar-accent-foreground"
                        : "text-sidebar-foreground/85 hover:bg-sidebar-accent/60 hover:text-white",
                    )}
                  >
                    <span
                      className={cn(
                        "absolute start-0 top-1/2 -translate-y-1/2 rounded-e-full bg-sidebar-active transition-opacity",
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
                    {collapsed ? <Tooltip content={it.label} side="right">{link}</Tooltip> : link}
                  </li>
                );
              })}
            </ul>
          </div>
        ))}
      </nav>

      <div className={cn("border-t border-sidebar-border", collapsed ? "p-2" : "p-2.5")}>
        {collapsed ? (
          <Tooltip side="right" content={<span className="font-medium">{email ?? "—"}</span>}>
            <div
              tabIndex={0}
              role="img"
              aria-label={`Signed in as ${email ?? "unknown"}`}
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
              <div className="truncate text-2xs text-sidebar-muted">Signed in to Central</div>
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
                "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-active",
                collapsed ? "h-10 justify-center px-0" : "gap-2.5 px-2.5 py-1.5",
              )}
            >
              <LogOut className="size-4 shrink-0 text-sidebar-muted rtl:-scale-x-100" />
              <span className={cn(collapsed ? "sr-only" : undefined)}>Sign out</span>
            </button>
          );
          return collapsed ? <Tooltip content="Sign out" side="right">{signOut}</Tooltip> : signOut;
        })()}
      </div>
    </aside>
  );
}
