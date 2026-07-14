"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { api, withStepUp, reauth, ApiError, ListResp } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { EmptyState } from "@/components/ui/empty-state";
import { DeleteDialog } from "@/components/delete-dialog";
import { Check, Loader2, Circle, Plug, ShieldCheck, RotateCcw, RefreshCw } from "lucide-react";
import { formatRelative } from "@/lib/utils";

/**
 * Connect an Appliance — pick a Pending appliance, choose Customer/Site + license terms,
 * click Activate ONCE. The server runs claim -> assign -> signed assignment ->
 * certificate -> hardware-bound license; the appliance then converges to Active
 * on its own. No token needed for the normal online flow.
 */

type Pending = {
  id: string; serial: string; public_key_fingerprint?: string; source_ip?: string;
  state?: string; first_seen?: string; wan_mac?: string; lan_mac?: string; model?: string; hostname?: string;
};
type Tenant = { id: string; slug: string; name: string };
type Site = { id: string; code: string; name: string };
type AssignmentStatus = { issued?: boolean; state?: string; adopted_at?: string };
type ApplianceRow = { id: string; serial: string; lifecycle_state?: string; tenant_id?: string; wan_mac?: string };

const STEPS = [
  { key: "detected", label: "Detected" },
  { key: "activating", label: "Activating (assign + certificate + license)" },
  { key: "converging", label: "Appliance converging (mTLS + assignment adoption)" },
  { key: "active", label: "Active" },
] as const;

function slugify(s: string) {
  return s.toLowerCase().trim().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 40);
}

export default function OnboardingPage() {
  const router = useRouter();
  const [pending, setPending] = useState<Pending[] | null>(null);
  const [tenants, setTenants] = useState<Tenant[]>([]);
  // Simple license model — the activation form carries the license terms.
  const [maxGuests, setMaxGuests] = useState("500");
  const [validUntil, setValidUntil] = useState("");
  const [graceDays, setGraceDays] = useState("30");
  const [sites, setSites] = useState<Site[]>([]);

  const [sel, setSel] = useState<Pending | null>(null);
  const [tenantMode, setTenantMode] = useState<"existing" | "new">("existing");
  const [tenantId, setTenantId] = useState("");
  const [newCustomer, setNewCustomer] = useState("");
  const [siteMode, setSiteMode] = useState<"existing" | "new">("existing");
  const [siteId, setSiteId] = useState("");
  const [newSite, setNewSite] = useState("");
  
  const [password, setPassword] = useState("");
  const [formErr, setFormErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const [phase, setPhase] = useState<string>("");     // "" = form; else running
  const [runErr, setRunErr] = useState<string | null>(null);
  const trackId = useRef<string | null>(null);

  // Registered (already-activated) appliances, for self-service reset.
  const [registered, setRegistered] = useState<ApplianceRow[]>([]);
  const [rowBusy, setRowBusy] = useState<string | null>(null);
  const [manageMsg, setManageMsg] = useState<string | null>(null);

  const loadPending = useCallback(async () => {
    try {
      const p = await api.get<{ data: Pending[] }>("/cloud/v1/appliances-admin/pending");
      setPending(p.data ?? []);
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) { router.replace("/login"); return; }
      setPending([]);
    }
  }, [router]);

  const loadBase = useCallback(async () => {
    try {
      const t = await api.get<{ data: Tenant[] }>("/v1/tenants");
      setTenants(t.data ?? []);
    } catch { /* ignore */ }
  }, []);

  // All non-pending appliances (fan out across tenants — the per-tenant list is
  // tenant-scoped), so activated boxes can be deactivated/deleted here.
  const loadRegistered = useCallback(async () => {
    try {
      const t = await api.get<{ data: Tenant[] }>("/v1/tenants");
      const per = await Promise.all((t.data ?? []).map((tn) =>
        api.get<{ data: ApplianceRow[] }>(`/v1/appliances?tenant_id=${tn.id}`).then((r) => r.data ?? []).catch(() => [])));
      setRegistered(per.flat());
    } catch { /* ignore */ }
  }, []);

  useEffect(() => { loadPending(); loadBase(); loadRegistered(); }, [loadPending, loadBase, loadRegistered]);
  // Poll the lists while on the form so newly self-registered appliances appear.
  useEffect(() => {
    if (phase) return;
    const t = setInterval(() => { loadPending(); loadRegistered(); }, 5000);
    return () => clearInterval(t);
  }, [phase, loadPending, loadRegistered]);

  async function onDeactivate(a: ApplianceRow) {
    if (!confirm(`Deactivate ${a.serial}? Its license is revoked; the appliance can be re-activated.`)) return;
    setRowBusy(a.id); setManageMsg(null);
    try {
      await withStepUp(() => api.post(`/cloud/v1/appliances-admin/${a.id}/deactivate`, {}));
      setManageMsg(`${a.serial} deactivated (license revoked).`);
      await loadRegistered();
    } catch (e) { setManageMsg(e instanceof Error ? e.message : "Deactivate failed"); }
    finally { setRowBusy(null); }
  }
  const [delApp, setDelApp] = useState<ApplianceRow | null>(null);
  const [delImpact, setDelImpact] = useState<any>(null);
  const [showAdvanced, setShowAdvanced] = useState(false);

  async function openDelete(a: ApplianceRow) {
    setDelApp(a); setDelImpact(null);
    try {
      setDelImpact(await api.get<any>(`/cloud/v1/appliances-admin/${a.id}/delete-impact`));
    } catch { /* preview is best-effort */ }
  }

  // Advanced Support: elevated technical actions (step-up + reason + audit).
  async function advancedAction(a: ApplianceRow, verb: string, url: string) {
    const reason = window.prompt(`Reason for ${verb} ${a.serial} (audited):`, "");
    if (reason === null) return;
    setRowBusy(a.id); setManageMsg(null);
    try {
      await withStepUp(() => api.post(url, { reason }));
      setManageMsg(`${a.serial}: ${verb} done.`);
      await loadRegistered();
    } catch (e) { setManageMsg(e instanceof Error ? e.message : `${verb} failed`); }
    finally { setRowBusy(null); }
  }

  useEffect(() => {
    if (tenantMode !== "existing" || !tenantId) { setSites([]); return; }
    api.get<{ data: Site[] }>(`/v1/sites?tenant_id=${tenantId}`).then((r) => setSites(r.data ?? [])).catch(() => setSites([]));
  }, [tenantMode, tenantId]);

  async function resolveTargets(): Promise<{ tenant: string; site: string }> {
    let tid = tenantId;
    if (tenantMode === "new") {
      const name = newCustomer.trim();
      if (!name) throw new Error("Enter a customer name.");
      const slug = slugify(name);
      await api.post("/v1/tenants", { slug, name });
      const r = await api.get<{ data: Tenant[] }>("/v1/tenants");
      tid = (r.data ?? []).find((x) => x.slug === slug)?.id ?? "";
    }
    if (!tid) throw new Error("Pick or create a customer.");
    let sid = siteMode === "existing" ? siteId : "";
    if (siteMode === "new") {
      const name = newSite.trim();
      if (!name) throw new Error("Enter a site name.");
      const code = slugify(name);
      const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
      await api.post(`/v1/sites?tenant_id=${tid}`, { code, name, timezone: tz });
      const r = await api.get<{ data: Site[] }>(`/v1/sites?tenant_id=${tid}`);
      sid = (r.data ?? []).find((x) => x.code === code)?.id ?? "";
    }
    if (!sid) throw new Error("Pick or create a site.");
    return { tenant: tid, site: sid };
  }

  async function onActivate() {
    setFormErr(null);
    if (!sel) { setFormErr("Select a pending appliance."); return; }
    if (!password.trim()) { setFormErr("Enter your password to authorize activation."); return; }
    setBusy(true);
    try {
      await reauth(password.trim());
      setPassword("");
      const t = await resolveTargets();
      trackId.current = sel.id;
      setPhase("activating");
      const body: any = {
        tenant_id: t.tenant, site_id: t.site,
        max_concurrent_online_guests: Number(maxGuests) || 0,
        grace_period_days: Number(graceDays) || 30,
      };
      if (validUntil) body.valid_until = new Date(validUntil + "T23:59:59Z").toISOString();
      else body.valid_days = 365;
      const res = await withStepUp(() =>
        api.post<{ status: string }>(`/cloud/v1/appliances-admin/${sel.id}/activate`, body));
      if (res.status !== "activated") throw new Error("activation did not complete");
      setPhase("converging");
    } catch (e) {
      if (e instanceof ApiError && (e.status === 401 || e.code === "reauth_required")) { setFormErr("Password confirmation failed."); }
      else setFormErr(e instanceof Error ? e.message : "Activation failed.");
      setPhase("");
    } finally {
      setBusy(false);
    }
  }

  // Once activating, poll the assignment adoption to flip to Active.
  const advance = useCallback(async () => {
    const id = trackId.current;
    if (!id || phase === "" || phase === "active") return;
    try {
      const a = await api.get<AssignmentStatus>(`/cloud/v1/appliances-admin/${id}/assignment`);
      if (a.adopted_at) setPhase("active");
    } catch { /* keep polling */ }
  }, [phase]);
  useEffect(() => {
    if (phase !== "converging") return;
    advance();
    const t = setInterval(advance, 4000);
    return () => clearInterval(t);
  }, [phase, advance]);

  function reset() {
    setPhase(""); setRunErr(null); trackId.current = null; setSel(null);
    setNewCustomer(""); setNewSite("");
    loadPending();
  }

  const selectCls = "w-full rounded-md border border-border bg-panel2 px-3 py-2 text-sm";
  const phaseIdx = STEPS.findIndex((s) => s.key === phase);

  return (
    <div className="p-6 max-w-5xl mx-auto">
      <div className="mb-1 text-xs text-muted uppercase tracking-wider">Infrastructure</div>
      <h1 className="mb-1 flex items-center gap-2 text-2xl font-semibold"><Plug size={22} /> Connect an Appliance</h1>
      <p className="mb-6 text-sm text-muted">
        A factory-clean appliance with internet appears here automatically. Pick it, choose customer, site and the license terms,
        and click Activate once — assignment, certificate, mTLS and the signed license all happen for you. No plan or subscription needed.
      </p>

      {!phase ? (
        <div className="space-y-6">
          <Card>
            <CardHeader className="flex flex-row items-center justify-between">
              <CardTitle>Pending activation</CardTitle>
              <Button variant="ghost" size="sm" onClick={loadPending}><RefreshCw size={14} /> Refresh</Button>
            </CardHeader>
            <CardBody className="p-0">
              {pending === null ? <EmptyState title="Loading…" />
                : pending.length === 0 ? <EmptyState title="No pending appliances" hint="A factory-clean appliance with Central connectivity self-registers and shows up here." />
                : (
                  <Table>
                    <THead><TR><TH></TH><TH>Serial</TH><TH>WAN MAC</TH><TH>Model</TH><TH>Source IP</TH><TH>First seen</TH></TR></THead>
                    <tbody>
                      {pending.map((p) => (
                        <TR key={p.id} className={sel?.id === p.id ? "bg-[#121a2e]" : "cursor-pointer"} onClick={() => setSel(p)}>
                          <TD>{sel?.id === p.id ? <Check className="h-4 w-4 text-brand" /> : <Circle className="h-4 w-4 text-muted/40" />}</TD>
                          <TD className="font-mono">{p.serial}</TD>
                          <TD className="font-mono text-xs">{p.wan_mac || "—"}</TD>
                          <TD className="text-muted">{p.model || "—"}</TD>
                          <TD className="text-muted">{p.source_ip || "—"}</TD>
                          <TD className="text-muted">{p.first_seen ? formatRelative(p.first_seen) : "—"}</TD>
                        </TR>
                      ))}
                    </tbody>
                  </Table>
                )}
            </CardBody>
          </Card>

          {sel && (
            <Card>
              <CardHeader><CardTitle>Activate {sel.serial}</CardTitle></CardHeader>
              <CardBody className="space-y-5">
                <div className="space-y-2">
                  <Label>Customer</Label>
                  <div className="flex gap-2 text-sm">
                    <button type="button" onClick={() => setTenantMode("existing")} className={`rounded px-3 py-1 ${tenantMode === "existing" ? "bg-brand/25 text-brand" : "bg-panel2 text-muted"}`}>Existing</button>
                    <button type="button" onClick={() => setTenantMode("new")} className={`rounded px-3 py-1 ${tenantMode === "new" ? "bg-brand/25 text-brand" : "bg-panel2 text-muted"}`}>+ New</button>
                  </div>
                  {tenantMode === "existing"
                    ? <select className={selectCls} value={tenantId} onChange={(e) => { setTenantId(e.target.value); setSiteId(""); }}><option value="">Select a customer…</option>{tenants.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}</select>
                    : <Input placeholder="Customer name" value={newCustomer} onChange={(e) => setNewCustomer(e.target.value)} />}
                </div>
                <div className="space-y-2">
                  <Label>Site</Label>
                  <div className="flex gap-2 text-sm">
                    <button type="button" onClick={() => setSiteMode("existing")} disabled={tenantMode === "new"} className={`rounded px-3 py-1 disabled:opacity-40 ${siteMode === "existing" && tenantMode !== "new" ? "bg-brand/25 text-brand" : "bg-panel2 text-muted"}`}>Existing</button>
                    <button type="button" onClick={() => setSiteMode("new")} className={`rounded px-3 py-1 ${siteMode === "new" || tenantMode === "new" ? "bg-brand/25 text-brand" : "bg-panel2 text-muted"}`}>+ New</button>
                  </div>
                  {siteMode === "existing" && tenantMode === "existing"
                    ? <select className={selectCls} value={siteId} onChange={(e) => setSiteId(e.target.value)} disabled={!tenantId}><option value="">Select a site…</option>{sites.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}</select>
                    : <Input placeholder="Site name" value={newSite} onChange={(e) => setNewSite(e.target.value)} />}
                </div>
                <div className="grid grid-cols-1 sm:grid-cols-3 gap-3">
                  <div className="space-y-1">
                    <Label>Max concurrent online guests</Label>
                    <Input type="number" min={0} value={maxGuests} onChange={(e) => setMaxGuests(e.target.value)} />
                    <div className="text-xs text-muted">0 = unlimited. Appliance-wide across all guest VLANs.</div>
                  </div>
                  <div className="space-y-1">
                    <Label>Valid until</Label>
                    <Input type="date" value={validUntil} onChange={(e) => setValidUntil(e.target.value)} />
                    <div className="text-xs text-muted">Empty = 365 days from now.</div>
                  </div>
                  <div className="space-y-1">
                    <Label>Grace period (days)</Label>
                    <Input type="number" min={0} value={graceDays} onChange={(e) => setGraceDays(e.target.value)} />
                    <div className="text-xs text-muted">After expiry, guests keep working with warnings.</div>
                  </div>
                </div>
                <div className="space-y-1 max-w-sm">
                  <Label htmlFor="ob-pw">Confirm your password</Label>
                  <Input id="ob-pw" type="password" autoComplete="off" value={password} onChange={(e) => setPassword(e.target.value)} />
                </div>
                {formErr && <div className="text-sm text-err">{formErr}</div>}
                <Button onClick={onActivate} disabled={busy}>{busy ? <><Loader2 className="mr-1 h-4 w-4 animate-spin" /> Activating…</> : "Activate"}</Button>
              </CardBody>
            </Card>
          )}

          {registered.length > 0 && (
            <Card>
              <CardHeader className="flex flex-row items-center justify-between">
                <CardTitle>Registered appliances</CardTitle>
                <label className="text-xs text-muted flex items-center gap-2 font-normal">
                  <input type="checkbox" checked={showAdvanced} onChange={(e) => setShowAdvanced(e.target.checked)} />
                  Advanced Support
                </label>
              </CardHeader>
              <CardBody className="p-0">
                {manageMsg && <div className="px-4 pt-3 text-sm text-muted">{manageMsg}</div>}
                <Table>
                  <THead><TR><TH>Serial</TH><TH>State</TH><TH>WAN MAC</TH><TH className="text-right">Reset</TH></TR></THead>
                  <tbody>
                    {registered.map((a) => (
                      <TR key={a.id}>
                        <TD className="font-mono">{a.serial}</TD>
                        <TD><Badge tone={a.lifecycle_state === "activated" || a.lifecycle_state === "online" ? "ok" : "default"}>{a.lifecycle_state || "—"}</Badge></TD>
                        <TD className="font-mono text-xs">{a.wan_mac || "—"}</TD>
                        <TD className="text-right">
                          <div className="flex justify-end gap-2">
                            <Button size="sm" variant="secondary" disabled={rowBusy === a.id} onClick={() => onDeactivate(a)}>Deactivate</Button>
                            {showAdvanced && <Button size="sm" variant="ghost" disabled={rowBusy === a.id} onClick={() => advancedAction(a, "reissue certificate", `/cloud/v1/certificates/${a.id}/issue`)}>Reissue cert</Button>}
                            {showAdvanced && <Button size="sm" variant="ghost" disabled={rowBusy === a.id} onClick={() => advancedAction(a, "force reconcile", `/cloud/v1/appliances-admin/${a.id}/force-reconcile`)}>Reconcile</Button>}
                            {showAdvanced && <Button size="sm" variant="ghost" disabled={rowBusy === a.id} onClick={() => advancedAction(a, "decommission", `/cloud/v1/appliances-admin/${a.id}/decommission`)}>Decommission</Button>}
                            <Button size="sm" variant="danger" disabled={rowBusy === a.id} onClick={() => openDelete(a)}>Delete</Button>
                          </div>
                        </TD>
                      </TR>
                    ))}
                  </tbody>
                </Table>
                <p className="px-4 py-3 text-xs text-muted">
                  <b>Deactivate</b> revokes the license (re-activate to restore). <b>Delete</b> removes the appliance, its
                  license, assignment and certificate — the box then re-appears above as a fresh Pending. Delete the site or
                  customer from their own pages if you also want those gone.
                </p>
              </CardBody>
            </Card>
          )}
        </div>
      ) : (
        <Card>
          <CardHeader><CardTitle className="flex items-center gap-2">{phase === "active" ? <ShieldCheck className="h-4 w-4 text-ok" /> : <Loader2 className="h-4 w-4 animate-spin text-brand" />}{phase === "active" ? "Appliance active" : "Activating…"}</CardTitle></CardHeader>
          <CardBody>
            <ol className="space-y-3">
              {STEPS.map((s, i) => {
                const done = phaseIdx > i || phase === "active";
                const active = STEPS[i].key === phase && phase !== "active";
                return (
                  <li key={s.key} className="flex items-center gap-3 text-sm">
                    {done ? <Check className="h-5 w-5 text-ok" /> : active ? <Loader2 className="h-5 w-5 animate-spin text-brand" /> : <Circle className="h-5 w-5 text-muted/40" />}
                    <span className={done ? "text-muted" : active ? "font-medium text-text" : "text-muted"}>{s.label}</span>
                  </li>
                );
              })}
            </ol>
            {runErr && <div className="mt-4 text-sm text-err">{runErr}</div>}
            {phase === "active" && (
              <div className="mt-4 flex items-center gap-2">
                <Badge tone="ok">Activated</Badge>
                <Button size="sm" variant="secondary" onClick={reset}><RotateCcw className="mr-1 h-4 w-4" /> Activate another</Button>
              </div>
            )}
          </CardBody>
        </Card>
      )}

      <DeleteDialog
        open={!!delApp}
        onClose={() => { setDelApp(null); setDelImpact(null); }}
        onDeleted={() => { setManageMsg(`${delApp?.serial} deleted — it will re-register as Pending.`); loadRegistered(); loadPending(); }}
        title={`Delete appliance ${delApp?.serial ?? ""}`}
        what="Appliance"
        expected={delApp?.serial ?? ""}
        confirmHint="Type the appliance serial"
        deleteUrl={`/cloud/v1/appliances-admin/${delApp?.id}`}
        extraImpact={delImpact && (
          <div className="rounded-md border border-border bg-panel2 p-3 text-xs">
            <div className="mb-1 font-medium text-text">This will remove and terminate:</div>
            <ul className="space-y-0.5 text-muted">
              {(delImpact.terminates ?? []).map((t: string) => <li key={t}>• {t}</li>)}
              {Object.entries(delImpact.technical_records ?? {}).map(([k, v]) => (
                <li key={k}>• {String(v)} {k.replace(/_/g, " ")}</li>
              ))}
              {(delImpact.licenses_revoked ?? []).length > 0 && <li>• {delImpact.licenses_revoked.length} site license(s) revoked</li>}
            </ul>
          </div>
        )}
      />
    </div>
  );
}
