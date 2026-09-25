"use client";

import { useEffect, useMemo, useState } from "react";
import { BadgeCheck, Ban, Download, PauseCircle, PlayCircle, Plus, RefreshCw } from "lucide-react";
import { api, withStepUp, ListResp, License, Site, FleetAppliance } from "@/lib/api";
import { useCustomer } from "@/lib/customer-context";
import { Card } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { ConfirmDialog, DialogForm } from "@/components/ui/dialog";
import { PageHeader, PageShell, StatCard, Toolbar } from "@/components/ui/page";
import { FilterChips, SearchInput } from "@/components/ui/data";
import { Meter, SkeletonRows } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { AllCustomersNotice, CustomerScope } from "@/components/customer-scope";
import { RoleRestricted } from "@/components/role-restricted";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { usePermissions } from "@/lib/permissions";
import { formatDate, formatRelative, errMsg } from "@/lib/utils";
import { graceEndOf, licenseState } from "@/lib/license-state";

type ApplianceRow = { id: string; site_id: string; serial: string; name: string };
type StateFilter = "all" | "active" | "grace" | "expired" | "suspended" | "revoked" | "superseded" | "awaiting";

/**
 * SIMPLE LICENSE MODEL. A license binds to exactly ONE appliance and carries only: max concurrent online guests,
 * valid_from..valid_until, grace period. Renew/limit/date changes always issue a NEW signed document with a
 * higher license_version (anti-rollback), never a silent mutation of the active document.
 */
export default function LicensesPage() {
  const { selectedTenantId: tenantID, ready } = useCustomer();
  const allCustomers = tenantID === "";
  // Issue/renew/suspend/resume/revoke sit behind auth.RequireRole("platform_admin") (api/licenses.go Routes) and
  // the offline package behind platform.certificates.issue (api/offline_api.go); every role with a customer can
  // READ the list (lib/permissions.ts). A reader who can change nothing gets the read-only notice.
  //
  // Issuing in All customers mode stays disabled even though POST /cloud/v1/licenses carries tenant_id in its
  // body: the form's Site and Appliance lists come from the selected customer, so the context is the customer.
  const { can } = usePermissions();
  const canChange = can["licenses.change"];
  const canPackage = can["licenses.offlinePackage"];
  const toast = useToast();
  const [rows, setRows] = useState<License[] | null>(null);
  const [sites, setSites] = useState<Site[]>([]);
  const [appliances, setAppliances] = useState<ApplianceRow[]>([]);
  const [fleet, setFleet] = useState<FleetAppliance[]>([]);
  const [tenantList, setTenantList] = useState<{ id: string; name: string }[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [stateFilter, setStateFilter] = useState<StateFilter>("all");

  async function load() {
    if (!ready) return;
    try {
      const [lic, st, ap, fl, tn] = await Promise.all([
        api.get<ListResp<License>>(`/cloud/v1/licenses?tenant_id=${tenantID}`),
        api.get<ListResp<Site>>(`/v1/sites?tenant_id=${tenantID}`),
        api.get<ListResp<ApplianceRow>>(`/v1/appliances?tenant_id=${tenantID}`).catch(() => ({ data: [] as ApplianceRow[] })),
        api.get<ListResp<FleetAppliance>>(`/cloud/v1/fleet?tenant_id=${tenantID}`).catch(() => ({ data: [] as FleetAppliance[] })),
        api.get<{ data: { id: string; name: string }[] }>(`/v1/tenants`).catch(() => ({ data: [] })),
      ]);
      setRows(lic.data ?? []);
      setSites(st.data ?? []);
      setAppliances(ap.data ?? []);
      setFleet(fl.data ?? []);
      setTenantList(tn.data ?? []);
      setErr(null);
    } catch (e) { setErr(errMsg(e)); }
  }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { setRows(null); load(); }, [ready, tenantID]);

  const customerOf = (l: License) => tenantList.find((t) => t.id === l.tenant_id)?.name ?? "—";
  const siteName = (sid: string) => sites.find((s) => s.id === sid)?.name ?? sid.slice(0, 8);
  const applianceOf = (l: License) => {
    const id = l.appliance_ids?.[0];
    return id ? appliances.find((a) => a.id === id) : undefined;
  };
  const usageOf = (l: License): { current?: number; at?: string } => {
    const id = l.appliance_ids?.[0];
    if (!id) return {};
    const f = fleet.find((x) => x.appliance_id === id);
    if (!f?.last_usage) return { at: f?.last_usage_at ?? undefined };
    try {
      const u = typeof f.last_usage === "string" ? JSON.parse(f.last_usage) : f.last_usage;
      return { current: u.active_sessions ?? undefined, at: f.last_usage_at ?? undefined };
    } catch { return { at: f.last_usage_at ?? undefined }; }
  };

  // ---- issue ----
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);
  const [issueErr, setIssueErr] = useState<string | null>(null);
  const [formSite, setFormSite] = useState("");
  const formAppliances = useMemo(
    () => appliances.filter((a) => !formSite || a.site_id === formSite),
    [appliances, formSite]);

  async function onIssue(e: React.FormEvent<HTMLFormElement>) {
    if (allCustomers) { setIssueErr("Select a customer in the sidebar to issue a license."); return; }
    setBusy(true); setIssueErr(null);
    const form = new FormData(e.currentTarget);
    try {
      const body: any = {
        tenant_id: tenantID,
        site_id: form.get("site_id"),
        appliance_id: form.get("appliance_id") || undefined,
        max_concurrent_online_guests: Number(form.get("max_guests")) || 0,
        grace_period_days: Number(form.get("grace_days")) || 30,
      };
      const vf = String(form.get("valid_from") || "");
      const vu = String(form.get("valid_until") || "");
      if (vf) body.valid_from = new Date(vf + "T00:00:00Z").toISOString();
      if (vu) body.valid_until = new Date(vu + "T23:59:59Z").toISOString();
      if (!vu) body.valid_days = 365;
      const res = await withStepUp(() => api.post<{ license_id: string; license_version: number }>(`/cloud/v1/licenses`, body));
      toast.success(`License v${res.license_version} issued`, `${res.license_id.slice(0, 8)}…`);
      setShowNew(false);
      setFormSite("");
      load();
    } catch (e) { setIssueErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  // ---- row actions ----
  const [actBusy, setActBusy] = useState(false);
  const [actErr, setActErr] = useState<string | null>(null);
  const [revokeL, setRevokeL] = useState<License | null>(null);
  const [suspendL, setSuspendL] = useState<License | null>(null);
  const [renewL, setRenewL] = useState<License | null>(null);
  const [resumeL, setResumeL] = useState<License | null>(null);
  const [rowBusy, setRowBusy] = useState<string | null>(null);

  async function onRevoke() {
    const l = revokeL; if (!l) return;
    setActBusy(true); setActErr(null);
    try { await withStepUp(() => api.post(`/cloud/v1/licenses/${l.id}/revoke`)); setRevokeL(null); toast.success("License revoked"); load(); }
    catch (e) { setActErr(errMsg(e)); }
    finally { setActBusy(false); }
  }
  async function onSuspend() {
    const l = suspendL; if (!l) return;
    setActBusy(true); setActErr(null);
    try { await withStepUp(() => api.post(`/cloud/v1/licenses/${l.id}/suspend`)); setSuspendL(null); toast.success("License suspended"); load(); }
    catch (e) { setActErr(errMsg(e)); }
    finally { setActBusy(false); }
  }
  async function onResume() {
    const l = resumeL; if (!l) return;
    setActBusy(true); setActErr(null);
    try { await withStepUp(() => api.post(`/cloud/v1/licenses/${l.id}/resume`)); setResumeL(null); toast.success("License resumed"); load(); }
    catch (e) { setActErr(errMsg(e)); }
    finally { setActBusy(false); }
  }
  async function onRenew(e: React.FormEvent<HTMLFormElement>) {
    const l = renewL; if (!l) return;
    const form = new FormData(e.currentTarget);
    const limit = String(form.get("limit") ?? "");
    const days = String(form.get("days") ?? "");
    const grace = String(form.get("grace") ?? "");
    setActBusy(true); setActErr(null);
    try {
      const res = await withStepUp(() => api.post<{ license_version: number }>(`/cloud/v1/licenses/${l.id}/renew`, {
        tenant_id: l.tenant_id,
        site_id: l.site_id,
        appliance_id: l.appliance_ids?.[0] || undefined,
        max_concurrent_online_guests: Number(limit) || 0,
        valid_days: Number(days) || 365,
        grace_period_days: Number(grace) || 30,
      }));
      setRenewL(null);
      toast.success(`Renewed as signed license v${res.license_version}`, "The previous document is superseded and can never be replayed.");
      load();
    } catch (e) { setActErr(errMsg(e)); }
    finally { setActBusy(false); }
  }

  // DOWNLOAD FOR OFFLINE INSTALL. The Central half of the two-step offline renewal: generate + download here,
  // upload in Hotel Admin. Bound to ONE appliance, single-use and expiring; it carries no private key.
  async function onDownloadOffline(l: License) {
    const applianceID = l.appliance_ids?.[0];
    if (!applianceID) { toast.error("Not bound to an appliance yet", "Bind it first."); return; }
    setRowBusy(l.id);
    try {
      const res = await withStepUp(() =>
        api.post<{ package_id: string; package: unknown }>(
          `/cloud/v1/offline-packages/${applianceID}/generate`, { valid_hours: 168 }));
      const ap = applianceOf(l);
      const blob = new Blob([JSON.stringify(res.package, null, 2)], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `activation-${ap?.serial || applianceID.slice(0, 8)}-v${l.license_version ?? 0}.json`;
      a.click();
      URL.revokeObjectURL(url);
      toast.success("Activation package downloaded", "Upload it in Hotel Admin under Appliance & licence. Valid 7 days, single use.");
    } catch (e: unknown) {
      toast.error("Download failed", e instanceof Error ? e.message : String(e));
    } finally { setRowBusy(null); }
  }

  const stateCounts = useMemo(() => {
    const c: Record<string, number> = {};
    for (const l of rows ?? []) { const k = licenseState(l).key; c[k] = (c[k] ?? 0) + 1; }
    return c;
  }, [rows]);

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (rows ?? []).filter((l) => {
      if (stateFilter !== "all" && licenseState(l).key !== stateFilter) return false;
      if (!q) return true;
      return [customerOf(l), siteName(l.site_id), applianceOf(l)?.serial ?? "", `v${l.license_version ?? 0}`]
        .join(" ").toLowerCase().includes(q);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rows, query, stateFilter, sites, appliances, tenantList]);

  const canIssue = canChange && !allCustomers && sites.length > 0;

  return (
    <PageShell width="wide">
      <PageHeader
        eyebrow="Commercial"
        title="Licenses"
        icon={<BadgeCheck />}
        description="Each appliance's signed license: max concurrent online guests, validity window and grace period. Renewing issues a new signed version."
        actions={
          canChange && can["licenses.read"] ? (
            <Button onClick={() => { setIssueErr(null); setShowNew(true); }} disabled={!canIssue}>
              <Plus /> Issue license
            </Button>
          ) : undefined
        }
      >
        <CustomerScope />
      </PageHeader>

      {!can["licenses.read"] ? (
        <RoleRestricted what="Licenses are listed per customer, and your sign-in has none." />
      ) : (
      <>
      {!canChange && !canPackage && <ReadOnlyNotice />}
      {allCustomers && canChange && (
        <AllCustomersNotice>Viewing licenses across all customers. Select a customer in the sidebar to issue a new license.</AllCustomersNotice>
      )}
      {canChange && !allCustomers && rows !== null && sites.length === 0 && (
        <Callout tone="warning" title="No site yet">Create a site first — a license needs a site and an appliance.</Callout>
      )}
      <ErrorBanner err={err} />

      <section aria-label="License counts" className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <StatCard label="Active" value={rows ? stateCounts.active ?? 0 : "—"} tone="ok" icon={<BadgeCheck />} hint="Valid and in force" />
        <StatCard label="In grace" value={rows ? stateCounts.grace ?? 0 : "—"} tone="warn" hint="Past valid-until; guests still served" />
        <StatCard label="Expired or revoked" value={rows ? (stateCounts.expired ?? 0) + (stateCounts.revoked ?? 0) : "—"} tone="err" hint="New guest logins refused" />
        <StatCard label="Awaiting appliance binding" value={rows ? stateCounts.awaiting ?? 0 : "—"} tone="warn" hint="Issued to a site, no appliance yet" />
      </section>

      <Card>
        <Toolbar className="border-b border-border px-4 py-3">
          <SearchInput value={query} onChange={setQuery} placeholder="Search customer, site, serial" label="Search licenses" />
          <FilterChips
            label="License state"
            value={stateFilter}
            onChange={setStateFilter}
            options={[
              { value: "all", label: "All", count: rows?.length ?? 0 },
              { value: "active", label: "Active", count: stateCounts.active ?? 0, tone: "ok" },
              { value: "grace", label: "Grace", count: stateCounts.grace ?? 0, tone: "warn" },
              { value: "expired", label: "Expired", count: stateCounts.expired ?? 0, tone: "err" },
              { value: "suspended", label: "Suspended", count: stateCounts.suspended ?? 0, tone: "warn" },
              { value: "revoked", label: "Revoked", count: stateCounts.revoked ?? 0, tone: "err" },
              { value: "superseded", label: "Superseded", count: stateCounts.superseded ?? 0 },
              { value: "awaiting", label: "Awaiting binding", count: stateCounts.awaiting ?? 0, tone: "warn" },
            ]}
          />
        </Toolbar>
        {rows === null ? (
          <SkeletonRows rows={5} cols={8} />
        ) : rows.length === 0 ? (
          <EmptyState
            icon={<BadgeCheck />}
            title="No licenses yet"
            hint={canChange ? "Activating an appliance under Onboarding issues its license. You can also issue one here." : "Licenses are issued when an appliance is activated."}
            action={canIssue ? <Button onClick={() => setShowNew(true)}><Plus /> Issue license</Button> : undefined}
          />
        ) : visible.length === 0 ? (
          <EmptyState
            title="No licenses match"
            hint="Nothing matches the search or state filter."
            action={<Button variant="secondary" onClick={() => { setQuery(""); setStateFilter("all"); }}>Clear filters</Button>}
          />
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>Customer</TH><TH>Site</TH><TH>Appliance</TH><TH className="hidden md:table-cell">Version</TH>
                <TH>Status</TH><TH>Online / Limit</TH><TH className="hidden lg:table-cell">Usage</TH>
                <TH className="hidden xl:table-cell">Valid</TH><TH className="hidden xl:table-cell">Grace ends</TH>
                <TH className="hidden lg:table-cell">Last sync</TH><TH><span className="sr-only">Actions</span></TH>
              </TR>
            </THead>
            <tbody>
              {visible.map((l) => {
                const s = licenseState(l);
                const ap = applianceOf(l);
                const u = usageOf(l);
                const limit = l.max_concurrent_online_guests ?? 0;
                const pct = limit > 0 && u.current !== undefined ? Math.round((u.current / limit) * 100) : null;
                return (
                  <TR key={l.id}>
                    <TD>{customerOf(l)}</TD>
                    <TD>{siteName(l.site_id)}</TD>
                    <TD className="font-mono text-xs">{ap ? ap.serial : <span className="font-sans italic text-muted-foreground">Not bound</span>}</TD>
                    <TD className="hidden font-mono text-xs md:table-cell">v{l.license_version ?? 0}</TD>
                    <TD><Badge tone={s.tone} dot>{s.label}</Badge></TD>
                    <TD className="font-mono text-xs tabular">
                      {u.current !== undefined ? u.current : "—"} / {limit > 0 ? limit : <span aria-label="unlimited">∞</span>}
                    </TD>
                    <TD className="hidden min-w-24 lg:table-cell">
                      {pct !== null ? <Meter value={pct} max={100} caption={`${pct}%`} /> : <span className="text-muted-foreground">—</span>}
                    </TD>
                    <TD className="hidden whitespace-nowrap text-xs text-muted-foreground xl:table-cell">
                      {l.valid_from ? formatDate(l.valid_from) : formatDate(l.issued_at)} → {formatDate(l.valid_until)}
                    </TD>
                    <TD className="hidden text-xs text-muted-foreground xl:table-cell">{formatDate(graceEndOf(l).toISOString())}</TD>
                    <TD className="hidden text-xs text-muted-foreground lg:table-cell">{u.at ? formatRelative(u.at) : "—"}</TD>
                    <TD>
                      <div className="flex justify-end gap-1">
                        {canChange && (l.status === "active" || l.status === "suspended") && (
                          <Button size="sm" variant="ghost" onClick={() => { setActErr(null); setRenewL(l); }}>
                            <RefreshCw /> Renew
                          </Button>
                        )}
                        {canPackage && (l.appliance_ids?.length ?? 0) > 0 && (
                          <Button size="sm" variant="ghost" disabled={rowBusy === l.id} onClick={() => onDownloadOffline(l)}
                            title="Signed, appliance-bound, single use. Upload in Hotel Admin under Appliance & licence.">
                            <Download /> <span className="hidden 2xl:inline">Download for offline</span>
                            <span className="sr-only 2xl:hidden">Download for offline</span>
                          </Button>
                        )}
                        {canChange && l.status === "active" && (
                          <Button size="sm" variant="ghost" onClick={() => { setActErr(null); setSuspendL(l); }}>
                            <PauseCircle /> Suspend
                          </Button>
                        )}
                        {canChange && l.status === "suspended" && (
                          <Button size="sm" variant="ghost" onClick={() => { setActErr(null); setResumeL(l); }}>
                            <PlayCircle /> Resume
                          </Button>
                        )}
                        {canChange && l.status !== "revoked" && l.status !== "superseded" && (
                          <Button size="sm" variant="ghost" className="text-destructive hover:text-destructive" onClick={() => { setActErr(null); setRevokeL(l); }}>
                            <Ban /> Revoke
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
      </>
      )}

      <DialogForm
        open={showNew}
        onOpenChange={(v) => { setShowNew(v); if (!v) setFormSite(""); }}
        title="Issue license"
        description="A license binds to one appliance. Empty dates mean it starts now and is valid for 365 days."
        submitLabel="Issue license"
        busyLabel="Issuing…"
        busy={busy}
        error={issueErr}
        onSubmit={onIssue}
      >
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Site" required>
            <Select name="site_id" required value={formSite} onChange={(e) => setFormSite(e.target.value)}>
              <option value="">Select a site…</option>
              {sites.map((s) => <option key={s.id} value={s.id}>{s.name} — {s.code}</option>)}
            </Select>
          </Field>
          <Field label="Appliance" required>
            <Select name="appliance_id" required defaultValue="">
              <option value="">Select an appliance…</option>
              {formAppliances.map((a) => <option key={a.id} value={a.id}>{a.serial}{a.name ? ` — ${a.name}` : ""}</option>)}
            </Select>
          </Field>
          <Field label="Max concurrent online guests" hint="0 = unlimited. Across the whole appliance.">
            <Input name="max_guests" type="number" defaultValue={500} min={0} />
          </Field>
          <Field label="Grace period (days)" hint="After expiry, guests are still served with warnings.">
            <Input name="grace_days" type="number" defaultValue={30} min={0} />
          </Field>
          <Field label="Valid from" hint="Empty = now.">
            <Input name="valid_from" type="date" />
          </Field>
          <Field label="Valid until" hint="Empty = 365 days.">
            <Input name="valid_until" type="date" />
          </Field>
        </div>
        <p className="text-caption text-muted-foreground">You may be asked to confirm your password.</p>
      </DialogForm>

      <DialogForm
        open={!!renewL}
        onOpenChange={(v) => { if (!v) setRenewL(null); }}
        title="Renew license"
        description="Renewing issues a new signed license version. The current document becomes Superseded and can never be replayed."
        submitLabel="Renew license"
        busyLabel="Renewing…"
        busy={actBusy}
        error={actErr}
        onSubmit={onRenew}
      >
        {renewL && (
          <div key={renewL.id} className="grid gap-4 sm:grid-cols-3">
            <Field label="Max concurrent online guests" hint="0 = unlimited.">
              <Input name="limit" type="number" min={0} defaultValue={String(renewL.max_concurrent_online_guests ?? 0)} />
            </Field>
            <Field label="Valid for (days from now)">
              <Input name="days" type="number" min={1} defaultValue="365" />
            </Field>
            <Field label="Grace period (days)">
              <Input name="grace" type="number" min={0} defaultValue={String(renewL.grace_period_days || 30)} />
            </Field>
          </div>
        )}
        <p className="text-caption text-muted-foreground">You may be asked to confirm your password.</p>
      </DialogForm>

      <ConfirmDialog
        open={!!suspendL}
        onOpenChange={(v) => { if (!v) setSuspendL(null); }}
        title="Suspend this license?"
        description="You can resume it later."
        confirmLabel="Suspend license"
        confirmVariant="danger"
        busy={actBusy}
        error={actErr}
        consequences={[
          "New guest sign-ins stop on the appliance.",
          "Existing guest sessions are not dropped; they run out naturally.",
          "The guest portal, DHCP, DNS and Hotel Admin stay up.",
        ]}
        onConfirm={onSuspend}
      />

      <ConfirmDialog
        open={!!resumeL}
        onOpenChange={(v) => { if (!v) setResumeL(null); }}
        title="Resume this license?"
        description="The license is back in force until its valid-until date and the appliance accepts new guest sign-ins again. You may be asked to confirm your password."
        confirmLabel="Resume license"
        busy={actBusy}
        error={actErr}
        onConfirm={onResume}
      />

      <ConfirmDialog
        open={!!revokeL}
        onOpenChange={(v) => { if (!v) setRevokeL(null); }}
        title="Revoke this license?"
        description="Revoking permanently ends this license."
        confirmLabel="Revoke license"
        confirmVariant="danger"
        busy={actBusy}
        error={actErr}
        consequences={[
          "The appliance will refuse new guest sign-ins.",
          "Existing guest sessions are not dropped.",
          "Issue a new license to restore service.",
          "It cannot be undone.",
        ]}
        onConfirm={onRevoke}
      />
    </PageShell>
  );
}
