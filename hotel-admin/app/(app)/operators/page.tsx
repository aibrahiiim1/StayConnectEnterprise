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
import { api, ListResp, Whoami, EdgeOperator } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Field, Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { DialogForm, ConfirmDialog } from "@/components/ui/dialog";
import { SkeletonRows } from "@/components/ui/misc";
import { Plus, X, Users } from "lucide-react";
import { canWrite, SITE_ROLES, ROLE_LABELS, SiteRole } from "@/lib/roles";
import { formatRelative } from "@/lib/utils";

const MIN_PASSWORD = 10;

export default function OperatorsPage() {
  const [rows, setRows] = useState<EdgeOperator[] | null>(null);
  const [me, setMe] = useState<Whoami | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [formErr, setFormErr] = useState<unknown>(null);

  const [showNew, setShowNew] = useState(false);
  const [email, setEmail] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [password, setPassword] = useState("");
  const [role, setRole] = useState<string>("site_viewer");

  const [pwFor, setPwFor] = useState<EdgeOperator | null>(null);
  const [newPw, setNewPw] = useState("");
  const [confirmPw, setConfirmPw] = useState("");

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
    setEmail(""); setDisplayName(""); setPassword(""); setRole("site_viewer");
    setFormErr(null); setNotice(null);
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
      });
      setShowNew(false);
      setPassword("");
      setNotice(`${email.trim()} can now sign in as ${ROLE_LABELS[role as SiteRole] ?? role}.`);
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
      await api.post(`/operators/${pwFor.id}/set-password`, { password: newPw });
      setNotice(`The password for ${pwFor.email} has been changed.`);
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
      setNotice(`${disabling.email} can no longer sign in.`);
      setDisabling(null);
      await load();
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  async function onAddRole(op: EdgeOperator, role: string) {
    if (!role) return;
    setErr(null);
    try { await api.post(`/operators/${op.id}/roles`, { role }); await load(); }
    catch (e) { setErr(e); }
  }

  async function onRemoveRole() {
    if (!removingRole) return;
    setBusy(true); setFormErr(null);
    try {
      await api.del(`/operators/${removingRole.op.id}/roles/${removingRole.role}`);
      setRemovingRole(null);
      await load();
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  return (
    <PageShell>
      <PageHeader
        eyebrow="System"
        title="Operators"
        description="Staff accounts for this appliance. They are local to this property — they are not cloud accounts and do not exist on any other site."
        actions={writable && <Button onClick={openNew}><Plus /> Add operator</Button>}
      />

      <ErrorBanner err={err} />
      {notice && <Callout tone="success">{notice}</Callout>}

      <Card>
        <CardBody className="p-0">
          {rows === null ? (
            <SkeletonRows rows={4} cols={5} />
          ) : rows.length === 0 ? (
            <EmptyState icon={<Users />} title="No operators" hint="Nobody but you can sign in to this appliance." />
          ) : (
            <Table>
              <THead>
                <TR><TH>Operator</TH><TH>What they can do</TH><TH>Status</TH><TH>Added</TH><TH /></TR>
              </THead>
              <tbody>
                {rows.map((op) => {
                  const isMe = op.id === me?.operator_id;
                  return (
                    <TR key={op.id}>
                      <TD>
                        <div className="font-medium">
                          {op.display_name || op.email}
                          {isMe && <span className="ml-1.5 text-xs font-normal text-primary">(you)</span>}
                        </div>
                        <div className="text-xs text-muted-foreground">{op.email}</div>
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
                                  className="rounded p-0.5 text-muted-foreground transition-colors hover:bg-surface hover:text-destructive"
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
                              onChange={(e) => { void onAddRole(op, e.target.value); e.currentTarget.value = ""; }}
                              className="h-7 w-auto pr-8 text-xs"
                            >
                              <option value="" disabled>+ role</option>
                              {SITE_ROLES.filter((r) => !op.roles.includes(r)).map((r) => (
                                <option key={r} value={r}>{ROLE_LABELS[r as SiteRole]}</option>
                              ))}
                            </Select>
                          )}
                        </div>
                      </TD>
                      <TD>
                        <Badge tone={op.status === "active" ? "ok" : "err"} dot>
                          {op.status === "active" ? "Can sign in" : "Disabled"}
                        </Badge>
                      </TD>
                      <TD className="text-sm text-muted-foreground">{formatRelative(op.created_at)}</TD>
                      <TD className="whitespace-nowrap text-right">
                        {writable && (
                          <Button
                            size="sm"
                            variant="ghost"
                            onClick={() => { setFormErr(null); setNewPw(""); setConfirmPw(""); setPwFor(op); }}
                          >
                            Change password
                          </Button>
                        )}
                        {writable && op.status === "active" && !isMe && (
                          <Button size="sm" variant="ghost" onClick={() => { setFormErr(null); setDisabling(op); }}>
                            Disable
                          </Button>
                        )}
                      </TD>
                    </TR>
                  );
                })}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

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
            <Input value={email} onChange={(e) => setEmail(e.target.value)} required placeholder="ops@hotel.com" />
          </Field>
          <Field label="Name" hint="Shown instead of the email where there is room for it.">
            <Input value={displayName} onChange={(e) => setDisplayName(e.target.value)} placeholder="Optional" />
          </Field>
        </div>
        <Field
          label="Password"
          required
          hint={`At least ${MIN_PASSWORD} characters. Tell them to change it after their first sign-in.`}
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
      </DialogForm>

      {/* ------------------------------------------------------------------ change password */}
      <DialogForm
        open={pwFor !== null}
        onOpenChange={(v) => !v && setPwFor(null)}
        size="sm"
        title={pwFor ? `Change the password for ${pwFor.display_name || pwFor.email}` : "Change password"}
        description="Their current sessions are not ended; they will need the new password the next time they sign in."
        submitLabel="Change password"
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
      </DialogForm>

      {/* ------------------------------------------------------------------ disable */}
      <ConfirmDialog
        open={disabling !== null}
        onOpenChange={(v) => !v && setDisabling(null)}
        title="Stop this operator signing in?"
        description={
          disabling
            ? `${disabling.display_name || disabling.email} will no longer be able to sign in to this appliance. Their account and everything they have done is kept, and the account can be re-enabled.`
            : undefined
        }
        confirmLabel="Disable account"
        confirmVariant="danger"
        busy={busy}
        error={formErr}
        onConfirm={onDisable}
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
