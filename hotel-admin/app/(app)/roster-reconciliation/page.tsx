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
import { ShieldCheck, Building2, RefreshCw, AlertTriangle, PlugZap, BookMarked } from "lucide-react";
import {
  api, RosterReconciliationState, ReconcileRunRecord, PmsConnectionSettings,
} from "@/lib/api";
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

// EVERY SETTING, EXPLAINED WHERE IT IS CHANGED.
//
// A number in a box with a unit beside it tells an administrator nothing about whether to touch it. Each of
// these says what it does, what happens if it is raised or lowered, and the situation that would actually
// justify changing it -- because the honest answer for nearly every property is "leave it alone", and that
// is worth saying out loud rather than implying by omission.
const CONN_FIELDS: {
  key: keyof PmsConnectionSettings;
  label: string;
  unit: string;
  what: string;
  when: string;
}[] = [
  {
    key: "backoff_min_ms",
    label: "Shortest wait before retrying",
    unit: "milliseconds",
    what:
      "After the PMS link drops, this is how soon the first reconnection attempt happens. Each further " +
      "attempt waits a little longer, up to the maximum below.",
    when:
      "Raise it if the hotel's PMS logs complain about repeated connections during its nightly restart. " +
      "Lowering it rarely helps: the first attempt is already under a second.",
  },
  {
    key: "backoff_max_ms",
    label: "Longest wait before retrying",
    unit: "milliseconds",
    what:
      "The reconnection attempts never get further apart than this, so a PMS that comes back at 3am is " +
      "picked up within this long, with nobody present. The appliance keeps retrying indefinitely — this " +
      "caps the gap between tries, never the number of them.",
    when:
      "Lower it if the PMS is restarted often and you want the guest list current again sooner. Raise it " +
      "if a fragile link is generating noise on the PMS side.",
  },
  {
    key: "stable_reset_seconds",
    label: "Connection must hold this long to count as recovered",
    unit: "seconds",
    what:
      "After a reconnection survives this long, the waiting resets to the shortest value. Without it, a " +
      "link that flaps all morning would still be waiting the maximum by lunchtime.",
    when: "Raise it if the link reconnects and drops again within a minute or two.",
  },
  {
    key: "link_down_alert_seconds",
    label: "Report the link as down after",
    unit: "seconds",
    what:
      "How long the PMS link may stay down before it appears under Needs attention. This governs when a " +
      "person is TOLD — nothing stops when the link drops. Guests keep signing in from the last good list " +
      "throughout.",
    when:
      "Lower it if you want to know sooner about a PMS outage. Raise it if a nightly maintenance window " +
      "produces an alert every single night that nobody needs to act on.",
  },
  {
    key: "blocked_after_refusals",
    label: "Report reconciliation as blocked after",
    unit: "consecutive runs",
    what:
      "Reconciliation declines to act when the PMS sends an incomplete or self-contradicting list, which " +
      "is correct and protects guests. After this many refusals in a row it is reported, because a " +
      "protection that stays silent forever is indistinguishable from a broken feature.",
    when:
      "Lower it to hear about a degrading feed sooner. Raise it if the PMS routinely sends one odd list " +
      "between good ones.",
  },
];

export default function RosterReconciliationPage() {
  const [state, setState] = useState<RosterReconciliationState | null>(null);
  const [runs, setRuns] = useState<ReconcileRunRecord[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [connForm, setConnForm] = useState<Record<string, number>>({});
  const [connReason, setConnReason] = useState("");
  const [connSaved, setConnSaved] = useState<number | null>(null);

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


  async function saveConnection() {
    setBusy(true);
    try {
      const out = await api.put<{ config_version: number }>(
        "/pms-roster-reconciliation/connection-settings", { ...connForm, reason: connReason });
      setConnSaved(out.config_version);
      setConnForm({});
      setConnReason("");
      await load();
    } catch (e: any) {
      setErr(e?.message ?? "the settings were rejected");
    } finally {
      setBusy(false);
    }
  }


  // A blocker either clears itself or it does not, and the screen must not imply the wrong one.
  const operational = (state?.blockers ?? []).filter((b) => b.blocker !== "DEPARTURE_FOR_UNKNOWN_STAY");
  const historical = (state?.blockers ?? []).filter((b) => b.blocker === "DEPARTURE_FOR_UNKNOWN_STAY");

  const p = state?.preview;
  const complete = p ? p.rooms_expected > 0 && p.rooms_enumerated >= p.rooms_expected - (state!.settings.inventory_tolerance) : false;
  const refused = p && p.outcome !== "COMPLETED";

  return (
    <PageShell>
      <PageHeader
        title="Roster reconciliation"
        description="Keeping this appliance's guest list identical to the hotel's — automatically."
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

      {err && <Card><CardBody><p className="text-sm text-err">{err}</p></CardBody></Card>}

      {/* TWO KINDS OF THING, AND THEY MUST NOT SHARE A FOOTER.
          The operational blockers below describe conditions that end -- a link that comes back, a feed that
          starts describing the building again -- and the line "these clear themselves" is true of them.
          The historical exception does NOT clear, and printing that line under it said the opposite of the
          exception's own text, three lines apart, on the same card. */}
      {operational.length > 0 && (
        <Card>
          <CardBody className="space-y-3">
            <div className="flex items-center gap-2">
              <AlertTriangle className="h-5 w-5" />
              <h2 className="text-base font-semibold">Needs attention</h2>
            </div>
            {operational.map((b, i) => (
              <div key={i} className="rounded-md border border-warning/30 bg-warning-subtle p-3">
                <div className="flex flex-wrap items-center gap-2">
                  <Badge tone="warn">{b.blocker.replace(/_/g, " ").toLowerCase()}</Badge>
                  {b.since && (
                    <span className="text-xs text-muted-foreground">
                      since {new Date(b.since).toLocaleString()}
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
          <CardBody className="space-y-3">
            <div className="flex items-center gap-2">
              <BookMarked className="h-5 w-5" />
              <h2 className="text-base font-semibold">Historical exception — for information</h2>
            </div>
            {historical.map((b, i) => (
              <div key={i} className="rounded-md border p-3">
                <div className="flex flex-wrap items-center gap-2">
                  <Badge tone="neutral">{b.blocker.replace(/_/g, " ").toLowerCase()}</Badge>
                  {b.since && (
                    <span className="text-xs text-muted-foreground">
                      recorded {new Date(b.since).toLocaleString()}
                    </span>
                  )}
                  <Badge tone="ok">guests not affected</Badge>
                </div>
                <p className="mt-2 text-sm">{b.detail}</p>
              </div>
            ))}
            <div className="rounded-md border border-dashed p-3 text-sm text-muted-foreground">
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

      {/* WHAT THE NEXT AUTOMATIC RUN WOULD DO. There is no button: reconciliation runs itself on every
          complete published generation, and a manual trigger for the same audited action would be a repair
          control for work that is not outstanding. This card exists so an operator can SEE the decision
          before it is taken, and see why it was refused when it is. */}
      <Card>
        <CardBody className="space-y-4">
          <div className="flex items-center gap-3">
            <h2 className="text-base font-semibold">What the next automatic run will do</h2>
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
        <CardBody className="space-y-2">
          <h2 className="text-base font-semibold">Historical roster-snapshot artifacts</h2>
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

      {/* RECOVERY SETTINGS. The connector reconnects on its own and retries indefinitely -- a PMS returning
          overnight must be picked up unattended -- so these bound the INTERVAL between attempts and when a
          person is told, never the number of attempts. */}
      <Card>
        <CardBody className="space-y-3">
          <div className="flex items-center gap-2">
            <PlugZap className="h-5 w-5" />
            <h2 className="text-base font-semibold">Connection recovery</h2>
            {state?.connection_settings?.is_default && <Badge tone="neutral">defaults</Badge>}
          </div>
          <p className="text-sm text-muted-foreground">
            The PMS link reconnects by itself, backing off between attempts and retrying for as long as it
            takes. These five numbers bound how fast it retries and how long a problem may last before it is
            reported above. <strong>Most properties never need to change any of them</strong> — they are here
            for the ones whose PMS behaves unusually, and every change is recorded with who made it and why.
          </p>
          <div className="grid gap-3 lg:grid-cols-2">
            {CONN_FIELDS.map((f) => (
              <div key={f.key} className="rounded-md border p-3">
                <label className="text-sm">
                  <span className="block font-medium">{f.label}</span>
                  <span className="mt-1 block text-xs text-muted-foreground">{f.what}</span>
                  <div className="mt-2 flex items-center gap-2">
                    <input
                      type="number"
                      className="w-40 rounded-md border px-3 py-2 text-sm"
                      value={connForm[f.key] ?? state?.connection_settings?.[f.key] ?? ""}
                      onChange={(e) => setConnForm({ ...connForm, [f.key]: Number(e.target.value) })}
                      disabled={busy}
                    />
                    <span className="text-xs text-muted-foreground">{f.unit}</span>
                  </div>
                  <span className="mt-2 block text-xs text-muted-foreground">
                    <strong>Currently:</strong> {state?.connection_settings?.[f.key] ?? "—"} {f.unit}
                    {state?.connection_settings?.is_default ? " (the approved default)" : ""}
                  </span>
                  <span className="mt-1 block text-xs text-muted-foreground">
                    <strong>Change it when:</strong> {f.when}
                  </span>
                </label>
              </div>
            ))}
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <input
              className="min-w-[20rem] flex-1 rounded-md border px-3 py-2 text-sm"
              placeholder="Reason - recorded against this change"
              value={connReason}
              onChange={(e) => setConnReason(e.target.value)}
              disabled={busy}
            />
            <Button
              onClick={() => void saveConnection()}
              disabled={busy || Object.keys(connForm).length === 0}
            >
              Save recovery settings
            </Button>
          </div>
          {connSaved && (
            <p className="text-sm text-ok">
              Saved as version {connSaved}. It applies on the next reconnect.
            </p>
          )}
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
