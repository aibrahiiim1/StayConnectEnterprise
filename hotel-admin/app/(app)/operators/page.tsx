"use client";

import { useEffect, useState } from "react";
import { api, ListResp, Whoami, EdgeOperator } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { Card } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button, LinkButton } from "@/components/ui/button";
import { Input, Field, Select } from "@/components/ui/input";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { DialogForm, ConfirmDialog } from "@/components/ui/dialog";
import { SkeletonRows } from "@/components/ui/misc";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { RowActions } from "@/components/ui/row-actions";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { DescriptionList } from "@/components/ui/data";
import { Plus, Smartphone, UserCog, UserMinus, Users, X } from "lucide-react";
import { canWrite, SITE_ROLES, ROLE_LABELS, SiteRole } from "@/lib/roles";
import { formatRelative } from "@/lib/utils";
import { HelpList, HelpSection } from "@/components/help";
import { OperatorSigninProtection } from "@/components/operator-signin-protection";
import { OperatorSessionLimits } from "@/components/operator-session-limits";
import { OperatorAccountRetention } from "@/components/operator-account-retention";

const MIN_PASSWORD = 10;

export default function OperatorsPage() {
  const toast = useToast();
  const [rows, setRows] = useState<EdgeOperator[] | null>(null);
  const [me, setMe] = useState<Whoami | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [formErr, setFormErr] = useState<unknown>(null);

  const [showNew, setShowNew] = useState(false);
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<string>("site_viewer");
  // The administrator knows a password they chose, so by default the operator replaces it at first sign-in
  // (edged must_change_password).
  const [requireChangeNew, setRequireChangeNew] = useState(true);

  const [pwFor, setPwFor] = useState<EdgeOperator | null>(null);
  const [newPw, setNewPw] = useState("");
  const [confirmPw, setConfirmPw] = useState("");
  const [requireChangeReset, setRequireChangeReset] = useState(true);

  const [disabling, setDisabling] = useState<EdgeOperator | null>(null);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [mfaFor, setMfaFor] = useState<EdgeOperator | null>(null);
  const [mfaReason, setMfaReason] = useState("");
  const [mfaPassword, setMfaPassword] = useState("");
  const [removingRole, setRemovingRole] = useState<{ op: EdgeOperator; role: string } | null>(null);
  // The acting administrator's password; cleared when a dialog opens or the change succeeds.
  const [yourPw, setYourPw] = useState("");

  const writable = canWrite("operators", me?.roles ?? []);

  async function load() {
    try { setRows((await api.get<ListResp<EdgeOperator>>("/operators")).data); }
    catch (e) { setErr(e); }
  }
  useEffect(() => {
    load();
    api.get<Whoami>("/auth/whoami").then(setMe).catch(() => {});
  }, []);

  function openNew() {
    setEmail(""); setDisplayName(""); setPassword(""); setRole("site_viewer"); setRequireChangeNew(true); setYourPw("");
    setFormErr(null);
    setShowNew(true);
  }

  async function onCreate() {
    setBusy(true); setFormErr(null);
    if (password.length < MIN_PASSWORD) {
      setFormErr(`The password must be at least ${MIN_PASSWORD} characters.`); setBusy(false); return;
    }
    try {
      await api.post("/operators", {
        email: email.trim(),
        display_name: displayName.trim() || undefined,
        password,
        role,
        require_change: requireChangeNew,
        your_password: yourPw,
      });
      setShowNew(false);
      setPassword(""); setYourPw("");
      toast.success("Operator added", `${email.trim()} can now sign in as ${ROLE_LABELS[role as SiteRole] ?? role}.`);
      await load();
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  async function onSetPassword() {
    if (!pwFor) return;
    setBusy(true); setFormErr(null);
    if (newPw.length < MIN_PASSWORD) {
      setFormErr(`The password must be at least ${MIN_PASSWORD} characters.`); setBusy(false); return;
    }
    if (newPw !== confirmPw) {
      setFormErr("The two passwords do not match."); setBusy(false); return;
    }
    try {
      await api.post(`/operators/${pwFor.id}/set-password`, { password: newPw, require_change: requireChangeReset, your_password: yourPw });
      toast.success("Password reset", `${pwFor.email} has been signed out everywhere${requireChangeReset ? " and must choose a new password at the next sign-in" : ""}.`);
      await load();
      setPwFor(null);
      setNewPw(""); setConfirmPw(""); setYourPw("");
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  async function onResetMFA() {
    if (!mfaFor) return;
    setBusy(true); setFormErr(null);
    try {
      await api.post(`/operators/${mfaFor.id}/reset-mfa`, { reason: mfaReason.trim(), password: mfaPassword });
      toast.success("Two-step sign-in reset", `${mfaFor.email} has been signed out and now signs in with the password alone until they turn two-step sign-in on again.`);
      setMfaFor(null); setMfaReason(""); setMfaPassword("");
      await load();
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  async function onDisable({ password }: { password: string }) {
    if (!disabling) return;
    setBusy(true); setFormErr(null);
    try {
      await api.del(`/operators/${disabling.id}`, { your_password: password });
      toast.success("Operator disabled", `${disabling.email} can no longer sign in.`);
      setDisabling(null);
      await load();
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  // Adding a role widens access, so it is confirmed first like removing one.
  const [addingRole, setAddingRole] = useState<{ op: EdgeOperator; role: string } | null>(null);
  async function onAddRole({ password }: { password: string }) {
    if (!addingRole) return;
    const { op, role } = addingRole;
    setBusy(true); setFormErr(null);
    try {
      await api.post(`/operators/${op.id}/roles`, { role, your_password: password });
      toast.success("Role added", `${op.email} is now also ${ROLE_LABELS[role as SiteRole] ?? role}.`);
      setAddingRole(null);
      await load();
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  async function onRemoveRole({ password }: { password: string }) {
    if (!removingRole) return;
    setBusy(true); setFormErr(null);
    try {
      await api.del(`/operators/${removingRole.op.id}/roles/${removingRole.role}`, { your_password: password });
      toast.success("Role removed");
      setRemovingRole(null);
      await load();
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  const selected = rows?.find((o) => o.id === selectedId) ?? null;
  const nameOf = (op: EdgeOperator) => op.display_name || op.email;
  const roleName = (r: string) => ROLE_LABELS[r as SiteRole] ?? r;

  function openReset(op: EdgeOperator) {
    setFormErr(null); setNewPw(""); setConfirmPw(""); setYourPw(""); setRequireChangeReset(true); setPwFor(op);
  }
  function openResetMfa(op: EdgeOperator) {
    setFormErr(null); setMfaReason(""); setMfaPassword(""); setMfaFor(op);
  }
  function askRemoveRole(op: EdgeOperator, r: string) {
    if (op.id === me?.operator_id && r === "site_admin") {
      setErr("You cannot remove your own Site admin role.");
      return;
    }
    setFormErr(null);
    setRemovingRole({ op, role: r });
  }

  return (
    <PageShell>
      <PageHeader
        title="Operators"
        description="Staff accounts that can sign in to this appliance."
        help={
          <>
            <HelpSection title="Local accounts">
              <p>
                Operators are local to this site. They are not cloud accounts and do not exist on any other site.
              </p>
            </HelpSection>
            <HelpSection title="Roles">
              <HelpList
                items={[
                  "A role decides what an operator can see and change. An operator can hold several.",
                  "An operator with no role can sign in but cannot see anything.",
                  "Open an operator to add or remove roles. Each change is confirmed first.",
                  "You cannot remove your own Site admin role, or disable your own account.",
                ]}
              />
            </HelpSection>
            <HelpSection title="Passwords and disabling">
              <HelpList
                items={[
                  `Passwords must be at least ${MIN_PASSWORD} characters and are typed twice when changed.`,
                  "Resetting someone's password signs them out everywhere and ends a sign-in lockout on their account.",
                  "A password you set for someone must, by default, be replaced by them at their next sign-in.",
                  "Your own password is changed with Change my password, which asks for your current one.",
                  "A disabled operator can no longer sign in, and is signed out at once.",
                  "A disabled account is anonymised after the period set under Disabled accounts (24 months by default).",
                  "Adding, disabling or resetting an operator, or changing their roles, asks for your own password first.",
                ]}
              />
            </HelpSection>
            <HelpSection title="Two-step sign-in">
              <HelpList
                items={[
                  "Optional for each operator and recommended: each turns it on under Your account > Two-step sign-in, which also gives ten single-use recovery codes.",
                  "Once on, signing in asks for the password and the 6-digit code from the authenticator app on the operator's phone.",
                  "Someone who lost their phone signs in with a recovery code, or you reset their two-step sign-in: they are signed out and sign in with the password alone until they turn it on again.",
                  "You cannot reset your own; another site administrator must.",
                ]}
              />
            </HelpSection>
          </>
        }
        actions={writable && <Button onClick={openNew}><Plus /> Add operator</Button>}
      />

      {me && !writable && <ReadOnlyNotice>Your role can see who can sign in, but not add or change operators.</ReadOnlyNotice>}
      <ErrorBanner err={err} className="mb-0" />

      <Card className="overflow-hidden">
        {rows === null ? (
          <SkeletonRows rows={4} cols={5} />
        ) : rows.length === 0 ? (
          <EmptyState size="sm" icon={<Users />} title="No operators" hint="Nobody but you can sign in to this appliance." />
        ) : (
          <Table aria-label="Operators">
            <THead>
              <TR>
                <TH>Operator</TH>
                <TH className="hidden sm:table-cell">Roles</TH>
                <TH className="hidden md:table-cell">Two-step</TH>
                <TH className="hidden sm:table-cell">Status</TH>
                <TH className="hidden lg:table-cell">Added</TH>
                {writable && <TH className="w-0"><span className="sr-only">Actions</span></TH>}
              </TR>
            </THead>
            <TBody>
              {rows.map((op) => {
                const isMe = op.id === me?.operator_id;
                const active = op.status === "active";
                return (
                  <TR key={op.id} onClick={() => setSelectedId(op.id)}>
                    <TD>
                      <div className="flex min-w-0 flex-wrap items-center gap-1.5">
                        <span className="truncate font-medium">{nameOf(op)}</span>
                        {isMe && <Badge tone="accent" size="sm">You</Badge>}
                      </div>
                      {op.display_name && <div className="truncate text-xs text-muted-foreground">{op.email}</div>}
                      {(!active || op.must_change_password) && (
                        <div className="mt-1 flex flex-wrap gap-1">
                          {!active && <StatusBadge className="sm:hidden">Disabled</StatusBadge>}
                          {active && op.must_change_password && <Badge tone="warn" size="sm">Must set a new password</Badge>}
                        </div>
                      )}
                    </TD>
                    <TD className="hidden sm:table-cell">
                      {op.roles.length === 0 ? (
                        <span className="text-xs text-muted-foreground">No role</span>
                      ) : (
                        <div className="flex flex-wrap gap-1">
                          {op.roles.map((r) => <Badge key={r} tone="neutral" size="sm">{roleName(r)}</Badge>)}
                        </div>
                      )}
                    </TD>
                    <TD nowrap className="hidden md:table-cell">
                      {active && typeof op.two_step_enrolled === "boolean"
                        ? <TwoStepBadge on={op.two_step_enrolled} />
                        : <span className="text-muted-foreground">—</span>}
                    </TD>
                    <TD nowrap className="hidden sm:table-cell">
                      <StatusBadge tone={active ? "ok" : "default"}>{active ? "Can sign in" : "Disabled"}</StatusBadge>
                    </TD>
                    <TD nowrap muted className="hidden lg:table-cell">{formatRelative(op.created_at)}</TD>
                    {writable && (
                      <TD className="w-0">
                        <RowActions
                          subject={op.email}
                          primary={isMe
                            ? { label: "Change my password", href: "/account/password" }
                            : { label: "Reset password", onSelect: () => openReset(op) }}
                          items={[
                            { label: "Roles and details", icon: <UserCog />, onSelect: () => setSelectedId(op.id) },
                            {
                              label: "Reset two-step sign-in",
                              icon: <Smartphone />,
                              hidden: isMe || !active || !op.two_step_enrolled,
                              onSelect: () => openResetMfa(op),
                            },
                            {
                              label: "Disable",
                              icon: <UserMinus />,
                              danger: true,
                              hidden: isMe || !active,
                              onSelect: () => { setFormErr(null); setDisabling(op); },
                            },
                          ]}
                        />
                      </TD>
                    )}
                  </TR>
                );
              })}
            </TBody>
          </Table>
        )}
      </Card>

      <OperatorSigninProtection writable={writable} />

      <OperatorSessionLimits writable={writable} />

      <OperatorAccountRetention writable={writable} />

      <DetailsDrawer
        open={selected !== null}
        onOpenChange={(v) => { if (!v) setSelectedId(null); }}
        eyebrow="Operator"
        title={selected ? nameOf(selected) : ""}
        description={selected?.display_name ? selected.email : undefined}
        badges={selected && (
          <>
            <StatusBadge tone={selected.status === "active" ? "ok" : "default"}>
              {selected.status === "active" ? "Can sign in" : "Disabled"}
            </StatusBadge>
            {selected.id === me?.operator_id && <Badge tone="accent">You</Badge>}
          </>
        )}
        actions={selected && writable && (
          selected.id === me?.operator_id ? (
            <LinkButton href="/account/password" variant="secondary">Change my password</LinkButton>
          ) : (
            <>
              {selected.status === "active" && (
                <Button variant="danger-outline" className="me-auto" onClick={() => { setFormErr(null); setDisabling(selected); }}>
                  Disable
                </Button>
              )}
              {selected.status === "active" && selected.two_step_enrolled && (
                <Button variant="secondary" onClick={() => openResetMfa(selected)}>Reset two-step</Button>
              )}
              <Button variant="secondary" onClick={() => openReset(selected)}>Reset password</Button>
            </>
          )
        )}
      >
        {selected && (
          <div className="space-y-6">
            <DetailsSection title="Account">
              <DescriptionList
                items={[
                  { label: "Email or username", value: selected.email },
                  { label: "Name", value: selected.display_name || undefined },
                  {
                    label: "Two-step sign-in",
                    value: typeof selected.two_step_enrolled === "boolean"
                      ? <TwoStepBadge on={selected.two_step_enrolled} /> : undefined,
                  },
                  {
                    label: "Password",
                    value: selected.must_change_password ? "Must set a new one at the next sign-in" : "Set",
                    hidden: selected.status !== "active",
                  },
                  { label: "Added", value: formatRelative(selected.created_at) },
                ]}
              />
            </DetailsSection>
            <DetailsSection
              title="Roles"
              description={selected.roles.length === 0
                ? "No role: they can sign in but cannot see anything."
                : "What they can see and change."}
            >
              {selected.roles.length > 0 && (
                <ul className="space-y-1.5">
                  {selected.roles.map((r) => (
                    <li key={r} className="flex min-h-9 items-center justify-between gap-3 rounded-md border border-border px-3 py-1">
                      <span className="text-sm">{roleName(r)}</span>
                      {writable && (
                        <Button
                          size="icon-xs"
                          variant="ghost"
                          title={`Remove ${roleName(r)}`}
                          aria-label={`Remove ${roleName(r)} from ${selected.email}`}
                          onClick={() => askRemoveRole(selected, r)}
                        >
                          <X />
                        </Button>
                      )}
                    </li>
                  ))}
                </ul>
              )}
              {writable && SITE_ROLES.some((r) => !selected.roles.includes(r)) && (
                <Select
                  value=""
                  aria-label={`Give ${selected.email} another role`}
                  onChange={(e) => {
                    const role = e.target.value;
                    e.currentTarget.value = "";
                    if (role) { setFormErr(null); setAddingRole({ op: selected, role }); }
                  }}
                  className="w-full sm:w-auto"
                >
                  <option value="" disabled>Add a role…</option>
                  {SITE_ROLES.filter((r) => !selected.roles.includes(r)).map((r) => (
                    <option key={r} value={r}>{ROLE_LABELS[r as SiteRole]}</option>
                  ))}
                </Select>
              )}
            </DetailsSection>
          </div>
        )}
      </DetailsDrawer>

      <DialogForm
        open={mfaFor !== null}
        onOpenChange={(v) => !v && setMfaFor(null)}
        size="sm"
        title={mfaFor ? `Reset two-step sign-in for ${mfaFor.display_name || mfaFor.email}` : "Reset two-step sign-in"}
        description="Their authenticator app and recovery codes stop working and they are signed out everywhere. They then sign in with the password alone until they turn two-step sign-in on again."
        submitLabel="Reset two-step sign-in"
        busy={busy}
        error={formErr}
        disabled={mfaReason.trim().length < 4 || mfaPassword === ""}
        onSubmit={onResetMFA}
      >
        <Field label="Reason" required hint="Recorded with the reset, for example: lost their phone.">
          <Input value={mfaReason} maxLength={500} onChange={(e) => setMfaReason(e.target.value)} />
        </Field>
        <Field label="Your password" required hint="Confirms the reset.">
          <Input type="password" autoComplete="current-password" value={mfaPassword} onChange={(e) => setMfaPassword(e.target.value)} />
        </Field>
      </DialogForm>

      <DialogForm
        open={showNew}
        onOpenChange={(v) => !v && setShowNew(false)}
        title="Add an operator"
        description="They will be able to sign in to this appliance immediately with the role you choose."
        submitLabel="Create operator"
        busy={busy}
        error={formErr}
        disabled={!email.trim() || password.length < MIN_PASSWORD || yourPw === ""}
        onSubmit={onCreate}
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Email or username" required>
            <Input value={email} onChange={(e) => setEmail(e.target.value)} required placeholder="ops@example.com" />
          </Field>
          <Field label="Name" hint="Shown instead of the email where there is room for it.">
            <Input value={displayName} onChange={(e) => setDisplayName(e.target.value)} placeholder="Optional" />
          </Field>
        </div>
        <Field
          label="Password"
          required
          hint={`At least ${MIN_PASSWORD} characters.`}
          error={password !== "" && password.length < MIN_PASSWORD ? `${MIN_PASSWORD - password.length} more characters needed.` : undefined}
        >
          <Input
            type="password"
            autoComplete="new-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
            minLength={MIN_PASSWORD}
          />
        </Field>
        <Field label="Role" hint="What they are allowed to see and change. It can be changed afterwards.">
          <Select value={role} onChange={(e) => setRole(e.target.value)}>
            {SITE_ROLES.map((r) => <option key={r} value={r}>{ROLE_LABELS[r]}</option>)}
          </Select>
        </Field>
        <RequireChangeBox checked={requireChangeNew} onChange={setRequireChangeNew} />
        <YourPasswordField value={yourPw} onChange={setYourPw} />
      </DialogForm>

      <DialogForm
        open={pwFor !== null}
        onOpenChange={(v) => !v && setPwFor(null)}
        size="sm"
        title={pwFor ? `Reset the password for ${pwFor.display_name || pwFor.email}` : "Reset password"}
        description="They are signed out everywhere at once, and any sign-in lockout on their account ends."
        submitLabel="Reset password"
        busy={busy}
        error={formErr}
        disabled={newPw.length < MIN_PASSWORD || newPw !== confirmPw || yourPw === ""}
        onSubmit={onSetPassword}
      >
        <Field label="New password" required hint={`At least ${MIN_PASSWORD} characters.`}>
          <Input
            type="password"
            autoComplete="new-password"
            value={newPw}
            onChange={(e) => setNewPw(e.target.value)}
            required
            minLength={MIN_PASSWORD}
          />
        </Field>
        <Field
          label="Type it again"
          required
          error={confirmPw !== "" && confirmPw !== newPw ? "These do not match." : undefined}
        >
          <Input
            type="password"
            autoComplete="new-password"
            value={confirmPw}
            onChange={(e) => setConfirmPw(e.target.value)}
            required
          />
        </Field>
        <RequireChangeBox checked={requireChangeReset} onChange={setRequireChangeReset} />
        <YourPasswordField value={yourPw} onChange={setYourPw} />
      </DialogForm>

      <ConfirmDialog
        open={disabling !== null}
        onOpenChange={(v) => !v && setDisabling(null)}
        title="Stop this operator signing in?"
        description={
          disabling
            ? `${disabling.display_name || disabling.email} will no longer be able to sign in to this appliance. Their account and everything they have done is kept until the disabled-account period ends; then their email and name are removed. There is no way to re-enable a disabled operator from this screen.`
            : undefined
        }
        confirmLabel="Disable account"
        confirmVariant="danger"
        requirePassword
        passwordLabel="Your password"
        busy={busy}
        error={formErr}
        onConfirm={onDisable}
      />

      <ConfirmDialog
        open={addingRole !== null}
        onOpenChange={(v) => !v && setAddingRole(null)}
        title="Give this role?"
        description={
          addingRole
            ? `${addingRole.op.display_name || addingRole.op.email} will also be able to do everything the ${ROLE_LABELS[addingRole.role as SiteRole] ?? addingRole.role} role allows, from their next action.`
            : undefined
        }
        confirmLabel="Add role"
        requirePassword
        passwordLabel="Your password"
        busy={busy}
        error={formErr}
        onConfirm={onAddRole}
      />

      <ConfirmDialog
        open={removingRole !== null}
        onOpenChange={(v) => !v && setRemovingRole(null)}
        title="Remove this role?"
        description={
          removingRole
            ? `${removingRole.op.display_name || removingRole.op.email} will lose everything the ${ROLE_LABELS[removingRole.role as SiteRole] ?? removingRole.role} role allows. If it is their only role they will be able to sign in and see nothing.`
            : undefined
        }
        confirmLabel="Remove role"
        confirmVariant="danger"
        requirePassword
        passwordLabel="Your password"
        busy={busy}
        error={formErr}
        onConfirm={onRemoveRole}
      />
    </PageShell>
  );
}

function YourPasswordField({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return (
    <Field label="Your password" required hint="Confirms that it is you making this change.">
      <Input type="password" autoComplete="current-password" value={value} onChange={(e) => onChange(e.target.value)} />
    </Field>
  );
}

function RequireChangeBox({ checked, onChange }: { checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <label className="flex items-start gap-2 rounded-md border border-border bg-surface/50 px-3.5 py-2.5 text-sm">
      <input type="checkbox" checked={checked} onChange={(e) => onChange(e.target.checked)}
        className="mt-0.5 size-3.5 accent-[hsl(var(--primary))]" />
      <span>
        Must choose their own password at the next sign-in
        <span className="block text-xs text-muted-foreground">
          You know the password you type here. With this on, the first thing they can do after signing in is
          replace it with one only they know.
        </span>
      </span>
    </label>
  );
}

function TwoStepBadge({ on }: { on: boolean }) {
  return <StatusBadge tone={on ? "ok" : "default"}>{on ? "On" : "Off"}</StatusBadge>;
}
