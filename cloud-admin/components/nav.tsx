"use client";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { cn } from "@/lib/utils";
import {
  LayoutDashboard, MapPin, Server, Radar, Users, LogOut,
  CreditCard, BadgeCheck, ScrollText, Building2, PlugZap, SlidersHorizontal,
} from "lucide-react";

type Item = { href: string; label: string; icon: any };
type Section = { title: string; items: Item[] };

const SECTIONS: Section[] = [
  {
    title: "Overview",
    items: [
      { href: "/dashboard", label: "Dashboard", icon: LayoutDashboard },
    ],
  },
  {
    title: "Infrastructure",
    items: [
      { href: "/sites",      label: "Sites",      icon: MapPin },
      { href: "/appliances", label: "Appliances", icon: Server },
      { href: "/onboarding", label: "Onboarding", icon: PlugZap },
      { href: "/fleet",      label: "Fleet",      icon: Radar },
    ],
  },
  {
    title: "Commercial",
    items: [
      { href: "/tenants",      label: "Customers",       icon: Building2 },
      { href: "/commercial",   label: "Plans & limits",  icon: SlidersHorizontal },
      { href: "/subscription", label: "Commercial plan", icon: CreditCard },
      { href: "/licenses",     label: "Licenses",        icon: BadgeCheck },
    ],
  },
  {
    title: "Administration",
    items: [
      { href: "/operators", label: "Operators", icon: Users },
      { href: "/audit",     label: "Audit log", icon: ScrollText },
    ],
  },
];

export function Nav({ onLogout, email }: { onLogout: () => void; email?: string }) {
  const path = usePathname();
  return (
    <aside className="w-56 shrink-0 border-r border-border bg-panel flex flex-col">
      <div className="px-5 py-5 border-b border-border">
        <div className="text-xs text-muted uppercase tracking-widest">StayConnect</div>
        <div className="text-sm font-semibold">Cloud Admin</div>
      </div>
      <nav className="flex-1 p-2 text-sm overflow-y-auto">
        {SECTIONS.map((sec) => (
          <div key={sec.title} className="mb-2">
            <div className="px-3 pt-2 pb-1 text-[10px] uppercase tracking-widest text-muted">
              {sec.title}
            </div>
            {sec.items.map((it) => {
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
        ))}
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
