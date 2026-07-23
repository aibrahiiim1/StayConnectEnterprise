"use client";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { cn } from "@/lib/utils";
import { canRead } from "@/lib/roles";
import {
  LayoutDashboard, Ticket, Users, LogOut, Monitor, FileText,
  Shield, ScrollText, Hotel, Send, KeyRound, Wallet, BadgeCheck,
  Paintbrush, Archive, Network, Wifi, History, Router, Cloud, ServerCog, Lock, Activity, Package,
} from "lucide-react";

// Phase 2 (DARK) commercial packages: the admin surface is off by default and the nav item stays hidden
// unless the deployment sets NEXT_PUBLIC_PHASE2_ADMIN=1 (mirrors the edged STAYCONNECT_PHASE2_* flags).
// Even when shown, the edged routes are the authority — they are absent unless the backend flag is on.
const PHASE2_ADMIN = process.env.NEXT_PUBLIC_PHASE2_ADMIN === "1";

// Phase 3 (DARK) PMS stay resolution + checkout grace: same rule as Phase 2 — the nav items stay hidden
// unless the deployment sets NEXT_PUBLIC_PHASE3_ADMIN=1 (mirroring the edged STAYCONNECT_PHASE3_* flags),
// and even then edged is the authority: its routes do not exist unless the backend flags are on.
const PHASE3_ADMIN = process.env.NEXT_PUBLIC_PHASE3_ADMIN === "1";

// Each item names the edged resource that gates its visibility. Items the
// operator's roles cannot read are hidden (edged still enforces server-side).
// `enabled: false` hides an item behind a dark feature flag regardless of role.
type Item = { href: string; label: string; icon: any; resource: string; enabled?: boolean };
type Section = { title: string; items: Item[] };

const SECTIONS: Section[] = [
  {
    title: "Overview",
    items: [
      { href: "/dashboard", label: "Dashboard", icon: LayoutDashboard, resource: "reports" },
    ],
  },
  {
    title: "Access",
    items: [
      { href: "/guest-access-plans", label: "Guest access plans", icon: FileText, resource: "guest-access-plans" },
      { href: "/voucher-batches",    label: "Voucher batches",    icon: Ticket,   resource: "voucher-batches" },
      { href: "/guest-accounts",     label: "Guest accounts",     icon: KeyRound, resource: "guest-accounts" },
      { href: "/commercial-packages", label: "Commercial packages", icon: Package, resource: "commercial-packages", enabled: PHASE2_ADMIN },
      { href: "/sessions",           label: "Sessions",           icon: Monitor,  resource: "sessions" },
    ],
  },
  {
    title: "Integrations",
    items: [
      { href: "/pms-providers",    label: "PMS providers", icon: Hotel,    resource: "pms-providers" },
      // The interface itself comes before what it produces: when guests cannot get online, "is the PMS
      // connected and what is it running" is the question, and the stays are the symptom.
      { href: "/pms-interfaces",     label: "PMS interfaces",     icon: Hotel,   resource: "pms-interfaces",     enabled: PHASE3_ADMIN },
      { href: "/pms-routing",        label: "Network routing",    icon: Router,  resource: "pms-routing",        enabled: PHASE3_ADMIN },
      { href: "/pms-source-conflicts", label: "Source conflicts", icon: Shield,  resource: "pms-source-conflicts", enabled: PHASE3_ADMIN },
      { href: "/pms-resolutions",    label: "Resolution evidence", icon: Send,   resource: "pms-resolutions",    enabled: PHASE3_ADMIN },
      { href: "/stays",             label: "Stays",             icon: Hotel,   resource: "pms-stays",          enabled: PHASE3_ADMIN },
      { href: "/stay-events",       label: "Stay events",       icon: Send,    resource: "pms-events",         enabled: PHASE3_ADMIN },
      { href: "/checkout-grace",    label: "Checkout grace",    icon: Shield,  resource: "checkout-grace",     enabled: PHASE3_ADMIN },
      { href: "/operational-alerts", label: "Operational alerts", icon: Shield, resource: "operational-alerts", enabled: PHASE3_ADMIN },
      { href: "/notifications",    label: "Notifications", icon: Send,     resource: "notification-providers" },
      { href: "/social-providers", label: "Social login",  icon: KeyRound, resource: "social-providers" },
      { href: "/payments",         label: "Payments",      icon: Wallet,   resource: "payments" },
    ],
  },
  {
    title: "Site",
    items: [
      { href: "/walled-garden",   label: "Walled garden",   icon: Shield,     resource: "walled-garden" },
      { href: "/portal-branding", label: "Portal branding", icon: Paintbrush, resource: "portal-branding" },
      { href: "/operators",       label: "Operators",       icon: Users,      resource: "operators" },
    ],
  },
  {
    title: "Networking",
    items: [
      { href: "/network/system",    label: "WAN / LAN settings", icon: Router, resource: "network" },
      { href: "/network/cloud",     label: "Cloud connection", icon: Cloud, resource: "network" },
      { href: "/network/certificate", label: "TLS certificate", icon: Lock, resource: "network" },
      { href: "/setup/enrollment",  label: "Setup / Activation", icon: ServerCog, resource: "network" },
      { href: "/network",           label: "Guest networks", icon: Network, resource: "network" },
      { href: "/network/dhcp",      label: "DHCP & leases",  icon: Wifi,    resource: "network" },
      { href: "/network/revisions", label: "Config history", icon: History, resource: "network" },
    ],
  },
  {
    title: "System",
    items: [
      { href: "/health",  label: "Diagnostics", icon: Activity, resource: "diagnostics" },
      { href: "/license", label: "License",   icon: BadgeCheck, resource: "license" },
      { href: "/backups", label: "Backups",   icon: Archive,    resource: "backups" },
      { href: "/audit",   label: "Audit log", icon: ScrollText, resource: "audit" },
    ],
  },
];

export function Nav({
  onLogout, email, roles,
}: { onLogout: () => void; email?: string; roles: string[] }) {
  const path = usePathname();
  return (
    <aside className="w-56 shrink-0 border-r border-border bg-panel flex flex-col">
      <div className="px-5 py-5 border-b border-border">
        <div className="text-xs text-muted uppercase tracking-widest">StayConnect</div>
        <div className="text-sm font-semibold">Hotel Admin</div>
      </div>
      <nav className="flex-1 p-2 text-sm overflow-y-auto">
        {SECTIONS.map((sec) => {
          const visible = sec.items.filter((it) => it.enabled !== false && canRead(it.resource, roles));
          if (visible.length === 0) return null;
          return (
            <div key={sec.title} className="mb-2">
              <div className="px-3 pt-2 pb-1 text-[10px] uppercase tracking-widest text-muted">
                {sec.title}
              </div>
              {visible.map((it) => {
                const active = path.startsWith(it.href);
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
            </div>
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
