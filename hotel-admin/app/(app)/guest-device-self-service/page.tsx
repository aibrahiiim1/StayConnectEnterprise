"use client";

// Route shell for the Phase-6 (DARK) Guest Device Self-Service setting. canAct only decides whether the
// switch is offered; edged enforces the real boundary on every request through the same role matrix, so a
// client that ignored this would still be refused.

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { GuestDeviceSelfServiceView } from "@/components/phase6/guest-device-self-service-view";

export default function GuestDeviceSelfServicePage() {
  const [roles, setRoles] = useState<string[] | null>(null);
  useEffect(() => {
    api
      .get<{ roles?: string[] }>("/auth/whoami")
      .then((m) => setRoles(m.roles ?? []))
      .catch(() => setRoles([]));
  }, []);
  return (
    <GuestDeviceSelfServiceView
      canAct={roles === null ? false : canWrite("guest-device-self-service", roles)}
    />
  );
}
