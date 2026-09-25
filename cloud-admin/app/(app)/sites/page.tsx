"use client";

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { Archive, ArchiveRestore, MapPin, Pencil, Plus, Trash2 } from "lucide-react";
import { api, ApiError, ListResp, Site } from "@/lib/api";
import { useCustomer } from "@/lib/customer-context";
import { Card } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { DialogForm } from "@/components/ui/dialog";
import { PageHeader, PageShell, Toolbar } from "@/components/ui/page";
import { FilterChips, SearchInput } from "@/components/ui/data";
import { SkeletonRows } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { DeleteDialog } from "@/components/delete-dialog";
import { CustomerScope } from "@/components/customer-scope";
import { formatRelative } from "@/lib/utils";

type StatusFilter = "all" | "active" | "archived";

export default function SitesPage() {
  // Sites are customer-owned. The owning customer comes from the Customer context. "All customers" ("") lists
  // every customer's sites (super-admin fan-out); a Site is still CREATED under one explicit customer, chosen in
  // the New site dialog.
  const { selectedTenantId, selectedTenantName, ready, tenants } = useCustomer();
  const allCustomers = selectedTenantId === "";
  const nameFor = (tid?: string) => tenants.find((t) => t.id === tid)?.name ?? tid ?? "—";
  const toast = useToast();

  const [rows, setRows] = useState<Site[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState<StatusFilter>("all");

  async function load() {
    if (!ready) return;
    setRows(null);
    try {
      const r = await api.get<ListResp<Site>>(`/v1/sites?tenant_id=${selectedTenantId}&status=all`);
      setRows(r.data);
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load");
    }
  }
  // Reload whenever the selected customer changes; clear stale rows first so one customer's data never flashes
  // under another's.
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { setShowNew(false); load(); }, [ready, selectedTenantId]);

  // ---- create ----
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);
  const [createErr, setCreateErr] = useState<string | null>(null);

  async function onCreate(e: React.FormEvent<HTMLFormElement>) {
    setBusy(true); setCreateErr(null);
    const form = new FormData(e.currentTarget);
    // The owning customer: the one selected in the form, otherwise the globally-selected customer.
    const owner = (form.get("tenant_id") as string) || selectedTenantId;
    if (!owner) { setCreateErr("Choose the owning customer for this site."); setBusy(false); return; }
    try {
      await api.post(`/v1/sites?tenant_id=${owner}`, {
        code: form.get("code"),
        name: form.get("name"),
        timezone: form.get("timezone") || "UTC",
        country: form.get("country") || undefined,
      });
      setShowNew(false);
      toast.success("Site created", String(form.get("name") ?? ""));
      load();
    } catch (e: any) {
      if (e instanceof ApiError && e.body?.error === "limit_exceeded") {
        setCreateErr(`License limit reached: ${e.body.limit_key} (${e.body.current}/${e.body.limit})`);
      } else {
        setCreateErr(e?.message ?? "Create failed");
      }
    } finally {
      setBusy(false);
    }
  }

  // ---- edit ----
  const [editSite, setEditSite] = useState<Site | null>(null);
  const [editBusy, setEditBusy] = useState(false);
  const [editErr, setEditErr] = useState<string | null>(null);

  async function onEdit(e: React.FormEvent<HTMLFormElement>) {
    const s = editSite;
    if (!s) return;
    const form = new FormData(e.currentTarget);
    const name = String(form.get("name") ?? "");
    const timezone = String(form.get("timezone") ?? "");
    const country = String(form.get("country") ?? "");
    setEditBusy(true); setEditErr(null);
    try {
      await api.patch(`/v1/sites/${s.id}?tenant_id=${s.tenant_id}`, {
        name: name.trim() || s.name,
        timezone: timezone.trim() || "UTC",
        country: country.trim() || undefined,
      });
      setEditSite(null);
      toast.success("Site updated", name.trim() || s.name);
      load();
    } catch (e: any) {
      setEditErr(e?.message ?? "Update failed");
    } finally {
      setEditBusy(false);
    }
  }

  // ---- archive / restore / delete ----
  const [rowBusy, setRowBusy] = useState<string | null>(null);
  const [delSite, setDelSite] = useState<Site | null>(null);

  async function onArchive(s: Site) {
    setErr(null); setRowBusy(s.id);
    try { await api.post(`/v1/sites/${s.id}/archive?tenant_id=${s.tenant_id}`); toast.success(`${s.name} archived`); load(); }
    catch (e: any) { toast.error("Archive failed", e?.message); }
    finally { setRowBusy(null); }
  }
  async function onRestore(s: Site) {
    setErr(null); setRowBusy(s.id);
    try { await api.post(`/v1/sites/${s.id}/restore?tenant_id=${s.tenant_id}`); toast.success(`${s.name} restored`); load(); }
    catch (e: any) { toast.error("Restore failed", e?.message); }
    finally { setRowBusy(null); }
  }

  const counts = useMemo(() => {
    const all = rows ?? [];
    const archived = all.filter((s) => (s.status ?? "active") === "archived").length;
    return { all: all.length, archived, active: all.length - archived };
  }, [rows]);

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (rows ?? []).filter((s) => {
      const st = (s.status ?? "active") === "archived" ? "archived" : "active";
      if (status !== "all" && st !== status) return false;
      if (!q) return true;
      return [s.code, s.name, s.timezone, s.country ?? "", allCustomers ? nameFor(s.tenant_id) : ""]
        .join(" ").toLowerCase().includes(q);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rows, query, status, allCustomers, tenants]);

  return (
    <PageShell>
      <PageHeader
        eyebrow="Infrastructure"
        title="Sites"
        icon={<MapPin />}
        description="A site is one physical property — one hotel or resort. It belongs to exactly one customer and holds one or more appliances. Buildings, floors, SSIDs and guest networks are configured on the appliance; they are not sites."
        actions={
          <Button onClick={() => { setCreateErr(null); setShowNew(true); }}>
            <Plus /> New site
          </Button>
        }
      >
        <CustomerScope />
      </PageHeader>

      <ErrorBanner err={err} />

      <Card>
        <Toolbar className="border-b border-border px-4 py-3">
          <SearchInput value={query} onChange={setQuery} placeholder="Search code, name, timezone" label="Search sites" />
          <FilterChips
            label="Status"
            value={status}
            onChange={setStatus}
            options={[
              { value: "all", label: "All", count: counts.all },
              { value: "active", label: "Active", count: counts.active, tone: "ok" },
              { value: "archived", label: "Archived", count: counts.archived },
            ]}
          />
        </Toolbar>
        {rows === null ? (
          <SkeletonRows rows={4} cols={allCustomers ? 7 : 6} />
        ) : rows.length === 0 ? (
          <EmptyState
            icon={<MapPin />}
            title="No sites yet"
            hint={allCustomers ? "No customer has a site yet." : `Create one under ${selectedTenantName} to start managing appliances.`}
            action={<Button onClick={() => setShowNew(true)}><Plus /> New site</Button>}
          />
        ) : visible.length === 0 ? (
          <EmptyState
            title="No sites match"
            hint="Nothing matches the search or status filter."
            action={<Button variant="secondary" onClick={() => { setQuery(""); setStatus("all"); }}>Clear filters</Button>}
          />
        ) : (
          <Table>
            <THead>
              <TR>
                {allCustomers && <TH>Customer</TH>}
                <TH>Code</TH><TH>Name</TH><TH>Status</TH>
                <TH className="hidden md:table-cell">Timezone</TH>
                <TH className="hidden md:table-cell">Country</TH>
                <TH className="hidden lg:table-cell">Created</TH>
                <TH><span className="sr-only">Actions</span></TH>
              </TR>
            </THead>
            <tbody>
              {visible.map((s) => {
                const archived = (s.status ?? "active") === "archived";
                return (
                  <TR key={s.id}>
                    {allCustomers && <TD className="text-muted-foreground">{nameFor(s.tenant_id)}</TD>}
                    <TD className="font-mono text-xs">{s.code}</TD>
                    <TD className="font-medium">{s.name}</TD>
                    <TD>{archived ? <Badge>Archived</Badge> : <Badge tone="ok" dot>Active</Badge>}</TD>
                    <TD className="hidden text-muted-foreground md:table-cell">{s.timezone}</TD>
                    <TD className="hidden text-muted-foreground md:table-cell">{s.country || "—"}</TD>
                    <TD className="hidden text-muted-foreground lg:table-cell">{formatRelative(s.created_at)}</TD>
                    <TD>
                      <div className="flex justify-end gap-1">
                        <Button size="sm" variant="ghost" onClick={() => { setEditErr(null); setEditSite(s); }} aria-label={`Edit ${s.name}`}>
                          <Pencil /> <span className="hidden sm:inline">Edit</span>
                        </Button>
                        {archived ? (
                          <Button size="sm" variant="ghost" disabled={rowBusy === s.id} onClick={() => onRestore(s)}>
                            <ArchiveRestore /> <span className="hidden sm:inline">Restore</span>
                          </Button>
                        ) : (
                          <Button size="sm" variant="ghost" disabled={rowBusy === s.id} onClick={() => onArchive(s)}>
                            <Archive /> <span className="hidden sm:inline">Archive</span>
                          </Button>
                        )}
                        <Button size="sm" variant="ghost" className="text-destructive hover:text-destructive" onClick={() => setDelSite(s)} aria-label={`Delete ${s.name}`}>
                          <Trash2 /> <span className="hidden sm:inline">Delete</span>
                        </Button>
                      </div>
                    </TD>
                  </TR>
                );
              })}
            </tbody>
          </Table>
        )}
      </Card>

      <DialogForm
        open={showNew}
        onOpenChange={setShowNew}
        title="New site"
        description="One physical property. It will belong to the customer you choose."
        submitLabel="Create site"
        busyLabel="Creating…"
        busy={busy}
        error={createErr}
        disabled={tenants.length === 0}
        onSubmit={onCreate}
      >
        {tenants.length === 0 ? (
          <Callout tone="warning">
            No customers exist yet. Create a customer on the <Link href="/tenants" className="underline">Customers</Link> page first.
          </Callout>
        ) : (
          <>
            <Field label="Owning customer" required hint="A site always has exactly one owning customer.">
              <Select name="tenant_id" required defaultValue={allCustomers ? "" : selectedTenantId}>
                {allCustomers && <option value="" disabled>Select a customer…</option>}
                {tenants.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
              </Select>
            </Field>
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Code" required hint="Short and unique, e.g. hurghada.">
                <Input name="code" required placeholder="hurghada" />
              </Field>
              <Field label="Name" required>
                <Input name="name" required placeholder="Coral Sea Resort Hurghada" />
              </Field>
              <Field label="Timezone" hint="Defaults to UTC.">
                <Input name="timezone" placeholder="UTC" />
              </Field>
              <Field label="Country" hint="Two-letter code, optional.">
                <Input name="country" placeholder="EG" />
              </Field>
            </div>
          </>
        )}
      </DialogForm>

      <DialogForm
        open={!!editSite}
        onOpenChange={(v) => { if (!v) setEditSite(null); }}
        title={`Edit ${editSite?.name ?? "site"}`}
        description={editSite ? <>Code <span className="font-mono">{editSite.code}</span> cannot be changed.</> : undefined}
        submitLabel="Save changes"
        busyLabel="Saving…"
        busy={editBusy}
        error={editErr}
        onSubmit={onEdit}
      >
        {editSite && (
          <div key={editSite.id} className="grid gap-4 sm:grid-cols-2">
            <Field label="Name" className="sm:col-span-2">
              <Input name="name" defaultValue={editSite.name} />
            </Field>
            <Field label="Timezone" hint="Empty means UTC.">
              <Input name="timezone" defaultValue={editSite.timezone || "UTC"} />
            </Field>
            <Field label="Country" hint="Two-letter code; empty for none.">
              <Input name="country" defaultValue={editSite.country || ""} />
            </Field>
          </div>
        )}
      </DialogForm>

      <DeleteDialog
        open={!!delSite}
        onClose={() => setDelSite(null)}
        onDeleted={() => { toast.success("Site permanently deleted"); load(); }}
        title={`Delete site "${delSite?.name ?? ""}"`}
        what="Site"
        expected={delSite?.code ?? ""}
        confirmHint="Type the site code"
        deleteUrl={`/v1/sites/${delSite?.id}?tenant_id=${delSite?.tenant_id ?? ""}`}
      />
    </PageShell>
  );
}
