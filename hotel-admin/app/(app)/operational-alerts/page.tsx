"use client";

import { canWrite } from "@/lib/roles";
import { useOperatorRoles } from "@/lib/whoami-context";
import { OperationalAlertsView } from "@/components/phase3/operational-alerts-view";

export default function OperationalAlertsPage() {
  const roles = useOperatorRoles();
  const canAct = roles === null ? false : canWrite("operational-alerts", roles);
  return <OperationalAlertsView canAct={canAct} readOnly={roles !== null && !canAct} />;
}
