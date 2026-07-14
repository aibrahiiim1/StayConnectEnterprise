"use client";

import { useEffect, useState } from "react";
import { api, ApiError, GuestAccessPlan, GuestAccount, ListResp } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X, KeyRound } from "lucide-react";
import { formatRelative } from "@/lib/utils";

export default function GuestAccountsPage() {
  const [rows, setRows] = useState<GuestAccount[] | null>(null);
  const [plans, setPlans] = useState<GuestAccessPlan[]>([]);
  const [portalOn, setPortalOn] = useState<boolean>(false);
  const [err, setErr] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);

  async function load() {
    try {
      const [ga, pl, pv] = await Promise.all([
        api.get<ListResp<GuestAccount>>("/guest-accounts"),
        api.get<ListResp<GuestAccessPlan>>("/guest-access-plans"),
        api.get<{ enabled: boolean }>("/guest-accounts/portal").catch(() => ({ enabled: false })),
      ]);
      setRows(ga.data);
      setPlans(pl.data.filter((t) => t.is_active));
      setPortalOn(!!pv.enabled);
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load");
    }
  }
  useEffect(() => { load(); }, []);

  const planName = (id: string) => plans.find((p) => p.id === id)?.name ?? id.slice(0, 8);

  async function onTogglePortal() {
    setErr(null); setMsg(null);
    try {
      await api.post("/guest-accounts/portal", { enabled: !portalOn });
      setPortalOn((v) => !v);
      setMsg(`Username & Password sign-in ${!portalOn ? "shown on" : "hidden from"} the captive portal.`);
    } catch (e: any) { setErr(e?.message ?? "Toggle failed"); }
  }

  async function onCreate(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true); setErr(null);
    const form = new FormData(e.currentTarget);
    try {
      await api.post("/guest-accounts", {
        username: (form.get("username") as string).trim(),
        password: form.get("password"),
        display_name: (form.get("display_name") as string) || undefined,
        notes: (form.get("notes") as string) || undefined,
        template_id: form.get("template_id"),
        valid_until: (form.get("valid_until") as string) ? new Date(form.get("valid_until") as string).toISOString() : undefined,
      });
      setShowNew(false);
      (e.target as HTMLFormElement).reset();
      load();
    } catch (e: any) {
      if (e instanceof ApiError && e.code === "conflict") setErr("That username already exists.");
      else if (e instanceof ApiError) setErr(e.body?.message ?? e.message);
      else setErr(e?.message ?? "Create failed");
    } finally { setBusy(false); }
  }

  async function onToggle(a: GuestAccount) {
    try { await api.patch(`/guest-accounts/${a.id}`, { enabled: !a.enabled }); load(); }
    catch (e: any) { setErr(e?.message ?? "Update failed"); }
  }
  async function onResetPw(a: GuestAccount) {
    const pw = prompt(`New password for "${a.username}" (min 6 chars):`);
    if (!pw) return;
    try { await api.post(`/guest-accounts/${a.id}/set-password`, { password: pw }); setMsg(`Password updated for ${a.username}.`); }
    catch (e: any) { setErr(e?.message ?? "Reset failed"); }
  }
  async function onChangePlan(a: GuestAccount) {
    const codes = plans.map((p) => `${p.name} (${p.code})`).join(", ");
    const name = prompt(`Plan for "${a.username}" — one of: ${codes}\nType the plan name:`);
    if (!name) return;
    const p = plans.find((x) => x.name === name || x.code === name);
    if (!p) { setErr("Plan not found."); return; }
    try { await api.patch(`/guest-accounts/${a.id}`, { template_id: p.id }); load(); }
    catch (e: any) { setErr(e?.message ?? "Update failed"); }
  }
  async function onDelete(a: GuestAccount) {
    if (!confirm(`Delete guest account "${a.username}"? This cannot be undone.`)) return;
    try { await api.del(`/guest-accounts/${a.id}`); load(); }
    catch (e: any) { setErr(e?.message ?? "Delete failed"); }
  }

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Access</div>
          <h1 className="text-2xl font-semibold">Guest accounts</h1>
        </div>
        <Button onClick={() => setShowNew((s) => !s)} disabled={plans.length === 0}>
          {showNew ? <><X size={14} /> Cancel</> : <><Plus size={14} /> New account</>}
        </Button>
      </div>

      <p className="text-sm text-muted mb-4">
        Username &amp; Password sign-in for guests — each account is bound to a Guest Access Plan (duration, data cap,
        speed, max devices) exactly like a voucher, but the guest signs in with a username and password instead of a code.
      </p>

      <div className="mb-4 flex items-center gap-3 text-sm">
        <span>Show <strong>Username &amp; Password</strong> tab on the captive portal:</span>
        <Button size="sm" variant={portalOn ? "secondary" : "ghost"} onClick={onTogglePortal}>
          {portalOn ? "On" : "Off"}
        </Button>
        <span className="text-muted text-xs">{portalOn ? "Guests can sign in with an account." : "The tab is hidden until enabled."}</span>
      </div>

      {plans.length === 0 && <div className="text-sm text-warn mb-4">No active guest access plans — create one first.</div>}
      {err && <div className="text-err text-sm mb-4">{err}</div>}
      {msg && <div className="text-ok text-sm mb-4">{msg}</div>}

      {showNew && (
        <Card className="mb-6">
          <CardHeader><CardTitle>New guest account</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onCreate} className="grid grid-cols-1 sm:grid-cols-3 gap-3">
              <div><Label>Username</Label><Input name="username" required minLength={3} maxLength={64} placeholder="room101" /></div>
              <div><Label>Password (min 6)</Label><Input name="password" type="password" required minLength={6} /></div>
              <div>
                <Label>Guest access plan</Label>
                <select name="template_id" required className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm">
                  {plans.map((t) => <option key={t.id} value={t.id}>{t.name} — {t.code}</option>)}
                </select>
              </div>
              <div><Label>Display name (optional)</Label><Input name="display_name" placeholder="Room 101 guest" /></div>
              <div><Label>Valid until (optional)</Label><Input name="valid_until" type="datetime-local" /></div>
              <div><Label>Notes (optional)</Label><Input name="notes" /></div>
              <div className="sm:col-span-3 flex justify-end">
                <Button type="submit" disabled={busy}>{busy ? "Creating…" : "Create account"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      <Card>
        <CardBody className="p-0">
          {rows === null ? <EmptyState title="Loading…" /> : rows.length === 0 ? (
            <EmptyState title="No guest accounts yet" hint="Create one to let guests sign in with a username and password." />
          ) : (
            <Table>
              <THead>
                <TR><TH>Username</TH><TH>Name</TH><TH>Plan</TH><TH>Status</TH><TH>Valid until</TH><TH>Last login</TH><TH>Logins</TH><TH></TH></TR>
              </THead>
              <tbody>
                {rows.map((a) => (
                  <TR key={a.id}>
                    <TD className="font-mono">{a.username}</TD>
                    <TD className="text-muted">{a.display_name || "—"}</TD>
                    <TD className="text-muted">{planName(a.template_id)}</TD>
                    <TD><Badge tone={a.enabled ? "ok" : "err"}>{a.enabled ? "enabled" : "disabled"}</Badge></TD>
                    <TD className="text-muted">{a.valid_until ? formatRelative(a.valid_until) : "—"}</TD>
                    <TD className="text-muted">{a.last_login_at ? formatRelative(a.last_login_at) : "never"}</TD>
                    <TD className="text-muted">{a.login_count}</TD>
                    <TD className="text-right space-x-1 whitespace-nowrap">
                      <Button size="sm" variant="ghost" onClick={() => onChangePlan(a)}>Plan</Button>
                      <Button size="sm" variant="ghost" onClick={() => onResetPw(a)}><KeyRound size={13} /> Reset</Button>
                      <Button size="sm" variant="secondary" onClick={() => onToggle(a)}>{a.enabled ? "Disable" : "Enable"}</Button>
                      <Button size="sm" variant="danger" onClick={() => onDelete(a)}>Delete</Button>
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
