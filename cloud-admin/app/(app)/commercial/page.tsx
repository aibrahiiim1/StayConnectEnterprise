"use client";

import { useCallback, useEffect, useState } from "react";
import { api, withStepUp } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { RefreshCw } from "lucide-react";

type Tenant = { id: string; slug: string; name: string };
type Plan = { id: string; code: string; name: string; billing_cycle: string;
  price_cents?: number; currency?: string; is_active?: boolean; is_public?: boolean };
type Sub = {
  id: string; plan_code?: string; status: string; billing_cycle: string;
  current_period_start: string; current_period_end: string;
  trial_end?: string | null; auto_renew?: boolean;
};
type Limit = {
  key: string; value_type: string; int_value?: number | null;
  bool_value?: boolean | null; str_value?: string | null; unit?: string; source?: string;
};
type Override = Limit & { starts_at: string; expires_at?: string | null; reason: string; in_effect: boolean };

function limitValue(l: Limit): string {
  if (l.value_type === "int") return String(l.int_value ?? "");
  if (l.value_type === "bool") return String(l.bool_value ?? "");
  return String(l.str_value ?? "");
}

/**
 * Commercial console.
 *
 * Entitlement resolution is layered and deterministic:
 *   Plan limits -> Subscription terms -> Tenant overrides -> the SIGNED LICENSE.
 * The license is a signed snapshot: editing a plan or an override never rewrites
 * an already-issued license — it must be re-issued for new terms to reach an
 * appliance. Every change here requires a reason and is audited.
 */
export default function CommercialPage() {
  const [tenants, setTenants] = useState<Tenant[]>([]);
  const [tenantID, setTenantID] = useState("");
  const [plans, setPlans] = useState<Plan[]>([]);
  const [sub, setSub] = useState<Sub | null>(null);
  const [eff, setEff] = useState<Limit[]>([]);
  const [overrides, setOverrides] = useState<Override[]>([]);
  const [planID, setPlanID] = useState("");
  const [planLimits, setPlanLimits] = useState<Limit[]>([]);
  const [planVersion, setPlanVersion] = useState<number | null>(null);
  const [activation, setActivation] = useState("active");
  const [err, setErr] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const [allPlans, setAllPlans] = useState<Plan[]>([]);
  const loadPlans = useCallback(async () => {
    const [pub, all] = await Promise.all([
      api.get<{ data: Plan[] }>("/v1/plans").catch(() => ({ data: [] as Plan[] })),
      api.get<{ data: Plan[] }>("/v1/plans?all=true").catch(() => ({ data: [] as Plan[] })),
    ]);
    setPlans(pub.data ?? []);
    setAllPlans(all.data ?? []);
    if (pub.data?.[0]) setPlanID((cur) => cur || pub.data[0].id);
  }, []);

  useEffect(() => {
    api.get<{ data: Tenant[] }>("/v1/tenants").then((r) => setTenants(r.data ?? []));
    loadPlans();
  }, [loadPlans]);

  // ---- Plan catalog (platform product definition) ----
  async function createPlan(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    const el = e.currentTarget;
    setBusy(true); setErr(null); setMsg(null);
    try {
      await api.post("/v1/plans", {
        code: String(f.get("code") || "").trim(),
        name: String(f.get("name") || "").trim(),
        billing_cycle: f.get("billing_cycle") || "monthly",
        price_cents: Number(f.get("price_cents")) || 0,
        currency: String(f.get("currency") || "USD").trim().toUpperCase(),
        is_public: f.get("is_public") === "on",
        is_active: true,
      });
      setMsg("Plan created."); el.reset(); await loadPlans();
    } catch (e: any) { setErr(e?.body?.message ?? e?.message ?? "Create failed"); }
    finally { setBusy(false); }
  }
  async function editPlan(p: Plan) {
    const name = window.prompt("Plan name:", p.name); if (name === null) return;
    const price = window.prompt("Price (cents):", String(p.price_cents ?? 0)); if (price === null) return;
    setBusy(true); setErr(null); setMsg(null);
    try {
      await api.patch(`/v1/plans/${p.id}`, { name: name.trim() || p.name, price_cents: Number(price) || 0 });
      setMsg("Plan updated."); await loadPlans();
    } catch (e: any) { setErr(e?.body?.message ?? e?.message ?? "Update failed"); }
    finally { setBusy(false); }
  }
  async function togglePlanActive(p: Plan) {
    setBusy(true); setErr(null); setMsg(null);
    try {
      await api.patch(`/v1/plans/${p.id}`, { is_active: !p.is_active });
      setMsg(`Plan ${p.is_active ? "deactivated" : "activated"}.`); await loadPlans();
    } catch (e: any) { setErr(e?.body?.message ?? e?.message ?? "Update failed"); }
    finally { setBusy(false); }
  }
  async function retirePlan(p: Plan) {
    if (!confirm(`Retire plan "${p.name}"? It can no longer be selected for new subscriptions, but stays visible in history and never changes existing signed licenses.`)) return;
    setBusy(true); setErr(null); setMsg(null);
    try {
      await api.post(`/v1/plans/${p.id}/retire`);
      setMsg("Plan retired."); await loadPlans();
    } catch (e: any) { setErr(e?.body?.message ?? e?.message ?? "Retire failed"); }
    finally { setBusy(false); }
  }

  const loadTenant = useCallback(async (id: string) => {
    if (!id) return;
    setErr(null);
    const [s, e, o] = await Promise.all([
      api.get<Sub>(`/v1/tenants/${id}/subscription`).catch(() => null),
      api.get<{ data: Limit[] }>(`/v1/tenants/${id}/effective-limits`).catch(() => ({ data: [] })),
      api.get<{ data: Override[] }>(`/v1/tenants/${id}/limit-overrides`).catch(() => ({ data: [] })),
    ]);
    setSub(s); setEff(e.data ?? []); setOverrides(o.data ?? []);
  }, []);
  useEffect(() => { loadTenant(tenantID); }, [tenantID, loadTenant]);

  const loadPlanLimits = useCallback(async (id: string) => {
    if (!id) return;
    const r = await api.get<{ data: Limit[]; limits_version: number }>(`/v1/plans/${id}/limits`).catch(() => null);
    setPlanLimits(r?.data ?? []);
    setPlanVersion(r?.limits_version ?? null);
  }, []);
  useEffect(() => { loadPlanLimits(planID); }, [planID, loadPlanLimits]);

  // ---- Subscription with EXPLICIT operator-chosen terms ----
  async function createSubscription(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!tenantID) { setErr("Pick a customer first."); return; }
    const f = new FormData(e.currentTarget);
    setBusy(true); setErr(null); setMsg(null);
    try {
      const body: any = {
        plan_id: f.get("plan_id"),
        activation: f.get("activation"),
        billing_cycle: f.get("billing_cycle") || undefined,
        start_date: f.get("start_date") || undefined,
        renewal_date: f.get("renewal_date") || undefined,
        auto_renew: f.get("auto_renew") === "on",
        reason: f.get("reason"),
      };
      if (f.get("activation") === "trial") body.trial_end = f.get("trial_end") || undefined;
      const r = await withStepUp(() => api.post<Sub>(`/v1/tenants/${tenantID}/subscription-terms`, body));
      setMsg(`Subscription created: ${r.status} · ${r.billing_cycle} · renews ${new Date(r.current_period_end).toLocaleDateString()}`);
      await loadTenant(tenantID);
    } catch (e: any) {
      setErr(e?.body?.message ?? e?.message ?? "Failed");
    } finally { setBusy(false); }
  }

  // ---- Tenant override ----
  async function setOverride(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!tenantID) { setErr("Pick a customer first."); return; }
    const f = new FormData(e.currentTarget);
    const el = e.currentTarget;
    const vt = String(f.get("value_type"));
    const body: any = {
      key: f.get("key"), value_type: vt, reason: f.get("reason"),
      starts_at: f.get("starts_at") || undefined,
      expires_at: f.get("expires_at") || undefined,
    };
    const raw = String(f.get("value") ?? "");
    if (vt === "int") body.int_value = Number(raw);
    else if (vt === "bool") body.bool_value = raw === "true";
    else body.str_value = raw;
    setBusy(true); setErr(null); setMsg(null);
    try {
      await withStepUp(() => api.put(`/v1/tenants/${tenantID}/limit-overrides`, body));
      setMsg("Override saved. Re-issue the license for it to reach the appliance.");
      el.reset();
      await loadTenant(tenantID);
    } catch (e: any) {
      setErr(e?.body?.message ?? e?.message ?? "Failed");
    } finally { setBusy(false); }
  }

  async function cancelSubscription() {
    if (!tenantID || !sub) return;
    const reason = window.prompt("Reason for cancelling this subscription (audited):", "");
    if (reason === null) return;
    setBusy(true); setErr(null); setMsg(null);
    try {
      await withStepUp(() => api.post(`/v1/tenants/${tenantID}/subscription/cancel`, { reason }));
      setMsg("Subscription canceled.");
      await loadTenant(tenantID);
    } catch (e: any) { setErr(e?.body?.message ?? e?.message ?? "Cancel failed"); }
    finally { setBusy(false); }
  }

  async function removeOverride(key: string) {
    const reason = window.prompt(`Reason for removing override "${key}"?`);
    if (!reason) return;
    setBusy(true); setErr(null);
    try {
      await withStepUp(() => api.del(`/v1/tenants/${tenantID}/limit-overrides/${key}?reason=${encodeURIComponent(reason)}`));
      setMsg(`Override ${key} removed.`);
      await loadTenant(tenantID);
    } catch (e: any) {
      setErr(e?.body?.message ?? e?.message ?? "Failed");
    } finally { setBusy(false); }
  }

  // ---- Plan limit (vendor product definition) ----
  async function setPlanLimit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    const el = e.currentTarget;
    const vt = String(f.get("value_type"));
    const body: any = { key: f.get("key"), value_type: vt, reason: f.get("reason"), unit: f.get("unit") || undefined };
    const raw = String(f.get("value") ?? "");
    if (vt === "int") body.int_value = Number(raw);
    else if (vt === "bool") body.bool_value = raw === "true";
    else body.str_value = raw;
    setBusy(true); setErr(null); setMsg(null);
    try {
      const r = await withStepUp(() => api.put<{ limits_version: number }>(`/v1/plans/${planID}/limits`, body));
      setMsg(`Plan limit saved (plan limits_version ${r.limits_version}). Already-issued licenses are unchanged — re-issue to apply.`);
      el.reset();
      await loadPlanLimits(planID);
      await loadTenant(tenantID);
    } catch (e: any) {
      setErr(e?.body?.message ?? e?.message ?? "Failed");
    } finally { setBusy(false); }
  }

  return (
    <div className="p-6 max-w-7xl mx-auto space-y-6">
      <div className="flex items-baseline justify-between">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Legacy · read-only</div>
          <h1 className="text-2xl font-semibold">Plans, subscription &amp; entitlements <span className="text-sm font-normal text-muted">(retired)</span></h1>
        </div>
        <Button variant="ghost" onClick={() => { loadTenant(tenantID); loadPlanLimits(planID); }}>
          <RefreshCw size={14} /> Refresh
        </Button>
      </div>

      <div className="rounded-md border border-warn/40 bg-warn/5 px-4 py-3 text-sm text-warn">
        This legacy commercial model (plans → subscription → overrides) is <strong>retired</strong> and has
        <strong> no effect on issued licenses</strong>. Guest capacity and validity now come solely from the
        <strong> signed appliance license</strong> (see <a href="/licenses" className="underline">Licenses</a>).
        Kept for historical/audit reference only.
      </div>

      {err && <div className="text-err text-sm">{err}</div>}
      {msg && <div className="text-sm">{msg}</div>}

      {/* ---------- Plan catalog (platform product definition) ---------- */}
      <Card>
        <CardHeader><CardTitle>Plan catalog</CardTitle></CardHeader>
        <CardBody>
          <form onSubmit={createPlan} className="grid grid-cols-1 sm:grid-cols-6 gap-3 mb-4">
            <div><Label>Code</Label><Input name="code" required placeholder="pro" /></div>
            <div><Label>Name</Label><Input name="name" required placeholder="Professional" /></div>
            <div>
              <Label>Billing</Label>
              <select name="billing_cycle" className="w-full border rounded px-2 py-1 text-sm bg-transparent">
                <option value="monthly">Monthly</option><option value="yearly">Yearly</option>
              </select>
            </div>
            <div><Label>Price (cents)</Label><Input name="price_cents" type="number" defaultValue={0} min={0} /></div>
            <div><Label>Currency</Label><Input name="currency" defaultValue="USD" /></div>
            <div><Label>Public</Label><div className="pt-2"><input type="checkbox" name="is_public" defaultChecked /></div></div>
            <div className="sm:col-span-6 flex justify-end">
              <Button type="submit" disabled={busy}>Create plan</Button>
            </div>
          </form>
          {allPlans.length === 0 ? <EmptyState title="No plans" /> : (
            <Table>
              <THead><TR><TH>Code</TH><TH>Name</TH><TH>Billing</TH><TH>Price</TH><TH>State</TH><TH className="text-right">Manage</TH></TR></THead>
              <tbody>
                {allPlans.map((p) => (
                  <TR key={p.id}>
                    <TD className="font-mono text-xs">{p.code}</TD>
                    <TD>{p.name}</TD>
                    <TD className="text-muted">{p.billing_cycle}</TD>
                    <TD className="font-mono">{((p.price_cents ?? 0) / 100).toFixed(2)} {p.currency ?? ""}</TD>
                    <TD className="text-muted">
                      {p.is_active ? "active" : "inactive"}{p.is_public ? " · public" : " · private"}
                    </TD>
                    <TD className="text-right">
                      <div className="flex gap-1 justify-end">
                        <Button size="sm" variant="ghost" disabled={busy} onClick={() => editPlan(p)}>Edit</Button>
                        <Button size="sm" variant="ghost" disabled={busy} onClick={() => togglePlanActive(p)}>{p.is_active ? "Deactivate" : "Activate"}</Button>
                        {p.is_active && <Button size="sm" variant="secondary" disabled={busy} onClick={() => retirePlan(p)}>Retire</Button>}
                      </div>
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
          <p className="text-xs text-muted mt-2">
            Retiring a plan removes it from new-subscription choices but keeps it in historical records and never
            rewrites an already-issued signed license. Plans are never physically deleted while history exists.
          </p>
        </CardBody>
      </Card>

      <Card>
        <CardHeader><CardTitle>Customer</CardTitle></CardHeader>
        <CardBody>
          <select
            className="border rounded px-2 py-1 text-sm bg-transparent"
            value={tenantID}
            onChange={(e) => setTenantID(e.target.value)}
          >
            <option value="">Select a customer…</option>
            {tenants.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
          </select>
          {sub && (
            <div className="mt-3 text-sm">
              Current: <strong>{sub.plan_code}</strong> · status <strong>{sub.status}</strong> ·{" "}
              {sub.billing_cycle} · {new Date(sub.current_period_start).toLocaleDateString()} →{" "}
              {new Date(sub.current_period_end).toLocaleDateString()}
              {sub.trial_end ? ` · trial ends ${new Date(sub.trial_end).toLocaleDateString()}` : ""}
              {sub.auto_renew === false ? " · auto-renew OFF" : " · auto-renew ON"}
              {sub.status !== "canceled" && (
                <Button size="sm" variant="secondary" className="ml-3" disabled={busy} onClick={cancelSubscription}>Cancel subscription</Button>
              )}
            </div>
          )}
          {tenantID && !sub && <div className="mt-3 text-sm text-muted">No subscription yet.</div>}
        </CardBody>
      </Card>

      {/* ---------- Subscription terms (activation control) ---------- */}
      <Card>
        <CardHeader><CardTitle>Create subscription — you choose the terms</CardTitle></CardHeader>
        <CardBody>
          <form onSubmit={createSubscription} className="grid grid-cols-1 sm:grid-cols-4 gap-3">
            <div>
              <Label>Plan</Label>
              <select name="plan_id" required className="w-full border rounded px-2 py-1 text-sm bg-transparent">
                {plans.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
              </select>
            </div>
            <div>
              <Label>Activation</Label>
              <select
                name="activation" className="w-full border rounded px-2 py-1 text-sm bg-transparent"
                value={activation} onChange={(e) => setActivation(e.target.value)}
              >
                <option value="active">Active immediately</option>
                <option value="trial">Start trial</option>
                <option value="scheduled">Scheduled (future start)</option>
              </select>
            </div>
            <div>
              <Label>Billing interval</Label>
              <select name="billing_cycle" className="w-full border rounded px-2 py-1 text-sm bg-transparent">
                <option value="">(plan default)</option>
                <option value="monthly">Monthly</option>
                <option value="yearly">Yearly</option>
              </select>
            </div>
            <div>
              <Label>Auto-renew</Label>
              <div className="pt-2"><input type="checkbox" name="auto_renew" defaultChecked /></div>
            </div>
            <div><Label>Start date</Label><Input name="start_date" type="date" /></div>
            <div><Label>Renewal date</Label><Input name="renewal_date" type="date" /></div>
            <div>
              <Label>Trial end {activation !== "trial" && <span className="text-muted">(trial only)</span>}</Label>
              <Input name="trial_end" type="date" disabled={activation !== "trial"} />
            </div>
            <div><Label>Reason (audited)</Label><Input name="reason" required placeholder="new customer contract" /></div>
            <div className="sm:col-span-4 flex justify-end">
              <Button type="submit" disabled={busy || !tenantID}>{busy ? "Saving…" : "Create subscription"}</Button>
            </div>
          </form>
          <p className="text-xs text-muted mt-2">
            A plan having trial days does <strong>not</strong> force a trial — “Active immediately” creates an{" "}
            <strong>Active</strong> subscription. “Scheduled” grants no entitlements and cannot be licensed until it activates.
          </p>
        </CardBody>
      </Card>

      {/* ---------- Effective entitlements ---------- */}
      <Card>
        <CardHeader><CardTitle>Effective entitlements (resolved)</CardTitle></CardHeader>
        <CardBody className="p-0">
          {eff.length === 0 ? <EmptyState title="No entitlements" hint="Create a subscription first." /> : (
            <Table>
              <THead><TR><TH>Key</TH><TH>Value</TH><TH>Type</TH><TH>Source</TH></TR></THead>
              <tbody>
                {eff.map((l) => (
                  <TR key={l.key}>
                    <TD className="font-mono text-xs">{l.key}</TD>
                    <TD className="font-mono">{limitValue(l)}{l.unit ? ` ${l.unit}` : ""}</TD>
                    <TD className="text-muted">{l.value_type}</TD>
                    <TD>
                      <span className={l.source === "override" ? "font-semibold" : "text-muted"}>{l.source}</span>
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      {/* ---------- Tenant overrides ---------- */}
      <Card>
        <CardHeader><CardTitle>Tenant overrides (this customer only)</CardTitle></CardHeader>
        <CardBody>
          <form onSubmit={setOverride} className="grid grid-cols-1 sm:grid-cols-6 gap-3 mb-4">
            <div><Label>Key</Label><Input name="key" required placeholder="max_appliances" /></div>
            <div>
              <Label>Type</Label>
              <select name="value_type" className="w-full border rounded px-2 py-1 text-sm bg-transparent">
                <option value="int">int</option><option value="bool">bool</option><option value="string">string</option>
              </select>
            </div>
            <div><Label>Value</Label><Input name="value" required placeholder="25" /></div>
            <div><Label>Starts</Label><Input name="starts_at" type="date" /></div>
            <div><Label>Expires</Label><Input name="expires_at" type="date" /></div>
            <div><Label>Reason</Label><Input name="reason" required placeholder="contract addendum" /></div>
            <div className="sm:col-span-6 flex justify-end">
              <Button type="submit" disabled={busy || !tenantID}>Set override</Button>
            </div>
          </form>
          {overrides.length === 0 ? <EmptyState title="No overrides" hint="This customer gets plan limits." /> : (
            <Table>
              <THead><TR><TH>Key</TH><TH>Value</TH><TH>Window</TH><TH>In effect</TH><TH>Reason</TH><TH></TH></TR></THead>
              <tbody>
                {overrides.map((o) => (
                  <TR key={o.key}>
                    <TD className="font-mono text-xs">{o.key}</TD>
                    <TD className="font-mono">{limitValue(o)}</TD>
                    <TD className="text-muted text-xs">
                      {new Date(o.starts_at).toLocaleDateString()} → {o.expires_at ? new Date(o.expires_at).toLocaleDateString() : "∞"}
                    </TD>
                    <TD>{o.in_effect ? "yes" : <span className="text-muted">expired / not started</span>}</TD>
                    <TD className="text-muted text-xs">{o.reason}</TD>
                    <TD className="text-right">
                      <Button size="sm" variant="ghost" onClick={() => removeOverride(o.key)}>Remove</Button>
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      {/* ---------- Plan limits ---------- */}
      <Card>
        <CardHeader>
          <CardTitle>
            Plan limits (product definition){planVersion !== null && <span className="text-muted text-sm font-normal"> — version {planVersion}</span>}
          </CardTitle>
        </CardHeader>
        <CardBody>
          <div className="mb-3">
            <Label>Plan</Label>
            <select
              className="border rounded px-2 py-1 text-sm bg-transparent"
              value={planID} onChange={(e) => setPlanID(e.target.value)}
            >
              {plans.map((p) => <option key={p.id} value={p.id}>{p.name}</option>)}
            </select>
          </div>
          <form onSubmit={setPlanLimit} className="grid grid-cols-1 sm:grid-cols-6 gap-3 mb-4">
            <div><Label>Key</Label><Input name="key" required placeholder="max_sites" /></div>
            <div>
              <Label>Type</Label>
              <select name="value_type" className="w-full border rounded px-2 py-1 text-sm bg-transparent">
                <option value="int">int</option><option value="bool">bool</option><option value="string">string</option>
              </select>
            </div>
            <div><Label>Value</Label><Input name="value" required placeholder="10" /></div>
            <div><Label>Unit</Label><Input name="unit" placeholder="sites" /></div>
            <div className="sm:col-span-2"><Label>Reason (audited)</Label><Input name="reason" required placeholder="pricing update" /></div>
            <div className="sm:col-span-6 flex justify-end">
              <Button type="submit" disabled={busy}>Set plan limit</Button>
            </div>
          </form>
          {planLimits.length === 0 ? <EmptyState title="No limits on this plan" /> : (
            <Table>
              <THead><TR><TH>Key</TH><TH>Value</TH><TH>Type</TH><TH>Unit</TH></TR></THead>
              <tbody>
                {planLimits.map((l) => (
                  <TR key={l.key}>
                    <TD className="font-mono text-xs">{l.key}</TD>
                    <TD className="font-mono">{limitValue(l)}</TD>
                    <TD className="text-muted">{l.value_type}</TD>
                    <TD className="text-muted">{l.unit || "—"}</TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>
    </div>
  );
}
