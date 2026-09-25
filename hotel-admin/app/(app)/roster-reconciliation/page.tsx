"use client";

// ROSTER RECONCILIATION — closing the stays the PMS no longer lists, and showing why that is safe to do.
//
// THE SISTER PAGE HAS NO ACTION, AND STILL DOES NOT. /pms-reconciliation lists departures the engine could
// not place, and those are answered by the PMS: stay_events is one-way and a checkout boundary must be an
// APPLIED departure event. Nothing here changes that.
//
// WHAT IS DIFFERENT HERE IS THE EVIDENCE. A COMPLETE published roster is the PMS stating, in full, who is in
// the building. A stay it does not mention has ended — a deduction from a complete statement, not a re-read
// of an old message. That is the only evidence this page acts on.
//
// SO THE PAGE LEADS WITH THE PROOF, NOT THE BUTTON. "Rooms named 589 of 589" is the sentence that makes the
// action legitimate, and if it ever reads 300 of 589 the run refuses and the operator can see exactly why
// without being told to trust anything. Six named refusals, each shown as a reason rather than an error.
//
// AND THE NUMBER NOBODY SHOULD HAVE TO ASK FOR: how many stays were held back because the PMS said something
// about them after the snapshot. A guest who checked in ten minutes ago is absent from a roster taken
// fifteen minutes ago, and closing them would disconnect somebody who just arrived. That is shown as a
// protection, permanently, not hidden because it is usually small.

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { ShieldCheck, Building2, AlertTriangle, BookMarked, ListChecks, History } from "lucide-react";
import { api, RosterReconciliationState, ReconcileRunRecord } from "@/lib/api";
import { cn, formatDate } from "@/lib/utils";
import { PageShell, PageHeader, StatCard } from "@/components/ui/page";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TBody, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { Skeleton } from "@/components/ui/misc";
import { LiveStatus, refreshingClass } from "@/components/ui/patterns";

const REFUSAL_MEANING: Record<string, string> = {
  REFUSED_ROSTER_INCOMPLETE:
    "The latest sweep did not name the whole property, so it is a partial answer. A partial answer must never be read as “the guests it missed have left”.",
  REFUSED_GENERATION_NOT_LATEST:
    "A newer roster exists. Acting on a superseded one would close guests who have arrived since it was taken.",
  REFUSED_GENERATION_UNPUBLISHED:
    "The most recent resync never completed, so there is no full statement of who is in the building.",
  REFUSED_LINK_NOT_HEALTHY:
    "The PMS link is faulted or resyncing. The connector is saying it no longer trusts its own picture, which is not a moment to act on it.",
  REFUSED_SCOPE_MISMATCH:
    "The interface does not belong to this property. Nothing was read or changed.",
  REFUSED_ROSTER_TOO_SMALL:
    "The roster is below the configured floor — a backstop against an obviously broken response.",
  REFUSED_CAP_EXCEEDED:
    "More stays would close than one run is allowed to close. This is deliberate: a surprise stops for a person.",
};

// The run outcome in words: "completed" or the refusal, without the REFUSED_ prefix the table already implies.
const outcomeWords = (o: string) =>
  o === "COMPLETED" ? "will complete" : `will refuse: ${o.replace(/^REFUSED_/, "").replace(/_/g, " ").toLowerCase()}`;

export default function RosterReconciliationPage() {
  const [state, setState] = useState<RosterReconciliationState | null>(null);
  const [runs, setRuns] = useState<ReconcileRunRecord[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);

  const load = useCallback(async () => {
    setErr(null);
    setBusy(true);
    try {
      const [s, r] = await Promise.all([
        api.get<RosterReconciliationState>("/pms-roster-reconciliation"),
        api.get<{ runs: ReconcileRunRecord[] }>("/pms-roster-reconciliation/runs"),
      ]);
      setState(s);
      setRuns(r.runs ?? []);
      setUpdatedAt(Date.now());
    } catch (e: any) {
      setErr(e?.message ?? "could not read the reconciliation state");
    } finally {
      setBusy(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  // A blocker either clears itself or it does not, and the screen must not imply the wrong one.
  const operational = (state?.blockers ?? []).filter((b) => b.blocker !== "DEPARTURE_FOR_UNKNOWN_STAY");
  const historical = (state?.blockers ?? []).filter((b) => b.blocker === "DEPARTURE_FOR_UNKNOWN_STAY");

  const p = state?.preview;
  const complete = p ? p.rooms_expected > 0 && p.rooms_enumerated >= p.rooms_expected - (state!.settings.inventory_tolerance) : false;
  const refused = p && p.outcome !== "COMPLETED";

  return (
    <PageShell>
      <PageHeader
        eyebrow="Property management system"
        title="Roster reconciliation"
        icon={<ListChecks />}
        description="Keeping this appliance's guest list identical to the hotel's — automatically. Read-only: there is nothing here to press."
        actions={<LiveStatus updatedAt={updatedAt} refreshing={busy} error={!!err && state !== null} onRefresh={() => void load()} />}
      />

      {/* WHAT THE PAGE IS FOR, in the words an operator would use. Written because the previous heading
          described the MECHANISM to somebody who already understood it. */}
      <Card>
        <CardBody className="space-y-2 text-sm">
          <p>
            The hotel&rsquo;s PMS sends this appliance a full list of who is in the building, many times a
            day. Reconciliation compares that list with the guest list this appliance is using and closes any
            stay the hotel no longer has — which is how somebody who checked out stops being able to sign in.
          </p>
          <p>
            <strong>This happens on its own.</strong> There is nothing on this page to press, no queue to work
            through and no routine task. Everything below is here so you can see what it did and why, and
            change the few numbers a hotel may reasonably want changed.
          </p>
          <p className="text-muted-foreground">
            Guests are never affected while it waits: if the PMS list is incomplete or the link is down, the
            appliance keeps using the last good list rather than guessing, and says so under Needs attention.
          </p>
        </CardBody>
      </Card>

      <ErrorBanner err={err} />

      <div className={cn("space-y-5", busy && state !== null && refreshingClass)}>
      {state === null && !err && (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4" aria-busy="true">
          <Skeleton className="h-28" /><Skeleton className="h-28" /><Skeleton className="h-28" /><Skeleton className="h-28" />
        </div>
      )}

      {/* TWO KINDS OF THING, AND THEY MUST NOT SHARE A FOOTER.
          The operational blockers below describe conditions that end -- a link that comes back, a feed that
          starts describing the building again -- and the line "these clear themselves" is true of them.
          The historical exception does NOT clear, and printing that line under it said the opposite of the
          exception's own text, three lines apart, on the same card. */}
      {operational.length > 0 && (
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <AlertTriangle className="size-4 text-warning-subtle-foreground" aria-hidden /> Needs attention
            </CardTitle>
          </CardHeader>
          <CardBody className="space-y-3">
            {operational.map((b, i) => (
              <div key={i} className="rounded-md border border-warning/30 bg-warning-subtle p-3">
                <div className="flex flex-wrap items-center gap-2">
                  <Badge tone="warn">{b.blocker.replace(/_/g, " ").toLowerCase()}</Badge>
                  {b.since && (
                    <span className="text-xs text-muted-foreground">
                      since {formatDate(b.since)}
                    </span>
                  )}
                  <Badge tone={b.guests_affected ? "err" : "ok"}>
                    {b.guests_affected ? "guests affected" : "guests not affected"}
                  </Badge>
                </div>
                <p className="mt-2 text-sm text-warning-subtle-foreground">{b.detail}</p>
              </div>
            ))}
            <p className="text-xs text-muted-foreground">
              These clear themselves when the condition ends. There is nothing to acknowledge.
            </p>
          </CardBody>
        </Card>
      )}

      {historical.length > 0 && (
        <Card id="historical-exception">
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <BookMarked className="size-4 text-muted-foreground" aria-hidden /> Historical exception — for information
            </CardTitle>
          </CardHeader>
          <CardBody className="space-y-3">
            {historical.map((b, i) => (
              <div key={i} className="rounded-md border border-border p-3">
                <div className="flex flex-wrap items-center gap-2">
                  <Badge tone="neutral">{b.blocker.replace(/_/g, " ").toLowerCase()}</Badge>
                  {b.since && (
                    <span className="text-xs text-muted-foreground">
                      recorded {formatDate(b.since)}
                    </span>
                  )}
                  <Badge tone="ok">guests not affected</Badge>
                </div>
                <p className="mt-2 text-sm">{b.detail}</p>
              </div>
            ))}
            <div className="rounded-md border border-dashed border-border-strong p-3 text-sm text-muted-foreground">
              <p className="font-medium text-foreground">What this is, and what to do about it</p>
              <p className="mt-1">
                When this appliance was first connected it joined a hotel that was already running, and its
                first roster sweeps did not yet cover every room. A guest checked out during that window, so
                the PMS announced a departure for a stay this appliance had never been told about.
              </p>
              <p className="mt-2">
                Nobody is affected. No guest is online because of it, no stay is held open by it, and it will
                not grow — the connector has covered the whole property on every sweep since.
              </p>
              <p className="mt-2">
                It stays on this list because closing it locally would mean inventing the arrival that was
                never received, and this system does not invent guest records. There are exactly two ways it
                ends: ask the hotel&rsquo;s PMS whether that reservation existed, or decide to leave it as a
                known gap from the appliance&rsquo;s first days. Either is a legitimate answer; doing nothing
                is also safe.
              </p>
            </div>
          </CardBody>
        </Card>
      )}

      {/* THE PROOF, BEFORE THE ACTION. */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          icon={<Building2 />}
          label="Rooms named by this sweep"
          value={p ? `${p.rooms_enumerated} of ${p.rooms_expected}` : "—"}
          tone={p ? (complete ? "ok" : "warn") : "default"}
          hint={complete ? "The sweep covered the property" : "Incomplete — the run will refuse"}
        />
        <StatCard label="Occupied per the PMS" value={p?.roster_size ?? "—"} hint={`Guest-list refresh #${state?.generation ?? "—"}`} />
        <StatCard label="In house per this appliance" value={p?.mirror_in_house ?? "—"} />
        <StatCard
          icon={<ShieldCheck />}
          label="Held back — PMS spoke since"
          value={p?.protected_by_newer_events ?? "—"}
          hint="Arrivals and changes after the snapshot are never closed"
        />
      </div>

      {/* WHAT THE NEXT AUTOMATIC RUN WOULD DO. There is no button: reconciliation runs itself on every
          complete published generation, and a manual trigger for the same audited action would be a repair
          control for work that is not outstanding. This card exists so an operator can SEE the decision
          before it is taken, and see why it was refused when it is. */}
      <Card>
        <CardHeader>
          <CardTitle>What the next automatic run will do</CardTitle>
          {p && <Badge tone={refused ? "warn" : "ok"} dot>{outcomeWords(p.outcome)}</Badge>}
        </CardHeader>
        <CardBody className="space-y-4">

          {refused && p && (
            <p className="text-sm text-muted-foreground">
              {REFUSAL_MEANING[p.outcome] ?? "The run refused, and nothing was changed."}
            </p>
          )}

          {!refused && p && (
            <p className="text-sm">
              <strong>{p.absent_from_roster}</strong> stay{p.absent_from_roster === 1 ? "" : "s"} would be
              closed — every one of them absent from a roster that named {p.rooms_enumerated} of{" "}
              {p.rooms_expected} rooms. {p.protected_by_newer_events} further stay
              {p.protected_by_newer_events === 1 ? " is" : "s are"} absent but held back, because the PMS has
              said something about {p.protected_by_newer_events === 1 ? "it" : "them"} since the snapshot.
            </p>
          )}

          <p className="text-xs text-muted-foreground">
            This happens on its own when the PMS publishes a complete roster. Nothing here needs pressing.
          </p>
        </CardBody>
      </Card>

      {/* THE HISTORICAL SNAPSHOT ARTIFACTS. Answered once, in bulk, from the fact that a resync mentions
          every room and an empty one has no booking to name. There is no button because there is nothing
          left to answer and nothing new can arrive: the connector no longer admits those records at all. */}
      <Card>
        <CardHeader><CardTitle>Historical roster-snapshot artifacts</CardTitle></CardHeader>
        <CardBody className="space-y-2">
          {(state?.undisposed_cases ?? 0) === 0 ? (
            <p className="text-sm text-muted-foreground">
              None outstanding. The recorded roster snapshots were answered as what they actually were — a
              sweep mentioning empty rooms, never a departure anybody announced — and no stay changed state
              and no record was deleted. New ones cannot arrive: the connector no longer admits them.
            </p>
          ) : (
            <p className="text-sm">
              <strong>{state!.undisposed_cases}</strong> recorded roster snapshot
              {state!.undisposed_cases === 1 ? "" : "s"} still to answer. They are answered automatically;
              nothing needs doing here.
            </p>
          )}
        </CardBody>
      </Card>

      {/* THE RECOVERY SETTINGS LIVE WITH THE CONNECTION THEY GOVERN.
          They are reconnect bounds for the PMS link, which is configured on PMS connection, not here. An
          administrator changing how the link retries was being asked to find a diagnostic page to do it. */}
      <Card>
        <CardHeader><CardTitle>Connection recovery</CardTitle></CardHeader>
        <CardBody className="space-y-1">
          <p className="text-sm text-muted-foreground">
            How the PMS link retries after a drop, and how long a problem may last before it is reported, are
            configured with the connection itself on{" "}
            <Link href="/pms-interfaces" className="font-medium underline underline-offset-2">
              PMS connection
            </Link>
            , under Advanced configuration.
          </p>
        </CardBody>
      </Card>

      <Card className="overflow-hidden">
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <History className="size-4 text-muted-foreground" aria-hidden /> Every run, including the refusals
          </CardTitle>
        </CardHeader>
        {runs.length === 0 ? (
          <EmptyState icon={<History />} title="No runs yet" hint="A run is recorded each time the PMS publishes a complete guest list." />
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>When</TH>
                <TH className="hidden sm:table-cell">Mode</TH>
                <TH>Outcome</TH>
                <TH className="hidden md:table-cell">Rooms</TH>
                <TH className="hidden md:table-cell text-end">Absent</TH>
                <TH className="text-end">Closed</TH>
                <TH className="hidden lg:table-cell text-end">Held back</TH>
                <TH className="hidden lg:table-cell">By</TH>
                <TH className="hidden xl:table-cell">Reason</TH>
              </TR>
            </THead>
            <TBody>
              {runs.map((r, i) => (
                <TR key={i}>
                  <TD className="whitespace-nowrap text-sm">{formatDate(r.run_at)}</TD>
                  <TD className="hidden text-sm sm:table-cell">{r.mode === "APPLY" ? "Applied" : "Preview"}</TD>
                  <TD>
                    <Badge tone={r.outcome === "COMPLETED" ? "ok" : "warn"} dot>
                      {r.outcome.replace(/^REFUSED_/, "").replace(/_/g, " ").toLowerCase()}
                    </Badge>
                  </TD>
                  <TD className="hidden tabular md:table-cell">{r.rooms_enumerated}/{r.rooms_expected}</TD>
                  <TD className="hidden text-end tabular md:table-cell">{r.absent_from_roster}</TD>
                  <TD className="text-end tabular">{r.stays_closed}</TD>
                  <TD className="hidden text-end tabular lg:table-cell">{r.protected_by_newer_events}</TD>
                  <TD className="hidden text-sm text-muted-foreground lg:table-cell">{r.run_by}</TD>
                  <TD className="hidden text-sm text-muted-foreground xl:table-cell">{r.reason}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      </Card>
      </div>
    </PageShell>
  );
}
