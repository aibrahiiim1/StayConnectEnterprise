"use client";

import { useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { DeleteDialog } from "@/components/delete-dialog";
import { Plus, X, Pencil, Archive, ArchiveRestore, Trash2 } from "lucide-react";
import { formatRelative } from "@/lib/utils";

type Tenant = {
  id: string;
  slug: string;
  name: string;
  status?: string;
  created_at: string;
};

/**
 * Customers (Tenants). This is step 1 of onboarding: a new hotel group is created
 * here, then a Site, then an Appliance is onboarded + activated (the signed
 * appliance license is the entitlement — there is no plan/subscription step).
 * Previously a customer could only be created with direct SQL.
 */
export default function TenantsPage() {
  const [rows, setRows] = useState<Tenant[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);

  async function load() {
    try {
      const r = await api.get<{ data: Tenant[] }>("/v1/tenants");
      setRows(r.data ?? []);
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load");
    }
  }
  useEffect(() => { load(); }, []);

  const [rowBusy, setRowBusy] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);

  async function onCreate(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true); setErr(null);
    const form = new FormData(e.currentTarget);
    const el = e.currentTarget;
    try {
      await api.post("/v1/tenants", {
        slug: String(form.get("slug") || "").trim(),
        name: String(form.get("name") || "").trim(),
      });
      setShowNew(false);
      el.reset();
      load();
    } catch (e: any) {
      if (e instanceof ApiError && e.code === "conflict") {
        setErr("That slug is already taken — pick another.");
      } else {
        setErr(e?.message ?? "Create failed");
      }
    } finally {
      setBusy(false);
    }
  }

  async function onRename(t: Tenant) {
    const name = window.prompt(`Rename "${t.name}" to:`, t.name);
    if (!name || name.trim() === t.name) return;
    setRowBusy(t.id); setErr(null); setMsg(null);
    try { await api.patch(`/v1/tenants/${t.id}`, { name: name.trim() }); setMsg(`Renamed to ${name.trim()}.`); await load(); }
    catch (e: any) { setErr(e?.message ?? "Rename failed"); }
    finally { setRowBusy(null); }
  }
  async function onArchive(t: Tenant) {
    if (!confirm(`Archive "${t.name}"? It's hidden from active lists but everything (sites, appliances, licenses, audit) is kept. You can restore it later.`)) return;
    setRowBusy(t.id); setErr(null); setMsg(null);
    try { await api.post(`/v1/tenants/${t.id}/archive`); setMsg(`${t.name} archived.`); await load(); }
    catch (e: any) { setErr(e?.message ?? "Archive failed"); }
    finally { setRowBusy(null); }
  }
  async function onRestore(t: Tenant) {
    setRowBusy(t.id); setErr(null); setMsg(null);
    try { await api.post(`/v1/tenants/${t.id}/restore`); setMsg(`${t.name} restored.`); await load(); }
    catch (e: any) { setErr(e?.message ?? "Restore failed"); }
    finally { setRowBusy(null); }
  }

  const [delTenant, setDelTenant] = useState<Tenant | null>(null);

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Commercial</div>
          <h1 className="text-2xl font-semibold">Customers</h1>
        </div>
        <Button onClick={() => setShowNew((s) => !s)}>
          {showNew ? <><X size={14} /> Cancel</> : <><Plus size={14} /> New customer</>}
        </Button>
      </div>

      <p className="text-sm text-muted mb-4">
        Onboarding order: <strong>Customer</strong> → Site → onboard &amp; activate an Appliance → License. (No plan or subscription — the signed appliance license is the entitlement.)
      </p>

      {err && <div className="text-err text-sm mb-4">{err}</div>}
      {msg && <div className="text-ok text-sm mb-4">{msg}</div>}

      {showNew && (
        <Card className="mb-6">
          <CardHeader><CardTitle>New customer</CardTitle></CardHeader>
          <CardBody>
            <form onSubmit={onCreate} className="grid grid-cols-1 sm:grid-cols-3 gap-3">
              <div>
                <Label>Slug</Label>
                <Input name="slug" required placeholder="acme-hotels" />
              </div>
              <div className="sm:col-span-2">
                <Label>Name</Label>
                <Input name="name" required placeholder="Acme Hotels Group" />
              </div>
              <div className="sm:col-span-3 flex justify-end">
                <Button type="submit" disabled={busy}>{busy ? "Creating…" : "Create customer"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}

      <Card>
        <CardBody className="p-0">
          {rows === null ? (
            <EmptyState title="Loading…" />
          ) : rows.length === 0 ? (
            <EmptyState title="No customers yet" hint="Create one to begin commercial onboarding." />
          ) : (
            <Table>
              <THead>
                <TR><TH>Slug</TH><TH>Name</TH><TH>Status</TH><TH>Created</TH><TH className="text-right">Manage</TH></TR>
              </THead>
              <tbody>
                {rows.map((t) => {
                  const archived = (t.status ?? "active") === "archived";
                  return (
                  <TR key={t.id}>
                    <TD className="font-mono">{t.slug}</TD>
                    <TD>{t.name}</TD>
                    <TD className="text-muted">{t.status ?? "active"}</TD>
                    <TD className="text-muted">{formatRelative(t.created_at)}</TD>
                    <TD>
                      <div className="flex gap-2 justify-end">
                        <Button size="sm" variant="ghost" disabled={rowBusy === t.id} onClick={() => onRename(t)}><Pencil size={13} /> Rename</Button>
                        {archived
                          ? <Button size="sm" variant="secondary" disabled={rowBusy === t.id} onClick={() => onRestore(t)}><ArchiveRestore size={13} /> Restore</Button>
                          : <Button size="sm" variant="secondary" disabled={rowBusy === t.id} onClick={() => onArchive(t)}><Archive size={13} /> Archive</Button>}
                        <Button size="sm" variant="danger" disabled={rowBusy === t.id} onClick={() => setDelTenant(t)}><Trash2 size={13} /> Delete</Button>
                      </div>
                    </TD>
                  </TR>
                  );
                })}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      <DeleteDialog
        open={!!delTenant}
        onClose={() => setDelTenant(null)}
        onDeleted={() => { setMsg(`Customer permanently deleted.`); load(); }}
        title={`Delete customer "${delTenant?.name ?? ""}"`}
        what="Customer"
        expected={delTenant?.name ?? ""}
        confirmHint="Type the customer name"
        deleteUrl={`/v1/tenants/${delTenant?.id}`}
      />
    </div>
  );
}
