"use client";

// edged enforces the real gate; every decision requires a password step-up.
// Card disputes are always shown: edged mounts them even when room-charge review is off, as a dispute must remain
// decidable.

import { useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { ManualReviewView } from "@/components/phase4/manual-review-view";
import { CardCasesCard } from "@/components/phase4/card-cases";
import { PageHeader, PageShell } from "@/components/ui/page";
import { Callout } from "@/components/ui/error-banner";

export default function FinancialReviewPage() {
  const [roles, setRoles] = useState<string[] | null>(null);
  const [reviewMounted, setReviewMounted] = useState<boolean | null>(null);
  useEffect(() => {
    api
      .get<{ roles?: string[] }>("/auth/whoami")
      .then((m) => setRoles(m.roles ?? []))
      .catch(() => setRoles([]));
    api
      .get("/financial-review/queue")
      .then(() => setReviewMounted(true))
      .catch((e) => setReviewMounted(!(e instanceof ApiError && e.status === 404)));
  }, []);
  const canAct = roles === null ? false : canWrite("financial-review", roles);
  return (
    <>
      {reviewMounted !== false && <ManualReviewView canAct={canAct} />}
      <PageShell className={reviewMounted === false ? undefined : "mt-5"}>
        {reviewMounted === false && (
          <>
            <PageHeader title="Manual review" description="Decide card disputes and chargebacks." />
            <Callout tone="neutral">
              <span data-testid="review-not-mounted">
                Room-charge review is not switched on at this appliance. Card disputes and chargebacks are decided below.
              </span>
            </Callout>
          </>
        )}
        <CardCasesCard canAct={canAct} />
      </PageShell>
    </>
  );
}
