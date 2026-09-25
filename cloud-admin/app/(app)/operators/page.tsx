"use client";

import { useEffect, useMemo, useState } from "react";
import { KeyRound, Plus, UserMinus, Users, X } from "lucide-react";
import { api, ApiError, ListResp, Operator } from "@/lib/api";
import { useCustomer } from "@/lib/customer-context";
import { Card } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { ConfirmDialog, DialogForm } from "@/components/ui/dialog";
import { PageHeader, PageShell, StatCard, Toolbar } from "@/components/ui/page";
import { SearchInput } from "@/components/ui/data";
import { SkeletonRows } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { CustomerScope, SelectCustomerCard } from "@/components/customer-scope";
import { RoleRestricted } from "@/components/role-restricted";
import { usePermissions } from "@/lib/permissions";
import { HelpList, HelpSection } from "@/components/help";

// "billing" is NOT offered. ctrlapi's operators API still accepts the legacy name, but the operator_roles CHECK
// constraint has not allowed it since migration 0021, so granting it always failed with a server error. Offering
// a choice that can only fail is a UI defect; the permission model itself is unchanged (see lib/permissions.ts).
const ROLES = ["tenant_admin", "tenant_operator", "viewer"] as const;

// The words shown for each role. The values sent to the API are unchanged.
const ROLE_LABELS: Record<string, string> = {
  tenant_admin: "Customer admin",
  tenant_operator: "Customer operator",
  viewer: "Viewer",
  billing: "Billing",
  platform_admin: "Platform admin",
};
const roleLabel = (r: string) => ROLE_LABELS[r] ?? r;

const STATUS: Record<string, { label: string; tone: "ok" | "err" | "warn" }> = {
  active: { label: "Active", tone: "ok" },
  disabled: { label: "Disabled", tone: "err" },
  invited: { label: "Invited", tone: "warn" },
};

export default function OperatorsPage() {
  // Operators are per-customer staff. Management requires a concrete customer in the Customer context; "All
  // customers" shows a prompt rather than allowing accidental cross-customer staff changes.
  const { me, selectedTenantId: tenantID, ready } = useCustomer();
  // Every Operators route, the list included, is gated by mayManageOperators (api/operators.go): a platform admin,
  // or the role "tenant_admin" in its own customer. Everyone else is refused even reading, so they get the
  // restricted block and no controls (lib/permissions.ts "operators.read" / "operators.write").
  const { can } = usePermissions();
  const canRead = can["operators.read"];
  const canWrite = can["operators.write"];
  const allCustomers = tenantID === "";
  const toast = useToast();
  const [rows, setRows] = useState<Operator[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [query, setQuery] = useState("");

  async function load() {
    if (!ready) return;
    if (allCustomers) { setRows(null); return; }
    try {
      const r = await api.get<ListResp<Operator>>(`/v1/operators?tenant_id=${tenantID}`);
      setRows(r.data);
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load");
    }
  }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { setShowNew(false); load(); }, [ready, tenantID]);

  // ---- create ----
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);
  const [createErr, setCreateErr] = useState<string | null>(null);

  async function onCreate(e: React.FormEvent<HTMLFormElement>) {
    if (allCustomers) return;
    setBusy(true); setCreateErr(null);
    const form = new FormData(e.currentTarget);
    const email = (form.get("email") as string).trim();
    try {
      await api.post(`/v1/operators?tenant_id=${tenantID}`, {
        email,
        display_name: form.get("display_name"),
        password: form.get("password"),
        role: form.get("role") || "tenant_operator",
      });
      setShowNew(false);
      toast.success("Operator created", email);
      load();
    } catch (e: any) {
      if (e instanceof ApiError && e.body?.error === "limit_exceeded") {
        setCreateErr(`License limit reached: ${e.body.limit_key} (${e.body.current}/${e.body.limit})`);
      } else setCreateErr(e?.message ?? "Create failed");
    } finally { setBusy(false); }
  }

  // ---- row actions ----
  const [actionBusy, setActionBusy] = useState(false);
  const [actionErr, setActionErr] = useState<string | null>(null);
  const [disableOp, setDisableOp] = useState<Operator | null>(null);
  const [resetOp, setResetOp] = useState<Operator | null>(null);
  const [newPw, setNewPw] = useState("");
  const [roleOp, setRoleOp] = useState<Operator | null>(null);
  const [removeRole, setRemoveRole] = useState<{ op: Operator; role: string } | null>(null);

  function beginAction() { setActionErr(null); }

  async function onDisable() {
    const op = disableOp;
    if (allCustomers || !op) return;
    setActionBusy(true); setActionErr(null);
    try {
      await api.del(`/v1/operators/${op.id}?tenant_id=${tenantID}`);
      setDisableOp(null);
      toast.success("Operator disabled", op.email);
      load();
    } catch (e: any) { setActionErr(e?.message ?? "Disable failed"); }
    finally { setActionBusy(false); }
  }

  async function onResetPassword() {
    const op = resetOp;
    if (allCustomers || !op || !newPw) return;
    setActionBusy(true); setActionErr(null);
    try {
      await api.post(`/v1/operators/${op.id}/set-password?tenant_id=${tenantID}`, { password: newPw });
      setResetOp(null); setNewPw("");
      toast.success("Password updated", op.email);
    } catch (e: any) { setActionErr(e?.message ?? "Reset failed"); }
    finally { setActionBusy(false); }
  }

  async function onAddRole(e: React.FormEvent<HTMLFormElement>) {
    const op = roleOp;
    if (allCustomers || !op) return;
    const role = String(new FormData(e.currentTarget).get("role") ?? "");
    if (!role || !ROLES.includes(role as any)) return;
    setActionBusy(true); setActionErr(null);
    try {
      await api.post(`/v1/operators/${op.id}/roles?tenant_id=${tenantID}`, { role });
      setRoleOp(null);
      toast.success(`${roleLabel(role)} added`, op.email);
      load();
    } catch (e: any) { setActionErr(e?.message ?? "Role add failed"); }
    finally { setActionBusy(false); }
  }

  async function onRemoveRole() {
    const rr = removeRole;
    if (allCustomers || !rr) return;
    setActionBusy(true); setActionErr(null);
    try {
      await api.del(`/v1/operators/${rr.op.id}/roles/${rr.role}?tenant_id=${tenantID}`);
      setRemoveRole(null);
      toast.success(`${roleLabel(rr.role)} removed`, rr.op.email);
      load();
    } catch (e: any) { setActionErr(e?.message ?? "Role remove failed"); }
    finally { setActionBusy(false); }
  }

  const counts = useMemo(() => {
    const all = rows ?? [];
    return {
      total: all.length,
      active: all.filter((o) => o.status === "active").length,
      disabled: all.filter((o) => o.status === "disabled").length,
      invited: all.filter((o) => o.status === "invited").length,
    };
  }, [rows]);

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (rows ?? []).filter((o) =>
      !q || `${o.email} ${o.display_name ?? ""} ${(o.roles ?? []).map((r) => roleLabel(r.role)).join(" ")}`.toLowerCase().includes(q));
  }, [rows, query]);

  const missingRoles = (op: Operator) => ROLES.filter((r) => !(op.roles ?? []).some((x) => x.role === r));

  return (
    <PageShell>
      <PageHeader
        eyebrow="Administration"
        title="Operators"
        icon={<Users />}
        description="A customer's staff sign-ins to Central and their roles."
        help={
          <>
            <HelpSection title="Who operators are">
              <p>
                Operators are a customer&apos;s own staff sign-ins to Central. Select a customer in the sidebar to
                list and manage its operators.
              </p>
            </HelpSection>
            <HelpSection title="Roles">
              <HelpList
                items={[
                  <><strong>Customer admin</strong>: manages the customer&apos;s operators and data.</>,
                  <><strong>Customer operator</strong>: day-to-day work for the customer.</>,
                  <><strong>Viewer</strong>: read-only.</>,
                ]}
              />
            </HelpSection>
            <HelpSection title="Managing operators">
              <HelpList
                items={[
                  <>Select a role badge to remove that role; <strong>Role</strong> adds another.</>,
                  <>Passwords must be at least 10 characters.</>,
                  <><strong>Disable</strong> stops an operator from signing in.</>,
                  <>You cannot disable yourself or change your own roles.</>,
                ]}
              />
            </HelpSection>
          </>
        }
        actions={
          canRead && canWrite ? (
            <Button onClick={() => { setCreateErr(null); setShowNew(true); }} disabled={allCustomers}>
              <Plus /> New operator
            </Button>
          ) : undefined
        }
      >
        <CustomerScope />
      </PageHeader>

      {canRead && <ErrorBanner err={err} />}

      {!canRead ? (
        <RoleRestricted what="Operators are managed by the customer's admin." />
      ) : allCustomers ? (
        <SelectCustomerCard what="Operators are a customer's own staff." />
      ) : (
        <>
          <section aria-label="Operator counts" className="grid grid-cols-2 gap-4 lg:grid-cols-4">
            <StatCard label="Operators" value={rows ? counts.total : "—"} icon={<Users />} />
            <StatCard label="Active" value={rows ? counts.active : "—"} tone="ok" />
            <StatCard label="Invited" value={rows ? counts.invited : "—"} tone="warn" />
            <StatCard label="Disabled" value={rows ? counts.disabled : "—"} tone="err" />
          </section>

          <Card>
            <Toolbar className="border-b border-border px-4 py-3">
              <SearchInput value={query} onChange={setQuery} placeholder="Search email, name or role" label="Search operators" />
            </Toolbar>
            {rows === null ? (
              <SkeletonRows rows={4} cols={5} />
            ) : rows.length === 0 ? (
              <EmptyState
                icon={<Users />}
                title="No operators yet"
                hint="Add the customer's first operator."
                action={canWrite ? <Button onClick={() => setShowNew(true)}><Plus /> New operator</Button> : undefined}
              />
            ) : visible.length === 0 ? (
              <EmptyState
                title="No operators match"
                hint="Nothing matches the search."
                action={<Button variant="secondary" onClick={() => setQuery("")}>Clear search</Button>}
              />
            ) : (
              <Table>
                <THead>
                  <TR>
                    <TH>Email</TH><TH className="hidden md:table-cell">Name</TH><TH>Status</TH><TH>Roles</TH>
                    <TH><span className="sr-only">Actions</span></TH>
                  </TR>
                </THead>
                <tbody>
                  {visible.map((op) => {
                    const isSelf = me?.operator_id === op.id;
                    const st = STATUS[op.status] ?? { label: op.status, tone: "warn" as const };
                    return (
                      <TR key={op.id}>
                        <TD>
                          <div className="flex flex-wrap items-center gap-1.5">
                            <span className="font-mono text-xs">{op.email}</span>
                            {isSelf && <Badge tone="accent">You</Badge>}
                          </div>
                        </TD>
                        <TD className="hidden text-muted-foreground md:table-cell">{op.display_name || "—"}</TD>
                        <TD><Badge tone={st.tone} dot>{st.label}</Badge></TD>
                        <TD>
                          <ul className="flex flex-wrap gap-1" aria-label={`Roles of ${op.email}`}>
                            {(op.roles ?? []).map((r) => (
                              <li key={r.id}>
                                {isSelf || !canWrite ? (
                                  <Badge tone={r.role === "platform_admin" ? "info" : "neutral"}>{roleLabel(r.role)}</Badge>
                                ) : (
                                  <button
                                    type="button"
                                    onClick={() => { beginAction(); setRemoveRole({ op, role: r.role }); }}
                                    aria-label={`Remove role ${roleLabel(r.role)} from ${op.email}`}
                                    className="group rounded-full focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                                  >
                                    <Badge tone={r.role === "platform_admin" ? "info" : "neutral"}>
                                      {roleLabel(r.role)}
                                      <X className="size-3 opacity-60 group-hover:opacity-100" aria-hidden />
                                    </Badge>
                                  </button>
                                )}
                              </li>
                            ))}
                          </ul>
                        </TD>
                        <TD>
                          {canWrite && (
                          <div className="flex justify-end gap-1">
                            <Button size="sm" variant="ghost" onClick={() => { beginAction(); setRoleOp(op); }} disabled={missingRoles(op).length === 0}>
                              <Plus /> Role
                            </Button>
                            <Button size="sm" variant="ghost" onClick={() => { beginAction(); setNewPw(""); setResetOp(op); }}>
                              <KeyRound /> <span className="hidden sm:inline">Reset password</span>
                            </Button>
                            {!isSelf && (
                              <Button size="sm" variant="ghost" className="text-destructive hover:text-destructive" onClick={() => { beginAction(); setDisableOp(op); }}>
                                <UserMinus /> <span className="hidden sm:inline">Disable</span>
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
          </Card>
        </>
      )}

      <DialogForm
        open={showNew}
        onOpenChange={setShowNew}
        title="New operator"
        description="A sign-in to Central for this customer's staff."
        submitLabel="Create operator"
        busyLabel="Creating…"
        busy={busy}
        error={createErr}
        onSubmit={onCreate}
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Email" required><Input name="email" type="email" required autoComplete="off" /></Field>
          <Field label="Display name"><Input name="display_name" /></Field>
          <Field label="Initial password" required hint="At least 10 characters.">
            <Input name="password" type="password" required minLength={10} autoComplete="new-password" />
          </Field>
          <Field label="Role">
            <Select name="role" defaultValue="tenant_operator">
              {ROLES.map((r) => <option key={r} value={r}>{roleLabel(r)}</option>)}
            </Select>
          </Field>
        </div>
      </DialogForm>

      <DialogForm
        open={!!resetOp}
        onOpenChange={(v) => { if (!v) { setResetOp(null); setNewPw(""); } }}
        title="Reset password"
        description={resetOp ? <>Set a new password for <span className="font-mono">{resetOp.email}</span>.</> : undefined}
        size="sm"
        submitLabel="Set password"
        busyLabel="Saving…"
        busy={actionBusy}
        error={actionErr}
        disabled={newPw.length < 10}
        onSubmit={onResetPassword}
      >
        <Field label="New password" required hint={`At least 10 characters. ${newPw.length}/10`}>
          <Input type="password" value={newPw} onChange={(e) => setNewPw(e.target.value)} minLength={10} required autoComplete="new-password" />
        </Field>
      </DialogForm>

      <DialogForm
        open={!!roleOp}
        onOpenChange={(v) => { if (!v) setRoleOp(null); }}
        title="Add a role"
        description={roleOp ? <>Give <span className="font-mono">{roleOp.email}</span> another role.</> : undefined}
        size="sm"
        submitLabel="Add role"
        busyLabel="Adding…"
        busy={actionBusy}
        error={actionErr}
        onSubmit={onAddRole}
      >
        {roleOp && (
          <Field label="Role" required>
            <Select key={roleOp.id} name="role" required defaultValue={missingRoles(roleOp)[0] ?? ""}>
              {missingRoles(roleOp).map((r) => <option key={r} value={r}>{roleLabel(r)}</option>)}
            </Select>
          </Field>
        )}
      </DialogForm>

      <ConfirmDialog
        open={!!removeRole}
        onOpenChange={(v) => { if (!v) setRemoveRole(null); }}
        title={`Remove ${removeRole ? roleLabel(removeRole.role) : "role"}?`}
        description={removeRole ? <>From <span className="font-mono">{removeRole.op.email}</span>. You can add it back later.</> : undefined}
        confirmLabel="Remove role"
        confirmVariant="danger"
        busy={actionBusy}
        error={actionErr}
        onConfirm={onRemoveRole}
      />

      <ConfirmDialog
        open={!!disableOp}
        onOpenChange={(v) => { if (!v) setDisableOp(null); }}
        title="Disable this operator?"
        description={disableOp ? <span className="font-mono">{disableOp.email}</span> : undefined}
        confirmLabel="Disable operator"
        confirmVariant="danger"
        busy={actionBusy}
        error={actionErr}
        consequences={["They will not be able to sign in."]}
        onConfirm={onDisable}
      />
    </PageShell>
  );
}
