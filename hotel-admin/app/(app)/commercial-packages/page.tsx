"use client";

// Phase 2 (DARK) Hotel-Admin commercial-packages management. Linked from the nav only when
// NEXT_PUBLIC_PHASE2_ADMIN=1; the edged routes are the authority and return 503 while the backend admin
// flag is OFF, in which case every tab renders a clear "not enabled" state. Tabs: Packages (list +
// immutable revision history + publish with a plan-revision selector, typed eligibility rule + ordered
// grant tier editors, sale window, duration policy, activate/deactivate-with-step-up), Service Plans
// (list + revision history + new immutable revision), Grace (validated site checkout-grace config) and
// Inspection (read-only, PII-free quotes + purchases).

import { Fragment, useCallback, useEffect, useState } from "react";
import { api, ApiError, ListResp } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X } from "lucide-react";
import { PublishPackageForm } from "./publish-form";
import type { PublishPayload } from "@/lib/commerce-form";

type PackageSummary = { package_id: string; code: string; active: boolean; current_revision_id: string; revision_count: number };
type PlanSummary = { plan_id: string; code: string; enabled: boolean; current_revision_id: string; revision_count: number };
type RevisionInfo = { revision_id: string; revision_no: number; is_current: boolean; label?: string; price_minor?: number; currency?: string; package_type?: string };
type GraceConfig = { grace_package_revision_id: string; config: Record<string, unknown> };
type QuoteInspect = { id: string; package_revision_id: string; price_minor: number; currency: string; expires_at: string; consumed_at: string | null };
type PurchaseInspect = { id: string; package_revision_id: string; state: string; amount_minor: number; currency: string };

type Tab = "packages" | "plans" | "grace" | "inspection";

function useDisabled() {
  const [disabled, setDisabled] = useState(false);
  const guard = useCallback((e: unknown): boolean => {
    if (e instanceof ApiError && e.status === 503) { setDisabled(true); return true; }
    return false;
  }, []);
  return { disabled, setDisabled, guard };
}

export default function CommercialPackagesPage() {
  const [tab, setTab] = useState<Tab>("packages");
  const [err, setErr] = useState<string | null>(null);
  const { disabled, guard } = useDisabled();

  const tabs: { id: Tab; label: string }[] = [
    { id: "packages", label: "Packages" },
    { id: "plans", label: "Service plans" },
    { id: "grace", label: "Checkout grace" },
    { id: "inspection", label: "Inspection" },
  ];

  return (
    <div className="space-y-4">
      <h1 className="text-lg font-semibold">Commercial packages</h1>
      {disabled ? (
        <Card><CardBody>
          <EmptyState title="Commercial packages are not enabled"
            hint="This is a Phase 2 (dark) feature, disabled on this appliance until an operator turns on the Phase-2 admin flag during a controlled cutover." />
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
          {tab === "plans" && <PlansTab guard={guard} setErr={setErr} />}
          {tab === "grace" && <GraceTab guard={guard} setErr={setErr} />}
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
  const [revs, setRevs] = useState<Record<string, RevisionInfo[]>>({});
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const [pk, pl] = await Promise.all([
        api.get<ListResp<PackageSummary>>("/commercial-packages"),
        api.get<ListResp<PlanSummary>>("/commercial-packages/plans"),
      ]);
      setRows(pk.data ?? []); setPlans(pl.data ?? []);
    } catch (e) { if (!guard(e)) setErr((e as Error)?.message ?? "Failed to load"); }
  }, [guard, setErr]);
  useEffect(() => { load(); }, [load]);

  async function toggleRevs(id: string) {
    if (revs[id]) { setRevs((r) => { const n = { ...r }; delete n[id]; return n; }); return; }
    try {
      const r = await api.get<ListResp<RevisionInfo>>(`/commercial-packages/${id}/revisions`);
      setRevs((s) => ({ ...s, [id]: r.data ?? [] }));
    } catch (e) { if (!guard(e)) setErr((e as Error)?.message ?? "Failed"); }
  }

  async function handlePublish(payload: PublishPayload) {
    setBusy(true); setErr(null);
    try {
      await api.post("/commercial-packages", payload);
      setShowNew(false); await load();
    } catch (e) { if (!guard(e)) setErr((e as Error)?.message ?? "Publish failed"); }
    finally { setBusy(false); }
  }

  async function toggleActive(p: PackageSummary) {
    setBusy(true); setErr(null);
    try {
      if (p.active) {
        const reason = window.prompt("Reason for deactivating this package?");
        if (!reason) { setBusy(false); return; }
        const password = window.prompt("Confirm your password to deactivate");
        if (!password) { setBusy(false); return; }
        await api.post(`/commercial-packages/${p.package_id}/active`, { active: false, reason, password });
      } else {
        await api.post(`/commercial-packages/${p.package_id}/active`, { active: true });
      }
      await load();
    } catch (e) { if (!guard(e)) setErr((e as Error)?.message ?? "Update failed"); }
    finally { setBusy(false); }
  }

  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <Button onClick={() => setShowNew((v) => !v)}>{showNew ? <X size={16} /> : <Plus size={16} />}{showNew ? "Cancel" : "Publish package"}</Button>
      </div>
      {showNew && (
        <Card>
          <CardHeader><CardTitle>Publish a free package revision</CardTitle></CardHeader>
          <CardBody>
            <PublishPackageForm plans={plans} busy={busy} onPublish={handlePublish} />
          </CardBody>
        </Card>
      )}
      <Card><CardBody>
        {rows && rows.length === 0 ? (
          <EmptyState title="No packages" hint="Publish a package to create its first immutable revision." />
        ) : (
          <Table>
            <THead><TR><TH>Code</TH><TH>Status</TH><TH>Revisions</TH><TH>Current revision</TH><TH></TH></TR></THead>
            <tbody>
              {(rows ?? []).map((p) => (
                <Fragment key={p.package_id}>
                  <TR key={p.package_id}>
                    <TD className="font-medium">{p.code}</TD>
                    <TD>{p.active ? <Badge tone="ok">Active</Badge> : <Badge tone="default">Inactive</Badge>}</TD>
                    <TD><button className="underline text-muted" onClick={() => toggleRevs(p.package_id)}>{p.revision_count} ▾</button></TD>
                    <TD className="font-mono text-xs text-muted">{p.current_revision_id || "—"}</TD>
                    <TD><Button variant="ghost" disabled={busy} onClick={() => toggleActive(p)}>{p.active ? "Deactivate" : "Activate"}</Button></TD>
                  </TR>
                  {revs[p.package_id] && (
                    <TR key={p.package_id + "-revs"}>
                      <TD className="text-xs text-muted" >History</TD>
                      <TD className="text-xs text-muted" ></TD>
                      <TD className="text-xs" >
                        {revs[p.package_id].map((r) => (
                          <div key={r.revision_id}>#{r.revision_no} {r.is_current ? <Badge tone="info">current · immutable</Badge> : <span className="text-muted">immutable</span>} {r.package_type} {r.price_minor === 0 ? "free" : `${r.price_minor} ${r.currency}`}</div>
                        ))}
                      </TD>
                      <TD></TD><TD></TD>
                    </TR>
                  )}
                </Fragment>
              ))}
            </tbody>
          </Table>
        )}
      </CardBody></Card>
    </div>
  );
}

function PlansTab({ guard, setErr }: TabProps) {
  const [rows, setRows] = useState<PlanSummary[] | null>(null);
  const [revs, setRevs] = useState<Record<string, RevisionInfo[]>>({});
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try { const r = await api.get<ListResp<PlanSummary>>("/commercial-packages/plans"); setRows(r.data ?? []); }
    catch (e) { if (!guard(e)) setErr((e as Error)?.message ?? "Failed to load"); }
  }, [guard, setErr]);
  useEffect(() => { load(); }, [load]);

  async function toggleRevs(id: string) {
    if (revs[id]) { setRevs((r) => { const n = { ...r }; delete n[id]; return n; }); return; }
    try { const r = await api.get<ListResp<RevisionInfo>>(`/commercial-packages/plans/${id}/revisions`); setRevs((s) => ({ ...s, [id]: r.data ?? [] })); }
    catch (e) { if (!guard(e)) setErr((e as Error)?.message ?? "Failed"); }
  }

  async function onPublish(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault(); setBusy(true); setErr(null);
    const el = e.currentTarget; const f = new FormData(el);
    const s = (k: string) => ((f.get(k) as string) || "").trim();
    const n = (k: string) => { const v = s(k); return v === "" ? undefined : Number(v); };
    try {
      await api.post("/commercial-packages/plans", {
        code: s("code"), name: s("name"),
        down_kbps: n("down_kbps"), up_kbps: n("up_kbps"),
        max_concurrent_devices: n("max_concurrent_devices") ?? 1,
        device_limit_policy: s("device_limit_policy") || "REJECT_NEW_DEVICE",
        idle_timeout_seconds: n("idle_timeout_seconds"),
        max_continuous_session_seconds: n("max_continuous_session_seconds"),
        time_quota_seconds: n("time_quota_seconds"),
        data_quota_bytes: n("data_quota_bytes"),
        time_accounting_mode: "VALIDITY_WINDOW",
      });
      el.reset(); setShowNew(false); await load();
    } catch (e) { if (!guard(e)) setErr((e as Error)?.message ?? "Publish failed"); }
    finally { setBusy(false); }
  }

  return (
    <div className="space-y-3">
      <div className="flex justify-end"><Button onClick={() => setShowNew((v) => !v)}>{showNew ? <X size={16} /> : <Plus size={16} />}{showNew ? "Cancel" : "New plan revision"}</Button></div>
      {showNew && (
        <Card>
          <CardHeader><CardTitle>Publish a service-plan revision</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onPublish} className="grid grid-cols-2 gap-3">
              <div><Label>Code</Label><Input name="code" required placeholder="GOLD" /></div>
              <div><Label>Name</Label><Input name="name" placeholder="Gold" /></div>
              <div><Label>Down kbps</Label><Input name="down_kbps" type="number" min={0} /></div>
              <div><Label>Up kbps</Label><Input name="up_kbps" type="number" min={0} /></div>
              <div><Label>Max concurrent devices</Label><Input name="max_concurrent_devices" type="number" min={1} defaultValue={1} /></div>
              <div>
                <Label>Device limit policy</Label>
                <select name="device_limit_policy" className="w-full bg-panel2 border border-border rounded-md px-2 py-2 text-sm">
                  <option value="REJECT_NEW_DEVICE">Reject new device</option>
                  <option value="DISCONNECT_OLDEST">Disconnect oldest</option>
                  <option value="ADMIN_APPROVAL">Admin approval</option>
                </select>
              </div>
              <div><Label>Idle timeout seconds</Label><Input name="idle_timeout_seconds" type="number" min={0} /></div>
              <div><Label>Max session seconds</Label><Input name="max_continuous_session_seconds" type="number" min={0} /></div>
              <div><Label>Time quota seconds</Label><Input name="time_quota_seconds" type="number" min={0} /></div>
              <div><Label>Data quota bytes</Label><Input name="data_quota_bytes" type="number" min={0} /></div>
              <div className="col-span-2"><Button type="submit" disabled={busy}>{busy ? "Publishing…" : "Publish revision"}</Button></div>
            </form>
          </CardBody>
        </Card>
      )}
      <Card><CardBody>
        {rows && rows.length === 0 ? (
          <EmptyState title="No service plans" hint="Publish a plan revision to define grant parameters." />
        ) : (
          <Table>
            <THead><TR><TH>Code</TH><TH>Revisions</TH><TH>Current revision</TH></TR></THead>
            <tbody>
              {(rows ?? []).map((p) => (
                <Fragment key={p.plan_id}>
                  <TR key={p.plan_id}>
                    <TD className="font-medium">{p.code}</TD>
                    <TD><button className="underline text-muted" onClick={() => toggleRevs(p.plan_id)}>{p.revision_count} ▾</button></TD>
                    <TD className="font-mono text-xs text-muted">{p.current_revision_id || "—"}</TD>
                  </TR>
                  {revs[p.plan_id] && (
                    <TR key={p.plan_id + "-revs"}>
                      <TD className="text-xs text-muted">History</TD>
                      <TD className="text-xs" colSpan={2}>
                        {revs[p.plan_id].map((r) => (
                          <div key={r.revision_id}>#{r.revision_no} {r.is_current ? <Badge tone="info">current · immutable</Badge> : <span className="text-muted">immutable</span>} {r.label}</div>
                        ))}
                      </TD>
                    </TR>
                  )}
                </Fragment>
              ))}
            </tbody>
          </Table>
        )}
      </CardBody></Card>
    </div>
  );
}

function GraceTab({ guard, setErr }: TabProps) {
  const [gc, setGc] = useState<GraceConfig | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try { const r = await api.get<GraceConfig>("/commercial-packages/grace"); setGc(r); }
    catch (e) { if (!guard(e)) setErr((e as Error)?.message ?? "Failed to load"); }
  }, [guard, setErr]);
  useEffect(() => { load(); }, [load]);

  async function onSet(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault(); setBusy(true); setErr(null);
    const el = e.currentTarget; const f = new FormData(el);
    const rev = ((f.get("grace_package_revision_id") as string) || "").trim();
    const minutes = Number((f.get("grace_minutes") as string) || "");
    try {
      await api.put("/commercial-packages/grace", { grace_package_revision_id: rev, config: minutes ? { grace_minutes: minutes } : {} });
      await load();
    } catch (e) { if (!guard(e)) setErr((e as Error)?.message ?? "Update failed (the package revision must be an active, free, CHECKOUT_GRACE revision)"); }
    finally { setBusy(false); }
  }

  return (
    <Card>
      <CardHeader><CardTitle>Checkout grace configuration</CardTitle></CardHeader>
      <CardBody className="space-y-3">
        <div className="text-sm text-muted">
          Current grace package revision: <span className="font-mono">{gc?.grace_package_revision_id || "— (none)"}</span>.
          The selected revision must be an active, free, <code>CHECKOUT_GRACE</code> package pinned to a valid plan revision.
          This records configuration only; no grace entitlement or checkout behavior is created (Phase 3).
        </div>
        <form onSubmit={onSet} className="grid grid-cols-2 gap-3">
          <div className="col-span-2"><Label>Grace package revision ID</Label><Input name="grace_package_revision_id" required placeholder="uuid of a CHECKOUT_GRACE revision" /></div>
          <div><Label>Grace minutes (optional)</Label><Input name="grace_minutes" type="number" min={0} /></div>
          <div className="col-span-2"><Button type="submit" disabled={busy}>{busy ? "Saving…" : "Save grace config"}</Button></div>
        </form>
      </CardBody>
    </Card>
  );
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
        <CardHeader><CardTitle>Offer quotes (read-only)</CardTitle></CardHeader>
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
        <CardHeader><CardTitle>Purchases (read-only)</CardTitle></CardHeader>
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
