"use client";

import { useEffect, useState } from "react";
import { api, withStepUp, ListResp, License, Site } from "@/lib/api";
import { useTenant } from "@/lib/use-tenant";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X, Copy } from "lucide-react";
import { formatDate, errMsg } from "@/lib/utils";

export default function LicensesPage() {
  const tenantID = useTenant();
  const [rows, setRows] = useState<License[] | null>(null);
  const [sites, setSites] = useState<Site[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);
  const [issued, setIssued] = useState<string | null>(null);

  const [appliances, setAppliances] = useState<{ id: string; serial: string }[]>([]);
  const [sub, setSub] = useState<{ plan_code?: string; status: string } | null>(null);
  const [customer, setCustomer] = useState<string>("");

  async function load() {
    if (!tenantID) return;
    try {
      const [lic, st, ap, s, tn] = await Promise.all([
        api.get<ListResp<License>>(`/cloud/v1/licenses?tenant_id=${tenantID}`),
        api.get<ListResp<Site>>(`/v1/sites?tenant_id=${tenantID}`),
        api.get<ListResp<{ id: string; serial: string }>>(`/v1/appliances?tenant_id=${tenantID}`).catch(() => ({ data: [] })),
        api.get<{ plan_code?: string; status: string }>(`/v1/tenants/${tenantID}/subscription`).catch(() => null),
        api.get<{ data: { id: string; name: string }[] }>(`/v1/tenants`).catch(() => ({ data: [] })),
      ]);
      setRows(lic.data ?? []);
      setSites(st.data ?? []);
      setAppliances(ap.data ?? []);
      setSub(s);
      setCustomer((tn.data ?? []).find((t) => t.id === tenantID)?.name ?? "");
    } catch (e) { setErr(errMsg(e)); }
  }
  useEffect(() => { load(); }, [tenantID]);

  async function onIssue(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!tenantID) return;
    setBusy(true); setErr(null); setIssued(null);
    const form = new FormData(e.currentTarget);
    // Capture before awaiting: React nulls e.currentTarget after the first await.
    const el = e.currentTarget;
    try {
      const res = await withStepUp(() => api.post<unknown>(`/cloud/v1/licenses`, {
        tenant_id: tenantID,
        site_id: form.get("site_id"),
        valid_days: Number(form.get("valid_days")) || 365,
        offline_grace_days: Number(form.get("offline_grace_days")) || 30,
      }));
      setIssued(JSON.stringify(res, null, 2));
      setShowNew(false);
      el.reset();
      load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onRevoke(id: string) {
    if (!confirm("Revoke this license? Appliances at the site will lose entitlement.")) return;
    setErr(null);
    try {
      await withStepUp(() => api.post(`/cloud/v1/licenses/${id}/revoke`));
      load();
    } catch (e) { setErr(errMsg(e)); }
  }
  async function onSuspend(id: string) {
    if (!confirm("Suspend this license? The appliance keeps working until its next check-in, then loses entitlement until resumed.")) return;
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
    const days = window.prompt("Renew: issue a fresh license valid for how many days? (supersedes the current one)", "365");
    if (!days) return;
    const n = Number(days);
    if (!Number.isFinite(n) || n <= 0) { setErr("Enter a positive number of days."); return; }
    setErr(null);
    try {
      await withStepUp(() => api.post(`/cloud/v1/licenses/${l.id}/renew`, {
        tenant_id: l.tenant_id,
        site_id: l.site_id,
        valid_days: n,
        offline_grace_days: l.offline_grace_days,
      }));
      load();
    } catch (e) { setErr(errMsg(e)); }
  }

  const siteName = (sid: string) => sites.find((s) => s.id === sid)?.name ?? sid.slice(0, 8);
  const applianceSerial = (id: string) => appliances.find((a) => a.id === id)?.serial ?? id.slice(0, 8);

  // Binding + effective status. An UNBOUND active license is a Site License still
  // awaiting an appliance — it must NOT read as an activated appliance.
  function binding(l: License): { label: string; tone: string; appliance: string | null } {
    const boundId = (l.appliance_ids && l.appliance_ids.length > 0) ? l.appliance_ids[0] : null;
    const expired = new Date(l.valid_until).getTime() < Date.now();
    if (l.status === "revoked") return { label: "Revoked", tone: "err", appliance: boundId ? applianceSerial(boundId) : null };
    if (l.status === "superseded") return { label: "Superseded", tone: "default", appliance: boundId ? applianceSerial(boundId) : null };
    if (l.status === "suspended") return { label: "Suspended", tone: "warn", appliance: boundId ? applianceSerial(boundId) : null };
    if (expired) return { label: "Expired", tone: "warn", appliance: boundId ? applianceSerial(boundId) : null };
    // active
    if (!boundId) return { label: "Site license — awaiting appliance binding", tone: "warn", appliance: null };
    return { label: "Appliance-bound — Active", tone: "ok", appliance: applianceSerial(boundId) };
  }

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Commercial</div>
          <h1 className="text-2xl font-semibold">Licenses</h1>
        </div>
        <Button onClick={() => setShowNew((s) => !s)} disabled={sites.length === 0}>
          {showNew ? <><X size={14} /> Cancel</> : <><Plus size={14} /> Issue license</>}
        </Button>
      </div>

      {sites.length === 0 && (
        <div className="text-sm text-warn mb-4">Create a site first — licenses are issued per site.</div>
      )}
      {err && <div className="text-err text-sm mb-4">{err}</div>}

      {issued && (
        <Card className="mb-6 border-ok">
          <CardHeader>
            <CardTitle className="text-ok">License issued — copy the envelope</CardTitle>
            <div className="flex items-center gap-2">
              <Button size="sm" variant="secondary" onClick={() => navigator.clipboard?.writeText(issued)}>
                <Copy size={12} /> Copy
              </Button>
              <Button size="sm" variant="ghost" onClick={() => setIssued(null)}><X size={14} /></Button>
            </div>
          </CardHeader>
          <CardBody>
            <pre className="bg-panel2 border border-border rounded p-3 text-xs overflow-x-auto whitespace-pre-wrap break-all">{issued}</pre>
          </CardBody>
        </Card>
      )}

      {showNew && (
        <Card className="mb-6">
          <CardHeader><CardTitle>Issue license</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onIssue} className="grid grid-cols-1 sm:grid-cols-3 gap-3">
              <div>
                <Label>Site</Label>
                <select name="site_id" required
                  className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm">
                  {sites.map((s) => <option key={s.id} value={s.id}>{s.name} — {s.code}</option>)}
                </select>
              </div>
              <div><Label>Valid days</Label><Input name="valid_days" type="number" defaultValue={365} min={1} /></div>
              <div><Label>Offline grace days</Label><Input name="offline_grace_days" type="number" defaultValue={30} min={0} /></div>
              <div className="sm:col-span-3 flex justify-end">
                <Button type="submit" disabled={busy}>{busy ? "Issuing…" : "Issue"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      <Card>
        <CardBody className="p-0">
          {rows === null ? <EmptyState title="Loading…" /> : rows.length === 0 ? (
            <EmptyState title="No licenses yet" hint="Issue one to entitle a site's appliances." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Customer</TH><TH>Site</TH><TH>Appliance</TH><TH>Plan</TH>
                  <TH>Binding status</TH><TH>Valid until</TH><TH>Grace</TH><TH></TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((l) => {
                  const b = binding(l);
                  return (
                  <TR key={l.id}>
                    <TD>{customer || "—"}</TD>
                    <TD>{siteName(l.site_id)}</TD>
                    <TD className="font-mono text-xs">
                      {b.appliance ?? <span className="text-muted italic">not bound</span>}
                    </TD>
                    <TD className="font-mono text-xs">
                      {l.commercial_plan_code}
                      {sub?.plan_code && sub.plan_code !== l.commercial_plan_code && (
                        <span className="text-muted"> (sub: {sub.plan_code})</span>
                      )}
                    </TD>
                    <TD><Badge tone={b.tone as any}>{b.label}</Badge></TD>
                    <TD className="text-muted">{formatDate(l.valid_until)}</TD>
                    <TD className="text-muted">{l.offline_grace_days}d</TD>
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
                        {l.status !== "revoked" && (
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
