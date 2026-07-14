"use client";

import { useEffect, useState } from "react";
import { api, ApiError, ListResp, Site, WalledGardenRule } from "@/lib/api";
import { useTenant } from "@/lib/use-tenant";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X } from "lucide-react";
import { formatRelative } from "@/lib/utils";

const KIND_TONE = { domain: "info", ip: "default", cidr: "warn" } as const;

export default function WalledGardenPage() {
  const tenantID = useTenant();
  const [rows, setRows] = useState<WalledGardenRule[] | null>(null);
  const [sites, setSites] = useState<Site[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);

  async function load() {
    if (!tenantID) return;
    try {
      const [wg, st] = await Promise.all([
        api.get<ListResp<WalledGardenRule>>(`/v1/walled-garden?tenant_id=${tenantID}`),
        api.get<ListResp<Site>>(`/v1/sites?tenant_id=${tenantID}`),
      ]);
      setRows(wg.data);
      setSites(st.data);
    } catch (e: any) { setErr(e?.message ?? "Failed to load"); }
  }
  useEffect(() => { load(); }, [tenantID]);

  async function onCreate(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!tenantID) return;
    setBusy(true); setErr(null);
    const form = new FormData(e.currentTarget);
    const portsRaw = (form.get("ports") as string ?? "").trim();
    const ports = portsRaw
      ? portsRaw.split(",").map((s) => s.trim()).filter(Boolean).map(Number)
      : undefined;
    if (ports && ports.some((n) => Number.isNaN(n) || n < 1 || n > 65535)) {
      setErr("ports must be comma-separated integers in 1..65535");
      setBusy(false);
      return;
    }
    try {
      await api.post(`/v1/walled-garden?tenant_id=${tenantID}`, {
        kind: form.get("kind"),
        value: (form.get("value") as string).trim(),
        site_id: (form.get("site_id") as string) || undefined,
        ports,
        description: (form.get("description") as string) || undefined,
      });
      setShowNew(false);
      (e.currentTarget as HTMLFormElement).reset();
      load();
    } catch (e: any) {
      if (e instanceof ApiError) setErr(e.message);
      else setErr(e?.message ?? "Create failed");
    } finally { setBusy(false); }
  }

  async function onDelete(id: string) {
    if (!tenantID) return;
    if (!confirm("Delete this rule?")) return;
    try {
      await api.del(`/v1/walled-garden/${id}?tenant_id=${tenantID}`);
      load();
    } catch (e: any) { setErr(e?.message ?? "Delete failed"); }
  }

  const siteName = (sid?: string | null) =>
    sid ? sites.find((s) => s.id === sid)?.name ?? sid.slice(0, 8) : "all sites";

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Policy</div>
          <h1 className="text-2xl font-semibold">Walled garden</h1>
        </div>
        <Button onClick={() => setShowNew((s) => !s)}>
          {showNew ? <><X size={14} /> Cancel</> : <><Plus size={14} /> New rule</>}
        </Button>
      </div>

      <p className="text-sm text-muted mb-4 max-w-2xl">
        Hosts, networks and domains in the walled garden are reachable by guests <b>before authentication</b>.
        Use it for captive-probe endpoints, login providers, and payment gateways.
      </p>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      {showNew && (
        <Card className="mb-6">
          <CardHeader><CardTitle>New rule</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onCreate} className="grid grid-cols-1 sm:grid-cols-6 gap-3">
              <div>
                <Label>Kind</Label>
                <select
                  name="kind" required defaultValue="domain"
                  className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm"
                >
                  <option value="domain">domain</option>
                  <option value="ip">ip</option>
                  <option value="cidr">cidr</option>
                </select>
              </div>
              <div className="sm:col-span-3">
                <Label>Value</Label>
                <Input name="value" required placeholder="captive.apple.com · 1.1.1.1 · 10.200.0.0/24" />
              </div>
              <div>
                <Label>Ports (optional)</Label>
                <Input name="ports" placeholder="53,443" />
              </div>
              <div>
                <Label>Site (optional)</Label>
                <select
                  name="site_id"
                  className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm"
                >
                  <option value="">(all sites)</option>
                  {sites.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
                </select>
              </div>
              <div className="sm:col-span-6"><Label>Description</Label><Input name="description" /></div>
              <div className="sm:col-span-6 flex justify-end">
                <Button type="submit" disabled={busy}>{busy ? "Creating…" : "Create"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      <Card>
        <CardBody className="p-0">
          {rows === null ? <EmptyState title="Loading…" /> : rows.length === 0 ? (
            <EmptyState title="No rules yet" hint="Add hosts reachable pre-auth (Apple/Google captive probes, payment gateways, etc.)" />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Kind</TH><TH>Value</TH><TH>Ports</TH><TH>Scope</TH>
                  <TH>Description</TH><TH>Added</TH><TH></TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((r) => (
                  <TR key={r.id}>
                    <TD><Badge tone={KIND_TONE[r.kind] as any}>{r.kind}</Badge></TD>
                    <TD className="font-mono">{r.value}</TD>
                    <TD className="text-muted">{r.ports?.length ? r.ports.join(", ") : "any"}</TD>
                    <TD className="text-muted">{siteName(r.site_id)}</TD>
                    <TD className="text-muted">{r.description || "—"}</TD>
                    <TD className="text-muted">{formatRelative(r.created_at)}</TD>
                    <TD className="text-right">
                      <Button size="sm" variant="ghost" onClick={() => onDelete(r.id)}>Delete</Button>
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
