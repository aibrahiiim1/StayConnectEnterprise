"use client";

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { useParams } from "next/navigation";
import { api, ListResp, Voucher, VoucherBatch, EDGE_BASE } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { ArrowLeft, Download, Printer, Copy, Check } from "lucide-react";
import { formatRelative } from "@/lib/utils";

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

export default function VoucherBatchDetail() {
  const { id } = useParams<{ id: string }>();
  const [batch, setBatch] = useState<VoucherBatch | null>(null);
  const [vouchers, setVouchers] = useState<Voucher[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [q, setQ] = useState("");
  const [stateFilter, setStateFilter] = useState("");
  const [copied, setCopied] = useState<string | null>(null);

  async function load() {
    if (!id) return;
    try {
      const [b, vs] = await Promise.all([
        api.get<VoucherBatch>(`/voucher-batches/${id}`),
        api.get<ListResp<Voucher>>(`/voucher-batches/${id}/codes?limit=5000`),
      ]);
      setBatch(b);
      setVouchers(vs.data);
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load");
    }
  }
  useEffect(() => { load(); }, [id]);

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

  async function onRevoke(v: Voucher) {
    if (v.state !== "unused") return;
    if (!confirm(`Revoke voucher ${v.code_display}?`)) return;
    try { await api.post(`/vouchers/${v.id}/revoke`); load(); }
    catch (e: any) { setErr(e?.message ?? "Revoke failed"); }
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

  return (
    <div className="p-6 max-w-7xl mx-auto">
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
              {t && <> · <span className="text-text">{t.unused}</span> unused · {t.active} active · {t.exhausted + t.expired} used · {t.revoked} revoked</>}
            </div>
          )}
        </div>
        <div className="flex gap-2">
          <Button variant="secondary" onClick={printList}><Printer size={14} /> Print</Button>
          <a href={`${EDGE_BASE}/voucher-batches/${id}/codes.csv`} target="_blank" rel="noopener"
            className="inline-flex items-center gap-2 h-9 px-4 text-sm rounded-md bg-panel2 border border-border hover:bg-[#222735]">
            <Download size={14} /> CSV
          </a>
        </div>
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      <div className="flex gap-2 mb-3">
        <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search codes…" className="max-w-xs" />
        <select value={stateFilter} onChange={(e) => setStateFilter(e.target.value)}
          className="h-9 rounded-md bg-panel2 border border-border px-3 text-sm">
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
                    <TD className="font-mono">{v.code_display}</TD>
                    <TD><Badge tone={toneFor(v.state) as any}>{v.state}</Badge></TD>
                    <TD className="text-muted">{formatRelative(v.issued_at)}</TD>
                    <TD className="text-muted">{v.activated_at ? formatRelative(v.activated_at) : "—"}</TD>
                    <TD className="text-right whitespace-nowrap">
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
