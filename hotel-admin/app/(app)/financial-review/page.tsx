"use client";

// Route shell for the Phase-4 (DARK) Manual Review screen. edged enforces the real gate on every request,
// and every decision additionally requires a password step-up with the actor taken from the session.

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { ManualReviewView } from "@/components/phase4/manual-review-view";
import { CardCasesCard } from "@/components/phase4/card-cases";

export default function FinancialReviewPage() {
  const [roles, setRoles] = useState<string[] | null>(null);
  useEffect(() => {
    api
      .get<{ roles?: string[] }>("/auth/whoami")
      .then((m) => setRoles(m.roles ?? []))
      .catch(() => setRoles([]));
  }, []);
  const canAct = roles === null ? false : canWrite("financial-review", roles);
  return (
    <>
      <ManualReviewView canAct={canAct} />
      <div className="mx-auto w-full max-w-7xl px-4 pb-8 sm:px-6">
        <CardCasesCard canAct={canAct} />
      </div>
    </>
  );
}
