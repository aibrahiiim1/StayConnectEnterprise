"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { api, ApiError, ListResp, TicketTemplate, VoucherBatch, Whoami } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X, Download } from "lucide-react";
import { formatRelative } from "@/lib/utils";

function useTenant() {
  const [id, setId] = useState<string | null>(null);
  useEffect(() => {
    api.get<Whoami>("/v1/auth/whoami").then(async (w) => {
      if (w.default_tenant_id) return setId(w.default_tenant_id);
      const ts = await api.get<{ data: { id: string; slug: string }[] }>("/v1/tenants");
      setId(ts.data.find((t) => t.slug === "dev")?.id ?? ts.data[0]?.id ?? null);
    });
  }, []);
  return id;
}

export default function VoucherBatchesPage() {
  const tenantID = useTenant();
  const [batches, setBatches] = useState<VoucherBatch[] | null>(null);
  const [templates, setTemplates] = useState<TicketTemplate[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);

  async function load() {
    if (!tenantID) return;
    try {
      const [vb, tt] = await Promise.all([
        api.get<ListResp<VoucherBatch>>(`/v1/voucher-batches?tenant_id=${tenantID}`),
        api.get<ListResp<TicketTemplate>>(`/v1/ticket-templates?tenant_id=${tenantID}`),
      ]);
      setBatches(vb.data);
      setTemplates(tt.data.filter((t) => t.is_active));
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load");
    }
  }
  useEffect(() => { load(); }, [tenantID]);

  async function onCreate(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!tenantID) return;
    setBusy(true); setErr(null);
    const form = new FormData(e.currentTarget);
    try {
      await api.post(`/v1/voucher-batches?tenant_id=${tenantID}`, {
        template_id: form.get("template_id"),
        count: Number(form.get("count")),
        name: (form.get("name") as string) || undefined,
      });
      setShowNew(false);
      (e.currentTarget as HTMLFormElement).reset();
      load();
    } catch (e: any) {
      if (e instanceof ApiError && e.body?.error === "limit_exceeded") {
        setErr(`Plan limit reached: ${e.body.limit_key} (${e.body.current}/${e.body.limit})`);
      } else {
        setErr(e?.message ?? "Create failed");
      }
    } finally {
      setBusy(false);
    }
  }

  async function onRevoke(id: string) {
    if (!tenantID) return;
    if (!confirm("Revoke all non-terminal vouchers in this batch?")) return;
    try {
      await api.post(`/v1/voucher-batches/${id}/revoke?tenant_id=${tenantID}`);
      load();
    } catch (e: any) { setErr(e?.message ?? "Revoke failed"); }
  }

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Access</div>
          <h1 className="text-2xl font-semibold">Voucher batches</h1>
        </div>
        <Button onClick={() => setShowNew((s) => !s)} disabled={templates.length === 0}>
          {showNew ? <><X size={14} /> Cancel</> : <><Plus size={14} /> New batch</>}
        </Button>
      </div>

      {templates.length === 0 && (
        <div className="text-sm text-warn mb-4">
          No active ticket templates — create one before generating vouchers.
        </div>
      )}
      {err && <div className="text-err text-sm mb-4">{err}</div>}

      {showNew && (
        <Card className="mb-6">
          <CardHeader><CardTitle>New batch</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onCreate} className="grid grid-cols-1 sm:grid-cols-3 gap-3">
              <div>
                <Label>Template</Label>
                <select
                  name="template_id" required
                  className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm"
                >
                  {templates.map((t) => (
                    <option key={t.id} value={t.id}>{t.name} — {t.code}</option>
                  ))}
                </select>
              </div>
              <div><Label>Count</Label><Input name="count" type="number" min={1} max={10000} required defaultValue={50} /></div>
              <div><Label>Label (optional)</Label><Input name="name" placeholder="Gold tournament" /></div>
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
            <EmptyState title="No batches yet" hint="Generate vouchers for your first ticket campaign." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Label</TH><TH>Count</TH><TH>Created</TH><TH></TH>
                </TR>
              </THead>
              <tbody>
                {batches.map((b) => (
                  <TR key={b.id}>
                    <TD>
                      <Link href={`/voucher-batches/${b.id}`} className="hover:text-brand">
                        {b.name || <span className="text-muted">— untitled —</span>}
                      </Link>
                      <div className="text-xs text-muted font-mono">{b.id.slice(0, 8)}</div>
                    </TD>
                    <TD>{b.count}</TD>
                    <TD className="text-muted">{formatRelative(b.created_at)}</TD>
                    <TD className="text-right space-x-2">
                      <a
                        href={`/api/v1/voucher-batches/${b.id}/codes.csv?tenant_id=${tenantID}`}
                        target="_blank" rel="noopener"
                        className="inline-flex items-center gap-1 text-sm text-muted hover:text-text"
                      >
                        <Download size={14} /> CSV
                      </a>
                      <Button size="sm" variant="ghost" onClick={() => onRevoke(b.id)}>Revoke all</Button>
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
