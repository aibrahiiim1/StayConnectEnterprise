"use client";
import Link from "next/link";
import { useMemo } from "react";
import { usePathname } from "next/navigation";
import { cn } from "@/lib/utils";
import {
  BadgeCheck, Building2, HardDrive, KeyRound, LayoutDashboard, LogOut, PanelLeftClose, PanelLeftOpen,
  ScrollText, Server, Settings2, ShieldAlert, Users,
} from "lucide-react";
import { BySemantics, OneGateLockup } from "@/components/brand";
import { Tooltip } from "@/components/ui/tooltip";
import { usePermissions, type Capability } from "@/lib/permissions";

export type NavItem = {
  href: string;
  label: string;
  icon: React.ComponentType<{ className?: string }>;
  /** The capability needed to see this item at all. */
  needs: Capability;
};

// CENTRAL'S FIVE DESTINATIONS (docs/CENTRAL_CONTROL_PLANE.md §2). One place per job: there is no customer
// selector, no second appliance list, and no separate onboarding screen — activation happens on the appliance.
export const NAV_ITEMS: NavItem[] = [
  { href: "/overview", label: "Overview", icon: LayoutDashboard, needs: "fleet.read" },
  { href: "/customers", label: "Customers", icon: Building2, needs: "fleet.read" },
  { href: "/appliances", label: "Appliances", icon: Server, needs: "fleet.read" },
  { href: "/licenses", label: "Licenses", icon: BadgeCheck, needs: "fleet.read" },
  { href: "/system", label: "System", icon: Settings2, needs: "system.read" },
];

/** The System section's pages: out of the daily workflow, platform operators only. */
export const SYSTEM_PAGES: { href: string; label: string; icon: React.ComponentType<{ className?: string }> }[] = [
  { href: "/system/security-alerts", label: "Security alerts", icon: ShieldAlert },
  { href: "/system/trust", label: "Trust & keys", icon: KeyRound },
  { href: "/system/audit", label: "Audit log", icon: ScrollText },
  { href: "/system/team", label: "Team", icon: Users },
  { href: "/system/backup-health", label: "Backup health", icon: HardDrive },
];

/** The nav item this path is (or is under). */
export function activeNavHref(path: string): string | null {
  const matches = NAV_ITEMS.map((it) => it.href).filter((href) => path === href || path.startsWith(href + "/"));
  return matches.sort((a, b) => b.length - a.length)[0] ?? null;
}

/** The top bar's "Section / Page". A detail page names its parent list as a link back to it. */
const DETAIL_LABEL: Record<string, string> = { "/customers": "Customer", "/appliances": "Appliance" };

export function crumbs(pathname: string): { parent?: { href: string; label: string }; label: string } {
  const sys = SYSTEM_PAGES.find((p) => pathname === p.href || pathname.startsWith(p.href + "/"));
  if (sys) return { parent: { href: "/system", label: "System" }, label: sys.label };
  const href = activeNavHref(pathname);
  const item = NAV_ITEMS.find((i) => i.href === href);
  if (!item) return { label: "Central" };
  if (pathname === item.href) return { label: item.label };
  return { parent: { href: item.href, label: item.label }, label: DETAIL_LABEL[item.href] ?? item.label };
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
  const { can, subject } = usePermissions();
  const items = useMemo(
    () =>
      NAV_ITEMS.filter((it) => can[it.needs]).map((it) =>
        // A customer's own user has one customer, so the menu says so.
        it.href === "/customers" && !can["customers.list"] ? { ...it, label: "Customer" } : it,
      ),
    [can],
  );

  return (
    <aside
      data-collapsed={collapsed ? "true" : undefined}
      className={cn(
        "sidebar-motion flex h-full shrink-0 flex-col border-e border-sidebar-border bg-sidebar text-sidebar-foreground",
        onToggleCollapsed ? "w-[var(--sidebar-width)] transition-[width] duration-base ease-onegate motion-reduce:transition-none" : "w-64",
      )}
    >
      <div
        className={cn(
          "flex min-h-[var(--topbar-height)] items-center gap-2.5 border-b border-sidebar-border py-2.5",
          collapsed ? "flex-col gap-2 px-2" : "px-4",
        )}
      >
        <OneGateLockup product="Central" collapsed={collapsed} inverse className={collapsed ? undefined : "flex-1"} />
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

      <nav id="sidebar-nav" className="nav-scroll flex-1 overflow-y-auto px-2 py-3" aria-label="Main">
        <ul className="space-y-0.5">
          {items.map((it) => {
            const active = it.href === activeHref;
            const Icon = it.icon;
            const link = (
              <Link
                href={it.href}
                onClick={onNavigate}
                aria-current={active ? "page" : undefined}
                className={cn(
                  "group relative flex items-center rounded-md text-label font-normal transition-colors duration-press",
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
              <div className="truncate text-2xs text-sidebar-muted">
                {subject.customerName ? subject.customerName : "Signed in to Central"}
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
        {!collapsed && (
          <div className="px-2.5 pb-0.5 pt-2">
            <BySemantics className="text-sidebar-muted" />
          </div>
        )}
      </div>
    </aside>
  );
}
