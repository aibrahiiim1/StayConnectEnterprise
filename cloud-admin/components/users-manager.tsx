"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { Download, KeyRound, Mail, Plus, Smartphone, UserCheck, UserMinus, UserPen, Users } from "lucide-react";
import { api, ApiError, itemsOf, withStepUp, type CentralUser, type Items } from "@/lib/api";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Field, Input, Select, useFieldErrors } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { ConfirmDialog, DialogForm } from "@/components/ui/dialog";
import { SkeletonRows } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { StateBadge } from "@/components/status-badge";
import { roleLabel } from "@/lib/permissions";
import { ago, userStatusInfo } from "@/lib/status";

const MIN_PASSWORD = 10;

type UserField = "email" | "password" | "reason";

/** Only known email or password refusals attach to fields; other failures stay in the banner. */
function fieldOfCreate(e: ApiError): UserField | null {
  if (e.status === 409) return "email";
  if (e.status === 400 && /email/i.test(e.message)) return "email";
  if (e.status === 400 && /password/i.test(e.message)) return "password";
  return null;
}

function saveJSON(doc: unknown, name: string) {
  const url = URL.createObjectURL(new Blob([JSON.stringify(doc, null, 2)], { type: "application/json" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

export function roleNames(u: { roles?: unknown }): string[] {
  const rs = Array.isArray(u.roles) ? u.roles : [];
  return rs.map((r) => (typeof r === "string" ? r : (r as { role?: string })?.role ?? "")).filter(Boolean);
}

/** Disabling preserves an account and audit trail; removing deletes the account. */
export function UsersManager({
  base,
  roles,
  canManage,
  meId,
  canResetPassword,
  canExportData = false,
  noun = "user",
}: {
  base: string;
  roles: readonly string[];
  canManage: boolean;
  meId: string;
  /** Only platform Team users can be reset here. */
  canResetPassword: boolean;
  /** Personal-data export is restricted to platform Team management. */
  canExportData?: boolean;
  noun?: string;
}) {
  const toast = useToast();
  const [rows, setRows] = useState<CentralUser[] | null>(null);
  const [err, setErr] = useState<unknown>(null);

  const load = useCallback(async () => {
    try {
      const r = await api.get<Items<CentralUser>>(base);
      setRows(itemsOf(r));
      setErr(null);
    } catch (e) {
      setErr(e);
      setRows((p) => p ?? []);
    }
  }, [base]);
  useEffect(() => { void load(); }, [load]);

  const [dialog, setDialog] = useState<
    | { kind: "new" }
    | { kind: "role"; u: CentralUser }
    | { kind: "password"; u: CentralUser }
    | { kind: "mfa"; u: CentralUser }
    | { kind: "email"; u: CentralUser }
    | { kind: "export"; u: CentralUser }
    | { kind: "disable"; u: CentralUser }
    | { kind: "remove"; u: CentralUser }
    | null
  >(null);
  const [busy, setBusy] = useState(false);
  const [dErr, setDErr] = useState<unknown>(null);
  const [form, setForm] = useState({ email: "", name: "", password: "", role: roles[roles.length - 1] as string });
  const [mfaReason, setMfaReason] = useState("");
  const fields = useFieldErrors<UserField>();

  function open(d: NonNullable<typeof dialog>) {
    setDErr(null);
    fields.clear();
    setMfaReason("");
    setForm({
      email: d.kind === "email" ? d.u.email : "",
      name: "",
      password: "",
      role: d.kind === "role" ? roleNames(d.u)[0] ?? roles[0] : roles[roles.length - 1],
    });
    setDialog(d);
  }

  /** Focus the specific field for recognized validation refusals. */
  async function run(fn: () => Promise<unknown>, done: string, detail?: string, fieldOf?: (e: ApiError) => UserField | null) {
    setBusy(true);
    setDErr(null);
    fields.clear();
    try {
      // Sign-in changes require recent password confirmation.
      await withStepUp(fn);
      toast.success(done, detail);
      setDialog(null);
      await load();
    } catch (e) {
      const field = e instanceof ApiError && fieldOf ? fieldOf(e) : null;
      if (field && e instanceof ApiError) fields.fail({ [field]: e.message });
      else setDErr(e);
    } finally {
      setBusy(false);
    }
  }

  const sorted = useMemo(() => [...(rows ?? [])].sort((a, b) => a.email.localeCompare(b.email)), [rows]);

  return (
    <>
      <div className="flex items-center justify-between gap-3 border-b border-border px-4 py-3">
        <span className="text-sm text-muted-foreground">
          {rows ? `${rows.length} ${rows.length === 1 ? noun : `${noun}s`}` : " "}
        </span>
        {canManage && (
          <Button size="sm" onClick={() => open({ kind: "new" })}>
            <Plus /> Add {noun}
          </Button>
        )}
      </div>
      <ErrorBanner err={err} className="m-4" />
      {rows === null ? (
        <SkeletonRows rows={3} cols={4} />
      ) : rows.length === 0 ? (
        <EmptyState icon={<Users />} title={`No ${noun}s yet`} />
      ) : (
        <Table aria-label={`${noun}s`}>
          <THead>
            <TR>
              <TH>Email</TH><TH>Role</TH><TH>Status</TH><TH className="hidden md:table-cell">Last sign-in</TH>
              <TH><span className="sr-only">Actions</span></TH>
            </TR>
          </THead>
          <tbody>
            {sorted.map((u) => {
              const self = u.id === meId;
              const names = roleNames(u);
              return (
                <TR key={u.id}>
                  <TD>
                    <div className="flex flex-wrap items-center gap-1.5">
                      <span className="text-sm font-medium">{u.email}</span>
                      {self && <Badge tone="accent">You</Badge>}
                    </div>
                    {u.display_name && <div className="text-caption text-muted-foreground">{u.display_name}</div>}
                    {u.status === "active" && typeof u.two_step_enrolled === "boolean" && (
                      <Badge tone={u.two_step_enrolled ? "ok" : "neutral"} className="mt-1">
                        {u.two_step_enrolled ? "Two-step on" : "Two-step off"}
                      </Badge>
                    )}
                  </TD>
                  <TD>
                    <div className="flex flex-wrap gap-1">
                      {names.length ? names.map((r) => <Badge key={r} tone="neutral">{roleLabel(r)}</Badge>) : "—"}
                    </div>
                  </TD>
                  <TD><StateBadge info={userStatusInfo(u.status)} /></TD>
                  <TD className="hidden text-muted-foreground md:table-cell">{u.last_login_at ? ago(u.last_login_at) : "Never"}</TD>
                  <TD>
                    {canManage && !self && (
                      <div className="flex justify-end gap-1">
                        <Button size="sm" variant="ghost" onClick={() => open({ kind: "role", u })}>
                          <UserPen /> <span className="hidden sm:inline">Role</span>
                          <span className="sr-only"> of {u.email}</span>
                        </Button>
                        {canResetPassword && (
                          <Button size="sm" variant="ghost" onClick={() => open({ kind: "password", u })}>
                            <KeyRound /> <span className="hidden sm:inline">Password</span>
                            <span className="sr-only"> of {u.email}</span>
                          </Button>
                        )}
                        <Button size="sm" variant="ghost" onClick={() => open({ kind: "email", u })}>
                          <Mail /> <span className="hidden sm:inline">Email</span>
                          <span className="sr-only"> of {u.email}</span>
                        </Button>
                        {u.two_step_enrolled && (
                          <Button size="sm" variant="ghost" onClick={() => open({ kind: "mfa", u })}>
                            <Smartphone /> <span className="hidden sm:inline">Reset two-step</span>
                            <span className="sr-only"> for {u.email}</span>
                          </Button>
                        )}
                        {canExportData && (
                          <Button size="sm" variant="ghost" onClick={() => open({ kind: "export", u })}>
                            <Download /> <span className="hidden sm:inline">Data</span>
                            <span className="sr-only"> of {u.email}</span>
                          </Button>
                        )}
                        {u.status === "disabled" ? (
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => run(() => api.patch(`${base}/${u.id}`, { status: "active" }), "Sign-in enabled", u.email)}
                          >
                            <UserCheck /> <span className="hidden sm:inline">Enable</span>
                            <span className="sr-only"> {u.email}</span>
                          </Button>
                        ) : (
                          <Button size="sm" variant="ghost" className="text-destructive hover:text-destructive" onClick={() => open({ kind: "disable", u })}>
                            <UserMinus /> <span className="hidden sm:inline">Disable</span>
                            <span className="sr-only"> {u.email}</span>
                          </Button>
                        )}
                      </div>
                    )}
                  </TD>
                </TR>
              );
            })}
          </tbody>
        </Table>
      )}

      <DialogForm
        open={dialog?.kind === "new"}
        onOpenChange={(v) => { if (!v) setDialog(null); }}
        title={`Add ${noun}`}
        description="They sign in to Central with this email and password."
        submitLabel={`Add ${noun}`}
        busyLabel="Adding…"
        busy={busy}
        error={dErr}
        disabled={!form.email.trim() || form.password.length < MIN_PASSWORD}
        disabledReason={!form.email.trim()
          ? `To continue, enter an email${form.password.length < MIN_PASSWORD ? ` and a password of at least ${MIN_PASSWORD} characters` : ""}.`
          : `To continue, enter a password of at least ${MIN_PASSWORD} characters.`}
        onSubmit={() =>
          run(
            () => api.post(base, { email: form.email.trim(), display_name: form.name.trim() || undefined, password: form.password, role: form.role }),
            `${noun[0].toUpperCase()}${noun.slice(1)} added`,
            form.email.trim(),
            fieldOfCreate,
          )
        }
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Email" required error={fields.error("email")}>
            <Input id={fields.id("email")} type="email" autoComplete="off" value={form.email} onChange={(e) => setForm({ ...form, email: e.target.value })} />
          </Field>
          <Field label="Name">
            <Input value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
          </Field>
          <Field label="Initial password" required hint={`At least ${MIN_PASSWORD} characters.`} error={fields.error("password")}>
            <Input id={fields.id("password")} type="password" autoComplete="new-password" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} />
          </Field>
          <Field label="Role" required>
            <Select value={form.role} onChange={(e) => setForm({ ...form, role: e.target.value })}>
              {roles.map((r) => <option key={r} value={r}>{roleLabel(r)}</option>)}
            </Select>
          </Field>
        </div>
      </DialogForm>

      <DialogForm
        open={dialog?.kind === "role"}
        onOpenChange={(v) => { if (!v) setDialog(null); }}
        title="Change role"
        description={dialog?.kind === "role" ? dialog.u.email : undefined}
        size="sm"
        submitLabel="Save role"
        busyLabel="Saving…"
        busy={busy}
        error={dErr}
        onSubmit={() => {
          if (dialog?.kind !== "role") return;
          const u = dialog.u;
          return run(() => api.patch(`${base}/${u.id}`, { roles: [form.role] }), "Role changed", u.email);
        }}
      >
        <Field label="Role" required hint="Takes effect the next time they sign in.">
          <Select value={form.role} onChange={(e) => setForm({ ...form, role: e.target.value })}>
            {roles.map((r) => <option key={r} value={r}>{roleLabel(r)}</option>)}
          </Select>
        </Field>
      </DialogForm>

      <DialogForm
        open={dialog?.kind === "email"}
        onOpenChange={(v) => { if (!v) setDialog(null); }}
        title="Correct the email"
        description="They sign in with the new address from now on, and are signed out. The history then shows the corrected address."
        size="sm"
        submitLabel="Save email"
        busyLabel="Saving…"
        busy={busy}
        error={dErr}
        disabled={!form.email.trim().includes("@")}
        disabledReason="To continue, enter a valid email address."
        onSubmit={() => {
          if (dialog?.kind !== "email") return;
          const u = dialog.u;
          return run(() => api.patch(`${base}/${u.id}`, { email: form.email.trim() }), "Email corrected", form.email.trim(),
            (e) => (e.status === 400 || e.status === 409 ? "email" : null));
        }}
      >
        <Field label="Email" required error={fields.error("email")}>
          <Input id={fields.id("email")} type="email" autoComplete="off" value={form.email} onChange={(e) => setForm({ ...form, email: e.target.value })} />
        </Field>
      </DialogForm>

      <DialogForm
        open={dialog?.kind === "export"}
        onOpenChange={(v) => { if (!v) setDialog(null); }}
        title="Download this person's data"
        description={dialog?.kind === "export"
          ? `Everything Central holds about ${dialog.u.email}, as a file to hand to them when they ask. The download is recorded.`
          : undefined}
        size="sm"
        submitLabel="Download"
        busyLabel="Preparing…"
        busy={busy}
        error={dErr}
        disabled={mfaReason.trim().length < 4}
        disabledReason="To continue, enter a reason of at least 4 characters."
        onSubmit={() => {
          if (dialog?.kind !== "export") return;
          const u = dialog.u;
          return run(async () => {
            const doc = await api.post<unknown>(`/cloud/v1/users/${u.id}/data-export`, { reason: mfaReason.trim() });
            saveJSON(doc, `central-user-${u.id.slice(0, 8)}.json`);
          }, "Data downloaded", u.email, (e) => (e.status === 400 ? "reason" : null));
        }}
      >
        <Field label="Reason" required hint="Recorded with the download, for example: their written request of 19 October." error={fields.error("reason")}>
          <Input id={fields.id("reason")} value={mfaReason} maxLength={500} onChange={(e) => setMfaReason(e.target.value)} />
        </Field>
      </DialogForm>

      <DialogForm
        open={dialog?.kind === "password"}
        onOpenChange={(v) => { if (!v) setDialog(null); }}
        title="Set a new password"
        description={dialog?.kind === "password" ? dialog.u.email : undefined}
        size="sm"
        submitLabel="Set password"
        busyLabel="Saving…"
        busy={busy}
        error={dErr}
        disabled={form.password.length < MIN_PASSWORD}
        disabledReason={`To continue, enter a password of at least ${MIN_PASSWORD} characters.`}
        onSubmit={() => {
          if (dialog?.kind !== "password") return;
          const u = dialog.u;
          return run(() => api.post(`${base}/${u.id}/password`, { password: form.password }), "Password changed", u.email,
            (e) => (e.status === 400 ? "password" : null));
        }}
      >
        <Field label="New password" required hint={`At least ${MIN_PASSWORD} characters. ${form.password.length}/${MIN_PASSWORD}`} error={fields.error("password")}>
          <Input id={fields.id("password")} type="password" autoComplete="new-password" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} />
        </Field>
      </DialogForm>

      <DialogForm
        open={dialog?.kind === "mfa"}
        onOpenChange={(v) => { if (!v) setDialog(null); }}
        title="Reset two-step sign-in"
        description={dialog?.kind === "mfa"
          ? `${dialog.u.email}: their authenticator app and recovery codes stop working and they are signed out. They then sign in with the password alone until they turn two-step sign-in on again.`
          : undefined}
        size="sm"
        submitLabel="Reset two-step sign-in"
        busyLabel="Resetting…"
        busy={busy}
        error={dErr}
        disabled={mfaReason.trim().length < 4}
        disabledReason="To continue, enter a reason of at least 4 characters."
        onSubmit={() => {
          if (dialog?.kind !== "mfa") return;
          const u = dialog.u;
          return run(() => api.post(`${base}/${u.id}/mfa-reset`, { reason: mfaReason.trim() }), "Two-step sign-in reset", u.email,
            (e) => (e.status === 400 ? "reason" : null));
        }}
      >
        <Field label="Reason" required hint="Recorded in the audit log, for example: lost their phone." error={fields.error("reason")}>
          <Input id={fields.id("reason")} value={mfaReason} maxLength={500} onChange={(e) => setMfaReason(e.target.value)} />
        </Field>
      </DialogForm>

      <ConfirmDialog
        open={dialog?.kind === "disable"}
        onOpenChange={(v) => { if (!v) setDialog(null); }}
        title={`Disable this ${noun}?`}
        description={dialog?.kind === "disable" ? dialog.u.email : undefined}
        confirmLabel="Disable sign-in"
        confirmVariant="danger"
        busy={busy}
        error={dErr}
        consequences={["They can no longer sign in to Central.", "You can enable them again later."]}
        onConfirm={() => {
          if (dialog?.kind !== "disable") return;
          const u = dialog.u;
          return run(() => api.patch(`${base}/${u.id}`, { status: "disabled" }), "Sign-in disabled", u.email);
        }}
      >
        {dialog?.kind === "disable" && (
          <p className="text-caption text-muted-foreground">
            To remove the account entirely,{" "}
            <button
              type="button"
              className="font-medium text-destructive underline underline-offset-2"
              onClick={() => setDialog({ kind: "remove", u: dialog.u })}
            >
              remove it instead
            </button>
            .
          </p>
        )}
      </ConfirmDialog>

      <ConfirmDialog
        open={dialog?.kind === "remove"}
        onOpenChange={(v) => { if (!v) setDialog(null); }}
        title={`Remove this ${noun}?`}
        description={dialog?.kind === "remove" ? dialog.u.email : undefined}
        confirmLabel={`Remove ${noun}`}
        confirmVariant="danger"
        busy={busy}
        error={dErr}
        consequences={["The account is deleted and cannot sign in.", "Its past actions stay in the audit log.", "It cannot be undone."]}
        confirmText={dialog?.kind === "remove" ? dialog.u.email : undefined}
        confirmTextLabel="Type the email"
        onConfirm={() => {
          if (dialog?.kind !== "remove") return;
          const u = dialog.u;
          return run(() => api.del(`${base}/${u.id}`), `${noun[0].toUpperCase()}${noun.slice(1)} removed`, u.email);
        }}
      />
    </>
  );
}
