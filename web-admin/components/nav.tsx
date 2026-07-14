"use client";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { cn } from "@/lib/utils";
import {
  LayoutDashboard, MapPin, Users, LogOut,
  Server, FileText, CreditCard, ScrollText, Send,
  Building2, BadgeCheck, Activity, Boxes, ShieldCheck, LifeBuoy,
} from "lucide-react";

type Item = { href: string; label: string; icon: any };

// Platform Console — StayConnect vendor/company scope. Cross-tenant, no personal
// subscription. Uses the /cloud/v1 platform surface.
const PLATFORM_ITEMS: Item[] = [
  { href: "/dashboard",     label: "Platform Dashboard", icon: LayoutDashboard },
  { href: "/customers",     label: "Customers",          icon: Building2 },
  { href: "/sites",         label: "Sites",              icon: MapPin },
  { href: "/appliances",    label: "Appliances",         icon: Server },
  { href: "/enrollment",    label: "Enrollment",         icon: ShieldCheck },
  { href: "/plan-catalog",  label: "Plan Catalog",       icon: Boxes },
  { href: "/licenses",      label: "Licenses",           icon: BadgeCheck },
  { href: "/fleet-health",  label: "Fleet Health",       icon: Activity },
  { href: "/operators",     label: "Platform Operators", icon: Users },
  { href: "/audit",         label: "Platform Audit",     icon: ScrollText },
];

// Tenant / Hotel-Group Portal — one customer's scope. Read-only subscription.
const TENANT_ITEMS: Item[] = [
  { href: "/dashboard",        label: "Group Dashboard",  icon: LayoutDashboard },
  { href: "/sites",            label: "Sites",            icon: MapPin },
  { href: "/appliances",       label: "Appliances",       icon: Server },
  { href: "/my-appliances",    label: "My Appliances",    icon: LifeBuoy },
  { href: "/ticket-templates", label: "Group templates",  icon: FileText },
  { href: "/notifications",    label: "Notifications",    icon: Send },
  { href: "/subscription",     label: "My Subscription",  icon: CreditCard },
  { href: "/operators",        label: "Group operators",  icon: Users },
  { href: "/audit",            label: "Group audit",      icon: ScrollText },
];

export function Nav({ onLogout, email, platform }: { onLogout: () => void; email?: string; platform: boolean }) {
  const path = usePathname();
  const items = platform ? PLATFORM_ITEMS : TENANT_ITEMS;
  return (
    <aside className="w-56 shrink-0 border-r border-border bg-panel flex flex-col">
      <div className="px-5 py-5 border-b border-border">
        <div className="text-xs text-muted uppercase tracking-widest">StayConnect{platform ? " Platform" : ""}</div>
        <div className="text-sm font-semibold">{platform ? "Platform Administration" : "Group Administration"}</div>
      </div>
      <nav className="flex-1 p-2 text-sm">
        {items.map((it) => {
          const active = it.href === "/dashboard" ? path === it.href : path.startsWith(it.href);
          const Icon = it.icon;
          return (
            <Link
              key={it.href}
              href={it.href}
              className={cn(
                "flex items-center gap-2 px-3 py-2 rounded-md transition-colors",
                active ? "bg-panel2 text-text" : "text-muted hover:text-text hover:bg-panel2"
              )}
            >
              <Icon size={16} />
              <span>{it.label}</span>
            </Link>
          );
        })}
      </nav>
      <div className="p-3 border-t border-border text-xs">
        <div className="flex items-center gap-2 text-muted mb-2 px-2">
          <Users size={14} />
          <span className="truncate" title={email}>{email ?? "—"}</span>
        </div>
        <button
          onClick={onLogout}
          className="flex items-center gap-2 w-full px-2 py-2 text-muted hover:text-text rounded-md hover:bg-panel2"
        >
          <LogOut size={14} /> Sign out
        </button>
      </div>
    </aside>
  );
}
