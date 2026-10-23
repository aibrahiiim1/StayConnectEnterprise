"use client";

// Only a COMPLETE published roster is acted on: a stay it omits has ended.
// Opening the page must run nothing (a READ must not write a run row); figures come from the last recorded run.
// Stays with newer PMS events than the snapshot are held back so a just-arrived guest is never disconnected.

import { useCallback, useEffect, useState } from "react";
import { Button } from "@/components/ui/button";
import { DialogForm } from "@/components/ui/dialog";
import { Field, Input, Select } from "@/components/ui/input";
import { useOperatorRoles } from "@/lib/whoami-context";
import { canWrite } from "@/lib/roles";
import { useToast } from "@/components/ui/toast";
import Link from "next/link";
import { ShieldCheck, Building2, AlertTriangle, BookMarked, ListChecks, History, BedDouble, Hotel } from "lucide-react";
import { api, RosterReconciliationState, ReconcileRunRecord, ReconcileRun } from "@/lib/api";
import { cn, formatDate } from "@/lib/utils";
import { PageShell, PageHeader, StatCard } from "@/components/ui/page";
import { HelpList, HelpSection, HelpTip } from "@/components/help";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TBody, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { Skeleton } from "@/components/ui/misc";
import { LiveStatus, refreshingClass } from "@/components/ui/patterns";

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
    "The roster is below the configured floor — a backstop against an obviously broken response.",
  REFUSED_CAP_EXCEEDED:
    "More stays would close than one run is allowed to close. This is deliberate: a surprise stops for a person.",
};

const outcomeWords = (o: string) =>
  o === "COMPLETED" ? "completed" : `refused: ${o.replace(/^REFUSED_/, "").replace(/_/g, " ").toLowerCase()}`;

export default function RosterReconciliationPage() {
  const [state, setState] = useState<RosterReconciliationState | null>(null);
  const [runs, setRuns] = useState<ReconcileRunRecord[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [iface, setIface] = useState("");
  const [dry, setDry] = useState<ReconcileRun | null>(null);
  const [dryBusy, setDryBusy] = useState(false);
  const [dryErr, setDryErr] = useState<unknown>(null);
  const roles = useOperatorRoles();
  const writable = roles ? canWrite("pms-roster-reconciliation", roles) : false;

  const load = useCallback(async () => {
    setErr(null);
    setBusy(true);
    try {
      const q = iface ? "?pms_interface_id=" + encodeURIComponent(iface) : "";
      const [s, r] = await Promise.all([
        api.get<RosterReconciliationState>("/pms-roster-reconciliation" + q),
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
  }, [iface]);

  useEffect(() => { void load(); }, [load]);

  // Recorded as a DRY_RUN in the ledger; changes no stay.
  async function runDry() {
    setDryBusy(true);
    setDryErr(null);
    try {
      const out = await api.post<ReconcileRun>("/pms-roster-reconciliation/preview", {
        pms_interface_id: state?.pms_interface_id, reason: "dry run requested from the console",
      });
      setDry(out);
      await load();
    } catch (e) {
      setDryErr(e);
    } finally {
      setDryBusy(false);
    }
  }

  // Operational blockers clear themselves; the historical exception does not.
  const operational = (state?.blockers ?? []).filter((b) => b.blocker !== "DEPARTURE_FOR_UNKNOWN_STAY");
  const historical = (state?.blockers ?? []).filter((b) => b.blocker === "DEPARTURE_FOR_UNKNOWN_STAY");

  const p = dry ?? state?.last_run ?? state?.preview ?? null;
  const complete = p ? p.rooms_expected > 0 && p.rooms_enumerated >= p.rooms_expected - (state!.settings.inventory_tolerance) : false;
  const refused = p && p.outcome !== "COMPLETED";

  return (
    <PageShell>
      <PageHeader
        eyebrow="Hotel"
        title="Roster reconciliation"
        icon={<ListChecks />}
        description="Keeps this appliance's guest list identical to the PMS's, automatically."
        help={
          <>
            <HelpSection title="What reconciliation does">
              <p>
                The site&rsquo;s PMS sends this appliance a full list of who is in the building, many times a day.
                Reconciliation compares that list with the guest list this appliance is using and closes any stay
                the PMS no longer has — which is how somebody who checked out stops being able to sign in.
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
                access ends (or moves to the departure grace package) and its devices are disconnected.
              </p>
              <p>
                Guests are never affected while it waits: if the PMS list is incomplete or the link is down, the
                appliance keeps using the last good list rather than guessing, and says so under Needs attention.
              </p>
            </HelpSection>
            <HelpSection title="Reading the figures">
              <HelpList
                items={[
                  <><strong>Rooms named by this sweep</strong> — how much of the site the PMS roster covered. An incomplete sweep makes the run refuse.</>,
                  <><strong>Held back — PMS spoke since</strong> — stays absent from the roster that are not closed, because the PMS has said something about them after the snapshot. Arrivals and changes after the snapshot are never closed.</>,
                  <><strong>Every run, including the refusals</strong> — a run is recorded each time the PMS publishes a complete guest list.</>,
                ]}
              />
            </HelpSection>
          </>
        }
        actions={<LiveStatus updatedAt={updatedAt} refreshing={busy} error={!!err && state !== null} onRefresh={() => void load()} />}
      />

      <ErrorBanner err={err} />

      <div className={cn("space-y-5", busy && state !== null && refreshingClass)}>
      {state === null && !err && (
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4" aria-busy="true">
          <Skeleton className="h-28" /><Skeleton className="h-28" /><Skeleton className="h-28" /><Skeleton className="h-28" />
        </div>
      )}

      {/* Operational and historical blockers must not share a footer: "these clear themselves" is false for the historical one. */}
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
              <HelpTip title="Historical exception">
                <HelpSection title="What this is">
                  <p>
                    When this appliance was first connected it joined a site that was already running, and its
                    first roster sweeps did not yet cover every room. A guest checked out during that window, so
                    the PMS announced a departure for a stay this appliance had never been told about.
                  </p>
                  <p>
                    Nobody is affected. No guest is online because of it, no stay is held open by it, and it will
                    not grow — the connector has covered the whole site on every sweep since.
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
          </CardBody>
        </Card>
      )}

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          icon={<Building2 />}
          label="Rooms named by this sweep"
          value={p ? `${p.rooms_enumerated} of ${p.rooms_expected}` : "—"}
          tone={p ? (complete ? "ok" : "warn") : "default"}
          hint={complete ? "The sweep covered the site" : "Incomplete — the run will refuse"}
        />
        <StatCard icon={<BedDouble />} label="Occupied per the PMS" value={p?.roster_size ?? "—"} hint={`Guest-list refresh #${state?.generation ?? "—"}`} />
        <StatCard icon={<Hotel />} label="In house per this appliance" value={p?.mirror_in_house ?? "—"} />
        <StatCard
          icon={<ShieldCheck />}
          label="Held back — PMS spoke since"
          value={p?.protected_by_newer_events ?? "—"}
          hint="Arrivals and changes after the snapshot are never closed"
        />
      </div>

      {/* No manual trigger: reconciliation runs itself on every complete published generation. */}
      {(state?.interfaces?.length ?? 0) > 1 && (
        <Card>
          <CardBody>
            <Field label="PMS connection" hint="Each connection has its own guest list and its own runs.">
              <Select value={iface || state?.pms_interface_id || ""} onChange={(e) => { setDry(null); setIface(e.target.value); }}>
                {state!.interfaces!.map((i) => (
                  <option key={i.id} value={i.id}>
                    {(i.label || i.connector_kind) + (i.lifecycle_state !== "ACTIVE" ? ` (${i.lifecycle_state.toLowerCase()})` : "")}
                  </option>
                ))}
              </Select>
            </Field>
          </CardBody>
        </Card>
      )}

      <Card>
        <CardHeader>
          <CardTitle>{dry ? "What a run would do now (dry run)" : "What the last run decided"}</CardTitle>
          {p && <Badge tone={refused ? "warn" : "ok"} dot>{outcomeWords(p.outcome)}</Badge>}
        </CardHeader>
        <CardBody className="space-y-4">
          <ErrorBanner err={dryErr} />
          {!p && (
            <p className="text-sm text-muted-foreground">
              No run has been recorded for this connection yet. One is recorded each time the PMS publishes a
              complete guest list.
            </p>
          )}
          {!dry && state?.last_run_at && (
            <p className="text-xs text-muted-foreground">
              {state.last_run_mode === "APPLY" ? "Automatic run" : "Dry run"} recorded {formatDate(state.last_run_at)}.
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
              {dry ? " would be checked out" : state?.last_run_mode === "APPLY" ? ` — ${p.stays_closed} sent to check-out` : " would be checked out"}
              . {p.protected_by_newer_events} further stay
              {p.protected_by_newer_events === 1 ? " is" : "s are"} absent but held back, because the PMS has
              said something about {p.protected_by_newer_events === 1 ? "it" : "them"} since the snapshot.
            </p>
          )}

          <div className="flex flex-wrap items-center justify-between gap-3">
            <p className="text-xs text-muted-foreground">
              This happens on its own when the PMS publishes a complete roster. Nothing here needs pressing.
            </p>
            {writable && (
              <Button size="sm" variant="secondary" disabled={dryBusy || !state} onClick={() => void runDry()}>
                {dryBusy ? "Running…" : "Run dry run"}
              </Button>
            )}
          </div>
        </CardBody>
      </Card>

      {state && <RosterSettingsCard settings={state.settings} writable={writable} onSaved={() => void load()} />}

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
                <TH className="text-end">Checked out</TH>
                <TH className="hidden lg:table-cell text-end">Held back</TH>
                <TH className="hidden lg:table-cell">By</TH>
                <TH className="hidden xl:table-cell">Reason</TH>
              </TR>
            </THead>
            <TBody>
              {runs.map((r, i) => (
                <TR key={i}>
                  <TD className="whitespace-nowrap text-sm">{formatDate(r.run_at)}</TD>
                  <TD className="hidden text-sm sm:table-cell">{r.mode === "APPLY" ? "Applied" : "Dry run"}</TD>
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

// Visible to all; changes need an integration-owning role, password, the on-screen version and a reason.
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
  const [err, setErr] = useState<unknown>(null);

  const bounds = { floor: [1, 100000], cap: [1, 100000], tol: [0, 10000], look: [1, 1000] } as const;
  const ok = (k: keyof typeof bounds) => {
    const n = Number(vals[k]);
    return Number.isInteger(n) && n >= bounds[k][0] && n <= bounds[k][1];
  };
  const allOk = ok("floor") && ok("cap") && ok("tol") && ok("look");

  function edit() {
    setVals({
      floor: String(settings.roster_trust_min), cap: String(settings.max_close_per_run),
      tol: String(settings.inventory_tolerance), look: String(settings.inventory_lookback),
    });
    setReason(""); setPassword(""); setErr(null); setOpen(true);
  }

  async function save() {
    setBusy(true); setErr(null);
    try {
      await api.put("/pms-roster-reconciliation/settings", {
        expected_config_version: settings.config_version,
        roster_trust_min: Number(vals.floor), max_close_per_run: Number(vals.cap),
        inventory_tolerance: Number(vals.tol), inventory_lookback: Number(vals.look),
        reason: reason.trim(), password,
      });
      toast.success("Reconciliation settings saved");
      setOpen(false);
      onSaved();
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false); setPassword("");
    }
  }

  const row = (label: string, value: number, hint: string) => (
    <div className="flex items-baseline justify-between gap-3 text-sm">
      <span>{label} <span className="text-xs text-muted-foreground">— {hint}</span></span>
      <span className="tabular font-medium">{value}</span>
    </div>
  );
  return (
    <Card>
      <CardHeader>
        <CardTitle>When a roster is trusted</CardTitle>
        {writable && <Button size="sm" variant="secondary" onClick={edit}>Change</Button>}
      </CardHeader>
      <CardBody className="space-y-1.5">
        {row("Smallest roster acted on", settings.roster_trust_min, "occupied rooms; a smaller list is treated as broken")}
        {row("Most stays one run may check out", settings.max_close_per_run, "more stops for a person")}
        {row("Rooms a sweep may miss", settings.inventory_tolerance, "and still count as complete")}
        {row("Sweeps remembered for the building", settings.inventory_lookback, "")}
        <p className="pt-1 text-xs text-muted-foreground">
          {settings.is_default ? "These are the defaults." : `Version ${settings.config_version}.`}
        </p>
      </CardBody>
      <DialogForm
        open={open}
        onOpenChange={(v) => { if (!busy) setOpen(v); }}
        title="When a roster is trusted"
        description="These decide when the appliance checks out stays the PMS no longer lists."
        busy={busy}
        error={err}
        disabled={!allOk || reason.trim().length < 3 || password === ""}
        onSubmit={save}
      >
        <Field label="Smallest roster acted on (rooms)" hint="1 – 100000. Default 50." required error={ok("floor") ? undefined : "1 – 100000"}>
          <Input type="number" value={vals.floor} onChange={(e) => setVals({ ...vals, floor: e.target.value })} />
        </Field>
        <Field label="Most stays one run may check out" hint="1 – 100000. Default 500." required error={ok("cap") ? undefined : "1 – 100000"}>
          <Input type="number" value={vals.cap} onChange={(e) => setVals({ ...vals, cap: e.target.value })} />
        </Field>
        <Field label="Rooms a sweep may miss" hint="0 – 10000. Default 2." required error={ok("tol") ? undefined : "0 – 10000"}>
          <Input type="number" value={vals.tol} onChange={(e) => setVals({ ...vals, tol: e.target.value })} />
        </Field>
        <Field label="Sweeps remembered" hint="1 – 1000. Default 10." required error={ok("look") ? undefined : "1 – 1000"}>
          <Input type="number" value={vals.look} onChange={(e) => setVals({ ...vals, look: e.target.value })} />
        </Field>
        <Field label="Reason" hint="Recorded in the activity log." required>
          <Input value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} />
        </Field>
        <Field label="Confirm your password" required>
          <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
        </Field>
      </DialogForm>
    </Card>
  );
}
