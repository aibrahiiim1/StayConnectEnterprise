"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { api, ApiError, GuestAccessPlan, ListResp, VoucherBatch, EDGE_BASE } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X, Download } from "lucide-react";
import { formatRelative } from "@/lib/utils";

// Character modes. Labels describe the exact set; `charset` is shown so the
// operator sees precisely which characters a mode can produce.
const CHAR_MODES = [
  { v: "numbers", label: "Numbers", charset: "0–9 (ambiguous 0/1/5 removed when excluded)" },
  { v: "letters", label: "Uppercase letters", charset: "A–Z (minus I/L/O/U)" },
  { v: "alnum", label: "Uppercase letters and numbers", charset: "A–Z + 0–9 (minus I/L/O/U and ambiguous)" },
  { v: "complex", label: "Uppercase/lowercase letters and numbers", charset: "a–z + A–Z + 0–9 (minus I/L/O/U and ambiguous)" },
];

// exampleCode builds a representative sample so the operator can preview the
// shape before generating. Digits/letters shown are illustrative only.
function exampleCode(mode: string, len: number, prefix: string): string {
  const pools: Record<string, string> = {
    numbers: "234679",
    letters: "ABCDEFGHJKMNPQR",
    alnum: "A2B4C6D8EFGHJK",
    complex: "a2B4c6D8efGhJk",
  };
  const pool = pools[mode] ?? pools.alnum;
  let s = "";
  for (let i = 0; i < Math.max(4, len); i++) s += pool[i % pool.length];
  return (prefix ? prefix.toUpperCase() + "-" : "") + s;
}

function fmtGeneration(b: VoucherBatch): string {
  if (!b.char_mode) return "legacy (12-char)";
  const mode = CHAR_MODES.find((m) => m.v === b.char_mode)?.label ?? b.char_mode;
  const len = b.code_prefix ? `${b.code_prefix}+${b.code_length}` : `${b.code_length}`;
  return `${mode} · len ${len}${b.exclude_ambiguous === false ? "" : " · no ambiguous"}`;
}

export default function VoucherBatchesPage() {
  const [batches, setBatches] = useState<VoucherBatch[] | null>(null);
  const [plans, setPlans] = useState<GuestAccessPlan[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);
  // Live-preview state for the generation options.
  const [mode, setMode] = useState("alnum");
  const [len, setLen] = useState(8);
  const [prefix, setPrefix] = useState("");

  async function load() {
    try {
      const [vb, pl] = await Promise.all([
        api.get<ListResp<VoucherBatch>>("/voucher-batches"),
        api.get<ListResp<GuestAccessPlan>>("/guest-access-plans"),
      ]);
      setBatches(vb.data);
      setPlans(pl.data.filter((t) => t.is_active));
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load");
    }
  }
  useEffect(() => { load(); }, []);

  async function onCreate(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true); setErr(null);
    const el = e.currentTarget;
    const form = new FormData(el);
    try {
      await api.post("/voucher-batches", {
        template_id: form.get("template_id"),
        count: Number(form.get("count")),
        name: (form.get("name") as string) || undefined,
        code_length: Number(form.get("code_length")),
        char_mode: form.get("char_mode"),
        code_prefix: (form.get("code_prefix") as string).trim().toUpperCase() || undefined,
        exclude_ambiguous: form.get("exclude_ambiguous") === "on",
      });
      setShowNew(false);
      (e.target as HTMLFormElement).reset();
      load();
    } catch (e: any) {
      if (e instanceof ApiError && e.body?.error === "limit_exceeded") {
        setErr(`License limit reached: ${e.body.limit_key} (${e.body.current}/${e.body.limit})`);
      } else if (e instanceof ApiError && e.body?.error === "license_restricted") {
        setErr("This appliance's license doesn't currently allow new voucher batches (expired, suspended, revoked or not activated) — renew or activate the license to re-enable.");
      } else if (e instanceof ApiError) {
        setErr(e.body?.message ?? e.message);
      } else {
        setErr(e?.message ?? "Create failed");
      }
    } finally {
      setBusy(false);
    }
  }

  async function onRevoke(id: string) {
    if (!confirm("Revoke all unused vouchers in this batch?")) return;
    try {
      await api.post(`/voucher-batches/${id}/revoke`);
      load();
    } catch (e: any) { setErr(e?.message ?? "Revoke failed"); }
  }

  return (
    <div className="p-6 max-w-[92rem] mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Access</div>
          <h1 className="text-2xl font-semibold">Voucher batches</h1>
        </div>
        <Button onClick={() => setShowNew((s) => !s)} disabled={plans.length === 0}>
          {showNew ? <><X size={14} /> Cancel</> : <><Plus size={14} /> New batch</>}
        </Button>
      </div>

      {plans.length === 0 && (
        <div className="text-sm text-warn mb-4">
          No active guest access plans — create one before generating vouchers.
        </div>
      )}
      {err && <div className="text-err text-sm mb-4">{err}</div>}

      {showNew && (
        <Card className="mb-6">
          <CardHeader><CardTitle>New batch</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onCreate} className="grid grid-cols-1 sm:grid-cols-3 gap-3">
              <div>
                <Label>Guest access plan</Label>
                <select name="template_id" required className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm">
                  {plans.map((t) => <option key={t.id} value={t.id}>{t.name} — {t.code}</option>)}
                </select>
              </div>
              <div><Label>Count</Label><Input name="count" type="number" min={1} max={10000} required defaultValue={50} /></div>
              <div><Label>Label (optional)</Label><Input name="name" placeholder="Gold tournament" /></div>
              <div>
                <Label>Code length (random portion)</Label>
                <select name="code_length" value={len} onChange={(e) => setLen(Number(e.target.value))} className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm">
                  {[6, 7, 8, 9, 10].map((n) => <option key={n} value={n}>{n}</option>)}
                </select>
              </div>
              <div>
                <Label>Character mode</Label>
                <select name="char_mode" value={mode} onChange={(e) => setMode(e.target.value)} className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm">
                  {CHAR_MODES.map((m) => <option key={m.v} value={m.v}>{m.label}</option>)}
                </select>
              </div>
              <div><Label>Prefix (optional, A-Z/0-9)</Label><Input name="code_prefix" maxLength={8} value={prefix} onChange={(e) => setPrefix(e.target.value)} placeholder="e.g. PARTY" /></div>
              <label className="sm:col-span-3 flex items-center gap-2 text-sm text-muted">
                <input type="checkbox" name="exclude_ambiguous" defaultChecked /> Exclude ambiguous characters (0/O, 1/I/L, 5/S) — recommended
              </label>
              <div className="sm:col-span-3 text-xs text-muted space-y-0.5">
                <div><span className="text-text">Example:</span> <code className="font-mono">{exampleCode(mode, len, prefix)}</code>
                  {" "}— the prefix is <strong>additional</strong> to the {len}-character random portion.</div>
                <div><span className="text-text">Character set:</span> {CHAR_MODES.find((m) => m.v === mode)?.charset}</div>
                <div>I, L, O and U are always excluded so codes are unambiguous and match exactly what the guest types.</div>
              </div>
              <div className="sm:col-span-3 flex justify-end">
                <Button type="submit" disabled={busy}>{busy ? "Generating…" : "Generate"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      <Card>
        <CardBody className="p-0">
          {batches === null ? (
            <EmptyState title="Loading…" />
          ) : batches.length === 0 ? (
            <EmptyState title="No batches yet" hint="Generate vouchers for your first campaign." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Label</TH><TH>Count</TH><TH>Format</TH><TH>Unused / Active / Used / Revoked</TH><TH>Created</TH><TH></TH>
                </TR>
              </THead>
              <tbody>
                {batches.map((b) => {
                  const t = b.totals;
                  return (
                  <TR key={b.id}>
                    <TD>
                      <Link href={`/voucher-batches/${b.id}`} className="hover:text-brand">
                        {b.name || <span className="text-muted">— untitled —</span>}
                      </Link>
                      <div className="text-xs text-muted font-mono">{b.id.slice(0, 8)}</div>
                    </TD>
                    <TD>{b.count}</TD>
                    <TD className="text-xs text-muted">{fmtGeneration(b)}</TD>
                    <TD className="text-xs">
                      {t ? (
                        <span className="flex gap-1 flex-wrap">
                          <Badge tone="default">{t.unused} unused</Badge>
                          <Badge tone="ok">{t.active} active</Badge>
                          <Badge tone="warn">{t.exhausted + t.expired} used</Badge>
                          <Badge tone="err">{t.revoked} revoked</Badge>
                        </span>
                      ) : "—"}
                    </TD>
                    <TD className="text-muted">{formatRelative(b.created_at)}</TD>
                    <TD className="text-right space-x-2 whitespace-nowrap">
                      <Link href={`/voucher-batches/${b.id}`} className="text-sm text-brand hover:underline">View codes</Link>
                      <a href={`${EDGE_BASE}/voucher-batches/${b.id}/codes.csv`} target="_blank" rel="noopener"
                        className="inline-flex items-center gap-1 text-sm text-muted hover:text-text">
                        <Download size={14} /> CSV
                      </a>
                      <Button size="sm" variant="ghost" onClick={() => onRevoke(b.id)}>Revoke unused</Button>
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
