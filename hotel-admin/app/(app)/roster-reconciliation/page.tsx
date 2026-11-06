"use client";

import { usePmsConnections } from "@/lib/pms-scope";
import { useCallback, useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { DialogForm } from "@/components/ui/dialog";
import { Field, FieldGrid, Input, Select } from "@/components/ui/input";
import { useOperatorRoles } from "@/lib/whoami-context";
import { canWrite } from "@/lib/roles";
import { useToast } from "@/components/ui/toast";
import Link from "next/link";
import { History, PlugZap, RefreshCw } from "lucide-react";
import { api, RosterReconciliationState, ReconcileRunRecord, ReconcileRun } from "@/lib/api";
import { cn, formatDate } from "@/lib/utils";
import { PageShell, PageHeader, StatCard, StatGrid } from "@/components/ui/page";
import { HelpList, HelpSection, HelpTip } from "@/components/help";
import { Card, SectionCard } from "@/components/ui/card";
import { Table, TBody, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { DescriptionList } from "@/components/ui/data";
import { DetailsDrawer } from "@/components/ui/details-drawer";
import { Skeleton } from "@/components/ui/misc";
import { LiveStatus, refreshingClass } from "@/components/ui/patterns";
import { FADE, LoadFailure, StaleNotice } from "@/app/(app)/backups/load-failure";

const REFUSAL_MEANING: Record<string, string> = {
  REFUSED_ROSTER_INCOMPLETE:
    "The latest sweep did not name the whole site, so it is a partial answer. A partial answer must never be read as “the guests it missed have left”.",
  REFUSED_GENERATION_NOT_LATEST:
    "A newer roster exists. Acting on a superseded one would close guests who have arrived since it was taken.",
  REFUSED_GENERATION_UNPUBLISHED:
    "The most recent resync never completed, so there is no full statement of who is in the building.",
  REFUSED_LINK_NOT_HEALTHY:
    "The PMS link is faulted or resyncing. The connector is saying it no longer trusts its own picture, which is not a moment to act on it.",
  REFUSED_SCOPE_MISMATCH:
    "The interface does not belong to this site. Nothing was read or changed.",
  REFUSED_ROSTER_TOO_SMALL:
    "The roster is below the configured floor. This is a backstop against an obviously broken response.",
  REFUSED_CAP_EXCEEDED:
    "More stays would close than one run is allowed to close. This is deliberate: a surprise stops for a person.",
};

const OUTCOME_WORDS: Record<string, string> = {
  COMPLETED: "Completed",
  REFUSED_ROSTER_INCOMPLETE: "Refused: the roster did not cover the site",
  REFUSED_GENERATION_NOT_LATEST: "Refused: a newer roster exists",
  REFUSED_GENERATION_UNPUBLISHED: "Refused: the last resync did not complete",
  REFUSED_LINK_NOT_HEALTHY: "Refused: the PMS link is not healthy",
  REFUSED_SCOPE_MISMATCH: "Refused: the connection is not this site's",
  REFUSED_ROSTER_TOO_SMALL: "Refused: the roster is below the smallest acted on",
  REFUSED_CAP_EXCEEDED: "Refused: more stays than one run may check out",
};
const outcomeWords = (o: string) => OUTCOME_WORDS[o] ?? (o === "COMPLETED" ? "Completed" : "Refused");

const BLOCKER_WORDS: Record<string, string> = {
  LINK_DOWN: "The PMS link is down",
  RECONCILIATION_BLOCKED: "Reconciliation is paused",
  DEPARTURE_FOR_UNKNOWN_STAY: "A departure for a stay this appliance never received",
};
const blockerWords = (b: string) => BLOCKER_WORDS[b] ?? "Something needs attention";

const NOTHING_CHANGED = "Nothing was changed.";
const CODE_WORDS: Record<string, string> = {
  reauth_required: `Your password was not accepted. ${NOTHING_CHANGED}`,
  version_conflict: `The settings changed since you opened them. Reload them and try again. ${NOTHING_CHANGED}`,
  reason_required: `Give a reason of 3 to 500 characters. ${NOTHING_CHANGED}`,
  expected_version_required: `Reload the settings and try again. ${NOTHING_CHANGED}`,
  reconcile_failed: `The dry run could not be completed. Try again in a moment. ${NOTHING_CHANGED}`,
  no_interface_runtime: `No PMS connection is running for this site, so a dry run cannot be made. ${NOTHING_CHANGED}`,
  not_found: `That PMS connection is no longer on this site. Reload the page. ${NOTHING_CHANGED}`,
};

function rosterError(e: unknown, fallback: string): string {
  const o = (e ?? {}) as { status?: unknown; code?: unknown; message?: unknown };
  const code = typeof o.code === "string" ? o.code : "";
  const text = typeof o.message === "string" ? o.message.trim() : "";
  if (CODE_WORDS[code]) return CODE_WORDS[code];
  if (/^[A-Z]/.test(text) && /[.!?]$/.test(text)) return /nothing/i.test(text) ? text : `${text} ${NOTHING_CHANGED}`;
  if (o.status === 429) return `Too many attempts. Wait a minute, then try again. ${NOTHING_CHANGED}`;
  return fallback;
}

type Read<T> = { data: T | null; failed: boolean };
type Outcome<T> = { ok: true; value: T } | { ok: false; notOn: boolean };

async function readOne<T>(path: string): Promise<Outcome<T>> {
  try {
    return { ok: true, value: await api.get<T>(path) };
  } catch (e) {
    return { ok: false, notOn: (e as { status?: unknown } | null)?.status === 404 };
  }
}

const timeWords = (at: number) => new Date(at).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });

export default function RosterReconciliationPage() {
  const [state, setState] = useState<Read<RosterReconciliationState>>({ data: null, failed: false });
  const [runs, setRuns] = useState<Read<ReconcileRunRecord[]>>({ data: null, failed: false });
  const [notOn, setNotOn] = useState(false);
  const [busy, setBusy] = useState(false);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [iface, setIface] = useState("");
  const [dry, setDry] = useState<{ run: ReconcileRun; at: number } | null>(null);
  const [dryBusy, setDryBusy] = useState(false);
  const [dryErr, setDryErr] = useState<string | null>(null);
  const [runOpen, setRunOpen] = useState<ReconcileRunRecord | null>(null);
  const { multi } = usePmsConnections();
  const roles = useOperatorRoles();
  const writable = roles ? canWrite("pms-roster-reconciliation", roles) : false;

  const load = useCallback(async () => {
    setBusy(true);
    const q = iface ? "?pms_interface_id=" + encodeURIComponent(iface) : "";
    const [s, r] = await Promise.all([
      readOne<RosterReconciliationState>("/pms-roster-reconciliation" + q),
      readOne<{ runs: ReconcileRunRecord[] }>("/pms-roster-reconciliation/runs" + q),
    ]);
    setNotOn(!s.ok && s.notOn && !r.ok && r.notOn);
    if (s.ok) setState({ data: s.value, failed: false });
    else setState((prev) => ({ data: prev.data, failed: true }));
    if (r.ok) setRuns({ data: r.value.runs ?? [], failed: false });
    else setRuns((prev) => ({ data: prev.data, failed: true }));
    if (s.ok && r.ok) setUpdatedAt(Date.now());
    setBusy(false);
  }, [iface]);

  useEffect(() => { void load(); }, [load]);

  const st = state.data;

  async function runDry() {
    setDryBusy(true);
    setDryErr(null);
    try {
      const out = await api.post<ReconcileRun>("/pms-roster-reconciliation/preview", {
        pms_interface_id: st?.pms_interface_id, reason: "dry run requested from the console",
      });
      setDry({ run: out, at: Date.now() });
      await load();
    } catch (e) {
      setDryErr(rosterError(e, `The dry run could not be completed. Try again in a moment. ${NOTHING_CHANGED}`));
    } finally {
      setDryBusy(false);
    }
  }

  const operational = (st?.blockers ?? []).filter((b) => b.blocker !== "DEPARTURE_FOR_UNKNOWN_STAY");
  const historical = (st?.blockers ?? []).filter((b) => b.blocker === "DEPARTURE_FOR_UNKNOWN_STAY");

  const p = dry?.run ?? st?.last_run ?? st?.preview ?? null;
  const complete = p && st ? p.rooms_expected > 0 && p.rooms_enumerated >= p.rooms_expected - st.settings.inventory_tolerance : false;
  const refused = p && p.outcome !== "COMPLETED";
  const stale = (state.failed && st !== null) || (runs.failed && runs.data !== null);
  const known = (v: number | undefined | null) => (v === undefined || v === null ? "Not known" : v);

  const help = (
    <>
      <HelpSection title="What reconciliation does">
        <p>
          The site&rsquo;s PMS sends this appliance a full list of who is in the building, many times a day.
          Reconciliation compares that list with the guest list this appliance is using and closes any stay
          the PMS no longer has. That is how somebody who checked out stops being able to sign in.
        </p>
      </HelpSection>
      <HelpSection title="This happens on its own">
        <p>
          There is no queue to work through and no routine task. Everything here is so you can see what it
          did and why. Opening the page changes nothing; <strong>Run dry run</strong> records what a run would
          do now, without closing any stay. The connection&rsquo;s recovery numbers are changed on PMS
          connection, under Advanced configuration.
        </p>
        <p>
          A stay the PMS no longer lists is checked out exactly like a departure the PMS sends: its internet
          access ends (or moves to After Check-out Access) and its devices are disconnected.
        </p>
        <p>
          Guests are never affected while it waits: if the PMS list is incomplete or the link is down, the
          appliance keeps using the last good list rather than guessing, and says so under Needs attention.
        </p>
      </HelpSection>
      <HelpSection title="Reading the figures">
        <HelpList
          items={[
            <><strong>Rooms named by this sweep:</strong> how much of the site the PMS roster covered. An incomplete sweep makes the run refuse.</>,
            <><strong>Held back, the PMS spoke since:</strong> stays absent from the roster that are not closed, because the PMS has said something about them after the snapshot. Arrivals and changes after the snapshot are never closed.</>,
            <><strong>Every run, including the refusals:</strong> a run is recorded each time the PMS publishes a complete guest list.</>,
          ]}
        />
      </HelpSection>
    </>
  );

  if (notOn) {
    return (
      <PageShell>
        <PageHeader title="Roster reconciliation" description="Keeps this appliance's guest list identical to the PMS's, automatically." help={help} />
        <Card className={FADE}>
          <EmptyState
            size="sm"
            icon={<PlugZap />}
            title="PMS administration is not turned on for this appliance"
            hint="So there is no roster to reconcile here."
          />
        </Card>
      </PageShell>
    );
  }

  return (
    <PageShell>
      <PageHeader
        title="Roster reconciliation"
        description="Keeps this appliance's guest list identical to the PMS's, automatically."
        help={help}
        actions={<LiveStatus updatedAt={updatedAt} refreshing={busy} error={stale} onRefresh={() => void load()} />}
      />

      {stale && <StaleNotice what="Part of this page" />}

      <div className={cn("space-y-5", busy && st !== null && refreshingClass)}>
      {st === null && !state.failed && (
        <StatGrid cols={4}>
          <Skeleton className="h-[5.5rem]" /><Skeleton className="h-[5.5rem]" /><Skeleton className="h-[5.5rem]" /><Skeleton className="h-[5.5rem]" />
        </StatGrid>
      )}

      {st === null && state.failed && (
        <Card>
          <LoadFailure what="The reconciliation state" busy={busy} onRetry={() => void load()} />
        </Card>
      )}

      {operational.length > 0 && (
        <section aria-label="Needs attention" className="space-y-2">
          {operational.map((b, i) => (
            <Callout key={i} tone="warning" title="Needs attention">
              <div className="flex flex-wrap items-center gap-2">
                <Badge tone="warn">{blockerWords(b.blocker)}</Badge>
                {b.since && <span className="text-xs">since {formatDate(b.since)}</span>}
                <StatusBadge tone={b.guests_affected ? "err" : "ok"}>
                  {b.guests_affected ? "Guests affected" : "Guests not affected"}
                </StatusBadge>
              </div>
              <p className="mt-1.5">{b.detail}</p>
            </Callout>
          ))}
          <p className="text-xs text-muted-foreground">
            These clear themselves when the condition ends. There is nothing to acknowledge.
          </p>
        </section>
      )}

      {historical.length > 0 && (
        <SectionCard
          id="historical-exception"
          title="Historical exception, for information"
          actions={
            <HelpTip title="Historical exception">
              <HelpSection title="What this is">
                <p>
                  When this appliance was first connected it joined a site that was already running, and its
                  first roster sweeps did not yet cover every room. A guest checked out during that window, so
                  the PMS announced a departure for a stay this appliance had never been told about.
                </p>
                <p>
                  Nobody is affected. No guest is online because of it, no stay is held open by it, and it will
                  not grow: the connector has covered the whole site on every sweep since.
                </p>
              </HelpSection>
              <HelpSection title="What to do about it">
                <p>
                  It stays on this list because closing it locally would mean inventing the arrival that was
                  never received, and this system does not invent guest records. There are exactly two ways it
                  ends: ask the site&rsquo;s PMS whether that reservation existed, or decide to leave it as a
                  known gap from the appliance&rsquo;s first days. Either is a legitimate answer; doing nothing
                  is also safe.
                </p>
              </HelpSection>
            </HelpTip>
          }
          bodyClassName="divide-y divide-border py-0"
        >
          {historical.map((b, i) => (
            <div key={i} className="py-3">
              <div className="flex flex-wrap items-center gap-2">
                <Badge tone="neutral">{blockerWords(b.blocker)}</Badge>
                {b.since && <span className="text-xs text-muted-foreground">recorded {formatDate(b.since)}</span>}
                <StatusBadge tone="ok">Guests not affected</StatusBadge>
              </div>
              <p className="mt-1.5 text-sm">{b.detail}</p>
            </div>
          ))}
        </SectionCard>
      )}

      {(st?.interfaces?.length ?? 0) > 1 && (
        <Field label="PMS connection" hint="Each connection has its own guest list and its own runs." className="max-w-sm">
          <Select value={iface || st?.pms_interface_id || ""} onChange={(e) => { setDry(null); setIface(e.target.value); }}>
            {st!.interfaces!.map((i) => (
              <option key={i.id} value={i.id}>
                {(i.label || i.connector_kind) + (i.lifecycle_state !== "ACTIVE" ? ` (${i.lifecycle_state.toLowerCase()})` : "")}
              </option>
            ))}
          </Select>
        </Field>
      )}

      {st && (
        <div className={cn("space-y-5", FADE)}>
          <StatGrid cols={4}>
            <StatCard
              label="Rooms named by this sweep"
              value={p ? `${p.rooms_enumerated} of ${p.rooms_expected}` : "Not known"}
              tone={p ? (complete ? "ok" : "warn") : "default"}
              hint={p ? (complete ? "The sweep covered the site" : "Incomplete: the run will refuse") : undefined}
            />
            <StatCard label="Occupied per the PMS" value={known(p?.roster_size)} hint={`Guest-list refresh ${st.generation ? `#${st.generation}` : "not recorded"}`} />
            <StatCard label="In house per this appliance" value={known(p?.mirror_in_house)} />
            <StatCard
              label="Held back, the PMS spoke since"
              value={known(p?.protected_by_newer_events)}
              hint="Later arrivals are never closed"
            />
          </StatGrid>

          <SectionCard
            title={dry ? `What a run would do now (dry run at ${timeWords(dry.at)})` : "What the last run decided"}
            actions={p ? <StatusBadge tone={refused ? "warn" : "ok"}>{outcomeWords(p.outcome)}</StatusBadge> : undefined}
            bodyClassName="space-y-3"
            footer={
              <div className="flex w-full flex-wrap items-center justify-between gap-3">
                <p className="text-xs text-muted-foreground">
                  This happens on its own when the PMS publishes a complete roster. Nothing here needs pressing.
                </p>
                <div className="flex flex-wrap gap-2">
                  {dry && (
                    <Button size="sm" variant="ghost" onClick={() => setDry(null)}>Show the last run</Button>
                  )}
                  {writable && (
                    <Button size="sm" variant="secondary" loading={dryBusy} onClick={() => void runDry()}>
                      Run dry run
                    </Button>
                  )}
                </div>
              </div>
            }
          >
            <div key={dry ? `dry-${dry.at}` : "last"} className={cn("space-y-3", FADE)}>
              <ErrorBanner err={dryErr} />
              {!p && (
                <p className="text-sm text-muted-foreground">
                  No run has been recorded for this connection yet. One is recorded each time the PMS publishes a
                  complete guest list.
                </p>
              )}
              {!dry && st.last_run_at && (
                <p className="text-xs text-muted-foreground">
                  {st.last_run_mode === "APPLY" ? "Automatic run" : "Dry run"} recorded {formatDate(st.last_run_at)}.
                </p>
              )}
              {refused && p && (
                <p className="text-sm text-muted-foreground">
                  {REFUSAL_MEANING[p.outcome] ?? "The run refused, and nothing was changed."}
                </p>
              )}
              {!refused && p && (
                <p className="text-sm">
                  <strong>{p.absent_from_roster}</strong> stay{p.absent_from_roster === 1 ? "" : "s"} absent from a
                  roster that named {p.rooms_enumerated} of {p.rooms_expected} rooms
                  {!dry && st.last_run_mode === "APPLY" ? `: ${p.stays_closed} sent to check-out` : " would be checked out"}
                  . {p.protected_by_newer_events} further stay
                  {p.protected_by_newer_events === 1 ? " is" : "s are"} absent but held back, because the PMS has
                  said something about {p.protected_by_newer_events === 1 ? "it" : "them"} since the snapshot.
                </p>
              )}
            </div>
          </SectionCard>

          <RosterSettingsCard settings={st.settings} writable={writable} onSaved={() => void load()} />

          <SectionCard title="Historical roster-snapshot artifacts">
            {st.undisposed_cases === 0 ? (
              <p className="text-sm text-muted-foreground">
                None outstanding. The recorded roster snapshots were answered as what they actually were: a sweep
                mentioning empty rooms, never an announced departure. No stay changed state and no record was
                deleted. New ones cannot arrive: the connector no longer admits them.
              </p>
            ) : (
              <p className="text-sm">
                <strong>{st.undisposed_cases}</strong> recorded roster snapshot
                {st.undisposed_cases === 1 ? "" : "s"} still to answer. They are answered automatically;
                nothing needs doing here.
              </p>
            )}
          </SectionCard>
        </div>
      )}

      <SectionCard flush title="Every run, including the refusals" description={multi ? (iface ? "Runs of the selected PMS connection." : "Runs of every PMS connection.") : undefined}>
        {runs.data === null ? (
          runs.failed
            ? <LoadFailure what="The run history" busy={busy} onRetry={() => void load()} />
            : <div className="space-y-2 p-4" aria-hidden><Skeleton className="h-4 w-2/3" /><Skeleton className="h-4 w-1/2" /></div>
        ) : runs.data.length === 0 ? (
          <div className={FADE}>
            <EmptyState size="sm" icon={<History />} title="No runs yet" hint="A run is recorded each time the PMS publishes a complete guest list." />
          </div>
        ) : (
          <div className={FADE}>
            <Table label="Every run, including the refusals" stickyHeader maxHeight="32rem">
              <THead>
                <TR>
                  <TH>When</TH>
                  {multi && <TH>PMS</TH>}
                  <TH className="hidden sm:table-cell">Mode</TH>
                  <TH>Outcome</TH>
                  <TH numeric className="hidden md:table-cell">Rooms</TH>
                  <TH numeric className="hidden md:table-cell">Absent</TH>
                  <TH numeric>Checked out</TH>
                  <TH numeric className="hidden 2xl:table-cell">Held back</TH>
                  <TH className="hidden 2xl:table-cell">By</TH>
                  <TH className="hidden xl:table-cell">Reason</TH>
                </TR>
              </THead>
              <TBody>
                {runs.data.map((r, i) => (
                  <TR key={i} onClick={() => setRunOpen(r)} selected={runOpen === r}>
                    <TD nowrap>{formatDate(r.run_at)}</TD>
                    {multi && <TD nowrap>{r.pms_interface_label || "Not recorded"}</TD>}
                    <TD nowrap className="hidden sm:table-cell">{r.mode === "APPLY" ? "Applied" : "Dry run"}</TD>
                    <TD nowrap>
                      <StatusBadge tone={r.outcome === "COMPLETED" ? "ok" : "warn"}>{outcomeWords(r.outcome)}</StatusBadge>
                    </TD>
                    <TD numeric className="hidden md:table-cell">{r.rooms_enumerated}/{r.rooms_expected}</TD>
                    <TD numeric className="hidden md:table-cell">{r.absent_from_roster}</TD>
                    <TD numeric>{r.stays_closed}</TD>
                    <TD numeric className="hidden 2xl:table-cell">{r.protected_by_newer_events}</TD>
                    <TD muted truncate title={r.run_by} className="hidden max-w-[12rem] 2xl:table-cell">{r.run_by}</TD>
                    <TD muted truncate title={r.reason} className="hidden max-w-[12rem] xl:table-cell">{r.reason}</TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          </div>
        )}
      </SectionCard>

      <DetailsDrawer
        open={runOpen !== null}
        onOpenChange={(v) => !v && setRunOpen(null)}
        eyebrow="Reconciliation run"
        title={runOpen ? formatDate(runOpen.run_at) : ""}
        badges={runOpen ? (
          <StatusBadge tone={runOpen.outcome === "COMPLETED" ? "ok" : "warn"}>{outcomeWords(runOpen.outcome)}</StatusBadge>
        ) : undefined}
      >
        {runOpen && (
          <DescriptionList
            items={[
              { label: "Mode", value: runOpen.mode === "APPLY" ? "Applied" : "Dry run" },
              { label: "Rooms read", value: `${runOpen.rooms_enumerated} of ${runOpen.rooms_expected}` },
              { label: "Absent from the roster", value: String(runOpen.absent_from_roster) },
              { label: "Checked out", value: String(runOpen.stays_closed) },
              { label: "Held back by newer PMS events", value: String(runOpen.protected_by_newer_events) },
              { label: "By", value: runOpen.run_by || "Not recorded" },
              { label: "Reason", value: runOpen.reason || "Not recorded" },
            ]}
          />
        )}
      </DetailsDrawer>
      </div>
    </PageShell>
  );
}

function RosterSettingsCard({
  settings, writable, onSaved,
}: {
  settings: RosterReconciliationState["settings"];
  writable: boolean;
  onSaved: () => void;
}) {
  const toast = useToast();
  const [open, setOpen] = useState(false);
  const [vals, setVals] = useState({ floor: "", cap: "", tol: "", look: "" });
  const [reason, setReason] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [conflict, setConflict] = useState(false);

  const bounds = { floor: [1, 100000], cap: [1, 100000], tol: [0, 10000], look: [1, 1000] } as const;
  const ok = (k: keyof typeof bounds) => {
    const n = Number(vals[k]);
    return Number.isInteger(n) && n >= bounds[k][0] && n <= bounds[k][1];
  };
  const allOk = ok("floor") && ok("cap") && ok("tol") && ok("look");
  const blocked = !allOk
    ? "Bring every value inside its range."
    : reason.trim().length < 3
      ? "Give a reason of at least 3 characters."
      : password === "" ? "Confirm your password." : undefined;

  function edit() {
    setVals({
      floor: String(settings.roster_trust_min), cap: String(settings.max_close_per_run),
      tol: String(settings.inventory_tolerance), look: String(settings.inventory_lookback),
    });
    setReason(""); setPassword(""); setErr(null); setConflict(false); setOpen(true);
  }

  async function save() {
    setBusy(true); setErr(null); setConflict(false);
    try {
      await api.put("/pms-roster-reconciliation/settings", {
        expected_config_version: settings.config_version,
        roster_trust_min: Number(vals.floor), max_close_per_run: Number(vals.cap),
        inventory_tolerance: Number(vals.tol), inventory_lookback: Number(vals.look),
        reason: reason.trim(), password,
      });
      toast.success("Reconciliation settings saved", "They apply from the next complete roster.");
      setOpen(false);
      onSaved();
    } catch (e) {
      setErr(rosterError(e, `The settings could not be saved. Try again in a moment. ${NOTHING_CHANGED}`));
      setConflict((e as { code?: unknown } | null)?.code === "version_conflict");
    } finally {
      setBusy(false); setPassword("");
    }
  }

  return (
    <SectionCard
      title="When a roster is trusted"
      description={settings.is_default ? "These are the defaults." : `Version ${settings.config_version}.`}
      actions={writable ? <Button size="sm" variant="secondary" onClick={edit}>Change</Button> : undefined}
    >
      <DescriptionList
        items={[
          { label: "Smallest roster acted on", value: <span className="tabular">{settings.roster_trust_min}</span>, hint: "Occupied rooms; a smaller list is treated as broken" },
          { label: "Most stays one run may check out", value: <span className="tabular">{settings.max_close_per_run}</span>, hint: "More stops for a person" },
          { label: "Rooms a sweep may miss", value: <span className="tabular">{settings.inventory_tolerance}</span>, hint: "And still count as complete" },
          { label: "Sweeps remembered for the building", value: <span className="tabular">{settings.inventory_lookback}</span> },
          {
            label: "Connection recovery",
            value: (
              <span className="text-muted-foreground">
                How the PMS link retries after a drop, and how long a problem may last before it is reported, are
                configured with the connection itself on{" "}
                <Link href="/pms-interfaces" className="font-medium text-primary hover:underline">PMS connection</Link>
                , under Advanced configuration.
              </span>
            ),
          },
        ]}
      />
      <DialogForm
        open={open}
        onOpenChange={(v) => { if (!busy) { setOpen(v); if (!v) { setErr(null); setConflict(false); } } }}
        title="When a roster is trusted"
        description="These decide when the appliance checks out stays the PMS no longer lists."
        busy={busy}
        error={err}
        disabled={blocked !== undefined}
        disabledReason={blocked}
        onSubmit={save}
      >
        {conflict && (
          <Button type="button" size="sm" variant="secondary" onClick={() => { setOpen(false); setErr(null); setConflict(false); onSaved(); }}>
            <RefreshCw /> Reload the settings
          </Button>
        )}
        <FieldGrid cols={2}>
          <Field label="Smallest roster acted on (rooms)" hint="1 – 100000. Default 50." required error={ok("floor") ? undefined : "1 – 100000"}>
            <Input type="number" inputMode="numeric" value={vals.floor} onChange={(e) => setVals({ ...vals, floor: e.target.value })} />
          </Field>
          <Field label="Most stays one run may check out" hint="1 – 100000. Default 500." required error={ok("cap") ? undefined : "1 – 100000"}>
            <Input type="number" inputMode="numeric" value={vals.cap} onChange={(e) => setVals({ ...vals, cap: e.target.value })} />
          </Field>
          <Field label="Rooms a sweep may miss" hint="0 – 10000. Default 2." required error={ok("tol") ? undefined : "0 – 10000"}>
            <Input type="number" inputMode="numeric" value={vals.tol} onChange={(e) => setVals({ ...vals, tol: e.target.value })} />
          </Field>
          <Field label="Sweeps remembered" hint="1 – 1000. Default 10." required error={ok("look") ? undefined : "1 – 1000"}>
            <Input type="number" inputMode="numeric" value={vals.look} onChange={(e) => setVals({ ...vals, look: e.target.value })} />
          </Field>
        </FieldGrid>
        <Field label="Reason" hint="Recorded in the activity log." required>
          <Input value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} />
        </Field>
        <Field label="Confirm your password" required>
          <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
        </Field>
      </DialogForm>
    </SectionCard>
  );
}
