"use client";

import { useEffect, useMemo, useState } from "react";
import {
  api, ApiError, GuestAccessPlan, GuestAccount, GuestAccountCreateResp,
  GuestAccountPasswordResp, ListResp,
} from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X, KeyRound, Copy, Check, Eye, EyeOff, Pencil, Power } from "lucide-react";
import { formatRelative } from "@/lib/utils";

const selectCls = "h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm";

// planLabel builds a rich option label: name + code + duration/speed/max-devices.
function planLabel(p: GuestAccessPlan): string {
  const bits: string[] = [];
  if (p.duration_seconds) bits.push(`${Math.round(p.duration_seconds / 3600)}h`);
  else bits.push("no time limit");
  if (p.down_kbps) bits.push(`${Math.round(p.down_kbps / 1000)}↓`);
  bits.push(`${p.max_concurrent_devices} dev`);
  return `${p.name} (${p.code}) — ${bits.join(" · ")}`;
}

function weakPassword(pw: string): boolean {
  return pw.length > 0 && pw.length < 8;
}

// A one-time password reveal shown once after create/reset. The password is
// NEVER retrievable afterwards.
function PasswordReveal({ username, password, onClose }: { username: string; password: string; onClose: () => void }) {
  const [copied, setCopied] = useState(false);
  const [show, setShow] = useState(true);
  return (
    <Card className="mb-6 border-brand">
      <CardHeader><CardTitle>Password for {username} — shown once</CardTitle></CardHeader>
      <CardBody>
        <div className="flex items-center gap-2 mb-2">
          <code className="font-mono text-lg bg-panel2 border border-border rounded px-3 py-1.5 select-all">
            {show ? password : "•".repeat(password.length)}
          </code>
          <Button size="sm" variant="ghost" onClick={() => setShow((s) => !s)}>
            {show ? <EyeOff size={14} /> : <Eye size={14} />}
          </Button>
          <Button size="sm" variant="secondary" onClick={async () => {
            try { await navigator.clipboard.writeText(password); setCopied(true); setTimeout(() => setCopied(false), 1500); } catch {}
          }}>
            {copied ? <><Check size={14} /> Copied</> : <><Copy size={14} /> Copy password</>}
          </Button>
          <Button size="sm" variant="ghost" onClick={onClose}>Done</Button>
        </div>
        <p className="text-xs text-warn">
          This password is shown once. It cannot be retrieved later — if it is lost, set a new password.
        </p>
      </CardBody>
    </Card>
  );
}

export default function GuestAccountsPage() {
  const [rows, setRows] = useState<GuestAccount[] | null>(null);
  const [plans, setPlans] = useState<GuestAccessPlan[]>([]);
  const [portalOn, setPortalOn] = useState<boolean>(false);
  const [err, setErr] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);
  const [q, setQ] = useState("");
  const [editing, setEditing] = useState<GuestAccount | null>(null);
  const [pwFor, setPwFor] = useState<GuestAccount | null>(null);
  const [reveal, setReveal] = useState<{ username: string; password: string } | null>(null);
  const [authority, setAuthority] = useState<"iam_v2" | "legacy" | null>(null);
  // Under IAM-v2 a credential carries no plan. What a guest may acquire is decided by PACKAGE ELIGIBILITY
  // RULES evaluated when packages are listed, so a "guest access plan" on this screen would be a control the
  // backend discards -- and the operator would believe they had chosen what the guest gets.
  const planApplies = authority !== "iam_v2";

  async function load() {
    try {
      const [ga, pv] = await Promise.all([
        api.get<ListResp<GuestAccount>>("/guest-accounts"),
        api.get<{ enabled: boolean }>("/guest-accounts/portal").catch(() => ({ enabled: false })),
      ]);
      setRows(ga.data);
      // The site's authority comes from the LIST ENVELOPE, not from a row: with zero accounts there is no row
      // to ask, and that is exactly when the create form has to decide whether a plan picker belongs on screen.
      const auth = ga.authority ?? null;
      setAuthority(auth);

      // GUEST ACCESS PLANS ARE FETCHED ONLY WHERE THEY EXIST.
      //
      // This used to sit inside the same Promise.all as the accounts themselves, which made a legacy
      // concept a hard dependency of an IAM-v2 screen: on a site with no plans, or for an operator whose
      // role cannot read that resource, or any time the legacy endpoint failed, the ENTIRE Guest Accounts
      // page rendered "Failed to load" -- with the accounts already successfully fetched and sitting unused.
      //
      // Under IAM-v2 the answer is not merely optional, it is irrelevant: a credential carries no plan.
      // So the call is skipped outright there, and where it does apply its failure degrades the plan column
      // rather than the screen.
      if (auth === "iam_v2") {
        setPlans([]);
      } else {
        const pl = await api.get<ListResp<GuestAccessPlan>>("/guest-access-plans").catch(() => null);
        setPlans(pl?.data ?? []);
      }
      setPortalOn(!!pv.enabled);
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load");
    }
  }
  useEffect(() => { load(); }, []);

  const activePlans = useMemo(() => plans.filter((p) => p.is_active), [plans]);
  // planById keeps ALL plans (incl. inactive) so historical assignments still resolve.
  const planById = useMemo(() => new Map(plans.map((p) => [p.id, p])), [plans]);

  const filtered = useMemo(() => {
    if (!rows) return [];
    const needle = q.trim().toLowerCase();
    if (!needle) return rows;
    return rows.filter((a) =>
      a.username.toLowerCase().includes(needle) ||
      (a.display_name ?? "").toLowerCase().includes(needle));
  }, [rows, q]);

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
    setBusy(true); setErr(null); setMsg(null);
    const form = new FormData(e.currentTarget);
    const generate = form.get("generate") === "on";
    const password = (form.get("password") as string) || "";
    try {
      const resp = await api.post<GuestAccountCreateResp>("/guest-accounts", {
        username: (form.get("username") as string).trim(),
        password: generate ? "" : password,
        generate,
        display_name: (form.get("display_name") as string) || undefined,
        notes: (form.get("notes") as string) || undefined,
        template_id: planApplies ? form.get("template_id") : undefined,
        valid_from: (form.get("valid_from") as string) ? new Date(form.get("valid_from") as string).toISOString() : undefined,
        valid_until: (form.get("valid_until") as string) ? new Date(form.get("valid_until") as string).toISOString() : undefined,
      });
      setShowNew(false);
      (e.target as HTMLFormElement).reset();
      const shown = resp.generated_password ?? password;
      if (shown) setReveal({ username: resp.account.username, password: shown });
      load();
    } catch (e: any) {
      if (e instanceof ApiError && e.code === "conflict") setErr("That username already exists.");
      else if (e instanceof ApiError) setErr(e.body?.message ?? e.message);
      else setErr(e?.message ?? "Create failed");
    } finally { setBusy(false); }
  }

  async function onSaveEdit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!editing) return;
    setBusy(true); setErr(null);
    const form = new FormData(e.currentTarget);
    try {
      await api.patch(`/guest-accounts/${editing.id}`, {
        username: (form.get("username") as string).trim(),
        display_name: (form.get("display_name") as string) || undefined,
        notes: (form.get("notes") as string) || undefined,
        template_id: planApplies ? form.get("template_id") : undefined,
        valid_from: (form.get("valid_from") as string) ? new Date(form.get("valid_from") as string).toISOString() : undefined,
        valid_until: (form.get("valid_until") as string) ? new Date(form.get("valid_until") as string).toISOString() : undefined,
      });
      setEditing(null);
      setMsg("Account updated.");
      load();
    } catch (e: any) {
      if (e instanceof ApiError && e.code === "conflict") setErr("That username already exists.");
      else setErr(e?.message ?? "Update failed");
    } finally { setBusy(false); }
  }

  async function onSavePassword(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!pwFor) return;
    setBusy(true); setErr(null);
    const form = new FormData(e.currentTarget);
    const generate = form.get("generate") === "on";
    const password = (form.get("password") as string) || "";
    const disconnect = form.get("disconnect_sessions") === "on";
    try {
      const resp = await api.post<GuestAccountPasswordResp>(`/guest-accounts/${pwFor.id}/set-password`, {
        password: generate ? "" : password, generate, disconnect_sessions: disconnect,
      });
      const shown = resp.generated_password ?? password;
      const uname = pwFor.username;
      setPwFor(null);
      if (shown) setReveal({ username: uname, password: shown });
      setMsg(`Password updated for ${uname}.` + (resp.disconnected_sessions ? ` ${resp.disconnected_sessions} session(s) disconnected.` : ""));
      load();
    } catch (e: any) {
      setErr(e instanceof ApiError ? (e.body?.message ?? e.message) : (e?.message ?? "Reset failed"));
    } finally { setBusy(false); }
  }

  async function onToggle(a: GuestAccount) {
    try { await api.patch(`/guest-accounts/${a.id}`, { enabled: !a.enabled }); load(); }
    catch (e: any) { setErr(e?.message ?? "Update failed"); }
  }
  async function onDisconnect(a: GuestAccount) {
    if (!confirm(`Disconnect all active devices for "${a.username}"?`)) return;
    try { const r = await api.post<{ disconnected_sessions: number }>(`/guest-accounts/${a.id}/disconnect`); setMsg(`${r.disconnected_sessions} session(s) disconnected.`); load(); }
    catch (e: any) { setErr(e?.message ?? "Disconnect failed"); }
  }
  async function onDelete(a: GuestAccount) {
    if (!confirm(`Delete guest account "${a.username}"? This cannot be undone.`)) return;
    try { await api.del(`/guest-accounts/${a.id}`); load(); }
    catch (e: any) { setErr(e?.message ?? "Delete failed"); }
  }

  const locked = (a: GuestAccount) => a.locked_until && new Date(a.locked_until) > new Date();

  return (
    <div className="p-6 max-w-[92rem] mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Access</div>
          <h1 className="text-2xl font-semibold">Guest accounts</h1>
        </div>
        {/*
          THE DEEPEST LEGACY DEPENDENCY ON THIS SCREEN.

          This button was disabled whenever there were no active guest access plans, whatever the authority.
          Under IAM-v2 a credential carries no plan, so plans are not merely optional there -- they are
          irrelevant, and on a site with none an operator could never create a guest account AT ALL. The only
          symptom was a dead button with no explanation.

          It is no longer gated on anything. Where a plan IS a genuine prerequisite -- a legacy site with none
          -- the FORM says so by name and refuses to submit. A disabled button explains nothing, and a `title`
          only explains it to someone who already suspected the button and hovered.
        */}
        <Button onClick={() => { setShowNew((s) => !s); setEditing(null); setPwFor(null); }}>
          {showNew ? <><X size={14} /> Cancel</> : <><Plus size={14} /> New account</>}
        </Button>
      </div>

      <p className="text-sm text-muted mb-4">
        Username &amp; Password sign-in for guests — each account is bound to a Guest Access Plan (duration, data cap,
        speed and max devices) exactly like a voucher, but the guest signs in with a username and password instead of a
        code. License capacity is appliance-wide; the plan&apos;s <strong>max devices</strong> is enforced per account.
      </p>

      <div className="mb-4 flex items-center gap-3 text-sm">
        <span>Show <strong>Username &amp; Password</strong> tab on the captive portal:</span>
        <Button size="sm" variant={portalOn ? "secondary" : "ghost"} onClick={onTogglePortal}>{portalOn ? "On" : "Off"}</Button>
        <span className="text-muted text-xs">{portalOn ? "Guests can sign in with an account." : "The tab is hidden until enabled."}</span>
      </div>

      {activePlans.length === 0 && <div className="text-sm text-warn mb-4">No active guest access plans — create one first.</div>}
      {err && <div className="text-err text-sm mb-4">{err}</div>}
      {msg && <div className="text-ok text-sm mb-4">{msg}</div>}

      {reveal && <PasswordReveal username={reveal.username} password={reveal.password} onClose={() => setReveal(null)} />}

      {showNew && (
        <Card className="mb-6">
          <CardHeader><CardTitle>New guest account</CardTitle></CardHeader>
          <CardBody><AccountForm plans={activePlans} planApplies={planApplies} onSubmit={onCreate} busy={busy} withPassword /></CardBody>
        </Card>
      )}

      {editing && (
        <Card className="mb-6 border-brand">
          <CardHeader><CardTitle>Edit {editing.username}</CardTitle></CardHeader>
          <CardBody><AccountForm plans={activePlans} allPlans={plans} planApplies={planApplies} account={editing} onSubmit={onSaveEdit} busy={busy} onCancel={() => setEditing(null)} /></CardBody>
        </Card>
      )}

      {pwFor && (
        <Card className="mb-6 border-brand">
          <CardHeader><CardTitle>Set password — {pwFor.username}</CardTitle></CardHeader>
          <CardBody><PasswordForm onSubmit={onSavePassword} busy={busy} onCancel={() => setPwFor(null)} /></CardBody>
        </Card>
      )}

      <div className="flex gap-2 mb-3">
        <Input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Search username or name…" className="max-w-xs" />
      </div>

      <Card>
        <CardBody className="p-0">
          {rows === null ? <EmptyState title="Loading…" /> : filtered.length === 0 ? (
            <EmptyState title="No guest accounts" hint="Create one to let guests sign in with a username and password." />
          ) : (
            <Table>
              <THead>
                <TR><TH>Username</TH><TH>Name</TH>{planApplies && <TH>Plan</TH>}<TH>Devices</TH><TH>Status</TH><TH>Validity</TH><TH>Last login</TH><TH>Logins</TH><TH></TH></TR>
              </THead>
              <tbody>
                {filtered.map((a) => {
                  const p = a.template_id ? planById.get(a.template_id) : undefined;
                  const cap = a.max_devices ?? p?.max_concurrent_devices;
                  return (
                  <TR key={a.id}>
                    <TD className="font-mono">{a.username}</TD>
                    <TD className="text-muted">{a.display_name || "—"}</TD>
                    {planApplies && (
                      <TD className="text-muted">
                        {p ? p.name : (a.template_id ? a.template_id.slice(0, 8) : "—")}
                        {p && !p.is_active && <Badge tone="warn" className="ml-1">inactive</Badge>}
                      </TD>
                    )}
                    <TD className="text-muted">{a.active_devices ?? 0}{cap ? ` of ${cap}` : ""}</TD>
                    <TD>
                      <Badge tone={a.enabled ? "ok" : "err"}>{a.enabled ? "enabled" : "disabled"}</Badge>
                      {locked(a) && <Badge tone="warn" className="ml-1">locked</Badge>}
                    </TD>
                    <TD className="text-muted text-xs">{a.valid_until ? `until ${formatRelative(a.valid_until)}` : "—"}</TD>
                    <TD className="text-muted">{a.last_login_at ? formatRelative(a.last_login_at) : "never"}</TD>
                    <TD className="text-muted">{a.login_count}</TD>
                    <TD className="text-right space-x-1 whitespace-nowrap">
                      <Button size="sm" variant="ghost" onClick={() => { setEditing(a); setPwFor(null); setShowNew(false); }}><Pencil size={13} /> Edit</Button>
                      <Button size="sm" variant="ghost" onClick={() => { setPwFor(a); setEditing(null); setShowNew(false); }}><KeyRound size={13} /> Password</Button>
                      <Button size="sm" variant="secondary" onClick={() => onToggle(a)}>{a.enabled ? "Disable" : "Enable"}</Button>
                      {(a.active_devices ?? 0) > 0 && <Button size="sm" variant="ghost" onClick={() => onDisconnect(a)}><Power size={13} /> Disconnect</Button>}
                      <Button size="sm" variant="danger" onClick={() => onDelete(a)}>Delete</Button>
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

// AccountForm is shared by create and edit. `withPassword` shows the create-time
// password controls; edit uses the separate Set-password panel.
function AccountForm({ plans, allPlans, planApplies = true, account, onSubmit, busy, withPassword, onCancel }: {
  plans: GuestAccessPlan[]; allPlans?: GuestAccessPlan[]; planApplies?: boolean; account?: GuestAccount;
  onSubmit: (e: React.FormEvent<HTMLFormElement>) => void; busy: boolean; withPassword?: boolean; onCancel?: () => void;
}) {
  const [generate, setGenerate] = useState(false);
  const [pw, setPw] = useState("");
  const [showPw, setShowPw] = useState(false);
  const dt = (s?: string | null) => (s ? new Date(s).toISOString().slice(0, 16) : "");
  // For edit, if the current plan is inactive, include it in the list so it stays selectable/visible.
  const planOptions = useMemo(() => {
    const list = [...plans];
    if (account && allPlans) {
      const cur = account.template_id ? allPlans.find((p) => p.id === account.template_id) : undefined;
      if (cur && !list.some((p) => p.id === cur.id)) list.unshift(cur);
    }
    return list;
  }, [plans, allPlans, account]);

  return (
    <form onSubmit={onSubmit} className="grid grid-cols-1 sm:grid-cols-3 gap-3">
      <div><Label>Username</Label><Input name="username" required minLength={1} maxLength={64} defaultValue={account?.username} placeholder="room101 · A · 1" /></div>
      {withPassword ? (
        <>
          <div>
            <Label>Password</Label>
            <div className="flex gap-1">
              <Input name="password" type={showPw ? "text" : "password"} value={pw} onChange={(e) => setPw(e.target.value)}
                disabled={generate} required={!generate} minLength={1} maxLength={128} placeholder={generate ? "auto-generated" : "any length ≥ 1"} />
              <Button type="button" size="sm" variant="ghost" onClick={() => setShowPw((s) => !s)}>{showPw ? <EyeOff size={14} /> : <Eye size={14} />}</Button>
            </div>
            <label className="flex items-center gap-1.5 text-xs text-muted mt-1">
              <input type="checkbox" name="generate" checked={generate} onChange={(e) => setGenerate(e.target.checked)} /> Generate a strong password
            </label>
            {!generate && weakPassword(pw) && <div className="text-xs text-warn mt-1">Weak guest password — short passwords are easier to guess. You may still save it.</div>}
          </div>
        </>
      ) : <div />}
      {planApplies && planOptions.length === 0 ? (
        // A `required` <select> with no options cannot be satisfied, so the form silently refuses to submit
        // and nothing on screen says why. On a legacy site a plan is genuinely required, so the honest answer
        // is to name the missing prerequisite rather than present a control that cannot be used.
        <div>
          <Label>Guest access plan</Label>
          <p className="text-xs text-warn mt-1.5 leading-relaxed">
            No active guest access plan exists yet. Create one on the Guest access plans screen first — on this
            site an account cannot be created without one.
          </p>
        </div>
      ) : planApplies ? (
        <div>
          <Label>Guest access plan</Label>
          <select name="template_id" required defaultValue={account?.template_id ?? undefined} className={selectCls}>
            {planOptions.map((t) => <option key={t.id} value={t.id}>{planLabel(t)}{!t.is_active ? " · inactive" : ""}</option>)}
          </select>
        </div>
      ) : (
        <div>
          <Label>Access</Label>
          <p className="text-xs text-muted mt-1.5 leading-relaxed">
            Decided by <strong>package eligibility rules</strong>, not by this account. Choose which packages a
            signed-in guest may take on the Commercial packages screen.
          </p>
        </div>
      )}
      <div><Label>Display name (optional)</Label><Input name="display_name" defaultValue={account?.display_name ?? ""} placeholder="Room 101 guest" /></div>
      <div><Label>Valid from (optional)</Label><Input name="valid_from" type="datetime-local" defaultValue={dt(account?.valid_from)} /></div>
      <div><Label>Valid until (optional)</Label><Input name="valid_until" type="datetime-local" defaultValue={dt(account?.valid_until)} /></div>
      <div className="sm:col-span-3"><Label>Notes (optional)</Label><Input name="notes" defaultValue={account?.notes ?? ""} /></div>
      <div className="sm:col-span-3 flex justify-end gap-2">
        {onCancel && <Button type="button" variant="ghost" onClick={onCancel}>Cancel</Button>}
        <Button type="submit" disabled={busy || (planApplies && planOptions.length === 0)}>
          {busy ? "Saving…" : account ? "Save changes" : "Create account"}
        </Button>
      </div>
    </form>
  );
}

function PasswordForm({ onSubmit, busy, onCancel }: { onSubmit: (e: React.FormEvent<HTMLFormElement>) => void; busy: boolean; onCancel: () => void }) {
  const [generate, setGenerate] = useState(false);
  const [pw, setPw] = useState("");
  const [showPw, setShowPw] = useState(false);
  return (
    <form onSubmit={onSubmit} className="grid grid-cols-1 sm:grid-cols-2 gap-3">
      <div>
        <Label>New password</Label>
        <div className="flex gap-1">
          <Input name="password" type={showPw ? "text" : "password"} value={pw} onChange={(e) => setPw(e.target.value)}
            disabled={generate} required={!generate} minLength={1} maxLength={128} placeholder={generate ? "auto-generated" : "any length ≥ 1"} />
          <Button type="button" size="sm" variant="ghost" onClick={() => setShowPw((s) => !s)}>{showPw ? <EyeOff size={14} /> : <Eye size={14} />}</Button>
        </div>
        <label className="flex items-center gap-1.5 text-xs text-muted mt-1">
          <input type="checkbox" name="generate" checked={generate} onChange={(e) => setGenerate(e.target.checked)} /> Generate a strong password
        </label>
        {!generate && weakPassword(pw) && <div className="text-xs text-warn mt-1">Weak guest password — short passwords are easier to guess. You may still save it.</div>}
      </div>
      <div className="flex flex-col justify-end">
        <label className="flex items-center gap-2 text-sm text-muted mb-2">
          <input type="checkbox" name="disconnect_sessions" /> Disconnect existing sessions after reset
        </label>
        <div className="flex justify-end gap-2">
          <Button type="button" variant="ghost" onClick={onCancel}>Cancel</Button>
          <Button type="submit" disabled={busy}>{busy ? "Saving…" : "Set password"}</Button>
        </div>
      </div>
    </form>
  );
}
