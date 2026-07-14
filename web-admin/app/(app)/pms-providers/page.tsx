"use client";

import { useEffect, useState } from "react";
import {
  api, ApiError, ListResp, Site,
  PMSProvider, PMSTestResult, PMSHealthResult, PMSCacheResult,
} from "@/lib/api";
import { useTenant } from "@/lib/use-tenant";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X, FlaskConical, Database, Activity, Trash2 } from "lucide-react";
import { formatRelative, errMsg } from "@/lib/utils";

const KINDS: PMSProvider["kind"][] = [
  "stub", "protel-fias", "opera-fias", "fidelio-fias", "mews", "apaleo",
];

const statusTone = (s: string): "ok" | "warn" | "err" | "info" | "default" => {
  switch (s) {
    case "connected": return "ok";
    case "connecting": return "info";
    case "degraded": return "warn";
    case "down": return "err";
    default: return "default";
  }
};

// Providers in the FIAS family share the same host/port/auth_key shape.
const isFIAS = (kind: string) => kind.endsWith("-fias");
const isREST = (kind: string) => kind === "mews" || kind === "apaleo";

// scopeSuffix turns a site_id (possibly empty) into the URL tail we append
// to all CRUD paths. The control plane uses ?site_id= to target a specific
// site-scoped row vs. the tenant-wide row.
function scopeSuffix(siteID?: string): string {
  return siteID ? `&site_id=${encodeURIComponent(siteID)}` : "";
}

// rowKey uniquely identifies a row across tenant-wide and site-scoped
// entries with the same name; used as React key + edit-mode selector.
function rowKey(p: PMSProvider): string {
  return p.site_id ? `${p.name}@${p.site_id}` : p.name;
}

export default function PMSProvidersPage() {
  const tenantID = useTenant();
  const [rows, setRows] = useState<PMSProvider[] | null>(null);
  const [sites, setSites] = useState<Site[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [editing, setEditing] = useState<string | null>(null); // rowKey being edited
  const [busy, setBusy] = useState(false);

  // Per-row action state (keyed by provider name).
  const [tests, setTests] = useState<Record<string, PMSTestResult>>({});
  const [healths, setHealths] = useState<Record<string, PMSHealthResult>>({});
  const [cacheOpen, setCacheOpen] = useState<string | null>(null);
  const [cacheData, setCacheData] = useState<PMSCacheResult | null>(null);

  async function load() {
    if (!tenantID) return;
    try {
      const [rRes, sRes] = await Promise.all([
        api.get<ListResp<PMSProvider>>(`/v1/pms-providers?tenant_id=${tenantID}`),
        api.get<ListResp<Site>>(`/v1/sites?tenant_id=${tenantID}`),
      ]);
      setRows(rRes.data ?? []);
      setSites(sRes.data ?? []);
    } catch (e) { setErr(errMsg(e)); }
  }
  useEffect(() => { load(); }, [tenantID]);

  async function onCreate(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!tenantID) return;
    setBusy(true); setErr(null);
    const form = new FormData(e.currentTarget);
    const body = buildBody(form, /*forCreate=*/true);
    try {
      await api.post(`/v1/pms-providers?tenant_id=${tenantID}`, body);
      setShowNew(false);
      (e.currentTarget as HTMLFormElement).reset();
      load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onUpdate(p: PMSProvider, e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!tenantID) return;
    setBusy(true); setErr(null);
    const form = new FormData(e.currentTarget);
    const body = buildBody(form, /*forCreate=*/false);
    try {
      await api.patch(`/v1/pms-providers/${p.name}?tenant_id=${tenantID}${scopeSuffix(p.site_id)}`, body);
      setEditing(null);
      load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onDelete(p: PMSProvider) {
    if (!tenantID) return;
    const scopeLabel = p.site_id ? ` (at site ${siteLabel(p.site_id)})` : " (tenant-wide)";
    if (!confirm(`Delete provider "${p.name}"${scopeLabel}?`)) return;
    try {
      await api.del(`/v1/pms-providers/${p.name}?tenant_id=${tenantID}${scopeSuffix(p.site_id)}`);
      load();
    } catch (e) { setErr(errMsg(e)); }
  }

  async function onToggle(p: PMSProvider) {
    if (!tenantID) return;
    try {
      await api.patch(`/v1/pms-providers/${p.name}?tenant_id=${tenantID}${scopeSuffix(p.site_id)}`,
        { enabled: !p.enabled });
      load();
    } catch (e) { setErr(errMsg(e)); }
  }

  function siteLabel(sid: string): string {
    return sites.find((s) => s.id === sid)?.name ?? sid.slice(0, 8);
  }

  // test/cache/health target the appliance's resolved provider by name —
  // there's only one registered entry per name at runtime. UI state keys
  // by rowKey() though, so two rows (tenant-wide + site override) with
  // the same name get independent status cells.
  async function onTest(p: PMSProvider) {
    if (!tenantID) return;
    const k = rowKey(p);
    setTests((t) => ({ ...t, [k]: { ok: false, latency_ms: -1 } }));
    try {
      const res = await api.post<PMSTestResult>(`/v1/pms-providers/${p.name}/test?tenant_id=${tenantID}`);
      setTests((t) => ({ ...t, [k]: res }));
    } catch (e) {
      const ae = e as ApiError;
      setTests((t) => ({ ...t, [k]: { ok: false, latency_ms: 0, error: ae.message } }));
    }
  }

  async function onHealth(p: PMSProvider) {
    if (!tenantID) return;
    const k = rowKey(p);
    try {
      const res = await api.get<PMSHealthResult>(`/v1/pms-providers/${p.name}/health?tenant_id=${tenantID}`);
      setHealths((h) => ({ ...h, [k]: res }));
    } catch (e) { setErr(errMsg(e)); }
  }

  async function onCache(p: PMSProvider) {
    if (!tenantID) return;
    setCacheOpen(rowKey(p));
    setCacheData(null);
    try {
      const res = await api.get<PMSCacheResult>(`/v1/pms-providers/${p.name}/cache?limit=100&tenant_id=${tenantID}`);
      setCacheData(res);
    } catch (e) { setErr(errMsg(e)); setCacheOpen(null); }
  }

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Integrations</div>
          <h1 className="text-2xl font-semibold">PMS providers</h1>
          <div className="text-xs text-muted mt-1">
            Property-Management-System connectors used for guest auth. Changes push live to appliances — no restart needed.
          </div>
        </div>
        <Button onClick={() => { setShowNew((s) => !s); setEditing(null); }}>
          {showNew ? <><X size={14} /> Cancel</> : <><Plus size={14} /> New provider</>}
        </Button>
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      {showNew && (
        <Card className="mb-6">
          <CardHeader><CardTitle>New provider</CardTitle></CardHeader>
          <CardBody>
            <ProviderForm busy={busy} sites={sites} onSubmit={onCreate} mode="create" />
          </CardBody>
        </Card>
      )}

      <Card>
        <CardBody className="p-0">
          {rows === null ? <EmptyState title="Loading…" /> :
           rows.length === 0 ? (
            <EmptyState title="No providers yet" hint="Create one to enable PMS-based guest auth." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Name</TH><TH>Kind</TH><TH>Scope</TH><TH>Status</TH><TH>Enabled</TH>
                  <TH>Last record</TH><TH>Test</TH><TH></TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((p) => {
                  const k = rowKey(p);
                  const tr = tests[k];
                  return (
                    <>
                      <TR key={k}>
                        <TD>
                          <div className="font-medium">{p.display_name || p.name}</div>
                          <div className="text-xs text-muted font-mono">{p.name}</div>
                        </TD>
                        <TD className="text-muted">{p.kind}</TD>
                        <TD>
                          {p.site_id ? (
                            <span title={p.site_id}><Badge tone="info">site: {siteLabel(p.site_id)}</Badge></span>
                          ) : (
                            <Badge tone="default">tenant-wide</Badge>
                          )}
                        </TD>
                        <TD><Badge tone={statusTone(p.status)}>{p.status}</Badge></TD>
                        <TD>
                          <button
                            onClick={() => onToggle(p)}
                            className={`text-xs px-2 py-0.5 rounded border ${p.enabled ? "text-ok border-[#1e5c3c] bg-[#123422]" : "text-muted border-border"}`}>
                            {p.enabled ? "enabled" : "disabled"}
                          </button>
                        </TD>
                        <TD className="text-muted text-xs">{formatRelative(p.last_record_at)}</TD>
                        <TD>
                          {tr === undefined ? <span className="text-muted text-xs">—</span> :
                           tr.latency_ms < 0 ? <span className="text-muted text-xs">testing…</span> :
                           tr.ok ? <Badge tone="ok">ok {tr.latency_ms}ms</Badge> :
                                   <span title={tr.error}><Badge tone="err" className="max-w-[220px] truncate">{tr.error ?? "failed"}</Badge></span>}
                        </TD>
                        <TD className="text-right whitespace-nowrap">
                          <Button size="sm" variant="ghost" onClick={() => onTest(p)}><FlaskConical size={12} /> Test</Button>
                          <Button size="sm" variant="ghost" onClick={() => onHealth(p)}><Activity size={12} /> Health</Button>
                          <Button size="sm" variant="ghost" onClick={() => onCache(p)}><Database size={12} /> Cache</Button>
                          <Button size="sm" variant="ghost" onClick={() => setEditing(editing === k ? null : k)}>Edit</Button>
                          <Button size="sm" variant="ghost" onClick={() => onDelete(p)}><Trash2 size={12} /></Button>
                        </TD>
                      </TR>
                      {healths[k] && (
                        <TR key={k + "-h"}>
                          <TD colSpan={8} className="bg-panel2/50">
                            <div className="text-xs font-mono text-muted whitespace-pre-wrap">
                              {JSON.stringify(healths[k].health, null, 2)}
                            </div>
                          </TD>
                        </TR>
                      )}
                      {editing === k && (
                        <TR key={k + "-edit"}>
                          <TD colSpan={8} className="bg-panel2/30">
                            <ProviderForm busy={busy} sites={sites}
                              onSubmit={(e) => onUpdate(p, e)} mode="edit" row={p} />
                          </TD>
                        </TR>
                      )}
                    </>
                  );
                })}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      {cacheOpen && (
        <div className="fixed inset-0 bg-black/60 flex items-center justify-center p-6 z-50" onClick={() => setCacheOpen(null)}>
          <div className="bg-panel border border-border rounded-md max-w-4xl w-full max-h-[80vh] overflow-auto" onClick={(e) => e.stopPropagation()}>
            <div className="p-4 border-b border-border flex items-center justify-between">
              <div>
                <div className="font-semibold">Cache — {cacheOpen}</div>
                <div className="text-xs text-muted">
                  {cacheData ? `${cacheData.count} row${cacheData.count === 1 ? "" : "s"} (${cacheData.kind})` : "loading…"}
                </div>
              </div>
              <Button size="sm" variant="ghost" onClick={() => setCacheOpen(null)}><X size={14} /></Button>
            </div>
            <div className="p-4">
              {!cacheData ? <EmptyState title="Loading…" /> :
               cacheData.rows.length === 0 ? <EmptyState title="No cached reservations" /> : (
                <Table>
                  <THead>
                    <TR>
                      <TH>Room</TH><TH>Guest</TH><TH>Reservation</TH><TH>Check-in</TH><TH>Check-out</TH>
                    </TR>
                  </THead>
                  <tbody>
                    {cacheData.rows.map((r, i) => (
                      <TR key={i}>
                        <TD className="font-mono">{r.room_number}</TD>
                        <TD>{[r.first_name, r.last_name].filter(Boolean).join(" ") || r.guest_display_name || "—"}</TD>
                        <TD className="font-mono text-xs">{r.reservation_number}</TD>
                        <TD className="text-muted text-xs">{formatRelative(r.check_in)}</TD>
                        <TD className="text-muted text-xs">{formatRelative(r.check_out)}</TD>
                      </TR>
                    ))}
                  </tbody>
                </Table>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

// ---------- form ----------

type FormMode = "create" | "edit";

function ProviderForm({
  busy, sites, onSubmit, mode, row,
}: {
  busy: boolean;
  sites: Site[];
  onSubmit: (e: React.FormEvent<HTMLFormElement>) => void;
  mode: FormMode;
  row?: PMSProvider;
}) {
  const [kind, setKind] = useState<PMSProvider["kind"]>(row?.kind ?? "stub");
  const fias = isFIAS(kind);
  const rest = isREST(kind);

  return (
    <form onSubmit={onSubmit} className="grid grid-cols-1 sm:grid-cols-4 gap-3">
      <div>
        <Label>Name</Label>
        <Input name="name" defaultValue={row?.name} required disabled={mode === "edit"}
               pattern="[A-Za-z0-9_\-]+" placeholder="main-pms" />
      </div>
      <div>
        <Label>Kind</Label>
        <select
          name="kind"
          defaultValue={row?.kind ?? "stub"}
          disabled={mode === "edit"}
          onChange={(e) => setKind(e.target.value as PMSProvider["kind"])}
          className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm disabled:opacity-60"
        >
          {KINDS.map((k) => <option key={k} value={k}>{k}</option>)}
        </select>
      </div>
      <div>
        <Label>Display name</Label>
        <Input name="display_name" defaultValue={row?.display_name ?? ""} placeholder="Main PMS" />
      </div>
      <div>
        <Label>Enabled</Label>
        <select
          name="enabled"
          defaultValue={String(row?.enabled ?? true)}
          className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm"
        >
          <option value="true">Enabled</option>
          <option value="false">Disabled</option>
        </select>
      </div>

      {/* Scope picker — create-only; edit mode shows it read-only so the
          operator sees whether they're editing the tenant-wide row or a
          site override. Moving a row between scopes requires delete + recreate. */}
      <div className="sm:col-span-2">
        <Label>Scope {mode === "edit" && <span className="text-muted">(immutable)</span>}</Label>
        <select
          name="site_id"
          defaultValue={row?.site_id ?? ""}
          disabled={mode === "edit"}
          className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm disabled:opacity-60"
        >
          <option value="">Tenant-wide (default for all sites)</option>
          {sites.map((s) => <option key={s.id} value={s.id}>Site override — {s.name}</option>)}
        </select>
      </div>

      {fias && (
        <>
          <div><Label>Host</Label><Input name="host" defaultValue={row?.host ?? ""} placeholder="pms.example.com" /></div>
          <div><Label>Port</Label><Input name="port" type="number" defaultValue={row?.port ?? ""} placeholder="5010" /></div>
          <div className="flex items-end gap-2">
            <div>
              <Label>TLS</Label>
              <select
                name="use_tls"
                defaultValue={String(row?.use_tls ?? false)}
                className="h-9 rounded-md bg-panel2 border border-border px-3 text-sm"
              >
                <option value="false">plain</option>
                <option value="true">TLS</option>
              </select>
            </div>
          </div>
          <div>
            <Label>Auth key {mode === "edit" && <span className="text-muted">(blank = keep existing)</span>}</Label>
            <Input name="auth_key" type="password" placeholder={mode === "edit" ? "••••••••" : "IfcAuthKey"} />
          </div>
        </>
      )}

      {rest && (
        <>
          <div className="sm:col-span-2"><Label>Base URL</Label><Input name="base_url" defaultValue={row?.base_url ?? ""} placeholder="https://api.mews.com" /></div>
          <div>
            <Label>API key {mode === "edit" && <span className="text-muted">(blank = keep existing)</span>}</Label>
            <Input name="api_key" type="password" placeholder={mode === "edit" ? "••••••••" : "..."} />
          </div>
          <div><Label>Property / Account</Label><Input name="property_id" defaultValue={row?.property_id ?? ""} placeholder="ACM" /></div>
        </>
      )}

      <div className="sm:col-span-4 border-t border-border pt-3 mt-2 text-xs text-muted">
        Advanced (JSON). Leave empty to use provider defaults.
      </div>
      <div className="sm:col-span-2">
        <Label>Field map</Label>
        <textarea name="field_map" defaultValue={jsonPretty(row?.field_map)} rows={3}
          className="w-full rounded-md bg-panel2 border border-border px-3 py-2 text-xs font-mono"
          placeholder='{"room_number":"RN","last_name":"GN"}' />
      </div>
      <div>
        <Label>Normalization</Label>
        <textarea name="normalization" defaultValue={jsonPretty(row?.normalization)} rows={3}
          className="w-full rounded-md bg-panel2 border border-border px-3 py-2 text-xs font-mono"
          placeholder='{"room_format":"%03d"}' />
      </div>
      <div>
        <Label>Stay window</Label>
        <textarea name="stay_window" defaultValue={jsonPretty(row?.stay_window)} rows={3}
          className="w-full rounded-md bg-panel2 border border-border px-3 py-2 text-xs font-mono"
          placeholder='{"early_checkin_minutes":60}' />
      </div>

      <div className="sm:col-span-4 flex justify-end">
        <Button type="submit" disabled={busy}>{busy ? "Saving…" : mode === "create" ? "Create" : "Save"}</Button>
      </div>
    </form>
  );
}

function jsonPretty(v: any): string {
  if (v === undefined || v === null) return "";
  if (typeof v !== "object" || Object.keys(v).length === 0) return "";
  try { return JSON.stringify(v, null, 2); } catch { return ""; }
}

// buildBody shapes a FormData into the ctrlapi write request. For patches
// we send only the fields the form contains, plus every jsonb field (the
// textarea is always present, empty string means "reset to {}"). Secrets
// (auth_key, api_key) are omitted when empty so the server keeps them.
function buildBody(form: FormData, forCreate: boolean): any {
  const body: any = {};
  if (forCreate) {
    body.name = form.get("name");
    body.kind = form.get("kind");
    // Scope is create-only. Empty string = tenant-wide (server interprets);
    // a UUID = site override.
    const site = form.get("site_id");
    if (typeof site === "string" && site !== "") body.site_id = site;
  }
  const str = (k: string) => {
    const v = form.get(k);
    return typeof v === "string" ? v : "";
  };
  const maybe = (k: string) => {
    const v = str(k);
    return v === "" ? undefined : v;
  };
  const bool = (k: string) => {
    const v = str(k);
    if (v === "") return undefined;
    return v === "true";
  };
  const int = (k: string) => {
    const v = str(k);
    if (v === "") return undefined;
    const n = Number(v);
    return Number.isFinite(n) ? n : undefined;
  };
  const jsonField = (k: string) => {
    const raw = str(k).trim();
    if (raw === "") return {}; // explicit reset
    try { return JSON.parse(raw); } catch { return undefined; /* ignored → server keeps existing */ }
  };

  const assign = (k: string, v: any) => { if (v !== undefined) body[k] = v; };

  assign("display_name", maybe("display_name"));
  assign("enabled", bool("enabled"));
  assign("host", maybe("host"));
  assign("port", int("port"));
  assign("use_tls", bool("use_tls"));
  if (maybe("auth_key")) body.auth_key = str("auth_key");
  assign("base_url", maybe("base_url"));
  if (maybe("api_key")) body.api_key = str("api_key");
  assign("property_id", maybe("property_id"));

  // jsonb fields — always send for edit mode so an emptied textarea resets to {}.
  // On create, skip empty to let defaults apply.
  const fm = jsonField("field_map");
  const nm = jsonField("normalization");
  const sw = jsonField("stay_window");
  if (forCreate) {
    if (fm && Object.keys(fm).length > 0) body.field_map = fm;
    if (nm && Object.keys(nm).length > 0) body.normalization = nm;
    if (sw && Object.keys(sw).length > 0) body.stay_window = sw;
  } else {
    if (fm !== undefined) body.field_map = fm;
    if (nm !== undefined) body.normalization = nm;
    if (sw !== undefined) body.stay_window = sw;
  }

  return body;
}
