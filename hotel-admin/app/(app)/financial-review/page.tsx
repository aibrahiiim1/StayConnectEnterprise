"use client";

// Route shell for the Phase-4 (DARK) Manual Review screen. edged enforces the real gate on every request,
// and every decision additionally requires a password step-up with the actor taken from the session.
//
// CARD DISPUTES ARE ALWAYS HERE. edged mounts the card-case routes under this surface whether or not the PMS
// room-charge review is switched on, because a card dispute never ends access by itself and somebody has to
// be able to decide it. When the review itself is not mounted (its queue answers 404), only the cases show.

import { useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { ManualReviewView } from "@/components/phase4/manual-review-view";
import { CardCasesCard } from "@/components/phase4/card-cases";

export default function FinancialReviewPage() {
  const [roles, setRoles] = useState<string[] | null>(null);
  // null while unknown; false when the room-charge review queue is not mounted on this appliance.
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
      <div className="mx-auto w-full max-w-7xl px-4 pb-8 sm:px-6">
        {reviewMounted === false && (
          <p className="pt-6 text-sm text-muted-foreground" data-testid="review-not-mounted">
            Room-charge review is not switched on at this appliance. Card disputes and chargebacks are decided below.
          </p>
        )}
        <CardCasesCard canAct={canAct} />
      </div>
    </>
  );
}
