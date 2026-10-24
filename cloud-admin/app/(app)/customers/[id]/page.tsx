"use client";

import Link from "next/link";
import { Suspense, useCallback, useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { Archive, ArchiveRestore, BadgeCheck, MapPin, Pencil, Plus, Server, Trash2 } from "lucide-react";
import {
  api, ApiError, itemsOf, qs, type ApplianceRow, type Customer, type Items, type LicenseRow, type Site,
} from "@/lib/api";
import { Card, SectionCard } from "@/components/ui/card";
import { Button, LinkButton } from "@/components/ui/button";
import { Field, FieldGrid, Input, useFieldErrors } from "@/components/ui/input";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { RowActions } from "@/components/ui/row-actions";
import { DescriptionList } from "@/components/ui/data";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { ConfirmDialog, DialogForm } from "@/components/ui/dialog";
import { PageHeader, PageShell, StatCard, StatGrid } from "@/components/ui/page";
import { TabList, TabPanel } from "@/components/ui/tabs";
import { NotAvailable } from "@/components/ui/patterns";
import { Skeleton } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { DeleteDialog } from "@/components/delete-dialog";
import { StateBadge } from "@/components/status-badge";
import { ListState } from "@/components/record-view";
import { ApplianceTable, sortAppliances } from "@/components/appliance-table";
import { LicenseTable } from "@/components/license-table";
import { UsersManager } from "@/components/users-manager";
import { AuditLog } from "@/components/audit-log";
import { SiteTypeSelect, TimezoneSelect, browserTimezone } from "@/components/placement-fields";
import { DEFAULT_SITE_TYPE, siteTypeLabel } from "@/lib/site-types";
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
  const renameErr = useFieldErrors<"name">();

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

  async function act(fn: () => Promise<unknown>, done: string, nameField = false) {
    setBusy(true);
    setDErr(null);
    renameErr.clear();
    try {
      await fn();
      toast.success(done, c?.name);
      setDialog(null);
      await load();
    } catch (e) {
      if (nameField && e instanceof ApiError && (e.status === 400 || e.status === 409)) renameErr.fail({ name: e.message });
      else setDErr(e);
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
        description={c.created_at ? `Customer since ${formatDay(c.created_at)}` : undefined}
        meta={archived ? <StateBadge info={recordStatusInfo(c.status)} /> : undefined}
        actions={
          can["customers.edit"] ? (
            <RowActions
              subject={c.name}
              primary={{ label: "Rename", icon: <Pencil />, onSelect: () => { setName(c.name); setDErr(null); setDialog("rename"); } }}
              items={[
                archived
                  ? { label: "Restore customer", icon: <ArchiveRestore />, disabled: busy, onSelect: () => act(() => api.post(`/cloud/v1/customers/${c.id}/restore`), "Customer restored") }
                  : { label: "Archive customer", icon: <Archive />, onSelect: () => { setDErr(null); setDialog("archive"); } },
                { label: "Delete customer", icon: <Trash2 />, danger: true, hidden: !can["customers.delete"], onSelect: () => setDialog("delete") },
              ]}
            />
          ) : undefined
        }
      />

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
            <Card className="overflow-hidden">
              <UsersManager
                base={`/cloud/v1/customers/${c.id}/users`}
                roles={CUSTOMER_USER_ROLES}
                canManage={can["users.manage"]}
                meId={me.operator_id}
                canResetPassword={false}
                canExportData={can["team.manage"]}
              />
            </Card>
          )}
          {tab === "activity" && (
            <Card className="overflow-hidden">
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
        disabledReason={!name.trim() ? "To continue, enter the customer's name." : "To continue, enter a different name."}
        onSubmit={() => act(() => api.patch(`/cloud/v1/customers/${c.id}`, { name: name.trim() }), "Customer renamed", true)}
      >
        <Field label="Name" required error={renameErr.error("name")}>
          <Input id={renameErr.id("name")} value={name} onChange={(e) => setName(e.target.value)} autoComplete="off" />
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

/** Include the count's subject in the button name so its purpose is clear to screen readers. */
function countLabel(n: number, one: string, many: string, tab: string): string {
  return `${n} ${n === 1 ? one : many}, open the ${tab} tab`;
}

/** A stat card that switches tab instead of navigating. */
function CountCard({
  label, value, hint, aria, onClick,
}: { label: string; value: number; hint?: string; aria: string; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={aria}
      className="block min-w-0 rounded-lg border border-border bg-card px-4 py-3.5 text-start shadow-card transition-[box-shadow,border-color] duration-base ease-onegate hover:border-border-strong hover:shadow-card-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="mt-1.5 text-metric tabular">{value.toLocaleString()}</div>
      {hint && <div className="mt-1 truncate text-xs text-muted-foreground">{hint}</div>}
    </button>
  );
}

function Summary({ c, onTab }: { c: Customer; onTab: (t: Tab) => void }) {
  return (
    <div className="space-y-5">
      <StatGrid cols={4}>
        <CountCard label="Sites" value={c.sites} aria={countLabel(c.sites, "site", "sites", "Sites")} onClick={() => onTab("sites")} />
        <CountCard
          label="Appliances"
          value={c.appliances}
          hint={`${c.activated} activated`}
          aria={countLabel(c.appliances, "appliance", "appliances", "Appliances")}
          onClick={() => onTab("appliances")}
        />
        <CountCard
          label="Active licenses"
          value={c.licenses_active}
          aria={countLabel(c.licenses_active, "active license", "active licenses", "Licenses")}
          onClick={() => onTab("licenses")}
        />
        <StatCard
          label="Needs attention"
          value={c.attention}
          tone={c.attention > 0 ? "warn" : "default"}
          hint={c.attention > 0 ? "See its appliances" : "Nothing"}
          href={c.attention > 0 ? `/appliances?customer_id=${c.id}` : undefined}
        />
      </StatGrid>
      <SectionCard title="Details">
        <DescriptionList
          columns={2}
          items={[
            { label: "Status", value: <StateBadge info={recordStatusInfo(c.status)} /> },
            { label: "Customer since", value: formatDay(c.created_at) },
            { label: "Short name", value: <span className="font-mono text-xs">{c.slug}</span>, hidden: !c.slug },
          ]}
        />
      </SectionCard>
    </div>
  );
}

type SiteDraft = { name: string; code: string; timezone: string; country: string; siteType: string };

type SiteField = "name" | "timezone" | "country" | "siteType" | "code";

function siteFieldOf(e: ApiError): SiteField | null {
  if (e.status !== 400 && e.status !== 409) return null;
  const m = e.message.toLowerCase();
  if (m.includes("timezone") || m.includes("time zone")) return "timezone";
  if (m.includes("site type") || m.includes("site_type")) return "siteType";
  if (m.includes("country")) return "country";
  if (m.includes("code")) return "code";
  if (m.includes("name")) return "name";
  return null;
}

function Sites({ customerId, canManage, onChanged }: { customerId: string; canManage: boolean; onChanged: () => void }) {
  const toast = useToast();
  const [rows, setRows] = useState<Site[] | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [edit, setEdit] = useState<{ site: Site | null; draft: SiteDraft } | null>(null);
  const [archiveSite, setArchiveSite] = useState<Site | null>(null);
  const [deleteSite, setDeleteSite] = useState<Site | null>(null);
  const [busy, setBusy] = useState(false);
  const [dErr, setDErr] = useState<unknown>(null);
  const siteErr = useFieldErrors<SiteField>();

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

  async function run(fn: () => Promise<unknown>, done: string, detail?: string, fieldOf?: (e: ApiError) => SiteField | null) {
    setBusy(true);
    setDErr(null);
    siteErr.clear();
    try {
      await fn();
      toast.success(done, detail);
      setEdit(null);
      setArchiveSite(null);
      await load();
      onChanged();
    } catch (e) {
      const field = e instanceof ApiError && fieldOf ? fieldOf(e) : null;
      if (field && e instanceof ApiError) siteErr.fail({ [field]: e.message });
      else setDErr(e);
    } finally {
      setBusy(false);
    }
  }

  function save() {
    if (!edit) return;
    const d = edit.draft;
    const body: Record<string, string> = { name: d.name.trim(), timezone: d.timezone.trim(), site_type: d.siteType };
    if (d.country.trim()) body.country = d.country.trim().toUpperCase();
    if (edit.site) {
      const s = edit.site;
      return run(() => api.patch(`/cloud/v1/sites/${s.id}`, body), "Site saved", body.name, siteFieldOf);
    }
    if (d.code.trim()) body.code = d.code.trim();
    return run(() => api.post(`/cloud/v1/customers/${customerId}/sites`, body), "Site created", body.name, siteFieldOf);
  }

  const open = (site: Site | null) => {
    setDErr(null);
    siteErr.clear();
    setEdit({
      site,
      draft: site
        ? {
            name: site.name, code: site.code ?? "", timezone: site.timezone, country: site.country ?? "",
            siteType: site.site_type || DEFAULT_SITE_TYPE,
          }
        : { name: "", code: "", timezone: browserTimezone(), country: "", siteType: DEFAULT_SITE_TYPE },
    });
  };

  const d = edit?.draft;
  const setD = (patch: Partial<SiteDraft>) => setEdit((e) => (e ? { ...e, draft: { ...e.draft, ...patch } } : e));

  return (
    <SectionCard
      flush
      title="Sites"
      description="A site is one physical location, such as a hotel, office or campus."
      actions={canManage ? <Button size="sm" onClick={() => open(null)}><Plus /> New site</Button> : undefined}
    >
      <ErrorBanner err={err} className="m-4" />
      <ListState
        rows={rows}
        cols={5}
        empty={<EmptyState size="sm" icon={<MapPin />} title="No sites yet" hint="Add one here, or when you activate an appliance for this customer." />}
      >
        <Table aria-label="Sites">
          <THead>
            <TR>
              <TH>Site</TH>
              <TH className="hidden sm:table-cell">Type</TH>
              <TH className="hidden md:table-cell">Time zone</TH>
              <TH numeric>Appliances</TH>
              <TH>Status</TH>
              {canManage && <TH className="w-0"><span className="sr-only">Actions</span></TH>}
            </TR>
          </THead>
          <TBody>
            {(rows ?? []).map((s) => (
              <TR key={s.id}>
                <TD>
                  <div className="font-medium">{s.name}</div>
                  <div className="text-xs text-muted-foreground">{[s.code, s.country].filter(Boolean).join(" · ") || "—"}</div>
                </TD>
                <TD nowrap className="hidden sm:table-cell">{siteTypeLabel(s.site_type)}</TD>
                <TD nowrap muted className="hidden md:table-cell">{s.timezone}</TD>
                <TD numeric>
                  {s.appliances > 0 ? (
                    <Link
                      href={`/appliances?customer_id=${customerId}&site_id=${s.id}`}
                      className="rounded underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    >
                      {s.appliances}
                    </Link>
                  ) : 0}
                </TD>
                <TD nowrap><StateBadge info={recordStatusInfo(s.status)} /></TD>
                {canManage && (
                  <TD className="w-0">
                    <RowActions
                      subject={s.name}
                      primary={{ label: "Edit", onSelect: () => open(s) }}
                      items={[
                        s.status === "archived"
                          ? { label: "Restore", icon: <ArchiveRestore />, disabled: busy, onSelect: () => run(() => api.post(`/cloud/v1/sites/${s.id}/restore`), "Site restored", s.name) }
                          : { label: "Archive", icon: <Archive />, onSelect: () => { setDErr(null); setArchiveSite(s); } },
                        { label: "Delete", icon: <Trash2 />, danger: true, onSelect: () => setDeleteSite(s) },
                      ]}
                    />
                  </TD>
                )}
              </TR>
            ))}
          </TBody>
        </Table>
      </ListState>

      <DialogForm
        open={!!edit}
        onOpenChange={(v) => { if (!v) setEdit(null); }}
        title={edit?.site ? `Edit ${edit.site.name}` : "New site"}
        submitLabel={edit?.site ? "Save site" : "Create site"}
        busyLabel="Saving…"
        busy={busy}
        error={dErr}
        disabled={!d?.name.trim() || !d?.timezone.trim()}
        disabledReason={!d?.name.trim() ? "To continue, enter the site's name." : "To continue, choose the site's time zone."}
        onSubmit={save}
      >
        {d && (
          <FieldGrid cols={2}>
            <Field label="Name" required className="sm:col-span-2" error={siteErr.error("name")}>
              <Input id={siteErr.id("name")} value={d.name} onChange={(e) => setD({ name: e.target.value })} autoComplete="off" />
            </Field>
            <Field label="Time zone" required error={siteErr.error("timezone")}>
              <TimezoneSelect id={siteErr.id("timezone")} value={d.timezone} onChange={(v) => setD({ timezone: v })} />
            </Field>
            <Field label="Country" optional hint="Two letters, such as EG." error={siteErr.error("country")}>
              <Input id={siteErr.id("country")} value={d.country} maxLength={2} className="uppercase" onChange={(e) => setD({ country: e.target.value })} />
            </Field>
            <Field label="Site type" hint="Describes the place. It does not turn anything on; the license's modules do." className="sm:col-span-2" error={siteErr.error("siteType")}>
              <SiteTypeSelect id={siteErr.id("siteType")} value={d.siteType} onChange={(v) => setD({ siteType: v })} />
            </Field>
            {!edit?.site && (
              <Field label="Short code" optional hint="Generated from the name when left empty." className="sm:col-span-2" error={siteErr.error("code")}>
                <Input id={siteErr.id("code")} value={d.code} onChange={(e) => setD({ code: e.target.value })} autoComplete="off" />
              </Field>
            )}
          </FieldGrid>
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
    </SectionCard>
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
    <Card className="overflow-hidden">
      <ErrorBanner err={err} className="m-4" />
      <ListState
        rows={rows}
        cols={4}
        empty={
          <EmptyState
            size="sm"
            icon={<Server />}
            title="No appliances yet"
            hint="An appliance joins this customer when it is activated."
            action={canActivate ? <LinkButton href="/appliances?activation=waiting" variant="secondary" size="sm">Appliances waiting for activation</LinkButton> : undefined}
          />
        }
      >
        <ApplianceTable rows={rows ?? []} showCustomer={false} />
      </ListState>
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
    <Card className="overflow-hidden">
      <ErrorBanner err={err} className="m-4" />
      <ListState
        rows={rows}
        cols={4}
        empty={<EmptyState size="sm" icon={<BadgeCheck />} title="No licenses yet" hint="A license is issued when one of its appliances is activated." />}
      >
        <LicenseTable rows={rows ?? []} showCustomer={false} />
      </ListState>
    </Card>
  );
}
