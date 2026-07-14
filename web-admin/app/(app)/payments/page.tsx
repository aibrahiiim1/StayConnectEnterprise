"use client";

import { useEffect, useState } from "react";
import { api, ListResp, Payment, StripeAccount } from "@/lib/api";
import { useTenant } from "@/lib/use-tenant";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X, Trash2 } from "lucide-react";
import { formatRelative, errMsg } from "@/lib/utils";

const statusTone = (s: string) =>
  s === "paid" ? "ok" : s === "pending" ? "info" : s === "failed" ? "err" : "warn";

// Stripe charges in the smallest currency unit (cents for USD/EUR). Zero-
// decimal currencies like JPY would skew this, but the display is
// admin-only; a proper money library is a future hardening item.
const fmtAmount = (cents: number, currency: string) =>
  `${(cents / 100).toFixed(2)} ${currency.toUpperCase()}`;

export default function PaymentsPage() {
  const tenantID = useTenant();
  const [accounts, setAccounts] = useState<StripeAccount[] | null>(null);
  const [payments, setPayments] = useState<Payment[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);

  async function load() {
    if (!tenantID) return;
    try {
      const [a, p] = await Promise.all([
        api.get<ListResp<StripeAccount>>(`/v1/stripe-accounts?tenant_id=${tenantID}`),
        api.get<ListResp<Payment>>(`/v1/payments?tenant_id=${tenantID}`),
      ]);
      setAccounts(a.data ?? []);
      setPayments(p.data ?? []);
    } catch (e) { setErr(errMsg(e)); }
  }
  useEffect(() => { load(); }, [tenantID]);

  async function onCreate(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!tenantID) return;
    setBusy(true); setErr(null);
    const form = new FormData(e.currentTarget);
    const body: any = {
      display_name: (form.get("display_name") as string) || undefined,
      publishable_key: form.get("publishable_key"),
      secret_key: form.get("secret_key"),
      webhook_secret: form.get("webhook_secret"),
      success_url: form.get("success_url"),
      cancel_url: form.get("cancel_url"),
      enabled: form.get("enabled") === "true",
    };
    try {
      await api.post(`/v1/stripe-accounts?tenant_id=${tenantID}`, body);
      setShowNew(false);
      (e.currentTarget as HTMLFormElement).reset();
      load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onToggle(a: StripeAccount) {
    if (!tenantID) return;
    try {
      await api.patch(`/v1/stripe-accounts/${a.id}?tenant_id=${tenantID}`, { enabled: !a.enabled });
      load();
    } catch (e) { setErr(errMsg(e)); }
  }

  async function onDelete(a: StripeAccount) {
    if (!tenantID) return;
    if (!confirm(`Delete Stripe account "${a.display_name || a.publishable_key.slice(0, 16)}…"?`)) return;
    try {
      await api.del(`/v1/stripe-accounts/${a.id}?tenant_id=${tenantID}`);
      load();
    } catch (e) { setErr(errMsg(e)); }
  }

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Integrations</div>
          <h1 className="text-2xl font-semibold">Payments</h1>
          <div className="text-xs text-muted mt-1">
            Stripe account + payment history. Webhook URL:{" "}
            <span className="font-mono">/v1/webhooks/stripe/{`{tenant_id}`}</span>
          </div>
        </div>
        <Button onClick={() => setShowNew((s) => !s)}>
          {showNew ? <><X size={14} /> Cancel</> : <><Plus size={14} /> New Stripe account</>}
        </Button>
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      {showNew && (
        <Card className="mb-6">
          <CardHeader><CardTitle>New Stripe account</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onCreate} className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <div><Label>Display name</Label><Input name="display_name" placeholder="Production" /></div>
              <div>
                <Label>Enabled</Label>
                <select name="enabled" defaultValue="true"
                  className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm">
                  <option value="true">Enabled</option>
                  <option value="false">Disabled</option>
                </select>
              </div>
              <div className="sm:col-span-2">
                <Label>Publishable key (public)</Label>
                <Input name="publishable_key" required placeholder="pk_live_…" />
              </div>
              <div className="sm:col-span-2">
                <Label>Secret key (write-only)</Label>
                <Input name="secret_key" type="password" required placeholder="sk_live_…" />
              </div>
              <div className="sm:col-span-2">
                <Label>Webhook secret (write-only)</Label>
                <Input name="webhook_secret" type="password" required placeholder="whsec_…" />
              </div>
              <div>
                <Label>Success URL</Label>
                <Input name="success_url" required placeholder="https://portal/thanks?s={CHECKOUT_SESSION_ID}" />
              </div>
              <div>
                <Label>Cancel URL</Label>
                <Input name="cancel_url" required placeholder="https://portal/cancel" />
              </div>
              <div className="sm:col-span-2 flex justify-end">
                <Button type="submit" disabled={busy}>{busy ? "Creating…" : "Create"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      <Card className="mb-6">
        <CardHeader><CardTitle>Stripe accounts</CardTitle></CardHeader>
        <CardBody className="p-0">
          {accounts === null ? <EmptyState title="Loading…" /> :
           accounts.length === 0 ? <EmptyState title="No Stripe account configured" hint="Guests can't purchase vouchers until you add one." /> : (
            <Table>
              <THead><TR>
                <TH>Name</TH><TH>Publishable key</TH><TH>Enabled</TH>
                <TH>Last activity</TH><TH></TH>
              </TR></THead>
              <tbody>
                {accounts.map((a) => (
                  <TR key={a.id}>
                    <TD>{a.display_name || "—"}</TD>
                    <TD className="font-mono text-xs truncate max-w-[320px]" title={a.publishable_key}>{a.publishable_key}</TD>
                    <TD>
                      <button onClick={() => onToggle(a)}
                        className={`text-xs px-2 py-0.5 rounded border ${a.enabled
                          ? "text-ok border-[#1e5c3c] bg-[#123422]"
                          : "text-muted border-border"}`}>
                        {a.enabled ? "enabled" : "disabled"}
                      </button>
                    </TD>
                    <TD className="text-muted text-xs">
                      {a.last_success_at ? formatRelative(a.last_success_at) :
                       a.last_error_at ? formatRelative(a.last_error_at) : "—"}
                    </TD>
                    <TD className="text-right">
                      <Button size="sm" variant="ghost" onClick={() => onDelete(a)}><Trash2 size={12} /></Button>
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
          {payments === null ? <EmptyState title="Loading…" /> :
           payments.length === 0 ? <EmptyState title="No payments yet" /> : (
            <Table>
              <THead><TR>
                <TH>Status</TH><TH>Amount</TH><TH>Session</TH>
                <TH>Voucher</TH><TH>Created</TH><TH>Completed</TH>
              </TR></THead>
              <tbody>
                {payments.map((p) => (
                  <TR key={p.id}>
                    <TD><Badge tone={statusTone(p.status) as any}>{p.status}</Badge></TD>
                    <TD className="font-mono">{fmtAmount(p.amount_cents, p.currency)}</TD>
                    <TD className="font-mono text-xs truncate max-w-[240px]" title={p.stripe_session_id}>{p.stripe_session_id}</TD>
                    <TD className="font-mono text-xs">{p.voucher_id ? "issued" : "—"}</TD>
                    <TD className="text-muted text-xs">{formatRelative(p.created_at)}</TD>
                    <TD className="text-muted text-xs">{p.completed_at ? formatRelative(p.completed_at) : "—"}</TD>
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
