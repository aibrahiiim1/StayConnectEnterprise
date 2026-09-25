"use client";

// Route shell — the screen itself lives in components/checkout-grace so it can be tested without the router.

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { CheckoutGraceScreen } from "@/components/checkout-grace/checkout-grace-screen";

export default function CheckoutGracePage() {
  const [roles, setRoles] = useState<string[] | null>(null);
  useEffect(() => {
    api
      .get<{ roles?: string[] }>("/auth/whoami")
      .then((m) => setRoles(m.roles ?? []))
      .catch(() => setRoles([]));
  }, []);
  return <CheckoutGraceScreen canWrite={roles === null ? false : canWrite("checkout-grace", roles)} />;
}
