"use client";

import { canWrite } from "@/lib/roles";
import { useOperatorRoles } from "@/lib/whoami-context";
import { FinancialRecoveryView } from "@/components/phase4/financial-recovery-view";

export default function FinancialRecoveryPage() {
  const roles = useOperatorRoles();
  return (
    <FinancialRecoveryView
      canAct={roles === null ? false : canWrite("financial-review", roles)}
      rolesKnown={roles !== null}
    />
  );
}
