"use client";

import Link from "next/link";
import { Suspense, useCallback, useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { Archive, ArchiveRestore, MapPin, Pencil, Plus, Server, Trash2 } from "lucide-react";
import {
  api, ApiError, itemsOf, qs, type ApplianceRow, type Customer, type Items, type LicenseRow, type Site,
} from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Button, LinkButton } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { ConfirmDialog, DialogForm } from "@/components/ui/dialog";
import { PageHeader, PageShell } from "@/components/ui/page";
import { TabList, TabPanel } from "@/components/ui/tabs";
import { NotAvailable } from "@/components/ui/patterns";
import { Skeleton, SkeletonRows } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { DeleteDialog } from "@/components/delete-dialog";
import { Fact, StateBadge } from "@/components/status-badge";
import { ApplianceTable, sortAppliances } from "@/components/appliance-table";
import { LicenseTable } from "@/components/license-table";
import { UsersManager } from "@/components/users-manager";
import { AuditLog } from "@/components/audit-log";
import { TimezoneSelect, browserTimezone } from "@/components/placement-fields";
import { CUSTOMER_USER_ROLES, usePermissions } from "@/lib/permissions";
import { useSession } from "@/lib/session";
import { useQueryState } from "@/lib/use-query-state";
import { formatDay, recordStatusInfo } from "@/lib/status";

const TABS = ["summary", "sites", "appliances", "licenses", "users", "activity"] as const;
type Tab = (typeof TABS)[number];

export default function CustomerPage({ params }: { params: { id: string } }) {
  return (
    <Suspense fallback={null}>
      <CustomerView id={params.id} />
    </Suspense>
  );
}

function CustomerView({ id }: { id: string }) {
  const router = useRouter();
  const toast = useToast();
  const { can } = usePermissions();
  const { me } = useSession();
  const [q, setQ] = useQueryState(["tab"] as const);
  const [c, setC] = useState<Customer | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [missing, setMissing] = useState(false);
  const [dialog, setDialog] = useState<"rename" | "archive" | "delete" | null>(null);
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [dErr, setDErr] = useState<unknown>(null);

  const load = useCallback(async () => {
    try {
      setC(await api.get<Customer>(`/cloud/v1/customers/${id}`));
      setErr(null);
    } catch (e) {
      if (e instanceof ApiError && (e.status === 404 || e.status === 403)) setMissing(true);
      else setErr(e);
    }
  }, [id]);
  useEffect(() => { void load(); }, [load]);

  const tabs = TABS.filter((t) => t !== "users" || can["users.read"]);
  const tab: Tab = (tabs as readonly string[]).includes(q.tab) ? (q.tab as Tab) : "summary";
  const idBase = `customer-${id}`;

  async function act(fn: () => Promise<unknown>, done: string) {
    setBusy(true);
    setDErr(null);
    try {
      await fn();
      toast.success(done, c?.name);
      setDialog(null);
      await load();
    } catch (e) {
      setDErr(e);
    } finally {
      setBusy(false);
    }
  }

  if (missing) {
    return (
      <PageShell>
        <PageHeader title="Customer not found" />
        <NotAvailable title="This customer does not exist" reason="It may have been deleted, or you cannot see it." />
      </PageShell>
    );
  }
  if (!c) {
    return (
      <PageShell>
        <ErrorBanner err={err} />
        <div className="space-y-4" aria-busy="true">
          <span className="sr-only">Loading</span>
          <Skeleton className="h-10 w-72" />
          <Skeleton className="h-10" />
          <Skeleton className="h-64" />
        </div>
      </PageShell>
    );
  }

  const archived = c.status === "archived";

  return (
    <PageShell width="wide">
      <PageHeader
        title={c.name}
        description={`${c.sites} ${c.sites === 1 ? "site" : "sites"} · ${c.appliances} ${c.appliances === 1 ? "appliance" : "appliances"}`}
        actions={
          can["customers.edit"] ? (
            <>
              <Button variant="ghost" size="sm" onClick={() => { setName(c.name); setDErr(null); setDialog("rename"); }}>
                <Pencil /> Rename
              </Button>
              {archived ? (
                <Button variant="ghost" size="sm" disabled={busy} onClick={() => act(() => api.post(`/cloud/v1/customers/${c.id}/restore`), "Customer restored")}>
                  <ArchiveRestore /> Restore
                </Button>
              ) : (
                <Button variant="ghost" size="sm" onClick={() => { setDErr(null); setDialog("archive"); }}>
                  <Archive /> Archive
                </Button>
              )}
              {can["customers.delete"] && (
                <Button variant="ghost" size="sm" className="text-destructive hover:text-destructive" onClick={() => setDialog("delete")}>
                  <Trash2 /> Delete
                </Button>
              )}
            </>
          ) : undefined
        }
      >
        {archived && <StateBadge info={recordStatusInfo(c.status)} />}
      </PageHeader>

      <div>
        <TabList
          label="Customer sections"
          idBase={idBase}
          value={tab}
          onChange={(v) => setQ({ tab: v === "summary" ? "" : v })}
          tabs={tabs.map((t) => ({
            value: t,
            label: { summary: "Summary", sites: "Sites", appliances: "Appliances", licenses: "Licenses", users: "Users", activity: "Activity" }[t],
            count: t === "sites" ? c.sites : t === "appliances" ? c.appliances : undefined,
          }))}
        />
        <TabPanel idBase={idBase} value={tab} className="pt-5">
          {tab === "summary" && <Summary c={c} onTab={(t) => setQ({ tab: t })} />}
          {tab === "sites" && <Sites customerId={c.id} canManage={can["sites.manage"]} onChanged={load} />}
          {tab === "appliances" && <CustomerAppliances customerId={c.id} canActivate={can["appliances.activate"]} />}
          {tab === "licenses" && <CustomerLicenses customerId={c.id} />}
          {tab === "users" && (
            <Card>
              <UsersManager
                base={`/cloud/v1/customers/${c.id}/users`}
                roles={CUSTOMER_USER_ROLES}
                canManage={can["users.manage"]}
                meId={me.operator_id}
                canResetPassword={false}
              />
            </Card>
          )}
          {tab === "activity" && (
            <Card>
              <AuditLog filters={{ customer_id: c.id }} showCustomer={false} />
            </Card>
          )}
        </TabPanel>
      </div>

      <DialogForm
        open={dialog === "rename"}
        onOpenChange={(v) => { if (!v) setDialog(null); }}
        title="Rename customer"
        size="sm"
        submitLabel="Save name"
        busyLabel="Saving…"
        busy={busy}
        error={dErr}
        disabled={!name.trim() || name.trim() === c.name}
        onSubmit={() => act(() => api.patch(`/cloud/v1/customers/${c.id}`, { name: name.trim() }), "Customer renamed")}
      >
        <Field label="Name" required>
          <Input value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" />
        </Field>
      </DialogForm>

      <ConfirmDialog
        open={dialog === "archive"}
        onOpenChange={(v) => { if (!v) setDialog(null); }}
        title={`Archive ${c.name}?`}
        description="Archived customers are hidden from lists and cannot receive new appliances. Nothing is deleted, and you can restore it."
        confirmLabel="Archive customer"
        busy={busy}
        error={dErr}
        onConfirm={() => act(() => api.post(`/cloud/v1/customers/${c.id}/archive`), "Customer archived")}
      />

      <DeleteDialog
        open={dialog === "delete"}
        onClose={() => setDialog(null)}
        onDeleted={() => { toast.success("Customer deleted", c.name); router.push("/customers"); }}
        title={`Delete ${c.name}`}
        what="Customer"
        expected={c.name}
        confirmHint="Type the customer name"
        deleteUrl={`/cloud/v1/customers/${c.id}`}
        consequences={[
          "Refused while it still has sites or appliances — remove those first.",
          "Its audit history is kept.",
          "It cannot be undone.",
        ]}
        blockerHref={(b) => (/appliance/.test(b.type) ? `/customers/${c.id}?tab=appliances` : /site/.test(b.type) ? `/customers/${c.id}?tab=sites` : null)}
      />
    </PageShell>
  );
}

function Summary({ c, onTab }: { c: Customer; onTab: (t: Tab) => void }) {
  return (
    <div className="grid gap-5 lg:grid-cols-3">
      <Card className="lg:col-span-2">
        <CardBody>
          <dl className="grid gap-5 sm:grid-cols-3">
            <Fact label="Sites">
              <button type="button" className="text-headline tabular hover:underline" onClick={() => onTab("sites")}>{c.sites}</button>
            </Fact>
            <Fact label="Appliances">
              <button type="button" className="text-headline tabular hover:underline" onClick={() => onTab("appliances")}>{c.appliances}</button>
              <div className="text-caption text-muted-foreground">{c.activated} activated</div>
            </Fact>
            <Fact label="Active licenses">
              <button type="button" className="text-headline tabular hover:underline" onClick={() => onTab("licenses")}>{c.licenses_active}</button>
            </Fact>
          </dl>
        </CardBody>
      </Card>
      <Card>
        <CardBody className="space-y-3">
          <Fact label="Needs attention">
            {c.attention > 0 ? (
              <Link href={`/appliances?customer_id=${c.id}`} className="font-medium text-primary underline-offset-2 hover:underline">
                {c.attention} {c.attention === 1 ? "item" : "items"} — see appliances
              </Link>
            ) : "Nothing"}
          </Fact>
          <Fact label="Customer since">{formatDay(c.created_at)}</Fact>
          <Fact label="Status"><StateBadge info={recordStatusInfo(c.status)} /></Fact>
        </CardBody>
      </Card>
    </div>
  );
}

type SiteDraft = { name: string; code: string; timezone: string; country: string };

function Sites({ customerId, canManage, onChanged }: { customerId: string; canManage: boolean; onChanged: () => void }) {
  const toast = useToast();
  const [rows, setRows] = useState<Site[] | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [edit, setEdit] = useState<{ site: Site | null; draft: SiteDraft } | null>(null);
  const [archiveSite, setArchiveSite] = useState<Site | null>(null);
  const [deleteSite, setDeleteSite] = useState<Site | null>(null);
  const [busy, setBusy] = useState(false);
  const [dErr, setDErr] = useState<unknown>(null);

  const load = useCallback(async () => {
    try {
      setRows(itemsOf(await api.get<Items<Site>>(`/cloud/v1/customers/${customerId}/sites`)));
      setErr(null);
    } catch (e) {
      setErr(e);
      setRows((p) => p ?? []);
    }
  }, [customerId]);
  useEffect(() => { void load(); }, [load]);

  async function run(fn: () => Promise<unknown>, done: string, detail?: string) {
    setBusy(true);
    setDErr(null);
    try {
      await fn();
      toast.success(done, detail);
      setEdit(null);
      setArchiveSite(null);
      await load();
      onChanged();
    } catch (e) {
      setDErr(e);
    } finally {
      setBusy(false);
    }
  }

  function save() {
    if (!edit) return;
    const d = edit.draft;
    const body: Record<string, string> = { name: d.name.trim(), timezone: d.timezone.trim() };
    if (d.country.trim()) body.country = d.country.trim().toUpperCase();
    if (edit.site) {
      const s = edit.site;
      return run(() => api.patch(`/cloud/v1/sites/${s.id}`, body), "Site saved", body.name);
    }
    if (d.code.trim()) body.code = d.code.trim();
    return run(() => api.post(`/cloud/v1/customers/${customerId}/sites`, body), "Site created", body.name);
  }

  const open = (site: Site | null) => {
    setDErr(null);
    setEdit({
      site,
      draft: site
        ? { name: site.name, code: site.code ?? "", timezone: site.timezone, country: site.country ?? "" }
        : { name: "", code: "", timezone: browserTimezone(), country: "" },
    });
  };

  const d = edit?.draft;
  const setD = (patch: Partial<SiteDraft>) => setEdit((e) => (e ? { ...e, draft: { ...e.draft, ...patch } } : e));

  return (
    <Card>
      <div className="flex items-center justify-between gap-3 border-b border-border px-4 py-3">
        <span className="text-sm text-muted-foreground">A site is one physical location, such as a hotel, office or campus.</span>
        {canManage && (
          <Button size="sm" onClick={() => open(null)}><Plus /> New site</Button>
        )}
      </div>
      <ErrorBanner err={err} className="m-4" />
      {rows === null ? (
        <SkeletonRows rows={3} cols={4} />
      ) : rows.length === 0 ? (
        <EmptyState icon={<MapPin />} title="No sites yet" hint="Add one here, or when you activate an appliance for this customer." />
      ) : (
        <Table aria-label="Sites">
          <THead>
            <TR>
              <TH>Site</TH><TH className="hidden md:table-cell">Time zone</TH>
              <TH className="text-end">Appliances</TH><TH>Status</TH><TH><span className="sr-only">Actions</span></TH>
            </TR>
          </THead>
          <tbody>
            {rows.map((s) => (
              <TR key={s.id}>
                <TD>
                  <div className="font-medium">{s.name}</div>
                  <div className="text-caption text-muted-foreground">{[s.code, s.country].filter(Boolean).join(" · ") || "—"}</div>
                </TD>
                <TD className="hidden text-muted-foreground md:table-cell">{s.timezone}</TD>
                <TD className="text-end tabular">
                  {s.appliances > 0 ? (
                    <Link href={`/appliances?customer_id=${customerId}&site_id=${s.id}`} className="underline-offset-2 hover:underline">
                      {s.appliances}
                    </Link>
                  ) : 0}
                </TD>
                <TD><StateBadge info={recordStatusInfo(s.status)} /></TD>
                <TD>
                  {canManage && (
                    <div className="flex justify-end gap-1">
                      <Button size="sm" variant="ghost" onClick={() => open(s)}>
                        <Pencil /> <span className="hidden sm:inline">Edit</span><span className="sr-only"> {s.name}</span>
                      </Button>
                      {s.status === "archived" ? (
                        <Button size="sm" variant="ghost" disabled={busy} onClick={() => run(() => api.post(`/cloud/v1/sites/${s.id}/restore`), "Site restored", s.name)}>
                          <ArchiveRestore /> <span className="hidden sm:inline">Restore</span><span className="sr-only"> {s.name}</span>
                        </Button>
                      ) : (
                        <Button size="sm" variant="ghost" onClick={() => { setDErr(null); setArchiveSite(s); }}>
                          <Archive /> <span className="hidden sm:inline">Archive</span><span className="sr-only"> {s.name}</span>
                        </Button>
                      )}
                      <Button size="sm" variant="ghost" className="text-destructive hover:text-destructive" onClick={() => setDeleteSite(s)}>
                        <Trash2 /><span className="sr-only">Delete {s.name}</span>
                      </Button>
                    </div>
                  )}
                </TD>
              </TR>
            ))}
          </tbody>
        </Table>
      )}

      <DialogForm
        open={!!edit}
        onOpenChange={(v) => { if (!v) setEdit(null); }}
        title={edit?.site ? `Edit ${edit.site.name}` : "New site"}
        submitLabel={edit?.site ? "Save site" : "Create site"}
        busyLabel="Saving…"
        busy={busy}
        error={dErr}
        disabled={!d?.name.trim() || !d?.timezone.trim()}
        onSubmit={save}
      >
        {d && (
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Name" required className="sm:col-span-2">
              <Input value={d.name} onChange={(e) => setD({ name: e.target.value })} autoComplete="off" />
            </Field>
            <Field label="Time zone" required>
              <TimezoneSelect value={d.timezone} onChange={(v) => setD({ timezone: v })} />
            </Field>
            <Field label="Country" hint="Two letters, such as EG. Optional.">
              <Input value={d.country} maxLength={2} className="uppercase" onChange={(e) => setD({ country: e.target.value })} />
            </Field>
            {!edit?.site && (
              <Field label="Short code" hint="Optional. Generated from the name when left empty." className="sm:col-span-2">
                <Input value={d.code} onChange={(e) => setD({ code: e.target.value })} autoComplete="off" />
              </Field>
            )}
          </div>
        )}
      </DialogForm>

      <ConfirmDialog
        open={!!archiveSite}
        onOpenChange={(v) => { if (!v) setArchiveSite(null); }}
        title={`Archive ${archiveSite?.name ?? "site"}?`}
        description="An archived site is hidden when activating appliances. Nothing is deleted, and you can restore it."
        confirmLabel="Archive site"
        busy={busy}
        error={dErr}
        onConfirm={() => {
          const s = archiveSite;
          if (s) return run(() => api.post(`/cloud/v1/sites/${s.id}/archive`), "Site archived", s.name);
        }}
      />

      <DeleteDialog
        open={!!deleteSite}
        onClose={() => setDeleteSite(null)}
        onDeleted={() => { toast.success("Site deleted", deleteSite?.name); void load(); onChanged(); }}
        title={`Delete ${deleteSite?.name ?? "site"}`}
        what="Site"
        expected={deleteSite?.name ?? ""}
        confirmHint="Type the site name"
        deleteUrl={`/cloud/v1/sites/${deleteSite?.id ?? ""}`}
        consequences={["Refused while an appliance is still assigned to it.", "It cannot be undone."]}
        blockerHref={() => `/appliances?customer_id=${customerId}&site_id=${deleteSite?.id ?? ""}`}
      />
    </Card>
  );
}

function CustomerAppliances({ customerId, canActivate }: { customerId: string; canActivate: boolean }) {
  const [rows, setRows] = useState<ApplianceRow[] | null>(null);
  const [err, setErr] = useState<unknown>(null);
  useEffect(() => {
    api.get<Items<ApplianceRow>>(`/cloud/v1/appliances${qs({ customer_id: customerId })}`)
      .then((r) => setRows(sortAppliances(itemsOf(r))))
      .catch((e) => { setErr(e); setRows([]); });
  }, [customerId]);
  return (
    <Card>
      <ErrorBanner err={err} className="m-4" />
      {rows === null ? (
        <SkeletonRows rows={3} cols={4} />
      ) : rows.length === 0 ? (
        <EmptyState
          icon={<Server />}
          title="No appliances yet"
          hint="An appliance joins this customer when it is activated."
          action={canActivate ? <LinkButton href="/appliances?activation=waiting" variant="secondary">Appliances waiting for activation</LinkButton> : undefined}
        />
      ) : (
        <ApplianceTable rows={rows} showCustomer={false} />
      )}
    </Card>
  );
}

function CustomerLicenses({ customerId }: { customerId: string }) {
  const [rows, setRows] = useState<LicenseRow[] | null>(null);
  const [err, setErr] = useState<unknown>(null);
  useEffect(() => {
    api.get<Items<LicenseRow>>(`/cloud/v1/licenses${qs({ customer_id: customerId })}`)
      .then((r) => setRows(itemsOf(r)))
      .catch((e) => { setErr(e); setRows([]); });
  }, [customerId]);
  return (
    <Card>
      <ErrorBanner err={err} className="m-4" />
      {rows === null ? (
        <SkeletonRows rows={3} cols={4} />
      ) : rows.length === 0 ? (
        <EmptyState title="No licenses yet" hint="A license is issued when one of its appliances is activated." />
      ) : (
        <LicenseTable rows={rows} showCustomer={false} />
      )}
    </Card>
  );
}
