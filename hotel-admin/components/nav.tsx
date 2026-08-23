"use client";
import Link from "next/link";
import { useMemo } from "react";
import { usePathname } from "next/navigation";
import { cn } from "@/lib/utils";
import { canRead } from "@/lib/roles";
import {
  LayoutDashboard, Users, LogOut, Monitor, Shield, ScrollText, Hotel, Send, KeyRound,
  Wallet, BadgeCheck, Paintbrush, Archive, Network, Wifi, History, Router, Cloud,
  ServerCog, Lock, Activity, Package, Gauge, Smartphone, LogIn,
} from "lucide-react";

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
type Item = { href: string; label: string; icon: any; resource: string; enabled?: boolean };
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
      { href: "/dashboard", label: "Dashboard", icon: LayoutDashboard, resource: "reports" },
    ],
  },
  {
    // WHAT A GUEST CAN BE GIVEN, AND THE SERVICE BEHIND IT. These are one job in two steps — a Service plan
    // defines the technical service (speed, devices, duration/quota); an Internet package is the guest-facing
    // offer that uses it — so they sit adjacent instead of being buried in one tabbed screen.
    title: "Internet offering",
    items: [
      { href: "/internet-packages", label: "Internet packages", icon: Package, resource: "commercial-packages", enabled: CAP_INTERNET_OFFERING },
      { href: "/service-plans",     label: "Service plans",     icon: Gauge,   resource: "commercial-packages", enabled: CAP_INTERNET_OFFERING },
      { href: "/checkout-grace",    label: "Checkout grace",    icon: Shield,  resource: "checkout-grace",      enabled: CAP_PMS },
    ],
  },
  {
    title: "Guests",
    items: [
      { href: "/stays",          label: "Stays",           icon: Hotel,    resource: "pms-stays", enabled: CAP_PMS },
      { href: "/guest-accounts", label: "Guest accounts",  icon: KeyRound, resource: "guest-accounts" },
      { href: "/sessions",       label: "Active sessions", icon: Monitor,  resource: "sessions" },
      { href: "/guest-device-self-service", label: "Guest devices", icon: Smartphone, resource: "guest-device-self-service", enabled: CAP_GUEST_DEVICES },
      { href: "/online-time",    label: "Online-time budgets", icon: Activity, resource: "sessions", enabled: CAP_GUEST_DEVICES },
      { href: "/post-stay",      label: "Post-stay access", icon: KeyRound, resource: "post-stay-profiles", enabled: CAP_POST_STAY },
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
      { href: "/pms-interfaces",       label: "PMS connection",       icon: Hotel,  resource: "pms-interfaces",       enabled: CAP_PMS },
      { href: "/pms-routing",          label: "Network routing",      icon: Router, resource: "pms-routing",          enabled: CAP_PMS },
      { href: "/stay-events",          label: "PMS activity",         icon: Send,   resource: "pms-events",           enabled: CAP_PMS },
      { href: "/pms-resolutions",      label: "Guest sign-in checks", icon: Send,   resource: "pms-resolutions",      enabled: CAP_PMS },
      { href: "/pms-source-conflicts", label: "Duplicate sources",    icon: Shield, resource: "pms-source-conflicts", enabled: CAP_PMS },
      { href: "/stay-transfers",       label: "Cross-PMS transfer",   icon: Send,   resource: "stay-transfers",       enabled: CAP_POST_STAY },
    ],
  },
  {
    title: "Charges",
    items: [
      { href: "/financial-health",      label: "Charge health", icon: Wallet, resource: "financial-review", enabled: CAP_CHARGES },
      { href: "/financial-review",      label: "Manual review", icon: Shield, resource: "financial-review", enabled: CAP_CHARGES },
      { href: "/financial-settlements", label: "Settlements",   icon: Wallet, resource: "financial-review", enabled: CAP_CHARGES },
      { href: "/financial-recovery",    label: "Recovery",      icon: Shield, resource: "financial-review", enabled: CAP_CHARGES },
    ],
  },
  {
    title: "Guest portal",
    items: [
      // Sign-in methods leads the group: which ways a guest may prove who they are is the first thing an
      // operator sets up on the portal, and it was previously not settable anywhere in the product.
      { href: "/sign-in-methods",  label: "Sign-in methods", icon: LogIn,    resource: "auth-methods" },
      { href: "/portal-branding",  label: "Branding",        icon: Paintbrush, resource: "portal-branding" },
      { href: "/walled-garden",    label: "Allowed sites", icon: Shield,     resource: "walled-garden" },
      { href: "/social-providers", label: "Social login",  icon: KeyRound,   resource: "social-providers" },
      { href: "/notifications",    label: "Email & SMS",   icon: Send,       resource: "notification-providers" },
    ],
  },
  {
    title: "Networking",
    items: [
      { href: "/network",             label: "Guest networks",     icon: Network,   resource: "network" },
      { href: "/network/dhcp",        label: "DHCP & leases",      icon: Wifi,      resource: "network" },
      { href: "/network/system",      label: "WAN / LAN settings", icon: Router,    resource: "network" },
      { href: "/network/revisions",   label: "Config history",     icon: History,   resource: "network" },
      { href: "/network/cloud",       label: "Cloud connection",   icon: Cloud,     resource: "network" },
      { href: "/network/certificate", label: "TLS certificate",    icon: Lock,      resource: "network" },
      { href: "/setup/enrollment",    label: "Setup / Activation", icon: ServerCog, resource: "network" },
    ],
  },
  {
    title: "System",
    items: [
      { href: "/health",             label: "Diagnostics", icon: Activity,   resource: "diagnostics" },
      { href: "/operational-alerts", label: "Alerts",      icon: Shield,     resource: "operational-alerts", enabled: CAP_PMS },
      { href: "/operators",          label: "Operators",   icon: Users,      resource: "operators" },
      { href: "/license",            label: "License",     icon: BadgeCheck, resource: "license" },
      { href: "/backups",            label: "Backups",     icon: Archive,    resource: "backups" },
      { href: "/audit",              label: "Audit log",   icon: ScrollText, resource: "audit" },
    ],
  },
];

export function Nav({
  onLogout, email, roles,
}: { onLogout: () => void; email?: string; roles: string[] }) {
  const path = usePathname();

  // ACTIVE = the LONGEST href this path matches, not every href it starts with.
  //
  // `path.startsWith(it.href)` marked several items active at once, because these are siblings rather than a
  // parent and its children: "/network" is the Guest networks LEAF, so standing on "/network/dhcp" lit up both
  // "DHCP & leases" and "Guest networks". Two highlighted rows in a menu is not a cosmetic issue -- the
  // highlight is the only thing telling an operator which screen they are on.
  //
  // The "/" in the child test matters too: plain startsWith would make a hypothetical "/networkfoo" light up
  // "/network", matching on a shared prefix that is not a path boundary at all.
  const activeHref = useMemo(() => {
    const matches = SECTIONS.flatMap((sec) => sec.items.map((it) => it.href))
      .filter((href) => path === href || path.startsWith(href + "/"));
    return matches.sort((a, b) => b.length - a.length)[0] ?? null;
  }, [path]);

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
                const active = it.href === activeHref;
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
