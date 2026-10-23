"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { StayTransferView } from "@/components/phase5/stay-transfer-view";

export default function StayTransfersPage() {
  const [roles, setRoles] = useState<string[] | null>(null);
  useEffect(() => {
    api
      .get<{ roles?: string[] }>("/auth/whoami")
      .then((m) => setRoles(m.roles ?? []))
      .catch(() => setRoles([]));
  }, []);
  return <StayTransferView canAct={roles === null ? false : canWrite("stay-transfers", roles)} rolesKnown={roles !== null} />;
}
