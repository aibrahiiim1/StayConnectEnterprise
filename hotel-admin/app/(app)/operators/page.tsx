"use client";

// OPERATORS — the staff accounts that can sign in to this admin.
//
// THE PASSWORD WAS BEING TYPED INTO A window.prompt(), which is the one change here that is not cosmetic.
// `prompt()` renders a plain text field: every character of a new operator password was displayed on a
// front-desk screen, it could not be confirmed against a second entry, and the minimum length was checked only
// after the dialog closed — so a too-short password was reported as an error banner on the page behind it with
// the value already discarded. Disabling an operator and removing a role went through `window.confirm`, which
// cannot say what either does.
//
// All three are now real dialogs: masked input, a confirmation field, validation before anything is sent, and a
// sentence saying what the action will do to the person it is about.

import { useEffect, useState } from "react";
import Link from "next/link";
import { api, ListResp, Whoami, EdgeOperator } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Field, Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { DialogForm, ConfirmDialog } from "@/components/ui/dialog";
import { SkeletonRows } from "@/components/ui/misc";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { Plus, X, Users } from "lucide-react";
import { canWrite, SITE_ROLES, ROLE_LABELS, SiteRole } from "@/lib/roles";
import { formatRelative } from "@/lib/utils";
import { HelpList, HelpSection } from "@/components/help";
import { OperatorSigninProtection } from "@/components/operator-signin-protection";

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
  // A password an administrator chose is one the administrator knows: by default the operator replaces it at
  // their first sign-in (edged must_change_password, 0119).
  const [requireChangeNew, setRequireChangeNew] = useState(true);

  const [pwFor, setPwFor] = useState<EdgeOperator | null>(null);
  const [newPw, setNewPw] = useState("");
  const [confirmPw, setConfirmPw] = useState("");
  const [requireChangeReset, setRequireChangeReset] = useState(true);

  const [disabling, setDisabling] = useState<EdgeOperator | null>(null);
  const [removingRole, setRemovingRole] = useState<{ op: EdgeOperator; role: string } | null>(null);

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
    setEmail(""); setDisplayName(""); setPassword(""); setRole("site_viewer"); setRequireChangeNew(true);
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
      });
      setShowNew(false);
      setPassword("");
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
      // Checked BEFORE sending. With window.prompt there was no second entry to check against at all, so a
      // mistyped password became a locked-out colleague discovered at the next shift change.
      setFormErr("The two passwords do not match."); setBusy(false); return;
    }
    try {
      await api.post(`/operators/${pwFor.id}/set-password`, { password: newPw, require_change: requireChangeReset });
      toast.success("Password reset", `${pwFor.email} has been signed out everywhere${requireChangeReset ? " and must choose a new password at the next sign-in" : ""}.`);
      await load();
      setPwFor(null);
      setNewPw(""); setConfirmPw("");
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  async function onDisable() {
    if (!disabling) return;
    setBusy(true); setFormErr(null);
    try {
      await api.del(`/operators/${disabling.id}`);
      toast.success("Operator disabled", `${disabling.email} can no longer sign in.`);
      setDisabling(null);
      await load();
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  // ADDING A ROLE WIDENS WHAT SOMEONE CAN DO, so it is confirmed like removing one. Choosing from the menu used to
  // grant the role on the spot: one slip of a select widened a colleague's access with nothing to catch it. The
  // request is the same one, sent only after the operator confirms.
  const [addingRole, setAddingRole] = useState<{ op: EdgeOperator; role: string } | null>(null);
  async function onAddRole() {
    if (!addingRole) return;
    const { op, role } = addingRole;
    setBusy(true); setFormErr(null);
    try {
      await api.post(`/operators/${op.id}/roles`, { role });
      toast.success("Role added", `${op.email} is now also ${ROLE_LABELS[role as SiteRole] ?? role}.`);
      setAddingRole(null);
      await load();
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  async function onRemoveRole() {
    if (!removingRole) return;
    setBusy(true); setFormErr(null);
    try {
      await api.del(`/operators/${removingRole.op.id}/roles/${removingRole.role}`);
      toast.success("Role removed");
      setRemovingRole(null);
      await load();
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  return (
    <PageShell>
      <PageHeader
        icon={<Users />}
        eyebrow="System"
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
                  "Adding or removing a role is confirmed first, because it changes what someone can do.",
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
        <CardBody className="p-0">
          {rows === null ? (
            <SkeletonRows rows={4} cols={5} />
          ) : rows.length === 0 ? (
            <EmptyState icon={<Users />} title="No operators" hint="Nobody but you can sign in to this appliance." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Operator</TH>
                  <TH>Roles</TH>
                  <TH className="hidden sm:table-cell">Status</TH>
                  <TH className="hidden md:table-cell">Added</TH>
                  {writable && <TH className="text-end"><span className="sr-only">Actions</span></TH>}
                </TR>
              </THead>
              <TBody>
                {rows.map((op) => {
                  const isMe = op.id === me?.operator_id;
                  return (
                    <TR key={op.id}>
                      <TD>
                        <div className="flex flex-wrap items-center gap-1.5 font-medium">
                          {op.display_name || op.email}
                          {isMe && <Badge tone="accent">You</Badge>}
                        </div>
                        <div className="text-xs text-muted-foreground">{op.email}</div>
                        {/* On a phone the status column is hidden; a disabled account still says so here. */}
                        {op.status !== "active" && (
                          <Badge tone="neutral" className="mt-1 sm:hidden">Disabled</Badge>
                        )}
                        {op.status === "active" && op.must_change_password && (
                          <Badge tone="warn" className="mt-1">Must set a new password</Badge>
                        )}
                      </TD>
                      <TD>
                        <div className="flex flex-wrap items-center gap-1.5">
                          {op.roles.length === 0 && (
                            <span className="text-xs text-muted-foreground">No role — cannot see anything</span>
                          )}
                          {op.roles.map((r) => (
                            <span key={r} className="inline-flex items-center gap-0.5">
                              <Badge tone="info">{ROLE_LABELS[r as SiteRole] ?? r}</Badge>
                              {writable && (
                                <button
                                  type="button"
                                  className="rounded p-0.5 text-muted-foreground transition-colors hover:bg-surface hover:text-destructive focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                                  title={`Remove ${ROLE_LABELS[r as SiteRole] ?? r}`}
                                  aria-label={`Remove ${ROLE_LABELS[r as SiteRole] ?? r} from ${op.email}`}
                                  onClick={() => {
                                    if (isMe && r === "site_admin") {
                                      setErr("You cannot remove your own Site admin role.");
                                      return;
                                    }
                                    setFormErr(null);
                                    setRemovingRole({ op, role: r });
                                  }}
                                ><X className="size-3" /></button>
                              )}
                            </span>
                          ))}
                          {writable && (
                            <Select
                              value=""
                              aria-label={`Give ${op.email} another role`}
                              onChange={(e) => {
                                const role = e.target.value;
                                e.currentTarget.value = "";
                                if (role) { setFormErr(null); setAddingRole({ op, role }); }
                              }}
                              className="h-7 w-auto pe-8 text-xs"
                            >
                              <option value="" disabled>+ role</option>
                              {SITE_ROLES.filter((r) => !op.roles.includes(r)).map((r) => (
                                <option key={r} value={r}>{ROLE_LABELS[r as SiteRole]}</option>
                              ))}
                            </Select>
                          )}
                        </div>
                      </TD>
                      <TD className="hidden sm:table-cell">
                        <Badge tone={op.status === "active" ? "ok" : "neutral"} dot>
                          {op.status === "active" ? "Can sign in" : "Disabled"}
                        </Badge>
                      </TD>
                      <TD className="hidden text-sm text-muted-foreground md:table-cell">{formatRelative(op.created_at)}</TD>
                      {writable && (
                      <TD className="text-end">
                        <div className="flex flex-wrap justify-end gap-1">
                          {isMe ? (
                            <Link href="/account/password" className="inline-flex h-8 items-center rounded-md px-3 text-sm font-medium hover:bg-surface">
                              Change my password
                            </Link>
                          ) : (
                            <Button
                              size="sm"
                              variant="ghost"
                              aria-label={`Reset the password for ${op.email}`}
                              onClick={() => { setFormErr(null); setNewPw(""); setConfirmPw(""); setRequireChangeReset(true); setPwFor(op); }}
                            >
                              Reset password
                            </Button>
                          )}
                        {writable && op.status === "active" && !isMe && (
                          <Button size="sm" variant="ghost" aria-label={`Disable ${op.email}`} onClick={() => { setFormErr(null); setDisabling(op); }}>
                            Disable
                          </Button>
                        )}
                        </div>
                      </TD>
                      )}
                    </TR>
                  );
                })}
              </TBody>
            </Table>
          )}
        </CardBody>
      </Card>

      {/* ------------------------------------------------------------------ sign-in protection (0119) */}
      <OperatorSigninProtection writable={writable} />

      {/* ------------------------------------------------------------------ new operator */}
      <DialogForm
        open={showNew}
        onOpenChange={(v) => !v && setShowNew(false)}
        title="Add an operator"
        description="They will be able to sign in to this appliance immediately with the role you choose."
        submitLabel="Create operator"
        busy={busy}
        error={formErr}
        disabled={!email.trim() || password.length < MIN_PASSWORD}
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
      </DialogForm>

      {/* ------------------------------------------------------------------ change password */}
      <DialogForm
        open={pwFor !== null}
        onOpenChange={(v) => !v && setPwFor(null)}
        size="sm"
        title={pwFor ? `Reset the password for ${pwFor.display_name || pwFor.email}` : "Reset password"}
        description="They are signed out everywhere at once, and any sign-in lockout on their account ends."
        submitLabel="Reset password"
        busy={busy}
        error={formErr}
        disabled={newPw.length < MIN_PASSWORD || newPw !== confirmPw}
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
      </DialogForm>

      {/* ------------------------------------------------------------------ disable */}
      <ConfirmDialog
        open={disabling !== null}
        onOpenChange={(v) => !v && setDisabling(null)}
        title="Stop this operator signing in?"
        description={
          disabling
            ? `${disabling.display_name || disabling.email} will no longer be able to sign in to this appliance. Their account and everything they have done is kept. There is no way to re-enable a disabled operator from this screen.`
            : undefined
        }
        confirmLabel="Disable account"
        confirmVariant="danger"
        busy={busy}
        error={formErr}
        onConfirm={onDisable}
      />

      {/* ------------------------------------------------------------------ add role */}
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
        busy={busy}
        error={formErr}
        onConfirm={onAddRole}
      />

      {/* ------------------------------------------------------------------ remove role */}
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
        busy={busy}
        error={formErr}
        onConfirm={onRemoveRole}
      />
    </PageShell>
  );
}

/** "Must set a new password at the next sign-in" -- on by default whenever an administrator chooses one. */
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
