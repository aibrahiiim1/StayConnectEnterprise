"use client";

import { useCallback, useEffect, useState } from "react";
import { api, withStepUp } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { RefreshCw } from "lucide-react";
import { formatRelative } from "@/lib/utils";

type Tenant = { id: string; slug: string; name: string };
type Site = { id: string; code: string; name: string };
type Pending = {
  id: string;
  serial: string;
  public_key_fingerprint: string;
  source_ip: string;
  state: string;
  first_seen: string;
};
type Appliance = {
  id: string;
  serial: string;
  lifecycle_state?: string;
  tenant_id?: string;
  site_id?: string;
};
type AssignmentStatus = {
  issued: boolean;
  version?: number;
  state?: string;
  tenant_id?: string;
  site_id?: string;
  identity_key_fingerprint?: string;
};

/**
 * Appliance onboarding console.
 *
 * A factory appliance has NO customer identity. It enrolls (with a site-scoped
 * token) and then waits. The operator claims it and assigns it here — and that
 * assign is what makes Central mint a VENDOR-SIGNED ASSIGNMENT DOCUMENT bound to
 * this appliance's id + identity key. The appliance fetches it, verifies it, and
 * adopts the tenant/site. Nothing is ever hand-edited on the box.
 */
export default function OnboardingPage() {
  const [pending, setPending] = useState<Pending[] | null>(null);
  const [appliances, setAppliances] = useState<Appliance[]>([]);
  const [tenants, setTenants] = useState<Tenant[]>([]);
  const [sitesByTenant, setSitesByTenant] = useState<Record<string, Site[]>>({});
  const [assignments, setAssignments] = useState<Record<string, AssignmentStatus>>({});
  const [sel, setSel] = useState<Record<string, { tenant: string; site: string }>>({});
  const [err, setErr] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const load = useCallback(async () => {
    setErr(null);
    try {
      const [p, t] = await Promise.all([
        api.get<{ data: Pending[] }>("/cloud/v1/appliances-admin/pending"),
        api.get<{ data: Tenant[] }>("/v1/tenants"),
      ]);
      setPending(p.data ?? []);
      const tl = t.data ?? [];
      setTenants(tl);
      // The appliances endpoint is tenant-scoped (it 400s without one), so a
      // platform-wide view has to fan out across tenants and merge.
      const perTenant = await Promise.all(
        tl.map((tn) =>
          api.get<{ data: Appliance[] }>(`/v1/appliances?tenant_id=${tn.id}`)
            .then((r) => r.data ?? [])
            .catch(() => [] as Appliance[])
        )
      );
      const a = { data: perTenant.flat() };
      setAppliances(a.data);
      // assignment status for every known appliance
      const all = [...(p.data ?? []).map((x) => x.id), ...(a.data ?? []).map((x) => x.id)];
      const uniq = Array.from(new Set(all));
      const stats: Record<string, AssignmentStatus> = {};
      await Promise.all(
        uniq.map(async (id) => {
          try {
            stats[id] = await api.get<AssignmentStatus>(`/cloud/v1/appliances-admin/${id}/assignment`);
          } catch { /* ignore */ }
        })
      );
      setAssignments(stats);
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load");
    }
  }, []);
  // Auto-refresh: the onboarding lifecycle (pending → claimed → assigned → adopted
  // → certificate → online/licensed) advances on both Central and the appliance, so
  // poll every 5s. No manual reload needed; the Refresh button stays as a diagnostic.
  useEffect(() => {
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  }, [load]);

  async function loadSites(tenantID: string) {
    if (!tenantID || sitesByTenant[tenantID]) return;
    try {
      const r = await api.get<{ data: Site[] }>(`/v1/sites?tenant_id=${tenantID}`);
      setSitesByTenant((s) => ({ ...s, [tenantID]: r.data ?? [] }));
    } catch { /* ignore */ }
  }

  async function claim(id: string) {
    setBusy(id); setErr(null); setMsg(null);
    try {
      await withStepUp(() => api.post(`/cloud/v1/appliances-admin/${id}/claim`, {}));
      setMsg("Appliance claimed.");
      await load();
    } catch (e: any) {
      setErr(e?.message ?? "Claim failed");
    } finally { setBusy(null); }
  }

  async function assign(id: string) {
    const s = sel[id];
    if (!s?.tenant || !s?.site) { setErr("Pick a customer and a site first."); return; }
    setBusy(id); setErr(null); setMsg(null);
    try {
      const res = await withStepUp(() =>
        api.post<{ assignment_version: number }>(`/cloud/v1/appliances-admin/${id}/assign`, {
          tenant_id: s.tenant, site_id: s.site, reason: "platform onboarding",
        })
      );
      setMsg(`Assigned. Signed assignment v${res.assignment_version} issued — the appliance will adopt it automatically.`);
      await load();
    } catch (e: any) {
      setErr(e?.message ?? "Assign failed");
    } finally { setBusy(null); }
  }

  async function issueCert(id: string) {
    setBusy(id); setErr(null); setMsg(null);
    try {
      await withStepUp(() => api.post(`/cloud/v1/certificates/${id}/issue`, {}));
      setMsg("Client certificate issued — mTLS will come up on the appliance.");
      await load();
    } catch (e: any) {
      setErr(e?.message ?? "Certificate issue failed");
    } finally { setBusy(null); }
  }

  function AssignControls({ id }: { id: string }) {
    const s = sel[id] ?? { tenant: "", site: "" };
    return (
      <div className="flex flex-wrap items-center gap-2">
        <select
          className="border rounded px-2 py-1 text-sm bg-transparent"
          value={s.tenant}
          onChange={(e) => {
            const tenant = e.target.value;
            setSel((m) => ({ ...m, [id]: { tenant, site: "" } }));
            loadSites(tenant);
          }}
        >
          <option value="">Customer…</option>
          {tenants.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
        </select>
        <select
          className="border rounded px-2 py-1 text-sm bg-transparent"
          value={s.site}
          disabled={!s.tenant}
          onChange={(e) => setSel((m) => ({ ...m, [id]: { ...s, site: e.target.value } }))}
        >
          <option value="">Site…</option>
          {(sitesByTenant[s.tenant] ?? []).map((x) => <option key={x.id} value={x.id}>{x.name}</option>)}
        </select>
        <Button size="sm" disabled={busy === id} onClick={() => assign(id)}>Assign</Button>
      </div>
    );
  }

  function AssignmentCell({ id }: { id: string }) {
    const a = assignments[id];
    if (!a) return <span className="text-muted">—</span>;
    if (!a.issued) return <span className="text-muted">not issued</span>;
    return (
      <span className="font-mono text-xs">
        v{a.version} · {a.state}
      </span>
    );
  }

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Infrastructure</div>
          <h1 className="text-2xl font-semibold">Appliance onboarding</h1>
        </div>
        <Button variant="ghost" onClick={load}><RefreshCw size={14} /> Refresh</Button>
      </div>

      <p className="text-sm text-muted mb-4">
        Enroll the appliance with a site-scoped token (Appliances → mint token), then{" "}
        <strong>Claim</strong> it, <strong>Assign</strong> it to a customer + site, and{" "}
        <strong>Issue</strong> its certificate. Assigning mints a vendor-signed assignment
        the appliance verifies and adopts on its own — no configuration is ever edited on the box.
      </p>

      {err && <div className="text-err text-sm mb-3">{err}</div>}
      {msg && <div className="text-sm mb-3">{msg}</div>}

      <Card className="mb-6">
        <CardHeader><CardTitle>Pending appliances (awaiting claim / assignment)</CardTitle></CardHeader>
        <CardBody className="p-0">
          {pending === null ? (
            <EmptyState title="Loading…" />
          ) : pending.length === 0 ? (
            <EmptyState title="No pending appliances" hint="Enroll one with a bootstrap token to see it here." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Serial</TH><TH>State</TH><TH>Identity key</TH><TH>Source IP</TH>
                  <TH>Assignment</TH><TH>First seen</TH><TH></TH>
                </TR>
              </THead>
              <tbody>
                {pending.map((p) => (
                  <TR key={p.id}>
                    <TD className="font-mono">{p.serial}</TD>
                    <TD className="text-muted">{p.state}</TD>
                    <TD className="font-mono text-xs">{p.public_key_fingerprint}</TD>
                    <TD className="text-muted">{p.source_ip || "—"}</TD>
                    <TD><AssignmentCell id={p.id} /></TD>
                    <TD className="text-muted">{formatRelative(p.first_seen)}</TD>
                    <TD className="text-right">
                      <div className="flex justify-end gap-2">
                        <Button size="sm" variant="ghost" disabled={busy === p.id} onClick={() => claim(p.id)}>Claim</Button>
                        <AssignControls id={p.id} />
                      </div>
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      <Card>
        <CardHeader><CardTitle>Registered appliances</CardTitle></CardHeader>
        <CardBody className="p-0">
          {appliances.length === 0 ? (
            <EmptyState title="No registered appliances" />
          ) : (
            <Table>
              <THead>
                <TR><TH>Serial</TH><TH>Lifecycle</TH><TH>Assignment</TH><TH>Reassign</TH><TH></TH></TR>
              </THead>
              <tbody>
                {appliances.map((a) => (
                  <TR key={a.id}>
                    <TD className="font-mono">{a.serial}</TD>
                    <TD className="text-muted">{a.lifecycle_state ?? "—"}</TD>
                    <TD><AssignmentCell id={a.id} /></TD>
                    <TD><AssignControls id={a.id} /></TD>
                    <TD className="text-right">
                      <Button size="sm" variant="ghost" disabled={busy === a.id} onClick={() => issueCert(a.id)}>
                        Issue certificate
                      </Button>
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
