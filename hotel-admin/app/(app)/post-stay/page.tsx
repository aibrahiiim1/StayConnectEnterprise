"use client";

// edged enforces RBAC and step-up even if a client bypasses the canAct UI gate.

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { PostStayView } from "@/components/phase5/post-stay-view";

export default function PostStayPage() {
  const [roles, setRoles] = useState<string[] | null>(null);
  useEffect(() => {
    api
      .get<{ roles?: string[] }>("/auth/whoami")
      .then((m) => setRoles(m.roles ?? []))
      .catch(() => setRoles([]));
  }, []);
  return <PostStayView canAct={roles === null ? false : canWrite("post-stay-profiles", roles)} rolesKnown={roles !== null} />;
}
