"use client";

import { useEffect, useState } from "react";
import { api, ListResp, Whoami, EdgeOperator } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Plus, X } from "lucide-react";
import { canWrite, SITE_ROLES, ROLE_LABELS, SiteRole } from "@/lib/roles";
import { formatRelative, errMsg } from "@/lib/utils";

export default function OperatorsPage() {
  const [rows, setRows] = useState<EdgeOperator[] | null>(null);
  const [me, setMe] = useState<Whoami | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);

  const writable = canWrite("operators", me?.roles ?? []);

  async function load() {
    try { setRows((await api.get<ListResp<EdgeOperator>>("/operators")).data); }
    catch (e) { setErr(errMsg(e)); }
  }
  useEffect(() => {
    load();
    api.get<Whoami>("/auth/whoami").then(setMe).catch(() => {});
  }, []);

  async function onCreate(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true); setErr(null);
    const form = new FormData(e.currentTarget);
    const pw = form.get("password") as string;
    if (pw.length < 10) { setErr("Password must be at least 10 characters"); setBusy(false); return; }
    try {
      await api.post("/operators", {
        email: form.get("email"),
        display_name: (form.get("display_name") as string) || undefined,
        password: pw,
        role: form.get("role"),
      });
      setShowNew(false);
      (e.target as HTMLFormElement).reset();
      load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function onDisable(op: EdgeOperator) {
    if (op.id === me?.operator_id) { setErr("You cannot disable yourself."); return; }
    if (!confirm(`Disable operator ${op.email}?`)) return;
    try { await api.del(`/operators/${op.id}`); load(); }
    catch (e) { setErr(errMsg(e)); }
  }

  async function onSetPassword(op: EdgeOperator) {
    const pw = prompt(`New password for ${op.email} (min 10 chars):`);
    if (pw == null) return;
    if (pw.length < 10) { setErr("Password must be at least 10 characters"); return; }
    try { await api.post(`/operators/${op.id}/set-password`, { password: pw }); setErr(null); }
    catch (e) { setErr(errMsg(e)); }
  }

  async function onAddRole(op: EdgeOperator, role: string) {
    if (!role) return;
    try { await api.post(`/operators/${op.id}/roles`, { role }); load(); }
    catch (e) { setErr(errMsg(e)); }
  }

  async function onRemoveRole(op: EdgeOperator, role: string) {
    if (op.id === me?.operator_id && role === "site_admin") {
      setErr("You cannot remove your own site_admin role."); return;
    }
    if (!confirm(`Remove role "${role}" from ${op.email}?`)) return;
    try { await api.del(`/operators/${op.id}/roles/${role}`); load(); }
    catch (e) { setErr(errMsg(e)); }
  }

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Site</div>
          <h1 className="text-2xl font-semibold">Operators</h1>
        </div>
        {writable && (
          <Button onClick={() => setShowNew((v) => !v)}>
            {showNew ? <><X size={14} /> Cancel</> : <><Plus size={14} /> New operator</>}
          </Button>
        )}
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      {showNew && writable && (
        <Card className="mb-6">
          <CardHeader><CardTitle>New operator</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onCreate} className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <div><Label>Email</Label><Input name="email" type="email" required placeholder="ops@hotel.com" /></div>
              <div><Label>Display name</Label><Input name="display_name" placeholder="Optional" /></div>
              <div><Label>Password (min 10)</Label><Input name="password" type="password" required minLength={10} /></div>
              <div>
                <Label>Role</Label>
                <select name="role" defaultValue="site_viewer" className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm">
                  {SITE_ROLES.map((r) => <option key={r} value={r}>{ROLE_LABELS[r]}</option>)}
                </select>
              </div>
              <div className="sm:col-span-2 flex justify-end">
                <Button type="submit" disabled={busy}>{busy ? "Creating…" : "Create"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      <Card>
        <CardBody className="p-0">
          {rows === null ? <EmptyState title="Loading…" /> : rows.length === 0 ? (
            <EmptyState title="No operators" />
          ) : (
            <Table>
              <THead>
                <TR><TH>Operator</TH><TH>Roles</TH><TH>Status</TH><TH>Created</TH><TH></TH></TR>
              </THead>
              <tbody>
                {rows.map((op) => {
                  const isMe = op.id === me?.operator_id;
                  return (
                    <TR key={op.id}>
                      <TD>
                        <div>{op.display_name || op.email} {isMe && <span className="text-xs text-brand">(you)</span>}</div>
                        <div className="text-xs text-muted">{op.email}</div>
                      </TD>
                      <TD>
                        <div className="flex flex-wrap gap-1 items-center">
                          {op.roles.length === 0 && <span className="text-muted text-xs">—</span>}
                          {op.roles.map((r) => (
                            <span key={r} className="inline-flex items-center gap-1">
                              <Badge tone="info">{r}</Badge>
                              {writable && (
                                <button
                                  className="text-muted hover:text-err"
                                  title="Remove role"
                                  onClick={() => onRemoveRole(op, r)}
                                ><X size={11} /></button>
                              )}
                            </span>
                          ))}
                          {writable && (
                            <select
                              defaultValue=""
                              onChange={(e) => { onAddRole(op, e.target.value); e.currentTarget.value = ""; }}
                              className="h-6 rounded bg-panel2 border border-border px-1 text-xs text-muted"
                            >
                              <option value="" disabled>+ role</option>
                              {SITE_ROLES.filter((r) => !op.roles.includes(r)).map((r) => (
                                <option key={r} value={r}>{ROLE_LABELS[r as SiteRole]}</option>
                              ))}
                            </select>
                          )}
                        </div>
                      </TD>
                      <TD><Badge tone={op.status === "active" ? "ok" : "err"}>{op.status}</Badge></TD>
                      <TD className="text-muted">{formatRelative(op.created_at)}</TD>
                      <TD className="text-right space-x-2 whitespace-nowrap">
                        {writable && <Button size="sm" variant="ghost" onClick={() => onSetPassword(op)}>Set password</Button>}
                        {writable && op.status === "active" && !isMe && (
                          <Button size="sm" variant="ghost" onClick={() => onDisable(op)}>Disable</Button>
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
    </div>
  );
}
