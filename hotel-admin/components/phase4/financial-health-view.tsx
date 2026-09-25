"use client";

// Phase 4 (DARK) — Charge health.
//
// The job of this screen is to answer one question quickly: is money moving, and if not, why not. So the
// status word comes first and large, the reasons under it are the fixed codes the backend emits, and the
// numbers are grouped by rail so an operator can see WHICH rail is unhappy without reading all of them.
//
// Nothing on this screen identifies a guest, a folio, a card or a provider transaction, because the API
// does not send any of that. What the operator sees here is deliberately enough to act on and not enough to
// act around: the detail lives behind Manual review, where every decision is audited.
//
// The only action is Refresh. It is a diagnostic screen.

import { useCallback, useEffect, useState } from "react";
import { AlertTriangle, CircleCheck, HeartPulse, Hourglass, RefreshCw, Settings2, Wallet } from "lucide-react";
import { api, FinancialHealth, surfaceUnavailableMessage } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { PageHeader, PageShell, StatCard } from "@/components/ui/page";
import { ErrorBanner } from "@/components/ui/error-banner";
import { Skeleton } from "@/components/ui/misc";
import { LiveStatus, refreshingClass } from "@/components/ui/patterns";
import { cn } from "@/lib/utils";

const STATUS_TONE: Record<FinancialHealth["status"], "ok" | "info" | "warn" | "err"> = {
  OK: "ok",
  DEGRADED: "warn",
  ATTENTION_REQUIRED: "err",
  HELD: "err",
};

const STATUS_LABEL: Record<FinancialHealth["status"], string> = {
  OK: "OK",
  DEGRADED: "Degraded",
  ATTENTION_REQUIRED: "Attention required",
  HELD: "Held",
};

const STATUS_SUMMARY: Record<FinancialHealth["status"], string> = {
  OK: "Room charges and online payments are moving normally.",
  DEGRADED: "Money is moving, but something is slower or less certain than it should be.",
  ATTENTION_REQUIRED: "Something needs a person to look at it before money can move normally.",
  HELD: "Money movement is held on purpose until the items below are dealt with.",
};

// The backend emits fixed codes; the UI owns the sentence. Keeping the mapping here rather than sending
// prose over the wire is what keeps guest data out of an alerting path by construction.
const REASON_TEXT: Record<string, string> = {
  FINANCIAL_RECOVERY_MODE:
    "This site is in financial recovery. Money movement is deliberately held until every item in flight has been reconciled.",
  UNKNOWN_OUTCOMES_AWAITING_REVIEW:
    "One or more postings or payments ended UNKNOWN. Nobody knows yet whether the money moved, so nothing is retried automatically.",
  SETTLEMENTS_AWAITING_REVIEW: "Settlements are waiting on a manual review decision.",
  MANUAL_REVIEW_BACKLOG: "The manual review queue is longer or older than it should be.",
  POSTING_OUTBOX_STALLED: "Postings have been queued longer than expected. The PMS interface may be unreachable.",
  PAYMENTS_STUCK_PENDING: "Payments have been PENDING longer than a provider call should take.",
  NO_ACTIVE_PAYMENT_ACCOUNT:
    "No active default payment account is configured for this site, so online payment cannot be attempted.",
};

function ageText(seconds: number): string {
  if (seconds <= 0) return "—";
  if (seconds < 60) return `${seconds}s`;
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m`;
  return `${Math.floor(seconds / 3600)}h ${Math.floor((seconds % 3600) / 60)}m`;
}

/** A count that is a problem when it is not zero. */
const flag = (n: number, tone: "warn" | "err") => (n > 0 ? tone : "default");

export function FinancialHealthView() {
  const [health, setHealth] = useState<FinancialHealth | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const r = await api.get<{ health: FinancialHealth }>("/financial-ops/health");
      setHealth(r.health);
      setErr(null);
      setUpdatedAt(Date.now());
    } catch (e: any) {
      setErr(surfaceUnavailableMessage(e, "The financial subsystem"));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  return (
    <PageShell>
      <PageHeader
        icon={<HeartPulse />}
        eyebrow="Charges"
        title="Charge health"
        description="Whether money is moving — room charges posted to the PMS and online payments — and, if not, why."
        actions={
          <>
            {health && <LiveStatus updatedAt={updatedAt} refreshing={loading} error={!!err} />}
            <Button variant="secondary" onClick={() => void load()} disabled={loading} aria-label="Refresh financial health">
              <RefreshCw /> Refresh
            </Button>
          </>
        }
      />

      <ErrorBanner err={err} className="mb-0" />

      {!health && loading && (
        <div className="space-y-5" aria-busy="true">
          <span className="sr-only">Loading charge health</span>
          <Skeleton className="h-36 w-full" />
          <div className="grid grid-cols-2 gap-3 lg:grid-cols-4">
            {[0, 1, 2, 3].map((i) => <Skeleton key={i} className="h-24" />)}
          </div>
        </div>
      )}

      {health && (
        <div className={cn("space-y-5", loading && refreshingClass)}>
          <Card>
            <CardBody className="flex flex-col gap-4 p-5 sm:flex-row sm:items-start sm:gap-6">
              <div className="shrink-0 space-y-1">
                <div className="text-micro uppercase tracking-[0.08em] text-muted-foreground">Charges status</div>
                <Badge tone={STATUS_TONE[health.status]} dot className="px-3.5 py-1 text-sm">
                  {STATUS_LABEL[health.status]}
                </Badge>
              </div>
              <div className="min-w-0 space-y-2">
                <p className="text-emphasis">{STATUS_SUMMARY[health.status]}</p>
                <ul className="space-y-1.5 text-sm text-foreground">
                  {health.reasons.length === 0 ? (
                    <li className="flex items-start gap-2">
                      <CircleCheck className="mt-0.5 size-4 shrink-0 text-success" aria-hidden />
                      Nothing needs attention.
                    </li>
                  ) : (
                    health.reasons.map((r) => (
                      <li key={r} className="flex items-start gap-2">
                        <AlertTriangle className="mt-0.5 size-4 shrink-0 text-warning-subtle-foreground" aria-hidden />
                        <span>{REASON_TEXT[r] ?? r}</span>
                      </li>
                    ))
                  )}
                </ul>
              </div>
            </CardBody>
          </Card>

          <Rail
            title="PMS posting"
            description="Internet charges posted to a guest's room bill in the property management system."
          >
            <StatCard label="Queued" value={health.outbox_queued} />
            <StatCard label="In flight" value={health.outbox_in_flight} />
            <StatCard label="Held (recovery)" value={health.outbox_held_recovery} tone={flag(health.outbox_held_recovery, "warn")} />
            <StatCard label="Oldest waiting" value={ageText(health.outbox_oldest_age_seconds)} icon={<Hourglass />} />
            <StatCard
              label="Unknown outcomes"
              value={health.postings_unknown}
              hint="Never retried automatically"
              tone={flag(health.postings_unknown, "err")}
            />
            <StatCard
              label="Review queue"
              value={health.review_queue_open}
              tone={flag(health.review_queue_open, "warn")}
              href="/financial-review"
              hint="Open Manual review"
            />
            <StatCard label="Oldest unreviewed" value={ageText(health.review_oldest_age_seconds)} icon={<Hourglass />} />
          </Rail>

          <Rail title="Online payment" description="Payments a guest makes on the portal, and how each one settled.">
            <StatCard label="Created" value={health.payments_created} />
            <StatCard label="Pending" value={health.payments_pending} />
            <StatCard
              label="Unknown outcomes"
              value={health.payments_unknown}
              hint="Never retried automatically"
              tone={flag(health.payments_unknown, "err")}
            />
            <StatCard label="Oldest in flight" value={ageText(health.payments_oldest_age_seconds)} icon={<Hourglass />} />
            <StatCard label="Settlements required" value={health.settlements_required} href="/financial-settlements" />
            <StatCard label="Settlements in progress" value={health.settlements_in_progress} />
            <StatCard
              label="Settlements in manual review"
              value={health.settlements_manual_review}
              tone={flag(health.settlements_manual_review, "warn")}
            />
            <StatCard label="Settlements failed" value={health.settlements_failed} tone={flag(health.settlements_failed, "err")} />
          </Rail>

          <Rail title="Configuration" columns={2}>
            <StatCard
              label="Payment account"
              icon={<Wallet />}
              value={health.payment_account_configured ? "Configured" : "Not configured"}
              tone={health.payment_account_configured ? "ok" : "warn"}
              hint="Provider and merchant account are resolved from site configuration, never chosen per transaction."
            />
            <StatCard
              label="Provider egress"
              icon={<Settings2 />}
              value={health.provider_egress_enabled ? "Enabled" : "Disabled"}
              hint="No payment provider has been integrated or verified. No provider is contacted while egress is disabled."
            />
          </Rail>
        </div>
      )}
    </PageShell>
  );
}

function Rail({
  title,
  description,
  columns = 4,
  children,
}: {
  title: string;
  description?: string;
  columns?: 2 | 4;
  children: React.ReactNode;
}) {
  return (
    <section className="space-y-3" aria-label={title}>
      <div className="space-y-0.5">
        <h2 className="text-emphasis">{title}</h2>
        {description && <p className="text-sm text-muted-foreground">{description}</p>}
      </div>
      <div className={cn("grid grid-cols-1 gap-3 min-[420px]:grid-cols-2", columns === 4 && "lg:grid-cols-4")}>
        {children}
      </div>
    </section>
  );
}
