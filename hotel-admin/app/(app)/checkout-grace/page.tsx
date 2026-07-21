"use client";

// Route shell — see the alerts page for why the interactive form is a component.

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { CheckoutGraceForm } from "@/components/phase3/checkout-grace-form";

export default function CheckoutGracePage() {
  const [roles, setRoles] = useState<string[] | null>(null);
  useEffect(() => {
    api
      .get<{ roles?: string[] }>("/auth/whoami")
      .then((m) => setRoles(m.roles ?? []))
      .catch(() => setRoles([]));
  }, []);
  return <CheckoutGraceForm canWrite={roles === null ? false : canWrite("checkout-grace", roles)} />;
}
