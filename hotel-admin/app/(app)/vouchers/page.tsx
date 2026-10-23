"use client";

// The flags only decide which controls are offered; edged enforces the real gate (RBAC, password step-up, reason).
// Issue, reveal-code and format are separate powers a role can hold in any combination.

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { canRead, canWrite } from "@/lib/roles";
import { VouchersView } from "@/components/vouchers/vouchers-view";

export default function VouchersPage() {
  const [roles, setRoles] = useState<string[] | null>(null);
  useEffect(() => {
    api
      .get<{ roles?: string[] }>("/auth/whoami")
      .then((m) => setRoles(m.roles ?? []))
      .catch(() => setRoles([]));
  }, []);
  const r = roles ?? [];
  return (
    <VouchersView
      canIssue={roles === null ? false : canWrite("vouchers", r)}
      canRevealCodes={roles === null ? false : canWrite("voucher-codes", r)}
      // payments_operator has no read access, so the panel must not even be requested for it.
      canReadFormat={roles === null ? false : canRead("voucher-code-settings", r)}
      canEditFormat={roles === null ? false : canWrite("voucher-code-settings", r)}
      canReadBranding={roles === null ? false : canRead("portal-branding", r)}
      readOnly={roles !== null && !canWrite("vouchers", r) && !canWrite("voucher-codes", r)}
    />
  );
}
