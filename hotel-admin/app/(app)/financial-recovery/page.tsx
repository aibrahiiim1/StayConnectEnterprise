"use client";

// edged enforces the real gate; every action requires a password step-up.

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
