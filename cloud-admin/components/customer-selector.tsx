"use client";

import { useCustomer } from "@/lib/customer-context";
import { Building2, ChevronsUpDown } from "lucide-react";

/**
 * CustomerSelector is the Global Customer Context switcher shown at the top of the
 * sidebar for platform admins. Choosing a customer scopes every customer-owned
 * page (Sites, Appliances, Licenses, Operators, Audit, …) to that customer;
 * "All Customers" shows the whole fleet. It is hidden for tenant operators, who
 * are pinned to their own customer.
 */
export function CustomerSelector() {
  const { isPlatform, tenants, selectedTenantId, setSelectedTenantId, selectedTenantName } = useCustomer();

  if (!isPlatform) {
    // Tenant operator: show their fixed customer as a static label, no switcher.
    return (
      <div className="px-3 py-2 text-xs">
        <div className="text-[10px] uppercase tracking-widest text-muted mb-1">Customer</div>
        <div className="flex items-center gap-2 text-text">
          <Building2 size={13} /> <span className="truncate">{selectedTenantName}</span>
        </div>
      </div>
    );
  }

  return (
    <div className="px-3 py-2">
      <label className="text-[10px] uppercase tracking-widest text-muted mb-1 block">Customer context</label>
      <div className="relative">
        <Building2 size={13} className="pointer-events-none absolute left-2 top-1/2 -translate-y-1/2 text-muted" />
        <select
          value={selectedTenantId}
          onChange={(e) => setSelectedTenantId(e.target.value)}
          className="w-full appearance-none rounded-md border border-border bg-panel2 pl-7 pr-7 py-1.5 text-sm text-text focus:outline-none focus:ring-1 focus:ring-brand"
          title="Scope the console to one customer, or view all customers"
        >
          <option value="">All Customers</option>
          {tenants.map((t) => (
            <option key={t.id} value={t.id}>{t.name}</option>
          ))}
        </select>
        <ChevronsUpDown size={13} className="pointer-events-none absolute right-2 top-1/2 -translate-y-1/2 text-muted" />
      </div>
    </div>
  );
}
