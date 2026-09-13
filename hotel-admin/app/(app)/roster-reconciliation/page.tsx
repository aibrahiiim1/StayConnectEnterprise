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
import { ShieldCheck, Building2, RefreshCw, ArchiveRestore } from "lucide-react";
import { api, RosterReconciliationState, ReconcileRun, ReconcileRunRecord } from "@/lib/api";
import { PageShell, PageHeader, StatCard, Toolbar } from "@/components/ui/page";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";

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

export default function RosterReconciliationPage() {
  const [state, setState] = useState<RosterReconciliationState | null>(null);
  const [runs, setRuns] = useState<ReconcileRunRecord[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [reason, setReason] = useState("");
  const [lastApply, setLastApply] = useState<ReconcileRun | null>(null);

  const load = useCallback(async () => {
    setErr(null);
    try {
      const [s, r] = await Promise.all([
        api.get<RosterReconciliationState>("/pms-roster-reconciliation"),
        api.get<{ runs: ReconcileRunRecord[] }>("/pms-roster-reconciliation/runs"),
      ]);
      setState(s);
      setRuns(r.runs ?? []);
    } catch (e: any) {
      setErr(e?.message ?? "could not read the reconciliation state");
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  async function apply() {
    if (!reason.trim()) return;
    setBusy(true);
    try {
      const out = await api.post<ReconcileRun>("/pms-roster-reconciliation/apply", { reason });
      setLastApply(out);
      setReason("");
      await load();
    } catch (e: any) {
      setErr(e?.message ?? "the run failed");
    } finally {
      setBusy(false);
    }
  }

  async function disposeSnapshots() {
    setBusy(true);
    try {
      await api.post<{ disposed: number }>("/pms-roster-reconciliation/dispose-snapshots", {
        reason: "roster snapshots are not departure announcements",
      });
      await load();
    } catch (e: any) {
      setErr(e?.message ?? "could not dispose the snapshot cases");
    } finally {
      setBusy(false);
    }
  }

  const p = state?.preview;
  const complete = p ? p.rooms_expected > 0 && p.rooms_enumerated >= p.rooms_expected - (state!.settings.inventory_tolerance) : false;
  const refused = p && p.outcome !== "COMPLETED";

  return (
    <PageShell>
      <PageHeader
        title="Roster reconciliation"
        description="Close the stays a complete PMS roster no longer lists — by reservation, never by room."
      />

      {err && <Card><CardBody><p className="text-sm text-err">{err}</p></CardBody></Card>}

      <Toolbar>
        <Button variant="secondary" onClick={() => void load()} disabled={busy}>
          <RefreshCw className="h-4 w-4" /> Refresh
        </Button>
      </Toolbar>

      {/* THE PROOF, BEFORE THE ACTION. */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          icon={<Building2 className="h-5 w-5" />}
          label="Rooms named by this sweep"
          value={p ? `${p.rooms_enumerated} of ${p.rooms_expected}` : "—"}
          hint={complete ? "The sweep covered the property" : "Incomplete — the run will refuse"}
        />
        <StatCard label="Occupied per the PMS" value={p?.roster_size ?? "—"} hint={`Generation ${state?.generation ?? "—"}`} />
        <StatCard label="In house per this appliance" value={p?.mirror_in_house ?? "—"} />
        <StatCard
          icon={<ShieldCheck className="h-5 w-5" />}
          label="Held back — PMS spoke since"
          value={p?.protected_by_newer_events ?? "—"}
          hint="Arrivals and changes after the snapshot are never closed"
        />
      </div>

      <Card>
        <CardBody className="space-y-4">
          <div className="flex items-center gap-3">
            <h2 className="text-base font-semibold">What a run would do now</h2>
            {p && <Badge tone={refused ? "warn" : "ok"}>{p.outcome.replace(/_/g, " ").toLowerCase()}</Badge>}
          </div>

          {refused && p && (
            <p className="text-sm text-muted-foreground">
              {REFUSAL_MEANING[p.outcome] ?? "The run refused, and nothing was changed."}
            </p>
          )}

          {!refused && p && (
            <p className="text-sm">
              <strong>{p.absent_from_roster}</strong> stay{p.absent_from_roster === 1 ? "" : "s"} would be
              closed — every one of them absent from a roster that named{" "}
              {p.rooms_enumerated} of {p.rooms_expected} rooms. {p.protected_by_newer_events} further
              stay{p.protected_by_newer_events === 1 ? " is" : "s are"} absent but held back, because the PMS
              has said something about {p.protected_by_newer_events === 1 ? "it" : "them"} since the snapshot.
            </p>
          )}

          <div className="flex flex-wrap items-center gap-2">
            <input
              className="min-w-[22rem] flex-1 rounded-md border px-3 py-2 text-sm"
              placeholder="Reason — recorded against every stay this closes"
              value={reason}
              onChange={(e) => setReason(e.target.value)}
              disabled={!!refused || busy}
            />
            <Button onClick={() => void apply()} disabled={!!refused || busy || !reason.trim()}>
              Close {p?.absent_from_roster ?? 0} stay{p?.absent_from_roster === 1 ? "" : "s"}
            </Button>
          </div>

          {lastApply && (
            <p className="text-sm text-ok">
              Closed {lastApply.stays_closed} stay{lastApply.stays_closed === 1 ? "" : "s"}; held back{" "}
              {lastApply.protected_by_newer_events}.
            </p>
          )}
        </CardBody>
      </Card>

      {/* THE HISTORICAL CASES. Not deleted, not re-applied — answered. */}
      <Card>
        <CardBody className="space-y-3">
          <h2 className="text-base font-semibold">Historical roster-snapshot cases</h2>
          <p className="text-sm text-muted-foreground">
            {state?.undisposed_cases ?? 0} recorded departure{state?.undisposed_cases === 1 ? "" : "s"} are
            still waiting for an answer. Those admitted during a resync with no reservation number were never
            departure announcements — a sweep mentions every room, and an empty one has no booking to name.
            Disposing them records what they actually were. No stay changes state and no record is deleted.
          </p>
          <Button variant="secondary" onClick={() => void disposeSnapshots()} disabled={busy}>
            <ArchiveRestore className="h-4 w-4" /> Answer the snapshot cases
          </Button>
        </CardBody>
      </Card>

      <Card>
        <CardBody>
          <h2 className="mb-3 text-base font-semibold">Every run, including the refusals</h2>
          <Table>
            <THead>
              <TR>
                <TH>When</TH><TH>Mode</TH><TH>Outcome</TH><TH>Rooms</TH>
                <TH>Absent</TH><TH>Closed</TH><TH>Held back</TH><TH>By</TH><TH>Reason</TH>
              </TR>
            </THead>
            <tbody>
              {runs.length === 0 && (
                <TR><TD colSpan={9} className="text-sm text-muted-foreground">No runs yet.</TD></TR>
              )}
              {runs.map((r, i) => (
                <TR key={i}>
                  <TD>{new Date(r.run_at).toLocaleString()}</TD>
                  <TD>{r.mode === "APPLY" ? "Applied" : "Preview"}</TD>
                  <TD><Badge tone={r.outcome === "COMPLETED" ? "ok" : "warn"}>
                    {r.outcome.replace(/^REFUSED_/, "").replace(/_/g, " ").toLowerCase()}
                  </Badge></TD>
                  <TD>{r.rooms_enumerated}/{r.rooms_expected}</TD>
                  <TD>{r.absent_from_roster}</TD>
                  <TD>{r.stays_closed}</TD>
                  <TD>{r.protected_by_newer_events}</TD>
                  <TD>{r.run_by}</TD>
                  <TD>{r.reason}</TD>
                </TR>
              ))}
            </tbody>
          </Table>
        </CardBody>
      </Card>
    </PageShell>
  );
}
