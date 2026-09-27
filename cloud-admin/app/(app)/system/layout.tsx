"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { Lock } from "lucide-react";
import { SYSTEM_PAGES } from "@/components/nav";
import { NotAvailable } from "@/components/ui/patterns";
import { usePermissions } from "@/lib/permissions";
import { cn } from "@/lib/utils";

/**
 * SYSTEM: the platform's own housekeeping, out of the daily workflow. One menu entry in the sidebar; its pages
 * are this secondary bar. Platform operators only (a customer's user never reaches it from the menu, and gets
 * this notice when they arrive by address).
 */
export default function SystemLayout({ children }: { children: React.ReactNode }) {
  const path = usePathname() ?? "";
  const { can } = usePermissions();

  if (!can["system.read"]) {
    return (
      <div className="mx-auto max-w-7xl">
        <NotAvailable icon={<Lock />} title="Not available to your role" reason="The System pages are for the platform's own operators." />
      </div>
    );
  }

  return (
    <div className="space-y-5">
      <nav aria-label="System" className="mx-auto max-w-[96rem]">
        <ul className="flex gap-1 overflow-x-auto border-b border-border">
          {SYSTEM_PAGES.map((p) => {
            const active = path === p.href || path.startsWith(p.href + "/");
            const Icon = p.icon;
            return (
              <li key={p.href} className="shrink-0">
                <Link
                  href={p.href}
                  aria-current={active ? "page" : undefined}
                  className={cn(
                    "-mb-px inline-flex h-10 items-center gap-2 border-b-2 px-3 text-sm font-medium transition-colors",
                    "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
                    active
                      ? "border-primary text-foreground"
                      : "border-transparent text-muted-foreground hover:border-border-strong hover:text-foreground",
                  )}
                >
                  <Icon className="size-4" aria-hidden />
                  {p.label}
                </Link>
              </li>
            );
          })}
        </ul>
      </nav>
      {children}
    </div>
  );
}
