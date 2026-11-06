"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { AlertTriangle, BellRing, CheckCheck, RefreshCw } from "lucide-react";
import { api, ListResp, OperationalAlert, StayDetail } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { Card } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/dialog";
import { RowActions } from "@/components/ui/row-actions";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout } from "@/components/ui/error-banner";
import { SkeletonRows } from "@/components/ui/misc";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { cn, formatDate, formatRelative } from "@/lib/utils";
import { HelpList, HelpSection } from "@/components/help";

const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";
const LINK = "font-medium underline underline-offset-2";
const LIMIT = 200;
const STAY_LOOKUPS = 50;

const ALERT_WORDS: Record<string, string> = {
  CHECKOUT_GRACE_CONFIG_INVALID: "Emergency after check-out terms used",
};
const TRIGGER_WORDS: Record<string, string> = {
  EMERGENCY_GRACE: "Emergency terms",
  CHECKOUT_GRACE: "After check-out access",
  NO_GRACE: "No access given",
};
const REASON_WORDS: Record<string, string> = {
  CONFIG_INVALID_EMERGENCY_FALLBACK: "The After Check-out Access settings were invalid",
};

const alertWords = (c: string) => ALERT_WORDS[c] ?? "Not recognised";
const triggerWords = (c: string) => TRIGGER_WORDS[c] ?? "Not recognised";
const reasonWords = (c?: string | null) => (c ? REASON_WORDS[c] ?? "Not recognised" : "None recorded");

const stateTone = (s: string) => (s === "ACKNOWLEDGED" ? "info" : "warn");
const stateWord = (s: string) => (s === "ACKNOWLEDGED" ? "Acknowledged" : s === "OPEN" ? "Open" : "Not recognised");

type StayInfo = { room?: string | null; reservation?: string | null } | null;

function failure(e: unknown): { status?: number; code: string } {
  const x = e as { status?: number; code?: string } | null;
  return { status: x?.status, code: typeof x?.code === "string" ? x.code : "" };
}

function actionError(e: unknown): string {
  const { code } = failure(e);
  if (code === "not_found") return "This alert was already resolved. The queue has been refreshed.";
  if (code === "actor_invalid") return "Your account could not be recorded against this action. Sign in again, then retry. Nothing was changed.";
  return "The action could not be completed. Check the appliance is reachable, then try again. Nothing was changed.";
}

function WhatToDo({ a }: { a: OperationalAlert }) {
  if (a.alert_code === "CHECKOUT_GRACE_CONFIG_INVALID") {
    return (
      <>
        The guest got the emergency terms because the settings were invalid at check-out. Correct them on{" "}
        <Link href="/after-checkout-access" className={LINK}>After Check-out Access</Link>.
      </>
    );
  }
  return <>Check the stay on <Link href="/stays" className={LINK}>Stays</Link>.</>;
}

export function OperationalAlertsView({
  canAct = true,
  readOnly = false,
}: {
  canAct?: boolean;
  readOnly?: boolean;
}) {
  const toast = useToast();
  const [rows, setRows] = useState<OperationalAlert[] | null>(null);
  const [failed, setFailed] = useState(false);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [stays, setStays] = useState<Record<string, StayInfo>>({});
  const [resolving, setResolving] = useState<OperationalAlert | null>(null);
  const [resolveErr, setResolveErr] = useState<string | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const r = await api.get<ListResp<OperationalAlert>>("/operational-alerts");
      setRows(r.data ?? []);
      setFailed(false);
    } catch {
      setFailed(true);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (!rows) return;
    const wanted = [...new Set(rows.map((a) => a.stay_id))].filter((id) => !(id in stays)).slice(0, STAY_LOOKUPS);
    if (wanted.length === 0) return;
    let live = true;
    void Promise.all(
      wanted.map(async (id) => {
        try {
          const s = await api.get<StayDetail>(`/pms-stays/${id}`);
          return [id, { room: s?.room ?? null, reservation: s?.external_reservation_id ?? null }] as const;
        } catch {
          return [id, null] as const;
        }
      }),
    ).then((pairs) => {
      if (live) setStays((cur) => ({ ...cur, ...Object.fromEntries(pairs) }));
    });
    return () => { live = false; };
  }, [rows, stays]);

  function stayWords(a: OperationalAlert): string {
    const s = stays[a.stay_id];
    if (s === undefined) return "";
    return s?.room ? `Room ${s.room}` : "Room not known";
  }

  async function act(a: OperationalAlert, action: "acknowledge" | "resolve"): Promise<"ok" | "conflict" | "error"> {
    setBusy(a.audit_id + action);
    setErr(null);
    setNote(null);
    try {
      await api.post("/operational-alerts/" + a.audit_id + "/" + action, {
        expected_state: a.state,
        reason_code: action === "resolve" ? "OPERATOR_RESOLVED" : "OPERATOR_ACKNOWLEDGED",
      });
      toast.success(action === "resolve" ? "Alert resolved" : "Alert acknowledged");
      await load();
      return "ok";
    } catch (e: unknown) {
      const f = failure(e);
      if (f.status === 409 && f.code === "state_conflict") {
        setNote("This alert changed while you were looking at it. The queue has been refreshed.");
        await load();
        return "conflict";
      } else {
        const words = actionError(e);
        if (action === "resolve" && resolving) setResolveErr(words);
        else setErr(words);
        if (f.code === "not_found") await load();
      }
      return "error";
    } finally {
      setBusy(null);
    }
  }

  async function confirmResolve() {
    if (!resolving) return;
    setResolveErr(null);
    const result = await act(resolving, "resolve");
    if (result !== "error") setResolving(null);
  }

  function openResolve(a: OperationalAlert) {
    setResolveErr(null);
    setResolving(a);
  }

  const subject = (a: OperationalAlert) => {
    const where = stayWords(a);
    return `alert ${alertWords(a.alert_code)}${where ? `, ${where}` : ""}`;
  };

  return (
    <PageShell width="wide">
      <PageHeader
        title="Alerts"
        description="Checkouts the configured policy could not handle on its own."
        actions={rows ? (
          <Button size="sm" variant="secondary" onClick={() => void load()} disabled={loading}>
            <RefreshCw className={cn(loading && "animate-spin motion-reduce:animate-none")} /> Refresh
          </Button>
        ) : undefined}
        help={
          <>
            <HelpSection title="What raises an alert">
              <p>
                A checkout situation the configured policy could not handle on its own — for example when the
                emergency After Check-out Access terms were used.
              </p>
            </HelpSection>
            <HelpSection title="Working the queue">
              <HelpList
                items={[
                  <><strong>Acknowledge</strong> — you have seen it and are dealing with it. It stays in the queue.</>,
                  <><strong>Resolve</strong> — it is handled. Resolved alerts leave the queue.</>,
                  "If someone else changed an alert while you were looking at it, your action is refused and the queue refreshes so you see the current state.",
                ]}
              />
            </HelpSection>
            <HelpSection title="Clock suspect">
              <p>
                The appliance&rsquo;s clock may not have been trustworthy at the boundary time, so treat that time with
                care.
              </p>
            </HelpSection>
          </>
        }
      />

      {readOnly && <ReadOnlyNotice>Your role can view alerts but not acknowledge or resolve them.</ReadOnlyNotice>}

      {err && (
        <div className={FADE}>
          <Callout tone="danger">{err}</Callout>
        </div>
      )}
      {failed && rows && (
        <div className={FADE}>
          <Callout tone="warning">The queue could not be refreshed. What is shown may be out of date.</Callout>
        </div>
      )}
      {note && (
        <div className={FADE}>
          <Callout tone="warning">{note}</Callout>
        </div>
      )}

      <Card className="overflow-hidden">
        {rows === null ? (
          failed ? (
            <div role="alert" className={FADE}>
              <EmptyState
                size="sm"
                icon={<AlertTriangle />}
                title="Alerts could not be loaded"
                hint="Check the appliance is reachable, then try again. Nothing has been changed."
                action={
                  <Button size="sm" variant="secondary" onClick={() => void load()} disabled={loading}>
                    <RefreshCw className={cn(loading && "animate-spin motion-reduce:animate-none")} /> Try again
                  </Button>
                }
              />
            </div>
          ) : (
            <SkeletonRows rows={3} cols={5} />
          )
        ) : rows.length === 0 ? (
          <EmptyState
            size="sm"
            icon={<CheckCheck />}
            title="No open alerts"
            hint="Every checkout was handled with the configured policy."
          />
        ) : (
          <>
            <Table label="Alerts" aria-label="Alerts">
              <THead>
                <TR>
                  <TH>Alert</TH>
                  <TH>State</TH>
                  <TH className="hidden md:table-cell">What happened</TH>
                  <TH className="hidden lg:table-cell">What to do</TH>
                  <TH className="hidden sm:table-cell">Checked out</TH>
                  <TH className="hidden sm:table-cell">Raised</TH>
                  {canAct && <TH><span className="sr-only">Actions</span></TH>}
                </TR>
              </THead>
              <TBody>
                {rows.map((a) => {
                  const s = stays[a.stay_id];
                  return (
                    <TR key={a.audit_id}>
                      <TD>
                        <div className="font-medium">{alertWords(a.alert_code)}</div>
                        {s !== undefined && (
                          <div className={cn("text-caption text-muted-foreground", FADE)}>
                            {s?.room ? `Room ${s.room}` : "Room not known"}
                            {s?.reservation ? ` · Reservation ${s.reservation}` : ""}
                          </div>
                        )}
                        <div className="text-caption text-muted-foreground sm:hidden">
                          Raised {formatRelative(a.created_at)}
                        </div>
                        <div className="mt-1 text-caption lg:hidden"><WhatToDo a={a} /></div>
                      </TD>
                      <TD nowrap>
                        <StatusBadge tone={stateTone(a.state)}>{stateWord(a.state)}</StatusBadge>
                        {a.state === "ACKNOWLEDGED" && a.state_changed_at && (
                          <div className="mt-1 text-caption text-muted-foreground" title={formatDate(a.state_changed_at)}>
                            {formatRelative(a.state_changed_at)}
                          </div>
                        )}
                      </TD>
                      <TD className="hidden md:table-cell">
                        <div>{triggerWords(a.trigger)}</div>
                        <div className="text-caption text-muted-foreground">{reasonWords(a.reason_code)}</div>
                      </TD>
                      <TD className="hidden max-w-[22rem] text-sm lg:table-cell"><WhatToDo a={a} /></TD>
                      <TD nowrap className="hidden sm:table-cell">
                        <span title={formatDate(a.boundary_at)}>{formatRelative(a.boundary_at)}</span>
                        {a.boundary_clock_suspect && (
                          <>
                            {" "}
                            <Badge tone="warn" title="The appliance's clock may not have been trustworthy at this time, so treat it with care.">
                              Time may be wrong
                            </Badge>
                          </>
                        )}
                      </TD>
                      <TD nowrap muted className="hidden sm:table-cell">
                        <span title={formatDate(a.created_at)}>{formatRelative(a.created_at)}</span>
                      </TD>
                      {canAct && (
                        <TD className="w-0">
                          <RowActions
                            subject={subject(a)}
                            primary={a.state === "OPEN"
                              ? { label: "Acknowledge", disabled: busy === a.audit_id + "acknowledge", onSelect: () => void act(a, "acknowledge") }
                              : { label: "Resolve", disabled: busy === a.audit_id + "resolve", onSelect: () => openResolve(a) }}
                            items={[{
                              label: "Resolve",
                              hidden: a.state !== "OPEN",
                              disabled: busy === a.audit_id + "resolve",
                              onSelect: () => openResolve(a),
                            }]}
                          />
                        </TD>
                      )}
                    </TR>
                  );
                })}
              </TBody>
            </Table>
            {rows.length >= LIMIT && (
              <p className="border-t border-border px-4 py-2 text-caption text-muted-foreground">Showing the latest {LIMIT.toLocaleString()}.</p>
            )}
          </>
        )}
      </Card>

      <ConfirmDialog
        open={resolving !== null}
        onOpenChange={(v) => { if (!v) { setResolving(null); setResolveErr(null); } }}
        title="Resolve this alert?"
        description={resolving ? `${alertWords(resolving.alert_code)}${stayWords(resolving) ? `, ${stayWords(resolving)}` : ""}.` : undefined}
        consequences={["It leaves the queue and cannot be reopened."]}
        confirmLabel="Resolve"
        busy={resolving !== null && busy === resolving.audit_id + "resolve"}
        error={resolveErr}
        onConfirm={confirmResolve}
      />
    </PageShell>
  );
}
