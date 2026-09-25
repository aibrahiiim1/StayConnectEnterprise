"use client";

import { useEffect, useMemo, useState } from "react";
import { Archive, ArchiveRestore, Building2, Pencil, Plus, Trash2 } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Card } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { ConfirmDialog, DialogForm } from "@/components/ui/dialog";
import { PageHeader, PageShell, Toolbar } from "@/components/ui/page";
import { FilterChips, SearchInput } from "@/components/ui/data";
import { SkeletonRows } from "@/components/ui/misc";
import { ConsequenceList, ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { DeleteDialog } from "@/components/delete-dialog";
import { formatRelative } from "@/lib/utils";
import { usePermissions } from "@/lib/permissions";
import { HelpList, HelpSection } from "@/components/help";

type Tenant = {
  id: string;
  slug: string;
  name: string;
  status?: string;
  created_at: string;
};

type StatusFilter = "all" | "active" | "archived";

/**
 * Customers. Step 1 of bringing a hotel group on: a customer is created here, then a Site, then an Appliance is
 * activated (the signed appliance license is the entitlement).
 */
export default function TenantsPage() {
  const toast = useToast();
  // api/tenants.go: create, archive, restore and delete are platform-admin only (IsSuperAdmin). Renaming is
  // allowed to a platform admin, and to ANY role on its own customer (patchTenant checks only the customer).
  // A customer user therefore keeps Rename and loses the rest (lib/permissions.ts).
  const { can } = usePermissions();
  const canCreate = can["customers.create"];
  const canRename = can["customers.rename"];
  const canArchive = can["customers.archive"];
  const canDelete = can["customers.delete"];
  const [rows, setRows] = useState<Tenant[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [status, setStatus] = useState<StatusFilter>("all");

  async function load() {
    try {
      const r = await api.get<{ data: Tenant[] }>("/v1/tenants");
      setRows(r.data ?? []);
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load");
    }
  }
  useEffect(() => { load(); }, []);

  // ---- create ----
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);
  const [createErr, setCreateErr] = useState<string | null>(null);

  async function onCreate(e: React.FormEvent<HTMLFormElement>) {
    setBusy(true); setCreateErr(null);
    const form = new FormData(e.currentTarget);
    const name = String(form.get("name") || "").trim();
    try {
      await api.post("/v1/tenants", {
        slug: String(form.get("slug") || "").trim(),
        name,
      });
      setShowNew(false);
      toast.success("Customer created", name);
      load();
    } catch (e: any) {
      if (e instanceof ApiError && e.code === "conflict") {
        setCreateErr("That slug is already taken — pick another.");
      } else {
        setCreateErr(e?.message ?? "Create failed");
      }
    } finally {
      setBusy(false);
    }
  }

  // ---- row actions ----
  const [rowBusy, setRowBusy] = useState<string | null>(null);
  const [renameT, setRenameT] = useState<Tenant | null>(null);
  const [renameErr, setRenameErr] = useState<string | null>(null);
  const [archiveT, setArchiveT] = useState<Tenant | null>(null);
  const [archiveErr, setArchiveErr] = useState<string | null>(null);
  const [delTenant, setDelTenant] = useState<Tenant | null>(null);

  async function onRename(e: React.FormEvent<HTMLFormElement>) {
    const t = renameT;
    if (!t) return;
    const name = String(new FormData(e.currentTarget).get("name") ?? "");
    if (!name || name.trim() === t.name) { setRenameT(null); return; }
    setRowBusy(t.id); setRenameErr(null);
    try {
      await api.patch(`/v1/tenants/${t.id}`, { name: name.trim() });
      setRenameT(null);
      toast.success(`Renamed to ${name.trim()}`);
      await load();
    } catch (e: any) { setRenameErr(e?.message ?? "Rename failed"); }
    finally { setRowBusy(null); }
  }
  async function onArchive() {
    const t = archiveT;
    if (!t) return;
    setRowBusy(t.id); setArchiveErr(null);
    try {
      await api.post(`/v1/tenants/${t.id}/archive`);
      setArchiveT(null);
      toast.success(`${t.name} archived`);
      await load();
    } catch (e: any) { setArchiveErr(e?.message ?? "Archive failed"); }
    finally { setRowBusy(null); }
  }
  async function onRestore(t: Tenant) {
    setRowBusy(t.id);
    try { await api.post(`/v1/tenants/${t.id}/restore`); toast.success(`${t.name} restored`); await load(); }
    catch (e: any) { toast.error("Restore failed", e?.message); }
    finally { setRowBusy(null); }
  }

  const counts = useMemo(() => {
    const all = rows ?? [];
    const archived = all.filter((t) => (t.status ?? "active") === "archived").length;
    return { all: all.length, archived, active: all.length - archived };
  }, [rows]);

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (rows ?? []).filter((t) => {
      const st = (t.status ?? "active") === "archived" ? "archived" : "active";
      if (status !== "all" && st !== status) return false;
      return !q || `${t.slug} ${t.name}`.toLowerCase().includes(q);
    });
  }, [rows, query, status]);

  return (
    <PageShell>
      <PageHeader
        eyebrow="Commercial"
        title="Customers"
        icon={<Building2 />}
        description="The hotel groups and companies that own sites."
        help={
          <>
            <HelpSection title="Order of work">
              <HelpList
                items={[
                  <>Create the <strong>Customer</strong> here.</>,
                  <>Add its <strong>Site</strong> (one physical property) under Sites.</>,
                  <>Activate an <strong>Appliance</strong> under Onboarding, which issues its <strong>License</strong>.</>,
                ]}
              />
            </HelpSection>
            <HelpSection title="Managing customers">
              <HelpList
                items={[
                  <>The <strong>slug</strong> is lower-case and unique, used in addresses and single sign-on. It cannot be changed; the name can.</>,
                  <><strong>Archive</strong> hides a customer from active lists and keeps everything; <strong>Restore</strong> brings it back.</>,
                  <><strong>Delete</strong> is permanent and asks you to type the customer name.</>,
                  <>Creating, archiving and deleting customers is for platform administrators; a customer&apos;s own users can rename it.</>,
                ]}
              />
            </HelpSection>
          </>
        }
        actions={canCreate ? <Button onClick={() => { setCreateErr(null); setShowNew(true); }}><Plus /> New customer</Button> : undefined}
      />

      {!canCreate && !canRename && !canArchive && !canDelete && <ReadOnlyNotice />}
      <ErrorBanner err={err} />

      <Card>
        <Toolbar className="border-b border-border px-4 py-3">
          <SearchInput value={query} onChange={setQuery} placeholder="Search slug or name" label="Search customers" />
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
          <SkeletonRows rows={4} cols={5} />
        ) : rows.length === 0 ? (
          <EmptyState
            icon={<Building2 />}
            title="No customers yet"
            hint="Create the first customer to start adding sites and activating appliances."
            action={canCreate ? <Button onClick={() => setShowNew(true)}><Plus /> New customer</Button> : undefined}
          />
        ) : visible.length === 0 ? (
          <EmptyState
            title="No customers match"
            hint="Nothing matches the search or status filter."
            action={<Button variant="secondary" onClick={() => { setQuery(""); setStatus("all"); }}>Clear filters</Button>}
          />
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>Slug</TH><TH>Name</TH><TH>Status</TH>
                <TH className="hidden md:table-cell">Created</TH>
                <TH><span className="sr-only">Actions</span></TH>
              </TR>
            </THead>
            <tbody>
              {visible.map((t) => {
                const archived = (t.status ?? "active") === "archived";
                return (
                  <TR key={t.id}>
                    <TD className="font-mono text-xs">{t.slug}</TD>
                    <TD className="font-medium">{t.name}</TD>
                    <TD>{archived ? <Badge>Archived</Badge> : <Badge tone="ok" dot>Active</Badge>}</TD>
                    <TD className="hidden text-muted-foreground md:table-cell">{formatRelative(t.created_at)}</TD>
                    <TD>
                      <div className="flex justify-end gap-1">
                        {canRename && (
                          <Button size="sm" variant="ghost" disabled={rowBusy === t.id} onClick={() => { setRenameErr(null); setRenameT(t); }}>
                            <Pencil /> <span className="hidden sm:inline">Rename</span>
                          </Button>
                        )}
                        {!canArchive ? null : archived ? (
                          <Button size="sm" variant="ghost" disabled={rowBusy === t.id} onClick={() => onRestore(t)}>
                            <ArchiveRestore /> <span className="hidden sm:inline">Restore</span>
                          </Button>
                        ) : (
                          <Button size="sm" variant="ghost" disabled={rowBusy === t.id} onClick={() => { setArchiveErr(null); setArchiveT(t); }}>
                            <Archive /> <span className="hidden sm:inline">Archive</span>
                          </Button>
                        )}
                        {canDelete && (
                          <Button size="sm" variant="ghost" className="text-destructive hover:text-destructive" disabled={rowBusy === t.id} onClick={() => setDelTenant(t)} aria-label={`Delete ${t.name}`}>
                            <Trash2 /> <span className="hidden sm:inline">Delete</span>
                          </Button>
                        )}
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
        title="New customer"
        description="A hotel group or company that owns sites."
        submitLabel="Create customer"
        busyLabel="Creating…"
        busy={busy}
        error={createErr}
        onSubmit={onCreate}
      >
        <Field label="Slug" required hint="Lower-case, unique, used in addresses and single sign-on. e.g. semantics">
          <Input name="slug" required placeholder="semantics" />
        </Field>
        <Field label="Name" required>
          <Input name="name" required placeholder="Semantics" />
        </Field>
      </DialogForm>

      <DialogForm
        open={!!renameT}
        onOpenChange={(v) => { if (!v) setRenameT(null); }}
        title={`Rename ${renameT?.name ?? "customer"}`}
        description={renameT ? <>The slug <span className="font-mono">{renameT.slug}</span> does not change.</> : undefined}
        submitLabel="Rename"
        busyLabel="Renaming…"
        busy={!!renameT && rowBusy === renameT.id}
        error={renameErr}
        onSubmit={onRename}
      >
        {renameT && (
          <Field label="Name" required>
            <Input key={renameT.id} name="name" required defaultValue={renameT.name} />
          </Field>
        )}
      </DialogForm>

      <ConfirmDialog
        open={!!archiveT}
        onOpenChange={(v) => { if (!v) setArchiveT(null); }}
        title={`Archive ${archiveT?.name ?? "customer"}?`}
        description="Archiving hides the customer from active lists. You can restore it later."
        confirmLabel="Archive customer"
        busy={!!archiveT && rowBusy === archiveT.id}
        error={archiveErr}
        onConfirm={onArchive}
      >
        <ConsequenceList
          tone="warning"
          title="What happens"
          items={[
            "The customer is hidden from active lists.",
            "Everything is kept: sites, appliances, licenses and the audit log.",
          ]}
        />
      </ConfirmDialog>

      <DeleteDialog
        open={!!delTenant}
        onClose={() => setDelTenant(null)}
        onDeleted={() => { toast.success("Customer permanently deleted"); load(); }}
        title={`Delete customer "${delTenant?.name ?? ""}"`}
        what="Customer"
        expected={delTenant?.name ?? ""}
        confirmHint="Type the customer name"
        deleteUrl={`/v1/tenants/${delTenant?.id}`}
      />
    </PageShell>
  );
}
