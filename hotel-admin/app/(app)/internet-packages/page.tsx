"use client";

// INTERNET PACKAGES — what a guest is offered, managed like anything else an administrator manages.
//
// The list used to be a code, a status badge and a revision count, so the screen that owns the guest offer
// could not say what any package GAVE. Finding out meant opening the revision history, reading a service-plan
// revision id out of it and going to look that up on another page. Changing a speed meant doing that in
// reverse, by hand, in the right order — and the step everyone forgets is the last one, which is why a
// correctly published 10/5 Mbps plan revision left guests on 2 Mbps.
//
// So: one row per package showing what it actually gives, and Add / Edit / Enable / Disable. The revision
// chain underneath is unchanged and still immutable — lib/package-save decides which revisions a save needs,
// and History keeps them visible for audit.

import { Fragment, useCallback, useEffect, useState } from "react";
import { api, ApiError, ListResp } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogBody, ConfirmDialog,
} from "@/components/ui/dialog";
import { MonoId, SkeletonRows } from "@/components/ui/misc";
import { Package, Plus, AlertTriangle } from "lucide-react";
import { PackageForm, type PackageFormInitial, type PackageFormValue, type PlanOption } from "./package-form";
import { decideSave, saveOutcomeMessage } from "@/lib/package-save";
import { formatSpeed, formatData, formatDuration, formatDevices } from "@/lib/units";
import { allocationFromPolicy, stayLengthWarnings, readStayLength, type PackageStayRange } from "@/lib/stay-packages";
import type { EligibilityRuleForm, GrantTierForm, DurationForm } from "@/lib/commerce-form";

type PackageSummary = {
  package_id: string; code: string; active: boolean;
  current_revision_id: string; revision_count: number;
  name?: string | null; price_minor?: number | null; currency?: string | null;
  package_type?: string | null;
  // The eligibility-rule and grant-tier counts are NOT returned: edged authors those tables and holds no
  // SELECT on them, so reading them made this whole list 500 under the real runtime role.
  service_plan_id?: string | null; service_plan_revision_id?: string | null;
  service_plan_code?: string | null; service_plan_revision_no?: number | null;
  down_kbps?: number | null; up_kbps?: number | null;
  max_concurrent_devices?: number | null; device_limit_policy?: string | null;
  time_quota_seconds?: number | null; data_quota_bytes?: number | null;
  speed_allocation?: string | null;
  plan_has_newer_revision?: boolean;
};
type PlanSummary = PlanOption & { used_by_active_packages?: number };
type PackageCurrent = {
  package_id: string; code: string; revision_id: string; revision_no: number;
  service_plan_revision_id: string;
  display?: Record<string, unknown> | null;
  duration_policy?: Record<string, unknown> | null;
  data_allocation_policy?: Record<string, unknown> | null;
  eligibility_rules?: { Type?: string; type?: string; Value?: Record<string, unknown>; value?: Record<string, unknown> }[] | null;
  grant_tiers?: { Order?: number; order?: number; Value?: Record<string, unknown>; value?: Record<string, unknown> }[] | null;
  visible_from?: string | null; visible_until?: string | null;
};
type RevisionInfo = { revision_id: string; revision_no: number; is_current: boolean; price_minor?: number; currency?: string; package_type?: string };
type QuoteInspect = { id: string; package_revision_id: string; price_minor: number; currency: string; expires_at: string; consumed_at: string | null };
type PurchaseInspect = { id: string; package_revision_id: string; state: string; amount_minor: number; currency: string };

type Tab = "packages" | "inspection";

function useDisabled() {
  const [disabled, setDisabled] = useState(false);
  const guard = useCallback((e: unknown): boolean => {
    if (e instanceof ApiError && e.status === 503) { setDisabled(true); return true; }
    return false;
  }, []);
  return { disabled, guard };
}

export default function InternetPackagesPage() {
  const [tab, setTab] = useState<Tab>("packages");
  const [err, setErr] = useState<string | null>(null);
  const { disabled, guard } = useDisabled();

  return (
    <PageShell width="wide">
      <PageHeader
        eyebrow="Internet offering"
        title="Internet packages"
        description="An internet package is what a guest sees and takes on the portal: how fast it is, how much data and time it includes, and who is offered it."
      />
      {disabled ? (
        <Card><CardBody>
          <EmptyState
            icon={<Package />}
            title="The internet offering is not switched on for this appliance"
            hint="Internet packages become available once this capability is enabled for the site. Contact your StayConnect administrator."
          />
        </CardBody></Card>
      ) : (
        <Tabs value={tab} onValueChange={(v) => setTab(v as Tab)}>
          <TabsList>
            <TabsTrigger value="packages">Packages</TabsTrigger>
            <TabsTrigger value="inspection">Guest activity</TabsTrigger>
          </TabsList>
          <ErrorBanner err={err} className="mt-4" />
          <TabsContent value="packages" className="mt-4">
            <PackagesTab guard={guard} setErr={setErr} />
          </TabsContent>
          <TabsContent value="inspection" className="mt-4">
            <InspectionTab guard={guard} setErr={setErr} />
          </TabsContent>
        </Tabs>
      )}
    </PageShell>
  );
}

type TabProps = { guard: (e: unknown) => boolean; setErr: (s: string | null) => void };

function PackagesTab({ guard, setErr }: TabProps) {
  const [rows, setRows] = useState<PackageSummary[] | null>(null);
  const [plans, setPlans] = useState<PlanSummary[]>([]);
  const [history, setHistory] = useState<Record<string, RevisionInfo[]>>({});
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<PackageFormInitial | null>(null);
  const [editingID, setEditingID] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [stayWarnings, setStayWarnings] = useState<string[]>([]);
  // THE DISABLE CONFIRMATION, which used to be two window.prompt() calls in a row.
  //
  // The first asked for a reason, the second for a password -- in a browser dialog that shows what you type, with
  // no way to say what the action does, and cancelling the second left the operator with no idea whether the
  // first had already taken effect. None of that is a property of the code; all of it is a property of
  // window.prompt, which is why it is gone.
  const [disabling, setDisabling] = useState<PackageSummary | null>(null);
  const [actionErr, setActionErr] = useState<unknown>(null);

  const load = useCallback(async () => {
    try {
      const [pk, pl] = await Promise.all([
        api.get<ListResp<PackageSummary>>("/commercial-packages"),
        api.get<ListResp<PlanSummary>>("/commercial-packages/plans"),
      ]);
      setRows(pk.data ?? []); setPlans(pl.data ?? []);

      // THE STAY-LENGTH OVERLAP CHECK.
      //
      // The list does not carry eligibility rules -- edged authors them and cannot read the table back, so
      // they come one package at a time through the scoped reader. That is a handful of extra reads on a page
      // with a handful of packages, and it buys the one thing the operator cannot work out by eye: whether
      // "1-7 / 8-10 / 11+" actually tiles the range or quietly double-covers part of it.
      //
      // It is a WARNING. Overlapping ranges are a legitimate design when the operator wants the guest to
      // choose, so nothing here rewrites a rule -- it says what will happen.
      const active = (pk.data ?? []).filter((p) => p.active);
      const ranges = await Promise.all(active.map(async (p): Promise<PackageStayRange> => {
        try {
          const cur = await api.get<PackageCurrent>(`/commercial-packages/${p.package_id}/current`);
          return {
            package_id: p.package_id, name: p.name || p.code, active: true,
            range: readStayLength((cur.eligibility_rules ?? []).map((r) => ({
              type: r.type ?? r.Type, value: r.value ?? r.Value,
            }))),
          };
        } catch {
          // A package whose conditions cannot be read is not evidence of an overlap. Reporting one from a
          // failed read would be a warning about nothing.
          return { package_id: p.package_id, name: p.name || p.code, active: true, range: null };
        }
      }));
      setStayWarnings(stayLengthWarnings(ranges));
    } catch (e) { if (!guard(e)) setErr((e as Error)?.message ?? "Could not load packages"); }
  }, [guard, setErr]);
  useEffect(() => { load(); }, [load]);

  async function toggleHistory(id: string) {
    if (history[id]) { setHistory((h) => { const n = { ...h }; delete n[id]; return n; }); return; }
    try {
      const r = await api.get<ListResp<RevisionInfo>>(`/commercial-packages/${id}/revisions`);
      setHistory((s) => ({ ...s, [id]: r.data ?? [] }));
    } catch (e) { if (!guard(e)) setErr((e as Error)?.message ?? "Could not load history"); }
  }

  // EDIT loads the CURRENT configuration — including the eligibility rules and grant tiers the form does not
  // display prominently. Saving republishes all of it, so anything not loaded would be silently dropped.
  async function startEdit(p: PackageSummary) {
    setErr(null); setNotice(null);
    try {
      const cur = await api.get<PackageCurrent>(`/commercial-packages/${p.package_id}/current`);
      const plan = plans.find((x) => x.plan_id === p.service_plan_id);
      setEditingID(p.package_id);
      setEditing({
        code: p.code,
        name: (cur.display?.name as string) ?? p.name ?? p.code,
        // The plan this package uses today, preselected so Edit opens on what is actually in force.
        planID: p.service_plan_id ?? "",
        planRevisionID: cur.service_plan_revision_id,
        rules: rulesToForm(cur.eligibility_rules),
        tiers: tiersToForm(cur.grant_tiers),
        duration: durationToForm(cur.duration_policy),
        visibleFrom: toLocalInput(cur.visible_from),
        visibleUntil: toLocalInput(cur.visible_until),
        // Loaded, not defaulted: a package on a per-night allowance must not be flattened by an edit that
        // was only meant to rename it.
        allocation: allocationFromPolicy(cur.data_allocation_policy),
      });
      setAdding(false);
    } catch (e) { if (!guard(e)) setErr((e as Error)?.message ?? "Could not open this package"); }
  }

  // SAVE. One operator action; lib/package-save decides whether that needs a new service-plan revision as
  // well as the package revision, and in which order.
  // SAVE. One operator action. It publishes a new immutable package revision pinned to the selected Service
  // Plan's current revision — and it NEVER creates or edits a Service Plan. Authoring plan settings from here
  // is what let a package silently change a plan other packages share; that belongs on Service plans.
  async function save(v: PackageFormValue) {
    setBusy(true); setErr(null); setNotice(null);
    try {
      const selected = plans.find((x) => x.plan_id === v.selectedPlanID);
      if (!selected?.current_revision_id) {
        setErr("That service plan has no settings published yet. Open Service plans and save it first.");
        return;
      }
      const decision = decideSave({
        pinnedPlanRevisionID: editing?.planRevisionID ?? "",
        selected,
        currentPlanID: editing?.planID,
      });
      await api.post("/commercial-packages", {
        ...v.payload,
        service_plan_revision_id: decision.pinPlanRevisionID,
      });
      setAdding(false); setEditing(null); setEditingID(null);
      setNotice(saveOutcomeMessage(decision));
      await load();
    } catch (e) { if (!guard(e)) setErr((e as Error)?.message ?? "Could not save this package"); }
    finally { setBusy(false); }
  }

  async function enable(p: PackageSummary) {
    setBusy(true); setErr(null); setNotice(null);
    try {
      await api.post(`/commercial-packages/${p.package_id}/active`, { active: true });
      setNotice(`${p.name || p.code} is being offered to guests again.`);
      await load();
    } catch (e) { if (!guard(e)) setErr((e as Error)?.message ?? "Could not update this package"); }
    finally { setBusy(false); }
  }

  async function disable({ reason, password }: { reason: string; password: string }) {
    if (!disabling) return;
    setBusy(true); setActionErr(null); setNotice(null);
    try {
      await api.post(`/commercial-packages/${disabling.package_id}/active`, { active: false, reason, password });
      setNotice(`${disabling.name || disabling.code} is no longer offered to guests. Anyone already online keeps what they have.`);
      setDisabling(null);
      await load();
    } catch (e) { setActionErr(e); }
    finally { setBusy(false); }
  }

  return (
    <div className="space-y-4">
      {notice && <Callout tone="success">{notice}</Callout>}
      {stayWarnings.length > 0 && (
        <div data-testid="stay-length-warnings">
          <Callout tone="warning" title="Overlapping stay lengths">
            <ul className="list-disc space-y-0.5 pl-4">
              {stayWarnings.map((w, i) => <li key={i}>{w}</li>)}
            </ul>
            <p className="mt-1 text-xs">
              Nothing has been changed. If you meant each length to have one package, adjust the night ranges.
            </p>
          </Callout>
        </div>
      )}
      <div className="flex justify-end">
        <Button onClick={() => { setAdding(true); setEditing(null); setEditingID(null); }}>
          <Plus /> Add package
        </Button>
      </div>

      {/*
        THE FORM IS A MODAL NOW. It used to be a card pushed above the table, with Edit pushing a second one
        below it -- so on a list of a dozen packages an operator clicked Edit and nothing visibly happened,
        because the form had opened off-screen. With both open at once nothing said which package Save applied to.
      */}
      <Dialog open={adding} onOpenChange={(v) => !v && setAdding(false)}>
        <DialogContent size="xl">
          <DialogHeader>
            <DialogTitle>Add an internet package</DialogTitle>
            <DialogDescription>
              What the guest is offered on the portal, and the service plan it hands out.
            </DialogDescription>
          </DialogHeader>
          <DialogBody>
            <PackageForm mode="add" plans={plans} busy={busy} onSave={save} onCancel={() => setAdding(false)} />
          </DialogBody>
        </DialogContent>
      </Dialog>

      <Dialog open={editing !== null} onOpenChange={(v) => { if (!v) { setEditing(null); setEditingID(null); } }}>
        <DialogContent size="xl">
          <DialogHeader>
            <DialogTitle>{editing ? `Edit ${editing.name || editing.code}` : "Edit package"}</DialogTitle>
            <DialogDescription>
              Saving records a new permanent version of this package. Guests already online keep the terms they
              connected under.
            </DialogDescription>
          </DialogHeader>
          <DialogBody>
            {editing && (
              <PackageForm mode="edit" initial={editing} plans={plans} busy={busy} onSave={save}
                onCancel={() => { setEditing(null); setEditingID(null); }} />
            )}
          </DialogBody>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={disabling !== null}
        onOpenChange={(v) => !v && setDisabling(null)}
        title={`Stop offering ${disabling?.name || disabling?.code || "this package"}?`}
        description="Guests will stop being offered it immediately. Anyone already online keeps the access they were given, and it can be switched back on at any time."
        confirmLabel="Stop offering it"
        confirmVariant="danger"
        busy={busy}
        error={actionErr}
        requireReason
        reasonLabel="Why are you disabling it?"
        reasonPlaceholder="Replaced by the new summer package"
        requirePassword
        onConfirm={disable}
      />

      <Card><CardBody className="p-0">
        {rows === null ? (
          <SkeletonRows rows={4} cols={7} />
        ) : rows.length === 0 ? (
          <EmptyState icon={<Package />} title="No internet packages yet"
            hint="Until a package exists, a verified guest has nothing to be given and cannot get online."
            action={<Button onClick={() => setAdding(true)}><Plus /> Add the first package</Button>} />
        ) : (
          <Table>
            <THead><TR>
              <TH>Package</TH><TH>Status</TH><TH>Price</TH><TH>Speed</TH><TH>Data</TH><TH>Time</TH>
              <TH>Devices</TH><TH></TH>
            </TR></THead>
            <tbody>
              {rows.map((p) => (
                <Fragment key={p.package_id}>
                  <TR>
                    <TD>
                      <div className="font-medium">{p.name || p.code}</div>
                      {/* Only when it adds something. A package with no separate display name printed its
                          code twice, one line under the other. */}
                      {p.name && p.name !== p.code && <div className="text-xs text-muted-foreground">{p.code}</div>}
                    </TD>
                    <TD>{p.active ? <Badge tone="ok">Active</Badge> : <Badge tone="default">Disabled</Badge>}</TD>
                    <TD>{p.price_minor ? `${p.price_minor} ${p.currency ?? ""}` : "Free"}</TD>
                    <TD>
                      <div>{formatSpeed(p.down_kbps)} down{p.speed_allocation === "SHARED" ? " (shared)" : ""}</div>
                      <div className="text-xs text-muted-foreground">{formatSpeed(p.up_kbps)} up</div>
                    </TD>
                    <TD>{formatData(p.data_quota_bytes)}</TD>
                    <TD>{formatDuration(p.time_quota_seconds)}</TD>
                    <TD>{formatDevices(p.max_concurrent_devices)}</TD>
                    <TD className="whitespace-nowrap text-right">
                      <Button size="sm" variant="ghost" disabled={busy} onClick={() => startEdit(p)}>Edit</Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        disabled={busy}
                        onClick={() => {
                          setActionErr(null);
                          if (p.active) setDisabling(p); else void enable(p);
                        }}
                      >
                        {p.active ? "Disable" : "Enable"}
                      </Button>
                      <Button size="sm" variant="ghost" onClick={() => toggleHistory(p.package_id)}>History</Button>
                    </TD>
                  </TR>
                  {/* The one internal fact worth surfacing: the plan this package uses has moved on and this
                      package has not. That is precisely the state that silently produced a 2 Mbps guest. */}
                  {p.plan_has_newer_revision && (
                    <TR>
                      <TD colSpan={8} className="bg-warning-subtle/40 text-xs">
                        <span className="inline-flex items-center gap-1.5 text-warning-subtle-foreground">
                          <AlertTriangle size={13} />
                          The <strong>{p.service_plan_code}</strong> service plan has newer settings that this
                          package does not use. Open Edit and save to bring it up to date.
                        </span>
                      </TD>
                    </TR>
                  )}
                  {history[p.package_id] && (
                    <TR>
                      <TD colSpan={8} className="bg-surface/50 text-xs">
                        <div className="mb-1 font-medium">History</div>
                        {history[p.package_id].map((r) => (
                          <div key={r.revision_id} className="py-0.5">
                            #{r.revision_no}{" "}
                            {r.is_current ? <Badge tone="info">in force</Badge> : <span className="text-muted-foreground">superseded</span>}{" "}
                            {r.price_minor === 0 ? "free" : `${r.price_minor} ${r.currency ?? ""}`}
                          </div>
                        ))}
                        <p className="mt-1 text-muted-foreground">
                          Each saved change is kept permanently. A guest keeps the terms that applied
                          when they connected.
                        </p>
                      </TD>
                    </TR>
                  )}
                </Fragment>
              ))}
            </tbody>
          </Table>
        )}
      </CardBody></Card>
    </div>
  );
}

// ---------------------------------------------------------------------------------------------------------
// shaping the API's current-configuration into the form's shape
// ---------------------------------------------------------------------------------------------------------


// Go marshals these structs with capitalised keys (the fields carry no json tags), so both spellings are
// accepted rather than assuming one. Getting this wrong would drop rules on save.
const pick = <T,>(a: T | undefined, b: T | undefined): T | undefined => (a !== undefined ? a : b);

function rulesToForm(rules: PackageCurrent["eligibility_rules"]): EligibilityRuleForm[] {
  const out: EligibilityRuleForm[] = [];
  for (const r of rules ?? []) {
    const type = pick(r.type, r.Type);
    const value = (pick(r.value, r.Value) ?? {}) as Record<string, unknown>;
    const list = (k: string) => (Array.isArray(value[k]) ? (value[k] as string[]).join(", ") : "");
    const num = (v: unknown) => (typeof v === "number" && Number.isFinite(v) ? String(v) : "");
    switch (type) {
      case "AUTH_METHOD": out.push({ type: "AUTH_METHOD", methods: list("methods") }); break;
      case "SUBJECT_KIND": out.push({ type: "SUBJECT_KIND", kinds: list("kinds") }); break;
      case "DATE_WINDOW": out.push({
        type: "DATE_WINDOW",
        from: toLocalInput(value.from as string | undefined) ?? "",
        until: toLocalInput(value.until as string | undefined) ?? "",
      }); break;
      case "PRIOR_PURCHASE": out.push({
        type: "PRIOR_PURCHASE", mode: value.requires_prior ? "requires_prior" : "forbids_prior",
      }); break;
      case "SITE_NETWORK": out.push({ type: "SITE_NETWORK", guest_network_ids: list("guest_network_ids") }); break;
      // THE STAY DIMENSIONS. A bound that is absent stays absent: reading a missing max_nights back as "0"
      // would republish "up to zero nights", i.e. a package for nobody.
      case "STAY_LENGTH": out.push({
        type: "STAY_LENGTH",
        min_nights: num(value.min_nights),
        max_nights: num(value.max_nights),
      }); break;
      case "ROOM_TYPE": out.push({ type: "ROOM_TYPE", room_types: list("room_types") }); break;
      case "RATE_PLAN": out.push({ type: "RATE_PLAN", rate_plans: list("rate_plans") }); break;
      case "VIP": out.push({ type: "VIP", is_vip: value.is_vip === false ? "false" : "true" }); break;
      case "TRAVEL_AGENT": out.push({ type: "TRAVEL_AGENT", travel_agents: list("travel_agents") }); break;
      case "PMS_INTERFACE": out.push({
        type: "PMS_INTERFACE", pms_interface_ids: list("pms_interface_ids"),
      }); break;
      default: break; // an unknown/unsupported type is not re-emitted; the form only offers supported ones
    }
  }
  return out;
}

function tiersToForm(tiers: PackageCurrent["grant_tiers"]): GrantTierForm[] {
  const out: GrantTierForm[] = [];
  for (const t of tiers ?? []) {
    const value = (pick(t.value, t.Value) ?? {}) as Record<string, unknown>;
    out.push({
      order: pick(t.order, t.Order) ?? 10,
      down_kbps: (value.down_kbps as number | undefined) ?? "",
      up_kbps: (value.up_kbps as number | undefined) ?? "",
    });
  }
  return out;
}

function durationToForm(d: PackageCurrent["duration_policy"]): DurationForm {
  const mode = (d?.end_mode as string) ?? "MANUAL_END";
  if (mode === "VALIDITY_WINDOW") return { end_mode: "VALIDITY_WINDOW", duration_seconds: Number(d?.duration_seconds ?? 0) };
  if (mode === "FIXED_AT") return { end_mode: "FIXED_AT", ends_at: toLocalInput(d?.ends_at as string | undefined) ?? "" };
  return { end_mode: "MANUAL_END" };
}

// datetime-local wants "YYYY-MM-DDTHH:mm" in local time; the API speaks RFC3339.
function toLocalInput(iso?: string | null): string | undefined {
  if (!iso) return undefined;
  const t = new Date(iso);
  if (!Number.isFinite(t.getTime())) return undefined;
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${t.getFullYear()}-${pad(t.getMonth() + 1)}-${pad(t.getDate())}T${pad(t.getHours())}:${pad(t.getMinutes())}`;
}

function InspectionTab({ guard, setErr }: TabProps) {
  const [quotes, setQuotes] = useState<QuoteInspect[]>([]);
  const [purchases, setPurchases] = useState<PurchaseInspect[]>([]);

  useEffect(() => {
    (async () => {
      try {
        const [q, p] = await Promise.all([
          api.get<ListResp<QuoteInspect>>("/commercial-packages/quotes"),
          api.get<ListResp<PurchaseInspect>>("/commercial-packages/purchases"),
        ]);
        setQuotes(q.data ?? []); setPurchases(p.data ?? []);
      } catch (e) { if (!guard(e)) setErr((e as Error)?.message ?? "Failed to load"); }
    })();
  }, [guard, setErr]);

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader><CardTitle>Packages offered to guests</CardTitle></CardHeader>
        <CardBody>
          {quotes.length === 0 ? <EmptyState title="No quotes" /> : (
            <Table>
              <THead><TR><TH>ID</TH><TH>Revision</TH><TH>Price</TH><TH>Expires</TH><TH>Consumed</TH></TR></THead>
              <tbody>{quotes.map((q) => (
                <TR key={q.id}><TD><MonoId value={q.id} title="Quote" /></TD><TD><MonoId value={q.package_revision_id} title="Package version" /></TD>
                  <TD>{q.price_minor === 0 ? "free" : `${q.price_minor} ${q.currency}`}</TD><TD className="text-xs">{q.expires_at}</TD><TD className="text-xs">{q.consumed_at || "—"}</TD></TR>
              ))}</tbody>
            </Table>
          )}
        </CardBody>
      </Card>
      <Card>
        <CardHeader><CardTitle>Packages taken by guests</CardTitle></CardHeader>
        <CardBody>
          {purchases.length === 0 ? <EmptyState title="No purchases" /> : (
            <Table>
              <THead><TR><TH>ID</TH><TH>Revision</TH><TH>State</TH><TH>Amount</TH></TR></THead>
              <tbody>{purchases.map((p) => (
                <TR key={p.id}><TD><MonoId value={p.id} title="Purchase" /></TD><TD><MonoId value={p.package_revision_id} title="Package version" /></TD>
                  <TD><Badge tone={p.state === "GRANTED" ? "ok" : "default"}>{p.state}</Badge></TD><TD>{p.amount_minor === 0 ? "free" : `${p.amount_minor} ${p.currency}`}</TD></TR>
              ))}</tbody>
            </Table>
          )}
        </CardBody>
      </Card>
    </div>
  );
}
