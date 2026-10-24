"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import { Download, KeyRound, Mail, Plus, Smartphone, UserCheck, UserMinus, Users } from "lucide-react";
import { api, ApiError, itemsOf, withStepUp, type CentralUser, type Items } from "@/lib/api";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { Field, Input, Select, useFieldErrors } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { ConfirmDialog, DialogForm } from "@/components/ui/dialog";
import { SkeletonRows } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { RowActions, type RowAction } from "@/components/ui/row-actions";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { DescriptionList } from "@/components/ui/data";
import { StateBadge } from "@/components/status-badge";
import { roleLabel } from "@/lib/permissions";
import { ago, formatDateTime, userStatusInfo } from "@/lib/status";

const MIN_PASSWORD = 10;
// On a phone the inline row action moves to the details drawer, so the menu stays on screen.
const INLINE_FROM_SM = "max-sm:[&>a]:hidden max-sm:[&>button:not([aria-haspopup])]:hidden";

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
  const [selectedId, setSelectedId] = useState<string | null>(null);
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
  const selected = rows?.find((u) => u.id === selectedId) ?? null;
  const enable = (u: CentralUser) => run(() => api.patch(`${base}/${u.id}`, { status: "active" }), "Sign-in enabled", u.email);

  function actionsFor(u: CentralUser): RowAction[] {
    return [
      { label: "Set a new password", icon: <KeyRound />, hidden: !canResetPassword, onSelect: () => open({ kind: "password", u }) },
      { label: "Correct the email", icon: <Mail />, onSelect: () => open({ kind: "email", u }) },
      { label: "Reset two-step sign-in", icon: <Smartphone />, hidden: !u.two_step_enrolled, onSelect: () => open({ kind: "mfa", u }) },
      { label: "Download their data", icon: <Download />, hidden: !canExportData, onSelect: () => open({ kind: "export", u }) },
      u.status === "disabled"
        ? { label: "Enable sign-in", icon: <UserCheck />, disabled: busy, onSelect: () => void enable(u) }
        : { label: "Disable sign-in", icon: <UserMinus />, danger: true, onSelect: () => open({ kind: "disable", u }) },
    ];
  }

  return (
    <>
      <div className="flex min-h-12 flex-wrap items-center justify-between gap-3 border-b border-border px-4 py-2.5">
        <span className="text-sm text-muted-foreground">
          {rows ? `${rows.length} ${rows.length === 1 ? noun : `${noun}s`}` : " "}
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
        <EmptyState size="sm" icon={<Users />} title={`No ${noun}s yet`} />
      ) : (
        <Table aria-label={`${noun}s`}>
          <THead>
            <TR>
              <TH>Email</TH>
              <TH className="hidden sm:table-cell">Role</TH>
              <TH className="hidden md:table-cell">Two-step</TH>
              <TH className="hidden sm:table-cell">Status</TH>
              <TH className="hidden lg:table-cell">Last sign-in</TH>
              {canManage && <TH className="w-0"><span className="sr-only">Actions</span></TH>}
            </TR>
          </THead>
          <TBody>
            {sorted.map((u) => {
              const self = u.id === meId;
              const names = roleNames(u);
              return (
                <TR key={u.id} onClick={() => setSelectedId(u.id)}>
                  <TD className="max-w-[16rem]">
                    <div className="flex min-w-0 items-center gap-1.5">
                      <span className="truncate font-medium" title={u.email}>{u.email}</span>
                      {self && <Badge tone="accent" size="sm">You</Badge>}
                    </div>
                    {u.display_name && <div className="truncate text-xs text-muted-foreground">{u.display_name}</div>}
                    {u.status !== "active" && <StateBadge info={userStatusInfo(u.status)} className="mt-1 sm:hidden" />}
                  </TD>
                  <TD className="hidden sm:table-cell">
                    <div className="flex flex-wrap gap-1">
                      {names.length ? names.map((r) => <Badge key={r} tone="neutral" size="sm">{roleLabel(r)}</Badge>) : "—"}
                    </div>
                  </TD>
                  <TD nowrap className="hidden md:table-cell">
                    {u.status === "active" && typeof u.two_step_enrolled === "boolean"
                      ? <TwoStepBadge on={u.two_step_enrolled} />
                      : <span className="text-muted-foreground">—</span>}
                  </TD>
                  <TD nowrap className="hidden sm:table-cell"><StateBadge info={userStatusInfo(u.status)} /></TD>
                  <TD nowrap muted className="hidden lg:table-cell">{u.last_login_at ? ago(u.last_login_at) : "Never"}</TD>
                  {canManage && (
                    <TD nowrap className="w-0">
                      {!self && (
                        <RowActions
                          subject={u.email}
                          className={INLINE_FROM_SM}
                          primary={{ label: "Change role", onSelect: () => open({ kind: "role", u }) }}
                          items={actionsFor(u)}
                        />
                      )}
                    </TD>
                  )}
                </TR>
              );
            })}
          </TBody>
        </Table>
      )}

      <DetailsDrawer
        open={selected !== null}
        onOpenChange={(v) => { if (!v) setSelectedId(null); }}
        eyebrow={`${noun[0].toUpperCase()}${noun.slice(1)}`}
        title={selected?.email ?? ""}
        description={selected?.display_name || undefined}
        badges={selected && (
          <>
            <StateBadge info={userStatusInfo(selected.status)} />
            {selected.id === meId && <Badge tone="accent">You</Badge>}
          </>
        )}
        actions={selected && canManage && selected.id !== meId && (
          <>
            {selected.status === "disabled" ? (
              <Button variant="secondary" className="me-auto" loading={busy} onClick={() => void enable(selected)}>
                Enable sign-in
              </Button>
            ) : (
              <Button variant="danger-outline" className="me-auto" onClick={() => open({ kind: "disable", u: selected })}>
                Disable sign-in
              </Button>
            )}
            <Button variant="secondary" onClick={() => open({ kind: "role", u: selected })}>Change role</Button>
          </>
        )}
      >
        {selected && (
          <div className="space-y-6">
            <DetailsSection title="Account">
              <DescriptionList
                items={[
                  { label: "Email", value: selected.email },
                  { label: "Name", value: selected.display_name || undefined },
                  { label: "Role", value: roleNames(selected).map(roleLabel).join(", ") || undefined },
                  {
                    label: "Two-step sign-in",
                    value: typeof selected.two_step_enrolled === "boolean" ? <TwoStepBadge on={selected.two_step_enrolled} /> : undefined,
                  },
                  { label: "Last sign-in", value: selected.last_login_at ? ago(selected.last_login_at) : "Never" },
                  { label: "Added", value: selected.created_at ? formatDateTime(selected.created_at) : undefined, hidden: !selected.created_at },
                ]}
              />
            </DetailsSection>
            {canManage && selected.id !== meId && (
              <DetailsSection title="More actions">
                <div className="flex flex-wrap gap-2">
                  {actionsFor(selected).filter((a) => !a.hidden && !a.danger && a.label !== "Enable sign-in").map((a) => (
                    <Button key={a.label} size="sm" variant="secondary" disabled={a.disabled} onClick={a.onSelect}>
                      {a.icon}{a.label}
                    </Button>
                  ))}
                </div>
              </DetailsSection>
            )}
          </div>
        )}
      </DetailsDrawer>

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

function TwoStepBadge({ on }: { on: boolean }) {
  return <StatusBadge tone={on ? "ok" : "default"}>{on ? "On" : "Off"}</StatusBadge>;
}
