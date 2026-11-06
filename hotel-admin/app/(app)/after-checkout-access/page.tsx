"use client";

import { canRead, canWrite } from "@/lib/roles";
import { useOperatorRoles } from "@/lib/whoami-context";
import { AfterCheckoutAccessScreen } from "@/components/after-checkout-access/after-checkout-access-screen";

export default function AfterCheckoutAccessPage() {
  const roles = useOperatorRoles();
  return (
    <AfterCheckoutAccessScreen
      canWrite={roles === null ? false : canWrite("after-checkout-access", roles)}
      canActOnReconnections={roles === null ? false : canWrite("post-stay-profiles", roles)}
      rolesKnown={roles !== null}
      canReadLimits={roles === null ? false : canRead("auth-methods", roles)}
      canEditLimits={roles === null ? false : canWrite("auth-methods", roles)}
    />
  );
}
