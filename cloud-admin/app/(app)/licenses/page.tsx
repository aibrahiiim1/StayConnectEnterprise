"use client";

import { useEffect, useMemo, useState } from "react";
import { api, withStepUp, ListResp, License, Site, FleetAppliance } from "@/lib/api";
import { useCustomer } from "@/lib/customer-context";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X } from "lucide-react";
import { formatDate, formatRelative, errMsg } from "@/lib/utils";

type ApplianceRow = { id: string; site_id: string; serial: string; name: string };

/**
 * SIMPLE LICENSE MODEL. A license binds to exactly ONE appliance and carries
 * only: max concurrent online guests, valid_from..valid_until, grace period.
 * No plan or subscription is involved. Renew/limit/date changes always issue a
 * NEW signed document with a higher license_version (anti-rollback), never a
 * silent mutation of the active document.
 */
export default function LicensesPage() {
  const { selectedTenantId: tenantID, ready } = useCustomer();
  const allCustomers = tenantID === "";
  const [rows, setRows] = useState<License[] | null>(null);
  const [sites, setSites] = useState<Site[]>([]);
  const [appliances, setAppliances] = useState<ApplianceRow[]>([]);
  const [fleet, setFleet] = useState<FleetAppliance[]>([]);
  const [customer, setCustomer] = useState<string>("");
  const [err, setErr] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);
  const [formSite, setFormSite] = useState("");

  async function load() {
    if (!ready) return;
    try {
      const [lic, st, ap, fl, tn] = await Promise.all([
        api.get<ListResp<License>>(`/cloud/v1/licenses?tenant_id=${tenantID}`),
        api.get<ListResp<Site>>(`/v1/sites?tenant_id=${tenantID}`),
        api.get<ListResp<ApplianceRow>>(`/v1/appliances?tenant_id=${tenantID}`).catch(() => ({ data: [] as ApplianceRow[] })),
        api.get<ListResp<FleetAppliance>>(`/cloud/v1/fleet?tenant_id=${tenantID}`).catch(() => ({ data: [] as FleetAppliance[] })),
        api.get<{ data: { id: string; name: string }[] }>(`/v1/tenants`).catch(() => ({ data: [] })),
      ]);
      setRows(lic.data ?? []);
      setSites(st.data ?? []);
      setAppliances(ap.data ?? []);
      setFleet(fl.data ?? []);
      setCustomer((tn.data ?? []).find((t) => t.id === tenantID)?.name ?? "");
    } catch (e) { setErr(errMsg(e)); }
  }
  useEffect(() => { setRows(null); load(); }, [ready, tenantID]);

  const siteName = (sid: string) => sites.find((s) => s.id === sid)?.name ?? sid.slice(0, 8);
  const applianceOf = (l: License) => {
    const id = l.appliance_ids?.[0];
    return id ? appliances.find((a) => a.id === id) : undefined;
  };
  const usageOf = (l: License): { current?: number; at?: string } => {
    const id = l.appliance_ids?.[0];
    if (!id) return {};
    const f = fleet.find((x) => x.appliance_id === id);
    if (!f?.last_usage) return { at: f?.last_usage_at ?? undefined };
    try {
      const u = typeof f.last_usage === "string" ? JSON.parse(f.last_usage) : f.last_usage;
      return { current: u.active_sessions ?? undefined, at: f.last_usage_at ?? undefined };
    } catch { return { at: f.last_usage_at ?? undefined }; }
  };

  const formAppliances = useMemo(
    () => appliances.filter((a) => !formSite || a.site_id === formSite),
    [appliances, formSite]);

  async function onIssue(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (allCustomers) { setErr("Select a customer (top-left) to issue a license."); return; }
    setBusy(true); setErr(null); setMsg(null);
    const form = new FormData(e.currentTarget);
    const el = e.currentTarget;
    try {
      const body: any = {
        tenant_id: tenantID,
        site_id: form.get("site_id"),
        appliance_id: form.get("appliance_id") || undefined,
        max_concurrent_online_guests: Number(form.get("max_guests")) || 0,
        grace_period_days: Number(form.get("grace_days")) || 30,
      };
      const vf = String(form.get("valid_from") || "");
      const vu = String(form.get("valid_until") || "");
      if (vf) body.valid_from = new Date(vf + "T00:00:00Z").toISOString();
      if (vu) body.valid_until = new Date(vu + "T23:59:59Z").toISOString();
      if (!vu) body.valid_days = 365;
      const res = await withStepUp(() => api.post<{ license_id: string; license_version: number }>(`/cloud/v1/licenses`, body));
      setMsg(`License v${res.license_version} issued (${res.license_id.slice(0, 8)}…).`);
      setShowNew(false);
      el.reset();
      load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onRevoke(id: string) {
    if (!confirm("Revoke this license? The appliance will refuse new guest authorizations.")) return;
    setErr(null);
    try { await withStepUp(() => api.post(`/cloud/v1/licenses/${id}/revoke`)); load(); }
    catch (e) { setErr(errMsg(e)); }
  }
  async function onSuspend(id: string) {
    if (!confirm("Suspend this license? New guest authorization stops; existing sessions run out naturally. Portal/DHCP/DNS/Hotel Admin stay up.")) return;
    setErr(null);
    try { await withStepUp(() => api.post(`/cloud/v1/licenses/${id}/suspend`)); load(); }
    catch (e) { setErr(errMsg(e)); }
  }
  async function onResume(id: string) {
    setErr(null);
    try { await withStepUp(() => api.post(`/cloud/v1/licenses/${id}/resume`)); load(); }
    catch (e) { setErr(errMsg(e)); }
  }
  async function onRenew(l: License) {
    const limit = window.prompt("Max concurrent online guests (0 = unlimited):",
      String(l.max_concurrent_online_guests ?? 0));
    if (limit === null) return;
    const days = window.prompt("Valid for how many days from now?", "365");
    if (days === null) return;
    const grace = window.prompt("Grace period days after expiry:", String(l.grace_period_days || 30));
    if (grace === null) return;
    setErr(null); setMsg(null);
    try {
      const res = await withStepUp(() => api.post<{ license_version: number }>(`/cloud/v1/licenses/${l.id}/renew`, {
        tenant_id: l.tenant_id,
        site_id: l.site_id,
        appliance_id: l.appliance_ids?.[0] || undefined,
        max_concurrent_online_guests: Number(limit) || 0,
        valid_days: Number(days) || 365,
        grace_period_days: Number(grace) || 30,
      }));
      setMsg(`Renewed as new signed license v${res.license_version}; the previous document is superseded and can never be replayed.`);
      load();
    } catch (e) { setErr(errMsg(e)); }
  }

  // Binding + time state for a row.
  function binding(l: License): { label: string; tone: string } {
    const bound = (l.appliance_ids?.length ?? 0) > 0;
    const now = Date.now();
    const vu = new Date(l.valid_until).getTime();
    const graceEnd = vu + (l.grace_period_days || 0) * 86400000;
    if (l.status === "revoked") return { label: "Revoked", tone: "err" };
    if (l.status === "superseded") return { label: "Superseded", tone: "default" };
    if (l.status === "suspended") return { label: "Suspended", tone: "warn" };
    if (now > graceEnd) return { label: "Expired", tone: "err" };
    if (now > vu) return { label: "Grace", tone: "warn" };
    if (!bound) return { label: "Site license — awaiting appliance binding", tone: "warn" };
    return { label: "Active", tone: "ok" };
  }
  const graceEndOf = (l: License) =>
    new Date(new Date(l.valid_until).getTime() + (l.grace_period_days || 0) * 86400000);

  return (
    <div className="p-6 max-w-[92rem] mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Commercial</div>
          <h1 className="text-2xl font-semibold">Licenses</h1>
          <div className="mt-1 text-sm text-muted">{allCustomers ? "All Customers" : <>Customer: <span className="text-text font-medium">{customer || "—"}</span></>}</div>
        </div>
        <Button onClick={() => setShowNew((s) => !s)} disabled={allCustomers || sites.length === 0}>
          {showNew ? <><X size={14} /> Cancel</> : <><Plus size={14} /> Issue license</>}
        </Button>
      </div>
      {allCustomers && (
        <div className="text-sm text-warn mb-4">
          Viewing licenses across all customers. Select a customer (top-left) to issue a new license.
        </div>
      )}

      <p className="text-sm text-muted mb-4">
        A license binds to <strong>one appliance</strong> and carries exactly three commercial controls:
        max concurrent online guests, validity window, grace period. No plan or subscription.
      </p>

      {sites.length === 0 && (
        <div className="text-sm text-warn mb-4">Create a site first — a license needs a site + appliance.</div>
      )}
      {err && <div className="text-err text-sm mb-4">{err}</div>}
      {msg && <div className="text-ok text-sm mb-4">{msg}</div>}

      {showNew && (
        <Card className="mb-6">
          <CardHeader><CardTitle>Issue license</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onIssue} className="grid grid-cols-1 sm:grid-cols-3 gap-3">
              <div>
                <Label>Site</Label>
                <select name="site_id" required value={formSite} onChange={(e) => setFormSite(e.target.value)}
                  className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm">
                  <option value="">Select a site…</option>
                  {sites.map((s) => <option key={s.id} value={s.id}>{s.name} — {s.code}</option>)}
                </select>
              </div>
              <div>
                <Label>Appliance</Label>
                <select name="appliance_id" required
                  className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm">
                  <option value="">Select an appliance…</option>
                  {formAppliances.map((a) => <option key={a.id} value={a.id}>{a.serial}{a.name ? ` — ${a.name}` : ""}</option>)}
                </select>
              </div>
              <div><Label>Max concurrent online guests (0 = unlimited)</Label><Input name="max_guests" type="number" defaultValue={500} min={0} /></div>
              <div><Label>Valid from</Label><Input name="valid_from" type="date" /></div>
              <div><Label>Valid until</Label><Input name="valid_until" type="date" /></div>
              <div><Label>Grace period days</Label><Input name="grace_days" type="number" defaultValue={30} min={0} /></div>
              <div className="sm:col-span-3 flex justify-end">
                <Button type="submit" disabled={busy}>{busy ? "Issuing…" : "Issue license"}</Button>
              </div>
            </form>
            <p className="text-xs text-muted mt-2">Empty dates = starts now, valid 365 days.</p>
          </CardBody>
        </Card>
      )}

      <Card>
        <CardBody className="p-0 overflow-x-auto">
          {rows === null ? <EmptyState title="Loading…" /> : rows.length === 0 ? (
            <EmptyState title="No licenses yet" hint="Issue one to entitle an appliance." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Customer</TH><TH>Site</TH><TH>Appliance</TH><TH>v#</TH>
                  <TH>Status</TH><TH>Online / Limit</TH><TH>Usage</TH>
                  <TH>Valid</TH><TH>Grace ends</TH><TH>Last sync</TH><TH></TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((l) => {
                  const b = binding(l);
                  const ap = applianceOf(l);
                  const u = usageOf(l);
                  const limit = l.max_concurrent_online_guests ?? 0;
                  const pct = limit > 0 && u.current !== undefined ? Math.round((u.current / limit) * 100) : null;
                  return (
                    <TR key={l.id}>
                      <TD>{customer || "—"}</TD>
                      <TD>{siteName(l.site_id)}</TD>
                      <TD className="font-mono text-xs">{ap ? ap.serial : <span className="text-muted italic">not bound</span>}</TD>
                      <TD className="font-mono text-xs">v{l.license_version ?? 0}</TD>
                      <TD><Badge tone={b.tone as any}>{b.label}</Badge></TD>
                      <TD className="font-mono text-xs">
                        {u.current !== undefined ? u.current : "—"} / {limit > 0 ? limit : "∞"}
                      </TD>
                      <TD className="text-muted">{pct !== null ? `${pct}%` : "—"}</TD>
                      <TD className="text-muted text-xs whitespace-nowrap">
                        {l.valid_from ? formatDate(l.valid_from) : formatDate(l.issued_at)} → {formatDate(l.valid_until)}
                      </TD>
                      <TD className="text-muted text-xs">{formatDate(graceEndOf(l).toISOString())}</TD>
                      <TD className="text-muted text-xs">{u.at ? formatRelative(u.at) : "—"}</TD>
                      <TD className="text-right">
                        <div className="flex gap-1 justify-end">
                          {(l.status === "active" || l.status === "suspended") && (
                            <Button size="sm" variant="ghost" onClick={() => onRenew(l)}>Renew</Button>
                          )}
                          {l.status === "active" && (
                            <Button size="sm" variant="ghost" onClick={() => onSuspend(l.id)}>Suspend</Button>
                          )}
                          {l.status === "suspended" && (
                            <Button size="sm" variant="ghost" onClick={() => onResume(l.id)}>Resume</Button>
                          )}
                          {l.status !== "revoked" && l.status !== "superseded" && (
                            <Button size="sm" variant="danger" onClick={() => onRevoke(l.id)}>Revoke</Button>
                          )}
                        </div>
                      </TD>
                    </TR>
                  );
                })}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>
    </div>
  );
}
