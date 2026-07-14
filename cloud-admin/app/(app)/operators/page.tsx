"use client";

import { useEffect, useState } from "react";
import { api, ApiError, ListResp, Operator } from "@/lib/api";
import { useCustomer } from "@/lib/customer-context";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X, KeyRound } from "lucide-react";

const ROLES = ["tenant_admin", "tenant_operator", "viewer", "billing"] as const;

export default function OperatorsPage() {
  // Operators are per-customer staff. Management requires a concrete customer in
  // the Global Customer Context (choose one top-left); "All Customers" shows a
  // prompt rather than allowing accidental cross-customer staff changes.
  const { me, selectedTenantId: tenantID, selectedTenantName, ready } = useCustomer();
  const allCustomers = tenantID === "";
  const [rows, setRows] = useState<Operator[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);

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
  useEffect(() => { setShowNew(false); load(); }, [ready, tenantID]);

  async function onCreate(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (allCustomers) return;
    setBusy(true); setErr(null);
    const form = new FormData(e.currentTarget);
    try {
      await api.post(`/v1/operators?tenant_id=${tenantID}`, {
        email: (form.get("email") as string).trim(),
        display_name: form.get("display_name"),
        password: form.get("password"),
        role: form.get("role") || "tenant_operator",
      });
      setShowNew(false);
      (e.currentTarget as HTMLFormElement).reset();
      load();
    } catch (e: any) {
      if (e instanceof ApiError && e.body?.error === "limit_exceeded") {
        setErr(`License limit reached: ${e.body.limit_key} (${e.body.current}/${e.body.limit})`);
      } else setErr(e?.message ?? "Create failed");
    } finally { setBusy(false); }
  }

  async function onDisable(id: string) {
    if (allCustomers) return;
    if (!confirm("Disable this operator? They won't be able to sign in.")) return;
    try {
      await api.del(`/v1/operators/${id}?tenant_id=${tenantID}`);
      load();
    } catch (e: any) { setErr(e?.message ?? "Disable failed"); }
  }

  async function onResetPassword(id: string) {
    if (allCustomers) return;
    const pw = prompt("New password (min 10 chars):");
    if (!pw) return;
    try {
      await api.post(`/v1/operators/${id}/set-password?tenant_id=${tenantID}`, { password: pw });
      alert("Password updated.");
    } catch (e: any) { setErr(e?.message ?? "Reset failed"); }
  }

  async function onAddRole(id: string) {
    if (allCustomers) return;
    const role = prompt(`Role to add — one of: ${ROLES.join(", ")}`);
    if (!role || !ROLES.includes(role as any)) return;
    try {
      await api.post(`/v1/operators/${id}/roles?tenant_id=${tenantID}`, { role });
      load();
    } catch (e: any) { setErr(e?.message ?? "Role add failed"); }
  }

  async function onRemoveRole(id: string, role: string) {
    if (allCustomers) return;
    if (!confirm(`Remove role "${role}"?`)) return;
    try {
      await api.del(`/v1/operators/${id}/roles/${role}?tenant_id=${tenantID}`);
      load();
    } catch (e: any) { setErr(e?.message ?? "Role remove failed"); }
  }

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Administration</div>
          <h1 className="text-2xl font-semibold">Operators</h1>
          <div className="mt-1 text-sm text-muted">{allCustomers ? "All Customers" : <>Customer: <span className="text-text font-medium">{selectedTenantName}</span></>}</div>
        </div>
        <Button onClick={() => setShowNew((s) => !s)} disabled={allCustomers}>
          {showNew ? <><X size={14} /> Cancel</> : <><Plus size={14} /> New operator</>}
        </Button>
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      {allCustomers && (
        <Card>
          <CardBody>
            <div className="text-sm text-muted">
              Operators are a customer&apos;s own staff. Select a customer in the <strong>Customer context</strong>
              {" "}selector (top-left) to view and manage its operators.
            </div>
          </CardBody>
        </Card>
      )}

      {showNew && (
        <Card className="mb-6">
          <CardHeader><CardTitle>New operator</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onCreate} className="grid grid-cols-1 sm:grid-cols-4 gap-3">
              <div className="sm:col-span-2"><Label>Email</Label><Input name="email" type="email" required /></div>
              <div className="sm:col-span-2"><Label>Display name</Label><Input name="display_name" /></div>
              <div className="sm:col-span-2"><Label>Initial password (min 10 chars)</Label><Input name="password" type="password" required minLength={10} /></div>
              <div className="sm:col-span-2">
                <Label>Role</Label>
                <select
                  name="role" defaultValue="tenant_operator"
                  className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm"
                >
                  {ROLES.map((r) => <option key={r} value={r}>{r}</option>)}
                </select>
              </div>
              <div className="sm:col-span-4 flex justify-end">
                <Button type="submit" disabled={busy}>{busy ? "Creating…" : "Create"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      {!allCustomers && (
      <Card>
        <CardBody className="p-0">
          {rows === null ? <EmptyState title="Loading…" /> : rows.length === 0 ? (
            <EmptyState title="No operators yet" />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Email</TH><TH>Name</TH><TH>Status</TH><TH>Roles</TH><TH></TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((op) => {
                  const isSelf = me?.operator_id === op.id;
                  return (
                    <TR key={op.id}>
                      <TD>
                        <div className="font-mono text-sm">{op.email}</div>
                        {isSelf && <div className="text-xs text-brand">you</div>}
                      </TD>
                      <TD className="text-muted">{op.display_name || "—"}</TD>
                      <TD>
                        <Badge tone={op.status === "active" ? "ok" : op.status === "disabled" ? "err" : "warn"}>
                          {op.status}
                        </Badge>
                      </TD>
                      <TD>
                        <div className="flex flex-wrap gap-1">
                          {(op.roles ?? []).map((r) => (
                            <button
                              key={r.id}
                              onClick={() => !isSelf && onRemoveRole(op.id, r.role)}
                              title={isSelf ? "" : "Click to remove"}
                              disabled={isSelf}
                            >
                              <Badge tone={r.role === "platform_admin" ? "info" : "default"}>{r.role}</Badge>
                            </button>
                          ))}
                        </div>
                      </TD>
                      <TD className="text-right space-x-2 whitespace-nowrap">
                        <Button size="sm" variant="ghost" onClick={() => onAddRole(op.id)}>+ role</Button>
                        <Button size="sm" variant="ghost" onClick={() => onResetPassword(op.id)}>
                          <KeyRound size={14} /> Reset
                        </Button>
                        {!isSelf && <Button size="sm" variant="ghost" onClick={() => onDisable(op.id)}>Disable</Button>}
                      </TD>
                    </TR>
                  );
                })}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>
      )}
    </div>
  );
}
