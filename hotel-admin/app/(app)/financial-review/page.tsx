"use client";

import { useCallback, useState } from "react";
import { canWrite } from "@/lib/roles";
import { useOperatorRoles } from "@/lib/whoami-context";
import { ManualReviewView } from "@/components/phase4/manual-review-view";
import { CardCasesCard } from "@/components/phase4/card-cases";
import { PageHeader, PageShell } from "@/components/ui/page";
import { Callout } from "@/components/ui/error-banner";

export default function FinancialReviewPage() {
  const roles = useOperatorRoles();
  const [reviewMounted, setReviewMounted] = useState(true);
  const onNotMounted = useCallback(() => setReviewMounted(false), []);
  const canAct = roles === null ? false : canWrite("financial-review", roles);
  return (
    <>
      {reviewMounted && <ManualReviewView canAct={canAct} rolesKnown={roles !== null} onNotMounted={onNotMounted} />}
      <PageShell className={reviewMounted ? "mt-5" : undefined}>
        {!reviewMounted && (
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
