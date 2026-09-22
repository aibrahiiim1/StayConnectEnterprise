"use client";

// Route shell for the voucher screen. edged enforces the real gate on every request; these three flags only
// decide which controls are offered, and a client that ignored them would still be refused by RBAC, by the
// password step-up and by the mandatory reason.
//
// THREE FLAGS, NOT ONE, because the screen covers three different powers: printing and cancelling cards,
// recovering a code in the clear, and choosing what a code looks like. A role can hold any combination —
// the property's IT manager sets the format and may not read a code; the desk prints and reads and does not
// set the format.

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { canWrite } from "@/lib/roles";
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
      canEditFormat={roles === null ? false : canWrite("voucher-code-settings", r)}
    />
  );
}
