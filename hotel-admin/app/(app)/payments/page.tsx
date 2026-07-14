"use client";

import { useEffect, useState } from "react";
import { api, ListResp, Whoami, StripeAccount, Payment } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X } from "lucide-react";
import { canWrite } from "@/lib/roles";
import { formatRelative, errMsg } from "@/lib/utils";

function payTone(s: string): "ok" | "info" | "err" | "default" {
  switch (s) {
    case "paid":    return "ok";
    case "pending": return "info";
    case "failed":  return "err";
    default:        return "default";
  }
}

export default function PaymentsPage() {
  const [accounts, setAccounts] = useState<StripeAccount[] | null>(null);
  const [payments, setPayments] = useState<Payment[] | null>(null);
  const [roles, setRoles] = useState<string[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<StripeAccount | null>(null);

  const writable = canWrite("stripe-accounts", roles);

  async function load() {
    try {
      const [a, p] = await Promise.all([
        api.get<ListResp<StripeAccount>>("/stripe-accounts"),
        api.get<ListResp<Payment>>("/payments"),
      ]);
      setAccounts(a.data);
      setPayments(p.data);
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
    try {
      await api.post("/stripe-accounts", {
        display_name: s("display_name"),
        publishable_key: form.get("publishable_key"),
        secret_key: form.get("secret_key"),
        webhook_secret: form.get("webhook_secret"),
        success_url: form.get("success_url"),
        cancel_url: form.get("cancel_url"),
        enabled: form.get("enabled") === "on",
      });
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
    const body: Record<string, unknown> = {
      display_name: (form.get("display_name") as string) ?? "",
      publishable_key: (form.get("publishable_key") as string) || undefined,
      success_url: (form.get("success_url") as string) || undefined,
      cancel_url: (form.get("cancel_url") as string) || undefined,
      enabled: form.get("enabled") === "on",
    };
    const sk = form.get("secret_key") as string;
    if (sk) body.secret_key = sk;
    const ws = form.get("webhook_secret") as string;
    if (ws) body.webhook_secret = ws;
    try {
      await api.patch(`/stripe-accounts/${editing.id}`, body);
      setEditing(null);
      load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onDelete(id: string) {
    if (!confirm("Delete this Stripe account?")) return;
    try { await api.del(`/stripe-accounts/${id}`); load(); }
    catch (e) { setErr(errMsg(e)); }
  }

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Integrations</div>
          <h1 className="text-2xl font-semibold">Payments</h1>
        </div>
        {writable && (
          <Button onClick={() => { setShowNew((v) => !v); setEditing(null); }}>
            {showNew ? <><X size={14} /> Cancel</> : <><Plus size={14} /> New Stripe account</>}
          </Button>
        )}
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      {showNew && writable && (
        <Card className="mb-6">
          <CardHeader><CardTitle>New Stripe account</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onCreate} className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <div><Label>Display name</Label><Input name="display_name" placeholder="Optional" /></div>
              <div><Label>Publishable key</Label><Input name="publishable_key" required placeholder="pk_live_…" /></div>
              <div><Label>Secret key</Label><Input name="secret_key" type="password" required placeholder="sk_live_… (write-only)" /></div>
              <div><Label>Webhook secret</Label><Input name="webhook_secret" type="password" required placeholder="whsec_… (write-only)" /></div>
              <div><Label>Success URL</Label><Input name="success_url" required placeholder="https://portal/paid" /></div>
              <div><Label>Cancel URL</Label><Input name="cancel_url" required placeholder="https://portal/cancel" /></div>
              <label className="flex items-center gap-2 text-sm text-muted"><input type="checkbox" name="enabled" defaultChecked /> Enabled</label>
              <div className="sm:col-span-2 flex justify-end">
                <Button type="submit" disabled={busy}>{busy ? "Creating…" : "Create"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      {editing && writable && (
        <Card className="mb-6">
          <CardHeader>
            <CardTitle>Edit Stripe account</CardTitle>
            <Button size="sm" variant="ghost" onClick={() => setEditing(null)}><X size={14} /></Button>
          </CardHeader>
          <CardBody>
            <form onSubmit={onEdit} className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <div><Label>Display name</Label><Input name="display_name" defaultValue={editing.display_name ?? ""} /></div>
              <div><Label>Publishable key</Label><Input name="publishable_key" defaultValue={editing.publishable_key} /></div>
              <div><Label>Secret key</Label><Input name="secret_key" type="password" placeholder="leave blank to keep" /></div>
              <div><Label>Webhook secret</Label><Input name="webhook_secret" type="password" placeholder="leave blank to keep" /></div>
              <div><Label>Success URL</Label><Input name="success_url" defaultValue={editing.success_url} /></div>
              <div><Label>Cancel URL</Label><Input name="cancel_url" defaultValue={editing.cancel_url} /></div>
              <label className="flex items-center gap-2 text-sm text-muted"><input type="checkbox" name="enabled" defaultChecked={editing.enabled} /> Enabled</label>
              <div className="sm:col-span-2 flex justify-end">
                <Button type="submit" disabled={busy}>{busy ? "Saving…" : "Save"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      <Card className="mb-6">
        <CardHeader><CardTitle>Stripe accounts</CardTitle></CardHeader>
        <CardBody className="p-0">
          {accounts === null ? <EmptyState title="Loading…" /> : accounts.length === 0 ? (
            <EmptyState title="No Stripe accounts" hint="Connect Stripe to sell WiFi vouchers on the portal." />
          ) : (
            <Table>
              <THead>
                <TR><TH>Name</TH><TH>Publishable key</TH><TH>URLs</TH><TH>Last success</TH><TH>Enabled</TH><TH></TH></TR>
              </THead>
              <tbody>
                {accounts.map((a) => (
                  <TR key={a.id}>
                    <TD>{a.display_name || "—"}</TD>
                    <TD className="font-mono text-xs max-w-xs truncate" title={a.publishable_key}>{a.publishable_key}</TD>
                    <TD className="text-muted text-xs max-w-xs truncate">
                      <div title={a.success_url}>✓ {a.success_url}</div>
                      <div title={a.cancel_url}>✗ {a.cancel_url}</div>
                    </TD>
                    <TD className="text-muted">{a.last_success_at ? formatRelative(a.last_success_at) : "—"}</TD>
                    <TD>{a.enabled ? <Badge tone="ok">on</Badge> : <Badge tone="default">off</Badge>}</TD>
                    <TD className="text-right space-x-2">
                      {writable && <Button size="sm" variant="ghost" onClick={() => { setEditing(a); setShowNew(false); }}>Edit</Button>}
                      {writable && <Button size="sm" variant="ghost" onClick={() => onDelete(a.id)}>Delete</Button>}
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      <Card>
        <CardHeader><CardTitle>Recent payments</CardTitle></CardHeader>
        <CardBody className="p-0">
          {payments === null ? <EmptyState title="Loading…" /> : payments.length === 0 ? (
            <EmptyState title="No payments yet" hint="Guest purchases will appear here." />
          ) : (
            <Table>
              <THead>
                <TR><TH>Status</TH><TH>Amount</TH><TH>Stripe session</TH><TH>Voucher</TH><TH>Created</TH><TH>Completed</TH></TR>
              </THead>
              <tbody>
                {payments.map((p) => (
                  <TR key={p.id}>
                    <TD><Badge tone={payTone(p.status)}>{p.status}</Badge></TD>
                    <TD>{(p.amount_cents / 100).toFixed(2)} {p.currency?.toUpperCase()}</TD>
                    <TD className="font-mono text-xs max-w-xs truncate" title={p.stripe_session_id}>{p.stripe_session_id}</TD>
                    <TD className="font-mono text-xs">{p.voucher_id ? p.voucher_id.slice(0, 8) : "—"}</TD>
                    <TD className="text-muted">{formatRelative(p.created_at)}</TD>
                    <TD className="text-muted">{p.completed_at ? formatRelative(p.completed_at) : "—"}</TD>
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
