"use client";

import { useEffect, useMemo, useState } from "react";
import {
  api, ApiError, GuestAccount, GuestAccountCreateResp,
  GuestAccountPasswordResp, ListResp,
} from "@/lib/api";
import { PageShell, PageHeader, StatCard } from "@/components/ui/page";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label, Field } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogBody, DialogFooter,
  ConfirmDialog,
} from "@/components/ui/dialog";
import { Switch, SkeletonRows } from "@/components/ui/misc";
import { Plus, KeyRound, Copy, Check, Eye, EyeOff, Pencil, Power, Users } from "lucide-react";
import { formatRelative } from "@/lib/utils";

function weakPassword(pw: string): boolean {
  return pw.length > 0 && pw.length < 8;
}

/**
 * The one-time password reveal, shown once after create or reset. The password is NEVER retrievable afterwards.
 *
 * IT IS A MODAL NOW, and for this screen that is a correctness change rather than a presentation one. It used to
 * be a card inserted above the table: on a list of accounts the operator scrolled to find, creating an account
 * put the only copy of its password off-screen, and the next click anywhere could scroll it out of reach for good.
 * A password shown exactly once must be impossible to miss.
 */
function PasswordReveal({ username, password, onClose }: { username: string; password: string; onClose: () => void }) {
  const [copied, setCopied] = useState(false);
  const [show, setShow] = useState(true);
  return (
    <Dialog open onOpenChange={(v) => !v && onClose()}>
      <DialogContent size="sm">
        <DialogHeader>
          <DialogTitle>Password for {username}</DialogTitle>
          <DialogDescription>
            Write it down or hand it over now. It is shown this once and cannot be looked up again.
          </DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-3">
          <div className="flex items-center gap-2">
            <code className="flex-1 select-all rounded-md border border-border bg-surface px-3 py-2 font-mono text-lg">
              {show ? password : "•".repeat(password.length)}
            </code>
            <Button
              size="icon"
              variant="ghost"
              aria-label={show ? "Hide the password" : "Show the password"}
              onClick={() => setShow((s) => !s)}
            >
              {show ? <EyeOff /> : <Eye />}
            </Button>
          </div>
          <Button
            variant="secondary"
            className="w-full"
            onClick={async () => {
              try {
                await navigator.clipboard.writeText(password);
                setCopied(true);
                setTimeout(() => setCopied(false), 1500);
              } catch { /* clipboard denied — the value is on screen */ }
            }}
          >
            {copied ? <><Check /> Copied</> : <><Copy /> Copy password</>}
          </Button>
          <Callout tone="warning">
            If this is lost, there is no way to recover it — you would set a new password instead.
          </Callout>
        </DialogBody>
        <DialogFooter>
          <Button onClick={onClose}>I have it</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

export default function GuestAccountsPage() {
  const [rows, setRows] = useState<GuestAccount[] | null>(null);
  const [portalOn, setPortalOn] = useState<boolean>(false);
  const [err, setErr] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);
  const [q, setQ] = useState("");
  const [editing, setEditing] = useState<GuestAccount | null>(null);
  const [pwFor, setPwFor] = useState<GuestAccount | null>(null);
  const [reveal, setReveal] = useState<{ username: string; password: string } | null>(null);
  // The two destructive actions, held while the operator confirms. They used to be `window.confirm` calls, which
  // could not say how many devices were about to be cut off or that a disabled account is the reversible option.
  const [disconnecting, setDisconnecting] = useState<GuestAccount | null>(null);
  const [deleting, setDeleting] = useState<GuestAccount | null>(null);
  const [actionErr, setActionErr] = useState<unknown>(null);
  // THE LIST HAS THREE OUTCOMES, NOT TWO. `rows === null` was doing double duty as "still loading" and "the
  // load failed", so a failed load rendered the error banner AND "Loading…" underneath it forever -- the
  // screen contradicting itself.
  const [loadFailed, setLoadFailed] = useState(false);

  // THERE IS NO AUTHORITY DIMENSION ANY MORE.
  //
  // This screen used to carry a full second personality: an `authority` state, a legacy plan picker, a
  // four-state lifecycle for a "guest access plans" request, and prose that differed depending on which
  // authority the site turned out to be running. All of it existed because a superseded guest-IAM
  // implementation sat behind the same URLs. That implementation and its tables are gone, so the branches
  // are gone with them -- not defaulted to the IAM-v2 side and left in place, which would leave the screen
  // still able to render sentences no site can be true for.
  //
  // A credential carries no plan. What a guest may acquire is decided by PACKAGE ELIGIBILITY RULES evaluated
  // when packages are listed, which is why there is no plan control here and no plans request at all.

  async function load() {
    // Clearing both at entry matters for the RETRY path: without it a successful reload leaves the previous
    // failure's banner sitting above fresh, correct data.
    setErr(null);
    setLoadFailed(false);
    try {
      const [ga, pv] = await Promise.all([
        api.get<ListResp<GuestAccount>>("/guest-accounts"),
        api.get<{ enabled: boolean }>("/guest-accounts/portal").catch(() => ({ enabled: false })),
      ]);
      setRows(ga.data);
      setPortalOn(!!pv.enabled);
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load");
      setLoadFailed(true);
    }
  }
  useEffect(() => { load(); }, []);

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

  async function onDisconnect() {
    if (!disconnecting) return;
    setBusy(true); setActionErr(null);
    try {
      const r = await api.post<{ disconnected_sessions: number }>(`/guest-accounts/${disconnecting.id}/disconnect`);
      setMsg(`${r.disconnected_sessions} device${r.disconnected_sessions === 1 ? "" : "s"} disconnected.`);
      setDisconnecting(null);
      load();
    } catch (e) { setActionErr(e); }
    finally { setBusy(false); }
  }

  async function onDelete() {
    if (!deleting) return;
    setBusy(true); setActionErr(null);
    try {
      await api.del(`/guest-accounts/${deleting.id}`);
      setMsg(`The account "${deleting.username}" has been deleted.`);
      setDeleting(null);
      load();
    } catch (e) { setActionErr(e); }
    finally { setBusy(false); }
  }

  const locked = (a: GuestAccount) => a.locked_until && new Date(a.locked_until) > new Date();

  const totals = useMemo(() => {
    const list = rows ?? [];
    return {
      all: list.length,
      enabled: list.filter((a) => a.enabled).length,
      online: list.reduce((n, a) => n + (a.active_devices ?? 0), 0),
      locked: list.filter((a) => locked(a)).length,
    };
  }, [rows]);

  return (
    <PageShell width="wide">
      <PageHeader
        eyebrow="Guests"
        title="Guest accounts"
        description="A username and password a guest can sign in with, instead of a room number or a code. What each guest may then take is decided by package eligibility rules on the Internet packages screen, not by anything stored on the account."
        actions={
          // This button used to be disabled whenever there were no active guest access plans, which under
          // IAM-v2 meant permanently: a credential carries no plan there, so on a site with none an operator
          // could never create a guest account at all, and the only symptom was a dead button. It is no longer
          // gated on anything, and there is no longer a plan for it to be gated on.
          <Button onClick={() => { setShowNew(true); setEditing(null); setPwFor(null); }}>
            <Plus /> Add account
          </Button>
        }
      />

      <ErrorBanner err={err} />
      {msg && <Callout tone="success">{msg}</Callout>}

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatCard label="Accounts" value={rows ? totals.all.toLocaleString() : "—"} icon={<Users />} tone="primary" />
        <StatCard label="Able to sign in" value={rows ? totals.enabled.toLocaleString() : "—"} />
        <StatCard
          label="Devices online"
          value={rows ? totals.online.toLocaleString() : "—"}
          href="/sessions"
        />
        <StatCard
          label="Locked out"
          value={rows ? totals.locked.toLocaleString() : "—"}
          tone={totals.locked > 0 ? "warn" : "default"}
          hint={totals.locked > 0 ? "Too many failed sign-in attempts" : "No account is locked"}
        />
      </div>

      {/* THE PORTAL SWITCH, as a switch. It was a button whose label was its state ("On"), which reads as the
          action rather than the setting — so an operator could not tell whether pressing it would turn the tab on
          or report that it already was. */}
      <Card>
        <CardBody className="flex flex-wrap items-center justify-between gap-4">
          <div>
            <div className="text-sm font-medium">Offer username-and-password sign-in on the guest portal</div>
            <p className="mt-0.5 text-xs text-muted-foreground">
              {portalOn
                ? "Guests see a Username & Password tab and can sign in with these accounts."
                : "The tab is hidden, so these accounts cannot be used even though they exist."}
            </p>
          </div>
          <Switch checked={portalOn} onCheckedChange={onTogglePortal} label="Offer username-and-password sign-in" />
        </CardBody>
      </Card>

      {reveal && <PasswordReveal username={reveal.username} password={reveal.password} onClose={() => setReveal(null)} />}

      <Card>
        <CardBody className="border-b border-border py-3">
          <Input
            value={q}
            onChange={(e) => setQ(e.target.value)}
            placeholder="Search username or name…"
            aria-label="Search guest accounts"
            className="max-w-xs"
          />
        </CardBody>
        <CardBody className="p-0">
          {loadFailed ? (
            // A failed load is not an empty list and not a slow one. Saying so, and offering the one action
            // that can help, beats a spinner that will never finish.
            <EmptyState
              title="Could not load guest accounts"
              hint="The appliance did not answer. Nothing has been changed."
              action={<Button onClick={() => void load()}>Try again</Button>}
            />
          ) : rows === null ? (
            <SkeletonRows rows={5} cols={6} />
          ) : filtered.length === 0 ? (
            <EmptyState
              icon={<KeyRound />}
              title={q ? "No account matches that search" : "No guest accounts"}
              hint={q ? undefined : "Create one to let a guest sign in with a username and password."}
              action={q ? undefined : <Button onClick={() => setShowNew(true)}><Plus /> Add the first account</Button>}
            />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Account</TH><TH>Devices</TH><TH>Status</TH><TH>Valid until</TH>
                  <TH>Last sign-in</TH><TH className="text-right">Sign-ins</TH><TH />
                </TR>
              </THead>
              <tbody>
                {filtered.map((a) => {
                  const cap = a.max_devices;
                  const atCap = cap ? (a.active_devices ?? 0) >= cap : false;
                  return (
                    <TR key={a.id}>
                      <TD>
                        <div className="font-mono text-sm font-medium">{a.username}</div>
                        {a.display_name && (
                          <div className="text-xs text-muted-foreground">{a.display_name}</div>
                        )}
                      </TD>
                      <TD className={atCap ? "text-warning-subtle-foreground" : "text-muted-foreground"}>
                        <span className="tabular">{a.active_devices ?? 0}</span>
                        {cap ? <span className="tabular"> / {cap}</span> : ""}
                        {atCap && <div className="text-2xs">At the limit</div>}
                      </TD>
                      <TD>
                        <Badge tone={a.enabled ? "ok" : "err"} dot>
                          {a.enabled ? "Can sign in" : "Disabled"}
                        </Badge>
                        {locked(a) && (
                          <div className="mt-0.5">
                            <Badge tone="warn">Locked until {formatRelative(a.locked_until)}</Badge>
                          </div>
                        )}
                      </TD>
                      <TD className="text-sm text-muted-foreground">
                        {a.valid_until ? formatRelative(a.valid_until) : "No end date"}
                      </TD>
                      <TD className="text-sm text-muted-foreground">
                        {a.last_login_at ? formatRelative(a.last_login_at) : "Never"}
                      </TD>
                      <TD className="text-right tabular text-muted-foreground">{a.login_count}</TD>
                      <TD className="whitespace-nowrap text-right">
                        <Button size="sm" variant="ghost" onClick={() => { setEditing(a); setPwFor(null); setShowNew(false); }}>
                          <Pencil /> Edit
                        </Button>
                        <Button size="sm" variant="ghost" onClick={() => { setPwFor(a); setEditing(null); setShowNew(false); }}>
                          <KeyRound /> Password
                        </Button>
                        <Button size="sm" variant="ghost" onClick={() => onToggle(a)}>
                          {a.enabled ? "Disable" : "Enable"}
                        </Button>
                        {(a.active_devices ?? 0) > 0 && (
                          <Button size="sm" variant="ghost" onClick={() => { setActionErr(null); setDisconnecting(a); }}>
                            <Power /> Disconnect
                          </Button>
                        )}
                        <Button size="sm" variant="ghost" onClick={() => { setActionErr(null); setDeleting(a); }}>
                          Delete
                        </Button>
                      </TD>
                    </TR>
                  );
                })}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      {/* ------------------------------------------------------------------ create / edit / password */}
      <Dialog open={showNew} onOpenChange={(v) => !v && setShowNew(false)}>
        <DialogContent size="lg">
          <DialogHeader>
            <DialogTitle>Add a guest account</DialogTitle>
            <DialogDescription>
              The guest signs in with this username and password. The password is shown once, after you save.
            </DialogDescription>
          </DialogHeader>
          <DialogBody>
            <AccountForm onSubmit={onCreate} busy={busy} withPassword onCancel={() => setShowNew(false)} />
          </DialogBody>
        </DialogContent>
      </Dialog>

      <Dialog open={editing !== null} onOpenChange={(v) => !v && setEditing(null)}>
        <DialogContent size="lg">
          <DialogHeader>
            <DialogTitle>Edit {editing?.username}</DialogTitle>
            <DialogDescription>
              Changing the username does not disconnect anyone who is already online.
            </DialogDescription>
          </DialogHeader>
          <DialogBody>
            {editing && (
              <AccountForm account={editing} onSubmit={onSaveEdit} busy={busy} onCancel={() => setEditing(null)} />
            )}
          </DialogBody>
        </DialogContent>
      </Dialog>

      <Dialog open={pwFor !== null} onOpenChange={(v) => !v && setPwFor(null)}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>Set a new password for {pwFor?.username}</DialogTitle>
            <DialogDescription>
              The new password is shown once, after you save. The old one stops working immediately.
            </DialogDescription>
          </DialogHeader>
          <DialogBody>
            <PasswordForm onSubmit={onSavePassword} busy={busy} onCancel={() => setPwFor(null)} />
          </DialogBody>
        </DialogContent>
      </Dialog>

      {/* ------------------------------------------------------------------ destructive confirmations */}
      <ConfirmDialog
        open={disconnecting !== null}
        onOpenChange={(v) => !v && setDisconnecting(null)}
        title="Disconnect this account's devices?"
        description={
          disconnecting
            ? `All ${disconnecting.active_devices ?? 0} device${(disconnecting.active_devices ?? 0) === 1 ? "" : "s"} signed in as "${disconnecting.username}" will lose internet access immediately. The account still works, so they can sign in again.`
            : undefined
        }
        confirmLabel="Disconnect devices"
        confirmVariant="danger"
        busy={busy}
        error={actionErr}
        onConfirm={onDisconnect}
      />

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(v) => !v && setDeleting(null)}
        title="Delete this guest account?"
        description={
          deleting
            ? `"${deleting.username}" will be removed permanently and anyone using it will be disconnected. This cannot be undone — if you only want to stop it being used for now, Disable it instead.`
            : undefined
        }
        confirmLabel="Delete permanently"
        confirmVariant="danger"
        busy={busy}
        error={actionErr}
        onConfirm={onDelete}
      />
    </PageShell>
  );
}

// AccountForm is shared by create and edit. `withPassword` shows the create-time
// password controls; edit uses the separate Set-password panel.
function AccountForm({ account, onSubmit, busy, withPassword, onCancel }: {
  account?: GuestAccount;
  onSubmit: (e: React.FormEvent<HTMLFormElement>) => void; busy: boolean; withPassword?: boolean; onCancel?: () => void;
}) {
  const [generate, setGenerate] = useState(false);
  const [pw, setPw] = useState("");
  const [showPw, setShowPw] = useState(false);
  const dt = (s?: string | null) => (s ? new Date(s).toISOString().slice(0, 16) : "");

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Username" required hint="What the guest types. Short is fine — even a single character.">
          <Input name="username" required minLength={1} maxLength={64} defaultValue={account?.username} placeholder="room101" />
        </Field>
        <Field label="Name" hint="For your own reference; the guest never sees it.">
          <Input name="display_name" defaultValue={account?.display_name ?? ""} placeholder="Room 101 guest" />
        </Field>
      </div>

      {withPassword && (
        <div className="rounded-md border border-border bg-surface/50 p-3.5">
          <Field
            label="Password"
            required={!generate}
            error={!generate && weakPassword(pw) ? "Short passwords are easy to guess. You can still save it." : undefined}
          >
            <div className="flex gap-1.5">
              <Input
                name="password"
                type={showPw ? "text" : "password"}
                autoComplete="new-password"
                value={pw}
                onChange={(e) => setPw(e.target.value)}
                disabled={generate}
                required={!generate}
                minLength={1}
                maxLength={128}
                placeholder={generate ? "Generated when you save" : ""}
              />
              <Button
                type="button"
                size="icon"
                variant="ghost"
                aria-label={showPw ? "Hide the password" : "Show the password"}
                onClick={() => setShowPw((s) => !s)}
              >
                {showPw ? <EyeOff /> : <Eye />}
              </Button>
            </div>
          </Field>
          <label className="mt-2 flex items-center gap-2 text-xs text-muted-foreground">
            <input
              type="checkbox"
              name="generate"
              checked={generate}
              onChange={(e) => setGenerate(e.target.checked)}
              className="size-3.5 accent-[hsl(var(--primary))]"
            />
            Generate a strong password instead
          </label>
        </div>
      )}

      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Valid from" hint="Leave empty to work immediately.">
          <Input name="valid_from" type="datetime-local" defaultValue={dt(account?.valid_from)} />
        </Field>
        <Field label="Valid until" hint="Leave empty for no end date.">
          <Input name="valid_until" type="datetime-local" defaultValue={dt(account?.valid_until)} />
        </Field>
      </div>

      <Field label="Notes">
        <Input name="notes" defaultValue={account?.notes ?? ""} placeholder="Optional" />
      </Field>

      {/*
        WHERE THE PLAN CONTROL USED TO BE.

        An account carries no plan, so there is nothing to choose here and nothing to wait for. This block
        used to be a five-way branch -- authority unknown, plans loading, plans failed, plans empty, plans
        present -- every arm of which existed to describe a superseded prerequisite. It says the one thing
        that is now true instead.
      */}
      <Callout tone="neutral" title="What this guest can take">
        Decided by the eligibility rules on each internet package, not by anything stored on the account. The
        licence capacity is appliance-wide.
      </Callout>

      <div className="flex justify-end gap-2 border-t border-border pt-4">
        {onCancel && <Button type="button" variant="ghost" onClick={onCancel}>Cancel</Button>}
        <Button type="submit" disabled={busy}>
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
    <form onSubmit={onSubmit} className="space-y-4">
      <Field
        label="New password"
        required={!generate}
        error={!generate && weakPassword(pw) ? "Short passwords are easy to guess. You can still save it." : undefined}
      >
        <div className="flex gap-1.5">
          <Input
            name="password"
            type={showPw ? "text" : "password"}
            autoComplete="new-password"
            value={pw}
            onChange={(e) => setPw(e.target.value)}
            disabled={generate}
            required={!generate}
            minLength={1}
            maxLength={128}
            placeholder={generate ? "Generated when you save" : ""}
          />
          <Button
            type="button"
            size="icon"
            variant="ghost"
            aria-label={showPw ? "Hide the password" : "Show the password"}
            onClick={() => setShowPw((s) => !s)}
          >
            {showPw ? <EyeOff /> : <Eye />}
          </Button>
        </div>
      </Field>
      <label className="flex items-center gap-2 text-xs text-muted-foreground">
        <input
          type="checkbox"
          name="generate"
          checked={generate}
          onChange={(e) => setGenerate(e.target.checked)}
          className="size-3.5 accent-[hsl(var(--primary))]"
        />
        Generate a strong password instead
      </label>

      <label className="flex items-start gap-2 rounded-md border border-border bg-surface/50 px-3.5 py-2.5 text-sm">
        <input type="checkbox" name="disconnect_sessions" className="mt-0.5 size-3.5 accent-[hsl(var(--primary))]" />
        <span>
          Disconnect this account&rsquo;s devices now
          <span className="block text-xs text-muted-foreground">
            Without this, devices already online stay connected on the old password until their access ends.
          </span>
        </span>
      </label>

      <div className="flex justify-end gap-2 border-t border-border pt-4">
        <Button type="button" variant="ghost" onClick={onCancel}>Cancel</Button>
        <Button type="submit" disabled={busy}>{busy ? "Saving…" : "Set password"}</Button>
      </div>
    </form>
  );
}
