"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { AlertTriangle, ArrowUpRight, CircleCheck, RefreshCw } from "lucide-react";
import { api, ApiError, FinancialHealth, surfaceUnavailableMessage } from "@/lib/api";
import { Card, CardBody, SectionCard } from "@/components/ui/card";
import { StatusBadge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DescriptionList, MetricStrip } from "@/components/ui/data";
import { EmptyState } from "@/components/ui/empty-state";
import { PageHeader, PageShell, StatGrid } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { ErrorBanner } from "@/components/ui/error-banner";
import { Skeleton } from "@/components/ui/misc";
import { LiveStatus, refreshingClass } from "@/components/ui/patterns";
import { cn, formatDate } from "@/lib/utils";

const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";

const STATUS_TONE: Record<string, "ok" | "info" | "warn" | "err"> = {
  OK: "ok",
  DEGRADED: "warn",
  ATTENTION_REQUIRED: "err",
  HELD: "err",
};

const STATUS_LABEL: Record<string, string> = {
  OK: "Normal",
  DEGRADED: "Degraded",
  ATTENTION_REQUIRED: "Attention required",
  HELD: "Held",
};

const STATUS_SUMMARY: Record<string, string> = {
  OK: "Room charges and online payments are moving normally.",
  DEGRADED: "Money is moving, but something is slower or less certain than it should be.",
  ATTENTION_REQUIRED: "Something needs a person to look at it before money can move normally.",
  HELD: "Money movement is held on purpose until the items below are dealt with.",
};

const REASON_TEXT: Record<string, { text: string; href: string; link: string }> = {
  FINANCIAL_RECOVERY_MODE: {
    text: "This site is in financial recovery. Money movement is deliberately held until every item in flight has been reconciled.",
    href: "/financial-recovery",
    link: "Recovery",
  },
  UNKNOWN_OUTCOMES_AWAITING_REVIEW: {
    text: "One or more postings or payments ended with an unknown outcome. Nobody knows yet whether the money moved, so nothing is retried automatically.",
    href: "/financial-review",
    link: "Manual review",
  },
  SETTLEMENTS_AWAITING_REVIEW: {
    text: "Some package payments are waiting on a Manual review decision.",
    href: "/financial-settlements",
    link: "Package payments",
  },
  MANUAL_REVIEW_BACKLOG: {
    text: "The manual review queue is longer or older than it should be.",
    href: "/financial-review",
    link: "Manual review",
  },
  POSTING_OUTBOX_STALLED: {
    text: "Postings have been queued longer than expected. The PMS connection may be unreachable.",
    href: "/pms-interfaces",
    link: "PMS connection",
  },
  PAYMENTS_STUCK_PENDING: {
    text: "Payments have been pending longer than a provider call should take.",
    href: "/financial-settlements",
    link: "Package payments",
  },
  NO_ACTIVE_PAYMENT_ACCOUNT: {
    text: "No active default payment account is configured for this site, so online payment cannot be attempted.",
    href: "/payment-methods",
    link: "Payment methods",
  },
};

const LINK = "inline-flex items-center gap-0.5 font-medium text-primary hover:underline";

function plural(n: number, word: string) {
  return `${n.toLocaleString()} ${word}${n === 1 ? "" : "s"}`;
}

function ageText(seconds: number, none: string): string {
  if (seconds <= 0) return none;
  if (seconds < 60) return plural(seconds, "second");
  if (seconds < 3600) return plural(Math.floor(seconds / 60), "minute");
  const h = Math.floor(seconds / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  return m === 0 ? plural(h, "hour") : `${plural(h, "hour")} ${plural(m, "minute")}`;
}

const flag = (n: number, tone: "warn" | "err") => (n > 0 ? tone : "default");
const count = (n: number) => n.toLocaleString();

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
    } catch (e) {
      setErr(
        e instanceof ApiError && e.status === 404
          ? surfaceUnavailableMessage(e, "The financial subsystem")
          : "Charge health could not be loaded. Nothing has been changed.",
      );
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const stale = health !== null && err !== null;
  const status = health?.status ?? "";

  return (
    <PageShell>
      <PageHeader
        title="Charge health"
        description="Whether money is moving, and if not, why."
        help={
          <>
            <HelpSection title="What this page answers">
              <p>
                Whether money is moving — room charges posted to the PMS and online payments — and, if not, why. The
                status comes first; the reasons under it say what is holding things up. It is a diagnostic screen:
                the only action is Refresh.
              </p>
            </HelpSection>
            <HelpSection title="The sections">
              <HelpList
                items={[
                  <><strong>PMS posting</strong> — internet charges posted to a guest&rsquo;s room bill in the property management system.</>,
                  <><strong>Online payment</strong> — payments a guest makes on the portal, and how each one settled.</>,
                  <><strong>Configuration</strong> — the payment account and provider egress. Provider and merchant account are resolved from site configuration, never chosen per transaction.</>,
                ]}
              />
            </HelpSection>
            <HelpSection title="Unknown outcomes">
              <p>
                A posting or payment that ended UNKNOWN is never retried automatically, because nobody knows yet
                whether the money moved. It is decided on <strong>Manual review</strong>.
              </p>
            </HelpSection>
            <HelpSection title="No guest detail here">
              <p>
                Nothing on this screen identifies a guest, a folio, a card or a provider transaction. The detail lives
                behind Manual review, where every decision is audited.
              </p>
            </HelpSection>
          </>
        }
        actions={
          health ? <LiveStatus updatedAt={updatedAt} refreshing={loading} error={stale} onRefresh={() => void load()} /> : undefined
        }
      />

      {!health && loading && (
        <div className="space-y-5" aria-busy="true">
          <span className="sr-only">Loading charge health</span>
          <Skeleton className="h-36 w-full" />
          <StatGrid cols={4}>
            {[0, 1, 2, 3].map((i) => <Skeleton key={i} className="h-24" />)}
          </StatGrid>
        </div>
      )}

      {!health && !loading && err && (
        <Card role="alert" className={FADE}>
          <EmptyState
            icon={<AlertTriangle />}
            title="Charge health could not be loaded"
            hint={err}
            action={
              <Button variant="secondary" disabled={loading} onClick={() => void load()}>
                <RefreshCw className={cn(loading && "animate-spin motion-reduce:animate-none")} /> Try again
              </Button>
            }
          />
        </Card>
      )}

      {health && (
        <div className={cn("space-y-5", (loading || stale) && refreshingClass)}>
          {stale && <ErrorBanner err="Charge health could not be refreshed. Nothing has been changed." className="mb-0" />}
          <Card>
            <CardBody key={`${status}:${health.reasons.join(",")}`} className={cn("flex flex-col gap-4 sm:flex-row sm:items-start sm:gap-6", FADE)}>
              <div className="shrink-0 space-y-1.5">
                <div className="text-xs text-muted-foreground">Charges status</div>
                <StatusBadge tone={STATUS_TONE[status] ?? "info"}>{STATUS_LABEL[status] ?? "Status not recognised"}</StatusBadge>
              </div>
              <div className="min-w-0 space-y-2">
                <p className="text-emphasis">
                  {STATUS_SUMMARY[status] ?? "The appliance reported a status this console does not recognise."}
                </p>
                {stale && updatedAt !== null && (
                  <p className={cn("text-xs text-muted-foreground", FADE)}>
                    These figures are from {formatDate(new Date(updatedAt).toISOString())} and may be out of date.
                  </p>
                )}
                <ul className="space-y-1.5 text-sm text-foreground">
                  {health.reasons.length === 0 ? (
                    <li className="flex items-start gap-2">
                      <CircleCheck className="mt-0.5 size-4 shrink-0 text-success" aria-hidden />
                      Nothing needs attention.
                    </li>
                  ) : (
                    health.reasons.map((r) => {
                      const words = REASON_TEXT[r];
                      return (
                        <li key={r} className="flex items-start gap-2">
                          <AlertTriangle className="mt-0.5 size-4 shrink-0 text-warning-subtle-foreground" aria-hidden />
                          {words ? (
                            <span>
                              {words.text}{" "}
                              <Link href={words.href} className={LINK}>
                                {words.link} <ArrowUpRight className="size-3.5" aria-hidden />
                              </Link>
                            </span>
                          ) : (
                            <span>
                              A reason this console does not recognise.{" "}
                              <span className="text-xs text-muted-foreground">For support: <span className="font-mono">{r}</span></span>
                            </span>
                          )}
                        </li>
                      );
                    })
                  )}
                </ul>
              </div>
            </CardBody>
          </Card>

          <Rail
            title="PMS posting"
            description="Internet charges posted to a guest's room bill in the PMS. Unknown outcomes are never retried automatically."
            link={{ href: "/financial-review", label: "Manual review" }}
            items={[
              { label: "Queued", value: count(health.outbox_queued) },
              { label: "Being sent", value: count(health.outbox_in_flight) },
              { label: "Held during recovery", value: count(health.outbox_held_recovery), tone: flag(health.outbox_held_recovery, "warn") },
              { label: "Oldest waiting", value: ageText(health.outbox_oldest_age_seconds, "Nothing waiting") },
              { label: "Unknown outcomes", value: count(health.postings_unknown), tone: flag(health.postings_unknown, "err") },
              { label: "Review queue", value: count(health.review_queue_open), tone: flag(health.review_queue_open, "warn") },
              { label: "Oldest unreviewed", value: ageText(health.review_oldest_age_seconds, "Nothing to review") },
            ]}
          />

          <Rail
            title="Online payment"
            description="Payments a guest makes on the portal, and how each one settled. Unknown outcomes are never retried automatically."
            link={{ href: "/financial-settlements", label: "Package payments" }}
            items={[
              { label: "Started", value: count(health.payments_created) },
              { label: "Pending", value: count(health.payments_pending) },
              { label: "Unknown outcomes", value: count(health.payments_unknown), tone: flag(health.payments_unknown, "err") },
              { label: "Oldest still open", value: ageText(health.payments_oldest_age_seconds, "None open") },
              { label: "Payments waiting", value: count(health.settlements_required) },
              { label: "Payments in progress", value: count(health.settlements_in_progress) },
              { label: "Payments to review", value: count(health.settlements_manual_review), tone: flag(health.settlements_manual_review, "warn") },
              { label: "Payments failed", value: count(health.settlements_failed), tone: flag(health.settlements_failed, "err") },
            ]}
          />
          <SectionCard title="Configuration">
            <DescriptionList
              items={[
                {
                  label: "Payment account",
                  value: (
                    <span className="inline-flex flex-wrap items-center gap-2">
                      <StatusBadge tone={health.payment_account_configured ? "ok" : "warn"}>
                        {health.payment_account_configured ? "Configured" : "Not configured"}
                      </StatusBadge>
                      {!health.payment_account_configured && (
                        <Link href="/payment-methods" className={cn(LINK, "text-xs")}>
                          Payment methods <ArrowUpRight className="size-3.5" aria-hidden />
                        </Link>
                      )}
                    </span>
                  ),
                },
                {
                  label: "Contacting the payment provider",
                  value: <StatusBadge tone="default">{health.provider_egress_enabled ? "Allowed" : "Not allowed"}</StatusBadge>,
                  hint: health.provider_egress_enabled
                    ? "This appliance may contact the configured payment provider."
                    : "This appliance does not contact any payment provider, so online payment cannot complete.",
                },
              ]}
            />
          </SectionCard>
        </div>
      )}
    </PageShell>
  );
}

type Tone = "ok" | "warn" | "err" | "info";

function Rail({ title, description, link, items }: {
  title: string;
  description: string;
  link: { href: string; label: string };
  items: { label: string; value: React.ReactNode; tone?: Tone | "default" }[];
}) {
  return (
    <SectionCard
      title={title}
      description={description}
      actions={
        <Link href={link.href} className="inline-flex items-center gap-0.5 text-xs font-medium text-primary hover:underline">
          {link.label} <ArrowUpRight className="size-3.5" aria-hidden />
        </Link>
      }
    >
      <MetricStrip
        items={items.map((it) => ({ ...it, tone: it.tone === "default" ? undefined : it.tone }))}
        className={items.length % 2 ? "[&>div:last-child]:col-span-2" : undefined}
      />
    </SectionCard>
  );
}
