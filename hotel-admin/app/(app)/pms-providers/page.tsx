"use client";

import { useEffect, useState } from "react";
import {
  api, ApiError, ListResp, Whoami,
  PMSProvider, PMSTestResult, PMSHealthResult, PMSCacheResult,
} from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X } from "lucide-react";
import { canWrite } from "@/lib/roles";
import { formatRelative, errMsg } from "@/lib/utils";

const KINDS = ["stub", "protel-fias", "opera-fias", "fidelio-fias", "mews", "apaleo"] as const;
const isFias = (kind: string) => kind.endsWith("-fias");
const isRest = (kind: string) => kind === "mews" || kind === "apaleo";

function statusTone(s: string): "ok" | "warn" | "err" | "default" {
  switch (s) {
    case "connected": return "ok";
    case "degraded":  return "warn";
    case "down":      return "err";
    default:          return "default";
  }
}

export default function PMSProvidersPage() {
  const [rows, setRows] = useState<PMSProvider[] | null>(null);
  const [roles, setRoles] = useState<string[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);
  const [kind, setKind] = useState<string>("stub");
  const [editing, setEditing] = useState<PMSProvider | null>(null);
  const [result, setResult] = useState<{ title: string; body: React.ReactNode } | null>(null);
  const [acting, setActing] = useState<string | null>(null);

  const writable = canWrite("pms-providers", roles);

  async function load() {
    try {
      const r = await api.get<ListResp<PMSProvider>>("/pms-providers");
      setRows(r.data);
    } catch (e) { setErr(errMsg(e)); }
  }
  useEffect(() => {
    load();
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => {});
  }, []);

  async function onCreate(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true); setErr(null);
    const form = new FormData(e.currentTarget);
    const s = (k: string) => ((form.get(k) as string) || undefined);
    const body: Record<string, unknown> = {
      name: form.get("name"),
      kind,
      display_name: s("display_name"),
      enabled: form.get("enabled") === "on",
    };
    if (isFias(kind)) {
      body.host = s("host");
      const port = form.get("port") as string;
      if (port) body.port = Number(port);
      body.use_tls = form.get("use_tls") === "on";
      body.auth_key = s("auth_key");
    } else if (isRest(kind)) {
      body.base_url = s("base_url");
      body.api_key = s("api_key");
      body.property_id = s("property_id");
    }
    try {
      await api.post("/pms-providers", body);
      setShowNew(false);
      (e.currentTarget as HTMLFormElement).reset();
      load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onEdit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!editing) return;
    setBusy(true); setErr(null);
    const form = new FormData(e.currentTarget);
    const s = (k: string) => {
      const v = form.get(k) as string;
      return v == null ? undefined : v;
    };
    const body: Record<string, unknown> = {
      display_name: s("display_name"),
      enabled: form.get("enabled") === "on",
    };
    if (isFias(editing.kind)) {
      body.host = s("host");
      const port = form.get("port") as string;
      if (port) body.port = Number(port);
      body.use_tls = form.get("use_tls") === "on";
      const ak = form.get("auth_key") as string;
      if (ak) body.auth_key = ak; // blank keeps existing secret
    } else if (isRest(editing.kind)) {
      body.base_url = s("base_url");
      body.property_id = s("property_id");
      const ak = form.get("api_key") as string;
      if (ak) body.api_key = ak;
    }
    try {
      await api.patch(`/pms-providers/${editing.name}`, body);
      setEditing(null);
      load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onDelete(name: string) {
    if (!confirm(`Delete provider "${name}"?`)) return;
    try { await api.del(`/pms-providers/${name}`); load(); }
    catch (e) { setErr(errMsg(e)); }
  }

  async function onTest(name: string) {
    setActing(name + ":test"); setErr(null);
    try {
      const r = await api.post<PMSTestResult>(`/pms-providers/${name}/test`);
      setResult({
        title: `Test — ${name}`,
        body: r.ok
          ? <div className="text-sm"><Badge tone="ok">ok</Badge> <span className="ml-2 text-muted">{r.latency_ms} ms</span></div>
          : <div className="text-sm text-err">{r.error ?? "failed"}</div>,
      });
    } catch (e) {
      setResult({ title: `Test — ${name}`, body: <div className="text-sm text-err">{errMsg(e)}</div> });
    } finally { setActing(null); }
  }

  async function onHealth(name: string) {
    setActing(name + ":health"); setErr(null);
    try {
      const r = await api.get<PMSHealthResult>(`/pms-providers/${name}/health`);
      setResult({
        title: `Health — ${name}`,
        body: <pre className="text-xs overflow-x-auto">{JSON.stringify(r.health, null, 2)}</pre>,
      });
    } catch (e) {
      setResult({ title: `Health — ${name}`, body: <div className="text-sm text-err">{errMsg(e)}</div> });
    } finally { setActing(null); }
  }

  async function onCache(name: string) {
    setActing(name + ":cache"); setErr(null);
    try {
      const r = await api.get<PMSCacheResult>(`/pms-providers/${name}/cache?limit=100`);
      setResult({
        title: `Cache — ${name} (${r.count})`,
        body: r.rows.length === 0
          ? <div className="text-sm text-muted">Cache is empty.</div>
          : (
            <Table>
              <THead><TR><TH>Room</TH><TH>Guest</TH><TH>Reservation</TH><TH>Check-in</TH><TH>Check-out</TH></TR></THead>
              <tbody>
                {r.rows.map((c, i) => (
                  <TR key={i}>
                    <TD className="font-mono">{c.room_number}</TD>
                    <TD>{c.guest_display_name || `${c.first_name} ${c.last_name}`.trim()}</TD>
                    <TD className="font-mono text-xs">{c.reservation_number}</TD>
                    <TD className="text-muted">{c.check_in ?? "—"}</TD>
                    <TD className="text-muted">{c.check_out ?? "—"}</TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          ),
      });
    } catch (e) {
      setResult({ title: `Cache — ${name}`, body: <div className="text-sm text-err">{errMsg(e)}</div> });
    } finally { setActing(null); }
  }

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Integrations</div>
          <h1 className="text-2xl font-semibold">PMS providers</h1>
        </div>
        {writable && (
          <Button onClick={() => { setShowNew((v) => !v); setEditing(null); }}>
            {showNew ? <><X size={14} /> Cancel</> : <><Plus size={14} /> New provider</>}
          </Button>
        )}
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      {showNew && writable && (
        <Card className="mb-6">
          <CardHeader><CardTitle>New provider</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onCreate} className="grid grid-cols-1 sm:grid-cols-3 gap-3">
              <div><Label>Name</Label><Input name="name" required placeholder="protel-main" /></div>
              <div>
                <Label>Kind</Label>
                <select
                  name="kind" value={kind} onChange={(e) => setKind(e.target.value)}
                  className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm"
                >
                  {KINDS.map((k) => <option key={k} value={k}>{k}</option>)}
                </select>
              </div>
              <div><Label>Display name</Label><Input name="display_name" placeholder="Optional" /></div>

              {isFias(kind) && (
                <>
                  <div><Label>Host</Label><Input name="host" placeholder="10.0.0.5" /></div>
                  <div><Label>Port</Label><Input name="port" type="number" min={1} max={65535} placeholder="5010" /></div>
                  <div><Label>Auth key</Label><Input name="auth_key" type="password" placeholder="write-only" /></div>
                  <label className="flex items-center gap-2 text-sm text-muted"><input type="checkbox" name="use_tls" /> Use TLS</label>
                </>
              )}
              {isRest(kind) && (
                <>
                  <div><Label>Base URL</Label><Input name="base_url" placeholder="https://api.mews.com" /></div>
                  <div><Label>API key</Label><Input name="api_key" type="password" placeholder="write-only" /></div>
                  <div><Label>Property ID</Label><Input name="property_id" placeholder="Optional" /></div>
                </>
              )}

              <label className="flex items-center gap-2 text-sm text-muted"><input type="checkbox" name="enabled" defaultChecked /> Enabled</label>
              <div className="sm:col-span-3 flex justify-end">
                <Button type="submit" disabled={busy}>{busy ? "Creating…" : "Create"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      {editing && writable && (
        <Card className="mb-6">
          <CardHeader>
            <CardTitle>Edit {editing.name} <span className="text-muted font-normal">({editing.kind})</span></CardTitle>
            <Button size="sm" variant="ghost" onClick={() => setEditing(null)}><X size={14} /></Button>
          </CardHeader>
          <CardBody>
            <form onSubmit={onEdit} className="grid grid-cols-1 sm:grid-cols-3 gap-3">
              <div><Label>Display name</Label><Input name="display_name" defaultValue={editing.display_name ?? ""} /></div>
              {isFias(editing.kind) && (
                <>
                  <div><Label>Host</Label><Input name="host" defaultValue={editing.host ?? ""} /></div>
                  <div><Label>Port</Label><Input name="port" type="number" min={1} max={65535} defaultValue={editing.port ?? ""} /></div>
                  <div><Label>Auth key</Label><Input name="auth_key" type="password" placeholder="leave blank to keep" /></div>
                  <label className="flex items-center gap-2 text-sm text-muted"><input type="checkbox" name="use_tls" defaultChecked={editing.use_tls} /> Use TLS</label>
                </>
              )}
              {isRest(editing.kind) && (
                <>
                  <div><Label>Base URL</Label><Input name="base_url" defaultValue={editing.base_url ?? ""} /></div>
                  <div><Label>API key</Label><Input name="api_key" type="password" placeholder="leave blank to keep" /></div>
                  <div><Label>Property ID</Label><Input name="property_id" defaultValue={editing.property_id ?? ""} /></div>
                </>
              )}
              <label className="flex items-center gap-2 text-sm text-muted"><input type="checkbox" name="enabled" defaultChecked={editing.enabled} /> Enabled</label>
              <div className="sm:col-span-3 flex justify-end">
                <Button type="submit" disabled={busy}>{busy ? "Saving…" : "Save"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      <Card>
        <CardBody className="p-0">
          {rows === null ? <EmptyState title="Loading…" /> : rows.length === 0 ? (
            <EmptyState title="No PMS providers" hint="Connect your property management system to enable room-match auth." />
          ) : (
            <Table>
              <THead>
                <TR><TH>Name</TH><TH>Kind</TH><TH>Endpoint</TH><TH>Status</TH><TH>Last record</TH><TH>Enabled</TH><TH></TH></TR>
              </THead>
              <tbody>
                {rows.map((p) => (
                  <TR key={p.id}>
                    <TD>
                      <div>{p.display_name || p.name}</div>
                      <div className="text-xs text-muted font-mono">{p.name}</div>
                    </TD>
                    <TD className="font-mono text-xs">{p.kind}</TD>
                    <TD className="text-muted font-mono text-xs">
                      {isFias(p.kind) ? `${p.host ?? "—"}:${p.port ?? "—"}` : (p.base_url ?? "—")}
                    </TD>
                    <TD>
                      <Badge tone={statusTone(p.status)}>{p.status}</Badge>
                      {p.last_error && <div className="text-xs text-err mt-1 max-w-xs truncate" title={p.last_error}>{p.last_error}</div>}
                    </TD>
                    <TD className="text-muted">{p.last_record_at ? formatRelative(p.last_record_at) : "—"}</TD>
                    <TD>{p.enabled ? <Badge tone="ok">on</Badge> : <Badge tone="default">off</Badge>}</TD>
                    <TD className="text-right space-x-2 whitespace-nowrap">
                      <Button size="sm" variant="ghost" disabled={acting === p.name + ":test"} onClick={() => onTest(p.name)}>Test</Button>
                      <Button size="sm" variant="ghost" disabled={acting === p.name + ":health"} onClick={() => onHealth(p.name)}>Health</Button>
                      <Button size="sm" variant="ghost" disabled={acting === p.name + ":cache"} onClick={() => onCache(p.name)}>Cache</Button>
                      {writable && <Button size="sm" variant="ghost" onClick={() => { setEditing(p); setShowNew(false); }}>Edit</Button>}
                      {writable && <Button size="sm" variant="ghost" onClick={() => onDelete(p.name)}>Delete</Button>}
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      {result && (
        <div className="fixed inset-0 bg-black/60 flex items-center justify-center p-6 z-50" onClick={() => setResult(null)}>
          <div className="bg-panel border border-border rounded-lg shadow-panel max-w-3xl w-full max-h-[80vh] overflow-auto" onClick={(e) => e.stopPropagation()}>
            <div className="px-5 py-4 border-b border-border flex items-center justify-between">
              <h2 className="text-base font-semibold">{result.title}</h2>
              <Button size="sm" variant="ghost" onClick={() => setResult(null)}><X size={14} /></Button>
            </div>
            <div className="px-5 py-4">{result.body}</div>
          </div>
        </div>
      )}
    </div>
  );
}
