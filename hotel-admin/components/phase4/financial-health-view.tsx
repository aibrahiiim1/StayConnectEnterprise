"use client";

// Phase 4 (DARK) — Financial health.
//
// The job of this screen is to answer one question quickly: is money moving, and if not, why not. So the
// status word comes first and large, the reasons under it are the fixed codes the backend emits, and the
// numbers are grouped by rail so an operator can see WHICH rail is unhappy without reading all of them.
//
// Nothing on this screen identifies a guest, a folio, a card or a provider transaction, because the API
// does not send any of that. What the operator sees here is deliberately enough to act on and not enough to
// act around: the detail lives behind Manual Review, where every decision is audited.

import { useCallback, useEffect, useState } from "react";
import { api, FinancialHealth, surfaceUnavailableMessage } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

const STATUS_TONE: Record<FinancialHealth["status"], "ok" | "info" | "warn" | "err"> = {
  OK: "ok",
  DEGRADED: "warn",
  ATTENTION_REQUIRED: "err",
  HELD: "err",
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

function Metric({ label, value, hint }: { label: string; value: number | string; hint?: string }) {
  return (
    <div className="rounded-md border border-slate-200 p-3">
      <dt className="text-xs uppercase tracking-wide text-slate-500">{label}</dt>
      <dd className="mt-1 text-2xl font-semibold tabular-nums">{value}</dd>
      {hint ? <p className="mt-1 text-xs text-slate-500">{hint}</p> : null}
    </div>
  );
}

export function FinancialHealthView() {
  const [health, setHealth] = useState<FinancialHealth | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const r = await api.get<{ health: FinancialHealth }>("/financial-ops/health");
      setHealth(r.health);
      setErr(null);
    } catch (e: any) {
      setErr(surfaceUnavailableMessage(e, "The financial subsystem"));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  if (loading && !health) return <p role="status">Loading financial health…</p>;
  if (err) return <p role="alert">{err}</p>;
  if (!health) return null;

  return (
    <div className="space-y-4">
      <Card>
        <CardBody>
          <div className="flex items-start justify-between gap-4">
            <div>
              <h2 className="text-sm font-medium text-slate-500">Financial subsystem</h2>
              <p className="mt-1 flex items-center gap-2 text-3xl font-semibold">
                <Badge tone={STATUS_TONE[health.status]}>{health.status.replace(/_/g, " ")}</Badge>
              </p>
              <ul className="mt-3 space-y-1 text-sm text-slate-700">
                {health.reasons.length === 0 ? (
                  <li>Nothing needs attention.</li>
                ) : (
                  health.reasons.map((r) => (
                    <li key={r}>{REASON_TEXT[r] ?? r}</li>
                  ))
                )}
              </ul>
            </div>
            <Button onClick={() => void load()} aria-label="Refresh financial health">
              Refresh
            </Button>
          </div>
        </CardBody>
      </Card>

      <Card>
        <CardBody>
          <h3 className="mb-3 text-sm font-medium text-slate-500">PMS posting rail</h3>
          <dl className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <Metric label="Queued" value={health.outbox_queued} />
            <Metric label="In flight" value={health.outbox_in_flight} />
            <Metric label="Held (recovery)" value={health.outbox_held_recovery} />
            <Metric label="Oldest waiting" value={ageText(health.outbox_oldest_age_seconds)} />
            <Metric label="UNKNOWN postings" value={health.postings_unknown} hint="Never retried automatically" />
            <Metric label="Review queue" value={health.review_queue_open} />
            <Metric label="Oldest unreviewed" value={ageText(health.review_oldest_age_seconds)} />
          </dl>
        </CardBody>
      </Card>

      <Card>
        <CardBody>
          <h3 className="mb-3 text-sm font-medium text-slate-500">Online payment rail</h3>
          <dl className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <Metric label="Created" value={health.payments_created} />
            <Metric label="Pending" value={health.payments_pending} />
            <Metric label="UNKNOWN" value={health.payments_unknown} hint="Never retried automatically" />
            <Metric label="Oldest in flight" value={ageText(health.payments_oldest_age_seconds)} />
            <Metric label="Settlements required" value={health.settlements_required} />
            <Metric label="In progress" value={health.settlements_in_progress} />
            <Metric label="Manual review" value={health.settlements_manual_review} />
            <Metric label="Failed" value={health.settlements_failed} />
          </dl>
        </CardBody>
      </Card>

      <Card>
        <CardBody>
          <h3 className="mb-3 text-sm font-medium text-slate-500">Configuration</h3>
          <dl className="grid grid-cols-1 gap-3 sm:grid-cols-2">
            <Metric
              label="Payment account"
              value={health.payment_account_configured ? "Configured" : "Not configured"}
              hint="Provider and merchant account are resolved from site configuration, never chosen per transaction."
            />
            <Metric
              label="Provider egress"
              value={health.provider_egress_enabled ? "Enabled" : "Disabled (DARK)"}
              hint="No payment provider has been integrated or verified. No provider is contacted while egress is disabled."
            />
          </dl>
        </CardBody>
      </Card>
    </div>
  );
}
