"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { AfterCheckoutAccessScreen } from "@/components/after-checkout-access/after-checkout-access-screen";

export default function AfterCheckoutAccessPage() {
  const [roles, setRoles] = useState<string[] | null>(null);
  useEffect(() => {
    api
      .get<{ roles?: string[] }>("/auth/whoami")
      .then((m) => setRoles(m.roles ?? []))
      .catch(() => setRoles([]));
  }, []);
  return (
    <AfterCheckoutAccessScreen
      canWrite={roles === null ? false : canWrite("after-checkout-access", roles)}
      canActOnReconnections={roles === null ? false : canWrite("post-stay-profiles", roles)}
      rolesKnown={roles !== null}
    />
  );
}
