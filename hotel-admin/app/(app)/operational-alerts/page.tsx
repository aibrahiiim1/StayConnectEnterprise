"use client";

// Route shell. The interactive view lives in a component so it can be rendered with an explicit permission in
// tests and stories; the page itself derives that permission from the signed-in operator's roles, and edged
// enforces the real gate on every request regardless.

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
  return <OperationalAlertsView canAct={roles === null ? false : canWrite("operational-alerts", roles)} />;
}
