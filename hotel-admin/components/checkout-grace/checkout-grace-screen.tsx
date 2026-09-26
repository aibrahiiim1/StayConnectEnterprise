"use client";

// Checkout grace — the hotel's policy for guests who check out while still online.
//
// The screen answers three questions in order: is grace working and whose terms are in force (status strip and
// warnings); what exactly does a departing guest receive (the policy card, written as product facts rather than
// a form); and who changed it, when and why (the history). Editing happens in a sheet with a review step.
//
// "Nothing published" is not "nothing happening": a site with no policy still gives departing guests the
// built-in emergency terms, and the screen says so rather than showing an empty state.

import * as React from "react";
import Link from "next/link";
import {
  AlertTriangle,
  CalendarClock,
  Gauge,
  History,
  LogOut,
  Pencil,
  Plus,
  RefreshCw,
  ShieldCheck,
} from "lucide-react";
import { api } from "@/lib/api";
import {
  type GraceHistoryItem,
  type GraceHistoryResp,
  type GraceState,
  type GraceTerms,
  devicesSummary,
  fmtData,
  fmtDuration,
  fmtSpeed,
  fmtWhen,
  graceWarnings,
  guestReceivesSentence,
  termsFromEffective,
} from "@/lib/api/checkout-grace";
import { PageHeader, PageShell, StatCard } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { KeyValueGrid, MetricStrip } from "@/components/ui/data";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { EmptyState } from "@/components/ui/empty-state";
import { Skeleton } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { formatRelative } from "@/lib/utils";
import { GraceEditorSheet } from "./grace-editor-sheet";
import { GraceHistory } from "./grace-history";

export function CheckoutGraceScreen({ canWrite = true }: { canWrite?: boolean }) {
  const toast = useToast();
  const [state, setState] = React.useState<GraceState | null>(null);
  const [loadError, setLoadError] = React.useState<string | null>(null);
  const [history, setHistory] = React.useState<GraceHistoryItem[]>([]);
  /** false when the appliance cannot read the publication record. "No history" and "I cannot see the
   *  history" are different claims. */
  const [historyAvailable, setHistoryAvailable] = React.useState(true);
  const [editing, setEditing] = React.useState(false);
  const [justPublished, setJustPublished] = React.useState<number | null>(null);

  const load = React.useCallback(async () => {
    try {
      const s = await api.get<GraceState>("/checkout-grace");
      setState(s);
      setLoadError(null);
      // History is layered on and allowed to be missing: a policy screen that fails because a ledger read
      // failed would be a worse outage than the one it reports.
      try {
        const h = await api.get<GraceHistoryResp>("/checkout-grace/history");
        setHistory(h?.data ?? []);
        setHistoryAvailable(h?.available !== false);
      } catch {
        setHistory([]);
        setHistoryAvailable(false);
      }
    } catch (e: any) {
      setLoadError(
        e?.status === 404
          ? "Checkout grace is not enabled on this appliance, so there is no policy to show."
          : `The checkout grace policy could not be loaded${e?.message ? `: ${e.message}` : "."}`,
      );
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  const effective = state?.effective;
  const published = !!state?.published;
  const version = state?.config_version ?? 0;
  const isEmergency = effective?.source === "EMERGENCY_FALLBACK" || (!!state && !published);
  const current = history.find((h) => h.config_version === version);
  const baseTerms: GraceTerms | null = effective ? termsFromEffective(effective) : null;
  const warnings = state ? graceWarnings(state, history, historyAvailable) : [];
  const used = state?.emergency_history?.count ?? 0;
  const lastUsed = state?.emergency_history?.last_at;
  const supported = state?.supported_device_policies?.length ? state.supported_device_policies : ["REJECT_NEW_DEVICE"];

  const editLabel = published ? "Edit policy" : "Create hotel policy";

  async function onPublished(newVersion: number) {
    setEditing(false);
    setJustPublished(newVersion);
    toast.success("Checkout grace policy published", `Version ${newVersion} applies to future checkouts.`);
    // Re-read rather than patch locally: what is in force is the server's answer.
    await load();
  }

  return (
    <PageShell>
      <PageHeader
        icon={<LogOut />}
        eyebrow="Internet offering"
        title="Checkout grace"
        description="A short, capped period online after checkout."
        help={
          <>
            <HelpSection title="What checkout grace does">
              <p>
                Keeps a guest online for a short, capped period after they check out, so leaving the hotel does not
                cut them off mid-journey.
              </p>
            </HelpSection>
            <HelpSection title="Who qualifies">
              <p>
                Every guest who still has active internet access when they check out qualifies &mdash; free, paid
                or included with the room. A guest with no active access at checkout gets no grace. Each stay
                receives grace once.
              </p>
            </HelpSection>
            <HelpSection title="When no hotel policy is published">
              <p>
                &ldquo;Nothing published&rdquo; is not &ldquo;nothing happening&rdquo;: departing guests still get
                the built-in emergency terms, a safe default that was not chosen for this hotel. Each use of it
                raises a critical alert.
              </p>
            </HelpSection>
            <HelpSection title="Versions and history">
              <HelpList items={[
                "Every published version is kept, newest first. The record is append-only: publishing never rewrites what an earlier version promised.",
                "A change applies to future checkouts only. A guest already in grace keeps the exact terms they were given at checkout.",
                "Each version records who published it and why.",
              ]} />
            </HelpSection>
          </>
        }
        actions={
          canWrite ? (
            <Button onClick={() => setEditing(true)} disabled={!state}>
              {published ? <Pencil /> : <Plus />}
              {editLabel}
            </Button>
          ) : undefined
        }
      />

      {!canWrite && state && (
        <ReadOnlyNotice>Your role can view this policy but not change it.</ReadOnlyNotice>
      )}

      {loadError && (
        <div className="space-y-2">
          <ErrorBanner err={loadError} className="mb-0" />
          <Button variant="secondary" size="sm" onClick={() => void load()}>
            <RefreshCw /> Try again
          </Button>
        </div>
      )}

      {!state && !loadError && (
        <div className="space-y-5" aria-busy="true">
          <span className="sr-only">Loading the checkout grace policy</span>
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
            {[0, 1, 2, 3].map((i) => (
              <Skeleton key={i} className="h-24" />
            ))}
          </div>
          <Skeleton className="h-64" />
        </div>
      )}

      {state && (
        <>
          {justPublished !== null && (
            <Callout tone="success" title={`Version ${justPublished} published`}>
              It is now in force for future checkouts. Guests already in grace keep the terms they were given.
            </Callout>
          )}

          {/* Status strip: is grace working, and whose terms. */}
          <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
            <StatCard
              label="Policy in force"
              value={isEmergency ? "Emergency fallback" : "Hotel policy"}
              tone={isEmergency ? "warn" : "ok"}
              icon={isEmergency ? <AlertTriangle /> : <ShieldCheck />}
              hint={isEmergency ? "Built-in safe default — not chosen for this hotel" : "Terms this hotel published"}
            />
            <StatCard
              label="Published version"
              value={published ? `v${version}` : "None"}
              icon={<Gauge />}
              hint={published ? "Applies to every checkout from now on" : "No hotel policy published yet"}
            />
            <StatCard
              label="Last changed"
              value={
                current ? (
                  <time dateTime={current.published_at} title={fmtWhen(current.published_at)}>
                    {formatRelative(current.published_at)}
                  </time>
                ) : published ? (
                  "Unknown"
                ) : (
                  "Never"
                )
              }
              icon={<CalendarClock />}
              hint={
                current
                  ? `by ${current.actor}`
                  : !historyAvailable
                    ? "History cannot be read on this appliance"
                    : published
                      ? "Not in the publication record"
                      : "Publish a policy to start the record"
              }
            />
            <StatCard
              label="Emergency fallback used"
              value={used}
              tone={used > 0 ? "warn" : "default"}
              icon={<History />}
              hint={
                used > 0 && lastUsed ? (
                  <>
                    Most recently <span title={fmtWhen(lastUsed)}>{formatRelative(lastUsed)}</span> · each use raised a
                    critical alert
                  </>
                ) : (
                  "Never used on this appliance"
                )
              }
              href={used > 0 ? "/operational-alerts" : undefined}
            />
          </div>

          {warnings.length > 0 && (
            <section aria-label="Needs attention" className="space-y-2">
              {warnings.map((w) => (
                <Callout key={w.id} tone={w.tone} title={w.title}>
                  <p>{w.body}</p>
                  {w.href && (
                    <p>
                      <Link href={w.href} className="font-medium underline underline-offset-4">
                        Open operational alerts
                      </Link>
                    </p>
                  )}
                </Callout>
              ))}
            </section>
          )}

          {/* The policy, as product facts. */}
          {effective && baseTerms && (
            <Card>
              <CardHeader>
                <div className="min-w-0 space-y-0.5">
                  <CardTitle>What a departing guest receives</CardTitle>
                  <CardDescription>
                    {isEmergency
                      ? "The built-in emergency terms, because no hotel policy is published."
                      : `Hotel policy version ${version}.`}
                  </CardDescription>
                </div>
                {isEmergency ? (
                  <Badge tone="warn" dot>
                    Emergency fallback
                  </Badge>
                ) : (
                  <Badge tone="ok" dot>
                    Hotel policy · v{version}
                  </Badge>
                )}
              </CardHeader>
              <CardBody className="space-y-5">
                <p className="text-base leading-relaxed" data-testid="grace-sentence">
                  {guestReceivesSentence(baseTerms)}
                </p>

                <section aria-label="Grace allowance">
                  <MetricStrip
                    items={[
                      { label: "Grace time", value: fmtDuration(effective.duration_seconds) },
                      { label: "Download", value: fmtSpeed(effective.down_kbps) },
                      { label: "Upload", value: fmtSpeed(effective.up_kbps) },
                      { label: "Data allowance", value: fmtData(effective.data_quota_bytes) },
                    ]}
                  />
                </section>

                <section aria-label="Policy details">
                  <KeyValueGrid
                    items={[
                      {
                        label: "Who qualifies",
                        value: "Guests who still have active internet access when they check out",
                        hint: "Free, paid or included with the room alike. No active access at checkout means no grace. Once per stay.",
                      },
                      {
                        label: "Devices",
                        value: devicesSummary(effective.device_limit, effective.device_limit_policy),
                        hint:
                          effective.device_limit_policy === "REJECT_NEW_DEVICE"
                            ? "Devices online at checkout stay online, even above the limit. The limit never lets a new device join."
                            : undefined,
                      },
                      {
                        label: "Grace time is counted",
                        value: "On the clock from checkout",
                        hint: "Not online time: grace ends when the time runs out or the data allowance is used up.",
                      },
                      {
                        label: "Stay rules after checkout",
                        value: effective.eligibility_window_seconds
                          ? fmtDuration(effective.eligibility_window_seconds)
                          : "Not part of the emergency terms",
                        hint: "How long after checkout the stay still counts for stay-based package rules. It never removes grace from a guest who qualifies.",
                      },
                      {
                        label: "Delivered as",
                        value: isEmergency
                          ? "Built-in emergency terms (no hotel package)"
                          : "Checkout grace package — free, built automatically from this policy",
                        hint: isEmergency ? undefined : "No payment is taken and the package is not offered for sale.",
                      },
                      {
                        label: "Changes apply to",
                        value: "Future checkouts only",
                        hint: "A guest already in grace keeps the exact terms they were given at checkout.",
                      },
                    ]}
                  />
                </section>
              </CardBody>
            </Card>
          )}

          <Card>
            <CardHeader>
              <div className="min-w-0 space-y-0.5">
                <CardTitle>Policy history</CardTitle>
                <CardDescription>Every published version, newest first.</CardDescription>
              </div>
              {historyAvailable && history.length > 0 && (
                <Badge tone="neutral">
                  {history.length} version{history.length === 1 ? "" : "s"}
                </Badge>
              )}
            </CardHeader>
            <CardBody>
              {!historyAvailable ? (
                <EmptyState
                  icon={<History />}
                  title="History cannot be read on this appliance"
                  hint="The record exists, but this appliance's admin service cannot read it, so versions are not listed here."
                />
              ) : history.length === 0 ? (
                <EmptyState
                  icon={<History />}
                  title="No version published yet"
                  hint={
                    canWrite
                      ? "Create the hotel's policy to replace the emergency fallback. Each version you publish is recorded here with who published it and why."
                      : "When a manager publishes the hotel's policy, each version is recorded here."
                  }
                  action={
                    canWrite && !published ? (
                      <Button variant="secondary" onClick={() => setEditing(true)}>
                        <Plus /> Create hotel policy
                      </Button>
                    ) : undefined
                  }
                />
              ) : (
                <GraceHistory history={history} inForce={version} />
              )}
            </CardBody>
          </Card>

          {baseTerms && (
            <GraceEditorSheet
              open={editing}
              onOpenChange={setEditing}
              base={baseTerms}
              baseLabel={isEmergency ? "the emergency fallback" : `version ${version}`}
              published={published}
              version={version}
              supportedPolicies={supported}
              canWrite={canWrite}
              onPublished={onPublished}
              reload={load}
            />
          )}
        </>
      )}
    </PageShell>
  );
}
