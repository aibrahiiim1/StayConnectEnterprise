"use client";

// Route shell for the Phase-4 (DARK) financial recovery screen. The view is given an explicit permission so
// it can be rendered read-only in tests; edged enforces the real gate, and every action additionally
// requires a password step-up.

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { FinancialRecoveryView } from "@/components/phase4/financial-recovery-view";

export default function FinancialRecoveryPage() {
  const [roles, setRoles] = useState<string[] | null>(null);
  useEffect(() => {
    api
      .get<{ roles?: string[] }>("/auth/whoami")
      .then((m) => setRoles(m.roles ?? []))
      .catch(() => setRoles([]));
  }, []);
  return <FinancialRecoveryView canAct={roles === null ? false : canWrite("financial-review", roles)} />;
}
