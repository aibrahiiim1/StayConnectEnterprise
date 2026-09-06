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
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X, AlertTriangle } from "lucide-react";
import { PackageForm, type PackageFormInitial, type PackageFormValue, type PlanOption } from "./package-form";
import { decideSave, saveOutcomeMessage } from "@/lib/package-save";
import { formatSpeed, formatData, formatDuration, formatDevices } from "@/lib/units";
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

  const tabs: { id: Tab; label: string }[] = [
    { id: "packages", label: "Packages" },
    { id: "inspection", label: "Guest activity" },
  ];

  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-lg font-semibold">Internet packages</h1>
        <p className="text-sm text-muted mt-1 max-w-2xl">
          An internet package is what a guest sees and takes on the portal: how fast it is, how much data and
          time it includes, and who is offered it.
        </p>
      </div>
      {disabled ? (
        <Card><CardBody>
          <EmptyState title="The internet offering is not switched on for this appliance"
            hint="Internet packages become available once this capability is enabled for the site. Contact your StayConnect administrator." />
        </CardBody></Card>
      ) : (
        <>
          <div className="flex gap-2 border-b border-border">
            {tabs.map((t) => (
              <button key={t.id} onClick={() => setTab(t.id)}
                className={`px-3 py-2 text-sm border-b-2 ${tab === t.id ? "border-brand text-text" : "border-transparent text-muted hover:text-text"}`}>
                {t.label}
              </button>
            ))}
          </div>
          {err && <div className="text-sm text-red-500">{err}</div>}
          {tab === "packages" && <PackagesTab guard={guard} setErr={setErr} />}
          {tab === "inspection" && <InspectionTab guard={guard} setErr={setErr} />}
        </>
      )}
    </div>
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

  const load = useCallback(async () => {
    try {
      const [pk, pl] = await Promise.all([
        api.get<ListResp<PackageSummary>>("/commercial-packages"),
        api.get<ListResp<PlanSummary>>("/commercial-packages/plans"),
      ]);
      setRows(pk.data ?? []); setPlans(pl.data ?? []);
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

  async function toggleActive(p: PackageSummary) {
    setBusy(true); setErr(null); setNotice(null);
    try {
      if (p.active) {
        const reason = window.prompt("Why are you disabling this package? Guests will stop being offered it immediately.");
        if (!reason) { setBusy(false); return; }
        const password = window.prompt("Confirm your password to disable");
        if (!password) { setBusy(false); return; }
        await api.post(`/commercial-packages/${p.package_id}/active`, { active: false, reason, password });
      } else {
        await api.post(`/commercial-packages/${p.package_id}/active`, { active: true });
      }
      await load();
    } catch (e) { if (!guard(e)) setErr((e as Error)?.message ?? "Could not update this package"); }
    finally { setBusy(false); }
  }

  return (
    <div className="space-y-3">
      {notice && (
        <div className="text-sm rounded-md border border-border bg-panel2 px-3 py-2" role="status">{notice}</div>
      )}
      <div className="flex justify-end">
        <Button onClick={() => { setAdding((v) => !v); setEditing(null); setEditingID(null); }}>
          {adding ? <X size={16} /> : <Plus size={16} />}{adding ? "Cancel" : "Add package"}
        </Button>
      </div>

      {adding && (
        <Card>
          <CardHeader><CardTitle>Add package</CardTitle></CardHeader>
          <CardBody><PackageForm mode="add" plans={plans} busy={busy} onSave={save} onCancel={() => setAdding(false)} /></CardBody>
        </Card>
      )}

      {editing && (
        <Card>
          <CardHeader><CardTitle>Edit {editing.name || editing.code}</CardTitle></CardHeader>
          <CardBody>
            <PackageForm mode="edit" initial={editing} plans={plans} busy={busy} onSave={save}
              onCancel={() => { setEditing(null); setEditingID(null); }} />
          </CardBody>
        </Card>
      )}

      <Card><CardBody>
        {rows === null ? (
          <div className="text-sm text-muted py-6 text-center">Loading packages…</div>
        ) : rows.length === 0 ? (
          <EmptyState title="No internet packages yet"
            hint="Until a package exists, a verified guest has nothing to be given and cannot get online." />
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
                      {p.name && p.name !== p.code && <div className="text-xs text-muted">{p.code}</div>}
                    </TD>
                    <TD>{p.active ? <Badge tone="ok">Active</Badge> : <Badge tone="default">Disabled</Badge>}</TD>
                    <TD>{p.price_minor ? `${p.price_minor} ${p.currency ?? ""}` : "Free"}</TD>
                    <TD>
                      <div>{formatSpeed(p.down_kbps)} down{p.speed_allocation === "SHARED" ? " (shared)" : ""}</div>
                      <div className="text-xs text-muted">{formatSpeed(p.up_kbps)} up</div>
                    </TD>
                    <TD>{formatData(p.data_quota_bytes)}</TD>
                    <TD>{formatDuration(p.time_quota_seconds)}</TD>
                    <TD>{formatDevices(p.max_concurrent_devices)}</TD>
                    <TD className="whitespace-nowrap">
                      <Button variant="ghost" disabled={busy} onClick={() => startEdit(p)}>Edit</Button>
                      <Button variant="ghost" disabled={busy} onClick={() => toggleActive(p)}>
                        {p.active ? "Disable" : "Enable"}
                      </Button>
                      <button className="underline text-muted text-xs ml-2" onClick={() => toggleHistory(p.package_id)}>
                        History
                      </button>
                    </TD>
                  </TR>
                  {/* The one internal fact worth surfacing: the plan this package uses has moved on and this
                      package has not. That is precisely the state that silently produced a 2 Mbps guest. */}
                  {p.plan_has_newer_revision && (
                    <TR>
                      <TD colSpan={8} className="text-xs">
                        <span className="inline-flex items-center gap-1 text-amber-500">
                          <AlertTriangle size={13} />
                          The <strong>{p.service_plan_code}</strong> service plan has newer settings that this
                          package does not use. Open Edit and save to bring it up to date.
                        </span>
                      </TD>
                    </TR>
                  )}
                  {history[p.package_id] && (
                    <TR>
                      <TD colSpan={8} className="text-xs bg-panel2/40">
                        <div className="font-medium mb-1">History</div>
                        {history[p.package_id].map((r) => (
                          <div key={r.revision_id} className="py-0.5">
                            #{r.revision_no}{" "}
                            {r.is_current ? <Badge tone="info">in force</Badge> : <span className="text-muted">superseded</span>}{" "}
                            {r.price_minor === 0 ? "free" : `${r.price_minor} ${r.currency ?? ""}`}
                          </div>
                        ))}
                        <p className="text-muted mt-1">
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
      {editingID && null}
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
                <TR key={q.id}><TD className="font-mono text-xs">{q.id}</TD><TD className="font-mono text-xs">{q.package_revision_id}</TD>
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
                <TR key={p.id}><TD className="font-mono text-xs">{p.id}</TD><TD className="font-mono text-xs">{p.package_revision_id}</TD>
                  <TD><Badge tone={p.state === "GRANTED" ? "ok" : "default"}>{p.state}</Badge></TD><TD>{p.amount_minor === 0 ? "free" : `${p.amount_minor} ${p.currency}`}</TD></TR>
              ))}</tbody>
            </Table>
          )}
        </CardBody>
      </Card>
    </div>
  );
}
