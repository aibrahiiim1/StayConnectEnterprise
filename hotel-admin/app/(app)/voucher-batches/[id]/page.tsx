"use client";

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { api, ApiError, GuestAccessPlan, ListResp, Voucher, VoucherBatch, EDGE_BASE } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { ArrowLeft, Download, Printer, Copy, Check } from "lucide-react";
import { formatRelative, formatBytes } from "@/lib/utils";

const selectCls = "h-9 rounded-md bg-panel2 border border-border px-3 text-sm";

function toneFor(state: string): "info" | "default" | "err" | "warn" {
  switch (state) {
    case "active": return "info";
    case "unused": return "default";
    case "revoked": return "err";
    case "expired":
    case "exhausted": return "warn";
    default: return "default";
  }
}

function fmtDuration(sec?: number | null): string {
  if (!sec) return "no time limit";
  if (sec % 86400 === 0) return `${sec / 86400}d`;
  if (sec % 3600 === 0) return `${sec / 3600}h`;
  return `${Math.round(sec / 60)}m`;
}

export default function VoucherBatchDetail() {
  const { id } = useParams<{ id: string }>();
  const [batch, setBatch] = useState<VoucherBatch | null>(null);
  const [vouchers, setVouchers] = useState<Voucher[] | null>(null);
  const [plans, setPlans] = useState<GuestAccessPlan[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [q, setQ] = useState("");
  const [stateFilter, setStateFilter] = useState("");
  const [copied, setCopied] = useState<string | null>(null);
  const [detail, setDetail] = useState<Voucher | null>(null);
  const [showBatchPlan, setShowBatchPlan] = useState(false);

  async function load() {
    if (!id) return;
    try {
      const [b, vs, pl] = await Promise.all([
        api.get<VoucherBatch>(`/voucher-batches/${id}`),
        api.get<ListResp<Voucher>>(`/voucher-batches/${id}/codes?limit=5000`),
        api.get<ListResp<GuestAccessPlan>>("/guest-access-plans"),
      ]);
      setBatch(b);
      setVouchers(vs.data);
      setPlans(pl.data);
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load");
    }
  }
  useEffect(() => { load(); }, [id]);

  const activePlans = useMemo(() => plans.filter((p) => p.is_active), [plans]);
  const planById = useMemo(() => new Map(plans.map((p) => [p.id, p])), [plans]);

  const filtered = useMemo(() => {
    if (!vouchers) return [];
    const needle = q.trim().toUpperCase();
    return vouchers.filter((v) =>
      (!stateFilter || v.state === stateFilter) &&
      (!needle || v.code.includes(needle) || v.code_display.toUpperCase().includes(needle)));
  }, [vouchers, q, stateFilter]);

  async function copy(code: string) {
    try { await navigator.clipboard.writeText(code); setCopied(code); setTimeout(() => setCopied(null), 1200); } catch {}
  }

  async function openDetail(v: Voucher) {
    setErr(null); setMsg(null);
    try { setDetail(await api.get<Voucher>(`/vouchers/${v.id}`)); }
    catch (e: any) { setErr(e?.message ?? "Failed to load voucher"); }
  }

  async function onRevoke(v: Voucher) {
    if (v.state !== "unused") return;
    if (!confirm(`Revoke voucher ${v.code_display}?`)) return;
    try { await api.post(`/vouchers/${v.id}/revoke`); load(); }
    catch (e: any) { setErr(e?.message ?? "Revoke failed"); }
  }

  async function onChangeVoucherPlan(v: Voucher, templateId: string, reason: string) {
    setErr(null); setMsg(null);
    try {
      await api.post(`/vouchers/${v.id}/change-plan`, { template_id: templateId, reason });
      setMsg(`Plan changed for ${v.code_display}.`);
      setDetail(await api.get<Voucher>(`/vouchers/${v.id}`));
      load();
    } catch (e: any) {
      if (e instanceof ApiError && e.code === "active_sessions") setErr("This voucher has an active guest session — disconnect it before changing the plan.");
      else setErr(e instanceof ApiError ? (e.body?.message ?? e.message) : (e?.message ?? "Change failed"));
    }
  }

  async function onBatchChangePlan(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setErr(null); setMsg(null);
    const form = new FormData(e.currentTarget);
    try {
      const r = await api.post<{ vouchers_changed: number }>(`/voucher-batches/${id}/change-plan`, {
        template_id: form.get("template_id"), scope: form.get("scope"), reason: (form.get("reason") as string) || undefined,
      });
      setShowBatchPlan(false);
      setMsg(`${r.vouchers_changed} voucher(s) moved to the new plan.`);
      load();
    } catch (e: any) { setErr(e instanceof ApiError ? (e.body?.message ?? e.message) : (e?.message ?? "Change failed")); }
  }

  function printList() {
    const rows = filtered.map((v) => `<tr><td>${v.code_display}</td><td>${v.state}</td></tr>`).join("");
    const w = window.open("", "_blank");
    if (!w) return;
    w.document.write(`<html><head><title>Vouchers — ${batch?.name ?? ""}</title>
      <style>body{font-family:system-ui;padding:24px}h1{font-size:18px}table{border-collapse:collapse;width:100%}
      td,th{border:1px solid #ccc;padding:6px 10px;text-align:left;font-family:monospace}</style></head>
      <body><h1>Vouchers — ${batch?.name ?? ""} (${filtered.length})</h1>
      <table><thead><tr><th>Code</th><th>State</th></tr></thead><tbody>${rows}</tbody></table></body></html>`);
    w.document.close();
    w.print();
  }

  const t = batch?.totals;
  const batchPlan = batch ? planById.get(batch.template_id) : undefined;

  return (
    <div className="p-6 max-w-[92rem] mx-auto">
      <Link href="/voucher-batches" className="text-sm text-muted hover:text-text inline-flex items-center gap-1 mb-4">
        <ArrowLeft size={14} /> Back to batches
      </Link>

      <div className="flex items-baseline justify-between mb-3">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Voucher batch</div>
          <h1 className="text-2xl font-semibold">{batch?.name || "—"}</h1>
          {batch && (
            <div className="text-xs text-muted mt-1">
              {batch.count} codes · {batch.char_mode ? `${batch.char_mode} · len ${batch.code_prefix ? batch.code_prefix + "+" : ""}${batch.code_length}` : "legacy 12-char"}
              {batchPlan && <> · plan <span className="text-text">{batchPlan.name}</span> ({batchPlan.max_concurrent_devices} max devices)</>}
              {t && <> · <span className="text-text">{t.unused}</span> unused · {t.active} active · {t.exhausted + t.expired} used · {t.revoked} revoked</>}
            </div>
          )}
        </div>
        <div className="flex gap-2">
          <Button variant="secondary" onClick={() => setShowBatchPlan((s) => !s)} disabled={activePlans.length === 0}>Change plan…</Button>
          <Button variant="secondary" onClick={printList}><Printer size={14} /> Print</Button>
          <a href={`${EDGE_BASE}/voucher-batches/${id}/codes.csv`} target="_blank" rel="noopener"
            className="inline-flex items-center gap-2 h-9 px-4 text-sm rounded-md bg-panel2 border border-border hover:bg-[#222735]">
            <Download size={14} /> CSV
          </a>
        </div>
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}
      {msg && <div className="text-ok text-sm mb-4">{msg}</div>}

      {showBatchPlan && (
        <Card className="mb-4 border-brand">
          <CardHeader><CardTitle>Change plan for this batch</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onBatchChangePlan} className="grid grid-cols-1 sm:grid-cols-4 gap-3 items-end">
              <div className="sm:col-span-2">
                <Label>New guest access plan</Label>
                <select name="template_id" required className={selectCls + " w-full"}>
                  {activePlans.map((p) => <option key={p.id} value={p.id}>{p.name} ({p.code}) — {p.max_concurrent_devices} dev</option>)}
                </select>
              </div>
              <div>
                <Label>Apply to</Label>
                <select name="scope" defaultValue="unused" className={selectCls + " w-full"}>
                  <option value="unused">Unused only</option>
                  <option value="eligible">All eligible (no active session)</option>
                </select>
              </div>
              <div><Label>Reason</Label><Input name="reason" placeholder="optional" /></div>
              <div className="sm:col-span-4 text-xs text-muted">
                Revoked/expired/exhausted vouchers and any voucher with an active session are never changed. Codes and
                usage history stay the same.
              </div>
              <div className="sm:col-span-4 flex justify-end gap-2">
                <Button type="button" variant="ghost" onClick={() => setShowBatchPlan(false)}>Cancel</Button>
                <Button type="submit">Apply</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      {detail && (
        <Card className="mb-4 border-brand">
          <CardHeader><CardTitle>Voucher {detail.code_display}</CardTitle></CardHeader>
          <CardBody>
            <div className="grid grid-cols-2 sm:grid-cols-4 gap-3 text-sm mb-3">
              <div><div className="text-xs text-muted">State</div><Badge tone={toneFor(detail.state) as any}>{detail.state}</Badge></div>
              <div><div className="text-xs text-muted">Plan</div>{detail.plan_name} ({detail.plan_code})</div>
              <div><div className="text-xs text-muted">Duration</div>{fmtDuration(detail.duration_seconds)}</div>
              <div><div className="text-xs text-muted">Data cap</div>{detail.data_cap_bytes ? formatBytes(detail.data_cap_bytes) : "unlimited"}</div>
              <div><div className="text-xs text-muted">Speed</div>{detail.down_kbps ? `${Math.round(detail.down_kbps / 1000)}↓/${Math.round((detail.up_kbps ?? 0) / 1000)}↑ Mbps` : "unshaped"}</div>
              <div><div className="text-xs text-muted">Max devices</div>{detail.max_devices ?? "—"}</div>
              <div><div className="text-xs text-muted">Active devices</div>{detail.active_devices ?? 0}{detail.max_devices ? ` of ${detail.max_devices}` : ""}</div>
              <div><div className="text-xs text-muted">Issued</div>{formatRelative(detail.issued_at)}</div>
              <div><div className="text-xs text-muted">Activated</div>{detail.activated_at ? formatRelative(detail.activated_at) : "—"}</div>
              <div><div className="text-xs text-muted">Expires</div>{detail.expires_at ? formatRelative(detail.expires_at) : "—"}</div>
            </div>
            {["revoked", "expired", "exhausted"].includes(detail.state) ? (
              <div className="text-xs text-muted">This voucher is {detail.state}; its plan can no longer be changed.</div>
            ) : (
              <ChangePlanInline plans={activePlans} current={detail.template_id} onApply={(tid, reason) => onChangeVoucherPlan(detail, tid, reason)} />
            )}
            <div className="mt-3 flex justify-end"><Button size="sm" variant="ghost" onClick={() => setDetail(null)}>Close</Button></div>
          </CardBody>
        </Card>
      )}

      <div className="flex gap-2 mb-3">
        <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search codes…" className="max-w-xs" />
        <select value={stateFilter} onChange={(e) => setStateFilter(e.target.value)} className={selectCls}>
          <option value="">All states</option>
          {["unused", "active", "exhausted", "expired", "revoked"].map((s) => <option key={s} value={s}>{s}</option>)}
        </select>
      </div>

      <Card>
        <CardBody className="p-0">
          {vouchers === null ? (
            <div className="p-6 text-muted text-sm">Loading…</div>
          ) : (
            <Table>
              <THead>
                <TR><TH>Code</TH><TH>State</TH><TH>Issued</TH><TH>Activated</TH><TH></TH></TR>
              </THead>
              <tbody>
                {filtered.map((v) => (
                  <TR key={v.id}>
                    <TD className="font-mono">
                      <button className="hover:text-brand" onClick={() => openDetail(v)}>{v.code_display}</button>
                    </TD>
                    <TD><Badge tone={toneFor(v.state) as any}>{v.state}</Badge></TD>
                    <TD className="text-muted">{formatRelative(v.issued_at)}</TD>
                    <TD className="text-muted">{v.activated_at ? formatRelative(v.activated_at) : "—"}</TD>
                    <TD className="text-right whitespace-nowrap">
                      <Button size="sm" variant="ghost" onClick={() => openDetail(v)}>Details</Button>
                      <Button size="sm" variant="ghost" onClick={() => copy(v.code_display)}>
                        {copied === v.code_display ? <><Check size={13} /> Copied</> : <><Copy size={13} /> Copy</>}
                      </Button>
                      {v.state === "unused" && <Button size="sm" variant="ghost" onClick={() => onRevoke(v)}>Revoke</Button>}
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
          {vouchers && filtered.length === 0 && <div className="p-6 text-muted text-sm">No codes match.</div>}
        </CardBody>
      </Card>
    </div>
  );
}

function ChangePlanInline({ plans, current, onApply }: { plans: GuestAccessPlan[]; current: string; onApply: (templateId: string, reason: string) => void }) {
  const [tid, setTid] = useState(current);
  const [reason, setReason] = useState("");
  return (
    <div className="flex flex-wrap items-end gap-2">
      <div>
        <Label>Change plan</Label>
        <select value={tid} onChange={(e) => setTid(e.target.value)} className={selectCls}>
          {plans.map((p) => <option key={p.id} value={p.id}>{p.name} ({p.code}) — {p.max_concurrent_devices} dev</option>)}
        </select>
      </div>
      <Input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="reason (optional)" className="max-w-[16rem]" />
      <Button size="sm" disabled={tid === current} onClick={() => onApply(tid, reason)}>Apply plan</Button>
    </div>
  );
}
