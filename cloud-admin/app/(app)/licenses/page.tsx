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

const statusTone = (s: string) =>
  s === "active" ? "ok" :
  s === "suspended" ? "warn" :
  s === "revoked" ? "err" :
  s === "superseded" ? "default" : "default";

export default function LicensesPage() {
  const tenantID = useTenant();
  const [rows, setRows] = useState<License[] | null>(null);
  const [sites, setSites] = useState<Site[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);
  const [issued, setIssued] = useState<string | null>(null);

  async function load() {
    if (!tenantID) return;
    try {
      const [lic, st] = await Promise.all([
        api.get<ListResp<License>>(`/cloud/v1/licenses?tenant_id=${tenantID}`),
        api.get<ListResp<Site>>(`/v1/sites?tenant_id=${tenantID}`),
      ]);
      setRows(lic.data ?? []);
      setSites(st.data ?? []);
    } catch (e) { setErr(errMsg(e)); }
  }
  useEffect(() => { load(); }, [tenantID]);

  async function onIssue(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!tenantID) return;
    setBusy(true); setErr(null); setIssued(null);
    const form = new FormData(e.currentTarget);
    try {
      const res = await withStepUp(() => api.post<unknown>(`/cloud/v1/licenses`, {
        tenant_id: tenantID,
        site_id: form.get("site_id"),
        valid_days: Number(form.get("valid_days")) || 365,
        offline_grace_days: Number(form.get("offline_grace_days")) || 30,
      }));
      setIssued(JSON.stringify(res, null, 2));
      setShowNew(false);
      (e.currentTarget as HTMLFormElement).reset();
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

  const siteName = (sid: string) => sites.find((s) => s.id === sid)?.name ?? sid.slice(0, 8);

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
                  <TH>Site</TH><TH>Plan</TH><TH>Status</TH>
                  <TH>Valid until</TH><TH>Grace days</TH><TH>Key</TH><TH></TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((l) => (
                  <TR key={l.id}>
                    <TD>{siteName(l.site_id)}</TD>
                    <TD className="font-mono text-xs">{l.commercial_plan_code}</TD>
                    <TD><Badge tone={statusTone(l.status) as any}>{l.status}</Badge></TD>
                    <TD className="text-muted">{formatDate(l.valid_until)}</TD>
                    <TD className="text-muted">{l.offline_grace_days}</TD>
                    <TD className="font-mono text-xs">{l.key_id}</TD>
                    <TD className="text-right">
                      {l.status !== "revoked" && (
                        <Button size="sm" variant="ghost" onClick={() => onRevoke(l.id)}>Revoke</Button>
                      )}
                    </TD>
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
