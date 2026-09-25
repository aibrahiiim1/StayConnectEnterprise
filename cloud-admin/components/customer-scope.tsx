"use client";

import { Building2 } from "lucide-react";
import { useCustomer } from "@/lib/customer-context";
import { Card } from "@/components/ui/card";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout } from "@/components/ui/error-banner";

/** The small "Customer: X" line under a page title, so a screenshot always says whose data it shows. */
export function CustomerScope() {
  const { selectedTenantId, selectedTenantName } = useCustomer();
  return (
    <span className="inline-flex items-center gap-1.5 text-sm text-muted-foreground">
      <Building2 className="size-3.5" aria-hidden />
      {selectedTenantId === "" ? (
        "All customers"
      ) : (
        <>
          Customer: <span className="font-medium text-foreground">{selectedTenantName}</span>
        </>
      )}
    </span>
  );
}

/**
 * The card a per-customer page shows in "All customers" mode instead of data (Operators, Audit log). It names
 * the one thing to do — choose a customer in the sidebar — rather than showing an empty or cross-customer list.
 */
export function SelectCustomerCard({ what }: { what: string }) {
  return (
    <Card>
      <EmptyState
        icon={<Building2 />}
        title="Select a customer"
        hint={
          <>
            {what} Choose a customer in the <strong className="font-semibold text-foreground">Customer context</strong>{" "}
            selector at the top of the sidebar.
          </>
        }
      />
    </Card>
  );
}

/** The notice list pages show in "All customers" mode, where creating is disabled. */
export function AllCustomersNotice({ children }: { children: React.ReactNode }) {
  return <Callout tone="info">{children}</Callout>;
}
