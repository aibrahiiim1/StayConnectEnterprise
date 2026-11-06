"use client";

import { canWrite } from "@/lib/roles";
import { useOperatorRoles } from "@/lib/whoami-context";
import { StayTransferView } from "@/components/phase5/stay-transfer-view";

export default function StayTransfersPage() {
  const roles = useOperatorRoles();
  return <StayTransferView canAct={roles === null ? false : canWrite("stay-transfers", roles)} rolesKnown={roles !== null} />;
}
