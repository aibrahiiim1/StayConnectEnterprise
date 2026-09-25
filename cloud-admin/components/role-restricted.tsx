"use client";

import { Lock } from "lucide-react";
import { NotAvailable } from "@/components/ui/patterns";

/**
 * What a page shows when the signed-in role cannot read it at all: the server refuses its list for this role
 * (see lib/permissions.ts). The menu item is already hidden; this covers a page reached by its address.
 */
export function RoleRestricted({ what }: { what: React.ReactNode }) {
  return (
    <NotAvailable
      icon={<Lock />}
      title="Not available to your role"
      reason={<>{what} Central does not show it to your role.</>}
    />
  );
}
