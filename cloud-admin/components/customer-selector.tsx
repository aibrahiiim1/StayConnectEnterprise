"use client";

import { useCustomer } from "@/lib/customer-context";
import { Building2 } from "lucide-react";
import { cn } from "@/lib/utils";
import { Tooltip } from "@/components/ui/tooltip";

/**
 * CustomerSelector — the Customer context switcher at the top of the sidebar.
 *
 * Platform admins choose "All customers" or one customer; the choice persists (lib/customer-context.tsx) and
 * scopes Dashboard, Sites, Appliances, Licenses, Operators and Audit log. Customer users are pinned to their own
 * customer and see a fixed label instead — never a selector.
 */
export function CustomerSelector({
  collapsed = false,
  onExpand,
}: {
  collapsed?: boolean;
  /** In the icon rail the control expands the sidebar, where the full selector lives. */
  onExpand?: () => void;
}) {
  const { isPlatform, tenants, selectedTenantId, setSelectedTenantId, selectedTenantName } = useCustomer();

  if (collapsed) {
    const label = `Customer: ${selectedTenantName}`;
    return (
      <Tooltip content={label} side="right">
        <button
          type="button"
          onClick={onExpand}
          aria-label={isPlatform ? `${label}. Expand the sidebar to change it` : label}
          className={cn(
            "flex size-10 w-full items-center justify-center rounded-md text-sidebar-muted",
            "transition-colors hover:bg-sidebar-accent/60 hover:text-white",
            "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-sidebar-active",
          )}
        >
          <Building2 className="size-4" />
        </button>
      </Tooltip>
    );
  }

  if (!isPlatform) {
    return (
      <div className="px-1">
        <div className="mb-1 text-nano uppercase tracking-[0.12em] text-sidebar-muted">Customer</div>
        <div className="flex items-center gap-2 text-[0.8125rem] font-medium text-sidebar-foreground">
          <Building2 className="size-4 shrink-0 text-sidebar-muted" aria-hidden />
          <span className="truncate">{selectedTenantName}</span>
        </div>
      </div>
    );
  }

  return (
    <div>
      <label
        htmlFor="customer-context"
        className="mb-1 block px-1 text-nano uppercase tracking-[0.12em] text-sidebar-muted"
      >
        Customer context
      </label>
      <div className="relative">
        <Building2
          className="pointer-events-none absolute start-2.5 top-1/2 size-4 -translate-y-1/2 text-sidebar-muted"
          aria-hidden
        />
        <select
          id="customer-context"
          value={selectedTenantId}
          onChange={(e) => setSelectedTenantId(e.target.value)}
          className={cn(
            "h-9 w-full cursor-pointer appearance-none rounded-md border border-sidebar-border bg-sidebar-accent/70 ps-8 pe-8 text-[0.8125rem]",
            "text-sidebar-foreground focus:border-sidebar-active focus:outline-none focus:ring-2 focus:ring-sidebar-active/30",
          )}
        >
          <option value="">All customers</option>
          {tenants.map((t) => (
            <option key={t.id} value={t.id}>{t.name}</option>
          ))}
        </select>
        <svg
          viewBox="0 0 24 24"
          className="pointer-events-none absolute end-2.5 top-1/2 size-4 -translate-y-1/2 text-sidebar-muted"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
          strokeLinecap="round"
          aria-hidden
        >
          <path d="m7 15 5 5 5-5M7 9l5-5 5 5" />
        </svg>
      </div>
    </div>
  );
}
