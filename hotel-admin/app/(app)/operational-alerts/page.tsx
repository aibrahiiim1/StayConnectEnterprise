"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { OperationalAlertsView } from "@/components/phase3/operational-alerts-view";

export default function OperationalAlertsPage() {
  const [roles, setRoles] = useState<string[] | null>(null);
  useEffect(() => {
    api
      .get<{ roles?: string[] }>("/auth/whoami")
      .then((m) => setRoles(m.roles ?? []))
      .catch(() => setRoles([]));
  }, []);
  const canAct = roles === null ? false : canWrite("operational-alerts", roles);
  return <OperationalAlertsView canAct={canAct} readOnly={roles !== null && !canAct} />;
}
