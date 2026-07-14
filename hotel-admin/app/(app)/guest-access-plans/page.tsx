"use client";

import { useEffect, useState } from "react";
import { api, ApiError, GuestAccessPlan, ListResp } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X } from "lucide-react";
import { formatBytes } from "@/lib/utils";

function formatDuration(s?: number | null): string {
  if (!s || s <= 0) return "∞";
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (h > 0) return `${h}h${m ? ` ${m}m` : ""}`;
  return `${m}m`;
}

export default function GuestAccessPlansPage() {
  const [rows, setRows] = useState<GuestAccessPlan[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);

  async function load() {
    try {
      const r = await api.get<ListResp<GuestAccessPlan>>("/guest-access-plans");
      setRows(r.data);
    } catch (e: any) { setErr(e?.message ?? "Failed to load"); }
  }
  useEffect(() => { load(); }, []);

  async function onCreate(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true); setErr(null);
    const form = new FormData(e.currentTarget);
    const num = (k: string) => {
      const v = form.get(k) as string;
      return v === "" || v == null ? undefined : Number(v);
    };
    try {
      await api.post("/guest-access-plans", {
        code: form.get("code"),
        name: form.get("name"),
        description: (form.get("description") as string) || undefined,
        duration_seconds: num("duration_seconds"),
        data_cap_bytes:   num("data_cap_bytes"),
        down_kbps:        num("down_kbps"),
        up_kbps:          num("up_kbps"),
        max_concurrent_devices: num("max_concurrent_devices") ?? 1,
        price_cents:      num("price_cents"),
        currency:         (form.get("currency") as string) || undefined,
      });
      setShowNew(false);
      (e.currentTarget as HTMLFormElement).reset();
      load();
    } catch (e: any) {
      if (e instanceof ApiError && e.body?.error === "limit_exceeded") {
        setErr(`License limit reached: ${e.body.limit_key} (${e.body.current}/${e.body.limit})`);
      } else if (e instanceof ApiError && e.body?.error === "license_restricted") {
        setErr("License is restricted — renewing the license re-enables plan creation.");
      } else if (e instanceof ApiError) setErr(e.message);
      else setErr(e?.message ?? "Create failed");
    } finally { setBusy(false); }
  }

  async function onToggle(t: GuestAccessPlan) {
    try {
      await api.patch(`/guest-access-plans/${t.id}`, { is_active: !t.is_active });
      load();
    } catch (e: any) { setErr(e?.message ?? "Toggle failed"); }
  }

  async function onDelete(id: string) {
    if (!confirm("Delete this plan?")) return;
    try {
      await api.del(`/guest-access-plans/${id}`);
      load();
    } catch (e: any) { setErr(e?.message ?? "Delete failed"); }
  }

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Access</div>
          <h1 className="text-2xl font-semibold">Guest access plans</h1>
        </div>
        <Button onClick={() => setShowNew((s) => !s)}>
          {showNew ? <><X size={14} /> Cancel</> : <><Plus size={14} /> New plan</>}
        </Button>
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      {showNew && (
        <Card className="mb-6">
          <CardHeader><CardTitle>New plan</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onCreate} className="grid grid-cols-1 sm:grid-cols-4 gap-3">
              <div><Label>Code</Label><Input name="code" required placeholder="h2" /></div>
              <div className="sm:col-span-3"><Label>Name</Label><Input name="name" required placeholder="2 Hour Pass" /></div>
              <div className="sm:col-span-4"><Label>Description</Label><Input name="description" placeholder="Optional" /></div>
              <div><Label>Duration (s)</Label><Input name="duration_seconds" type="number" min={0} placeholder="7200" /></div>
              <div><Label>Data cap (bytes)</Label><Input name="data_cap_bytes" type="number" min={0} placeholder="leave blank = unlimited" /></div>
              <div><Label>Down kbps</Label><Input name="down_kbps" type="number" min={0} placeholder="50000" /></div>
              <div><Label>Up kbps</Label><Input name="up_kbps" type="number" min={0} placeholder="10000" /></div>
              <div><Label>Max devices</Label><Input name="max_concurrent_devices" type="number" min={1} defaultValue={1} /></div>
              <div><Label>Price (cents)</Label><Input name="price_cents" type="number" min={0} placeholder="500" /></div>
              <div><Label>Currency</Label><Input name="currency" placeholder="USD" /></div>
              <div className="sm:col-span-4 flex justify-end">
                <Button type="submit" disabled={busy}>{busy ? "Creating…" : "Create"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      <Card>
        <CardBody className="p-0">
          {rows === null ? <EmptyState title="Loading…" /> : rows.length === 0 ? (
            <EmptyState title="No plans yet" hint="Create one to start issuing voucher batches." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Code</TH><TH>Name</TH><TH>Duration</TH><TH>Data cap</TH><TH>Down/Up</TH>
                  <TH>Devices</TH><TH>Price</TH><TH>Active</TH><TH></TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((t) => (
                  <TR key={t.id}>
                    <TD className="font-mono">{t.code}</TD>
                    <TD>{t.name}</TD>
                    <TD className="text-muted">{formatDuration(t.duration_seconds)}</TD>
                    <TD className="text-muted">{t.data_cap_bytes ? formatBytes(t.data_cap_bytes) : "∞"}</TD>
                    <TD className="text-muted font-mono text-xs">
                      {t.down_kbps ?? "∞"} / {t.up_kbps ?? "∞"}
                    </TD>
                    <TD>{t.max_concurrent_devices}</TD>
                    <TD className="text-muted">
                      {t.price_cents ? `${(t.price_cents/100).toFixed(2)} ${t.currency ?? ""}` : "—"}
                    </TD>
                    <TD>
                      <button onClick={() => onToggle(t)}>
                        <Badge tone={t.is_active ? "ok" : "default"}>{t.is_active ? "active" : "inactive"}</Badge>
                      </button>
                    </TD>
                    <TD className="text-right">
                      <Button size="sm" variant="ghost" onClick={() => onDelete(t.id)}>Delete</Button>
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
