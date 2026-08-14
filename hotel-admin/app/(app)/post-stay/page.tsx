"use client";

// Route shell for the Phase-5 (DARK) post-stay identity screen. edged enforces the real gate on every
// request; canAct only decides whether the buttons are offered, and a client that ignored it would still be
// refused by RBAC, the password step-up and the mandatory reason.

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
  return <PostStayView canAct={roles === null ? false : canWrite("post-stay-profiles", roles)} />;
}
