"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import {
  Check, Circle, Download, FileUp, Loader2, PlugZap, PowerOff, RotateCcw, ShieldCheck, Trash2, Wrench,
} from "lucide-react";
import { api, withStepUp, reauth, ApiError } from "@/lib/api";
import { Card, CardBody, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { ConfirmDialog } from "@/components/ui/dialog";
import { PageHeader, PageShell } from "@/components/ui/page";
import { Stepper } from "@/components/ui/data";
import { Segmented } from "@/components/ui/tabs";
import { SkeletonRows, Switch } from "@/components/ui/misc";
import { LiveStatus, ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { DeleteDialog } from "@/components/delete-dialog";
import { RoleRestricted } from "@/components/role-restricted";
import { usePermissions } from "@/lib/permissions";
import { statusWord } from "@/lib/license-state";
import { cn, formatRelative } from "@/lib/utils";

/**
 * Onboarding — connect an appliance. Pick a Pending appliance, choose Customer/Site + license terms, click
 * Activate ONCE. The server runs claim -> assign -> signed assignment -> certificate -> hardware-bound license;
 * the appliance then converges to Active on its own. No token needed for the normal online flow.
 */

type Pending = {
  id: string; serial: string; public_key_fingerprint?: string; source_ip?: string;
  state?: string; first_seen?: string; wan_mac?: string; lan_mac?: string; model?: string; hostname?: string;
};
type Tenant = { id: string; slug: string; name: string };
type Site = { id: string; code: string; name: string };
type AssignmentStatus = { issued?: boolean; state?: string; adopted_at?: string; online?: boolean; license_active?: boolean; converged?: boolean };
type ApplianceRow = { id: string; serial: string; lifecycle_state?: string; tenant_id?: string; wan_mac?: string };

const STEPS = [
  { key: "detected", label: "Detected", detail: "The appliance registered itself and is waiting." },
  { key: "activating", label: "Activating", detail: "Assigning it to the customer and site, issuing its certificate and license." },
  { key: "converging", label: "Appliance converging", detail: "The appliance connects securely and adopts its assignment. This can take a minute." },
  { key: "active", label: "Active", detail: "Online and licensed." },
] as const;

const POLL_SECONDS = 5;

function slugify(s: string) {
  return s.toLowerCase().trim().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 40);
}

type AdvancedAction = {
  a: ApplianceRow; verb: string; title: string; url: string; consequence: string; consequences?: string[];
};

export default function OnboardingPage() {
  const router = useRouter();
  const toast = useToast();
  // Every Onboarding call is catalog-gated (api/appliance_lifecycle.go LifecycleRoutes, offline_activation_api.go,
  // certificates.go): reading needs platform.appliances.view; activating platform.appliances.assign; importing a
  // request, deactivate, reconcile, decommission and delete platform.appliances.manage; the activation package
  // and certificate reissue platform.certificates.issue. Each control shows only for a role holding its
  // permission (lib/permissions.ts). A role that can only read (platform_support) sees the lists and a notice.
  const { can } = usePermissions();
  const canRead = can["onboarding.read"];
  const canActivate = can["onboarding.activate"];
  const canManage = can["onboarding.manage"];
  const canImport = can["onboarding.importRequest"];
  const canCert = can["onboarding.certificates"];
  const readOnly = !canActivate && !canManage && !canImport && !canCert;
  const [pending, setPending] = useState<Pending[] | null>(null);
  const [pendingAt, setPendingAt] = useState<number | null>(null);
  const [pendingFailed, setPendingFailed] = useState(false);
  // OFFLINE FIRST ACTIVATION. Importing a request only registers the appliance as Pending; it then flows through
  // the SAME Activate form below, so customer, site and licence terms are chosen exactly once and by an operator —
  // never taken from the imported file.
  const [offlineBusy, setOfflineBusy] = useState(false);
  const [tenants, setTenants] = useState<Tenant[]>([]);
  // Simple license model — the activation form carries the license terms.
  const [maxGuests, setMaxGuests] = useState("500");
  const [validUntil, setValidUntil] = useState("");
  const [graceDays, setGraceDays] = useState("30");
  const [sites, setSites] = useState<Site[]>([]);

  const [sel, setSel] = useState<Pending | null>(null);
  const [tenantMode, setTenantMode] = useState<"existing" | "new">("existing");
  const [tenantId, setTenantId] = useState("");
  const [newCustomer, setNewCustomer] = useState("");
  const [siteMode, setSiteMode] = useState<"existing" | "new">("existing");
  const [siteId, setSiteId] = useState("");
  const [newSite, setNewSite] = useState("");

  const [password, setPassword] = useState("");
  const [formErr, setFormErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const [phase, setPhase] = useState<string>("");     // "" = form; else running
  const [runErr, setRunErr] = useState<string | null>(null);
  const [activatedSerial, setActivatedSerial] = useState<string>("");
  const trackId = useRef<string | null>(null);

  // Registered (already-activated) appliances, for self-service reset.
  const [registered, setRegistered] = useState<ApplianceRow[]>([]);
  const [rowBusy, setRowBusy] = useState<string | null>(null);

  const loadPending = useCallback(async () => {
    try {
      const p = await api.get<{ data: Pending[] }>("/cloud/v1/appliances-admin/pending");
      setPending(p.data ?? []);
      setPendingAt(Date.now());
      setPendingFailed(false);
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) { router.replace("/login"); return; }
      setPending((prev) => prev ?? []);
      setPendingFailed(true);
    }
  }, [router]);

  const loadBase = useCallback(async () => {
    try {
      const t = await api.get<{ data: Tenant[] }>("/v1/tenants");
      setTenants(t.data ?? []);
    } catch { /* ignore */ }
  }, []);

  // All non-pending appliances (fan out across customers — the per-customer list is customer-scoped), so
  // activated appliances can be deactivated/deleted here.
  const loadRegistered = useCallback(async () => {
    try {
      const t = await api.get<{ data: Tenant[] }>("/v1/tenants");
      const per = await Promise.all((t.data ?? []).map((tn) =>
        api.get<{ data: ApplianceRow[] }>(`/v1/appliances?tenant_id=${tn.id}`).then((r) => r.data ?? []).catch(() => [])));
      setRegistered(per.flat());
    } catch { /* ignore */ }
  }, []);

  useEffect(() => { loadPending(); loadBase(); loadRegistered(); }, [loadPending, loadBase, loadRegistered]);
  // Poll the lists while on the form so newly self-registered appliances appear.
  useEffect(() => {
    if (phase) return;
    const t = setInterval(() => { loadPending(); loadRegistered(); }, POLL_SECONDS * 1000);
    return () => clearInterval(t);
  }, [phase, loadPending, loadRegistered]);

  async function importActivationRequest(f: File | null) {
    if (!f) return;
    setOfflineBusy(true);
    try {
      const req = JSON.parse(await f.text());
      const r = await api.post<{ appliance_id: string; serial: string }>(
        "/cloud/v1/offline-activation/requests", req);
      toast.success(`Appliance ${r.serial} imported`, "It is now pending. Select it below, choose customer, site and license terms, then Activate.");
      await loadPending();
    } catch (e: unknown) {
      toast.error("Import failed", e instanceof Error ? e.message : String(e));
    } finally {
      setOfflineBusy(false);
    }
  }

  // Generates the single file that completes a first activation offline. The appliance must already have a
  // customer, site and licence — the same things the online Activate button sets — because the package carries
  // the signed assignment and the signed licence, not a promise of them.
  async function generateActivationPackage(applianceID: string, serial: string) {
    setOfflineBusy(true);
    try {
      const res = await withStepUp(() =>
        api.post<{ package_id: string; package: unknown }>(
          `/cloud/v1/offline-activation/${applianceID}/package`, { valid_hours: 168 }));
      const blob = new Blob([JSON.stringify(res.package, null, 2)], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `activation-package-${serial || applianceID.slice(0, 8)}.json`;
      a.click();
      URL.revokeObjectURL(url);
      toast.success("Activation package downloaded", "Upload it in Hotel Admin under Appliance & licence. Valid 7 days, single use.");
    } catch (e: unknown) {
      toast.error("Activation package failed", e instanceof Error ? e.message : String(e));
    } finally {
      setOfflineBusy(false);
    }
  }

  // ---- deactivate ----
  const [deactivate, setDeactivate] = useState<ApplianceRow | null>(null);
  const [actBusy, setActBusy] = useState(false);
  const [actErr, setActErr] = useState<string | null>(null);

  async function onDeactivate() {
    const a = deactivate; if (!a) return;
    setActBusy(true); setActErr(null); setRowBusy(a.id);
    try {
      await withStepUp(() => api.post(`/cloud/v1/appliances-admin/${a.id}/deactivate`, {}));
      setDeactivate(null);
      toast.success(`${a.serial} deactivated`, "Its license is revoked.");
      await loadRegistered();
    } catch (e) { setActErr(e instanceof Error ? e.message : "Deactivate failed"); }
    finally { setActBusy(false); setRowBusy(null); }
  }

  // ---- delete ----
  const [delApp, setDelApp] = useState<ApplianceRow | null>(null);
  const [delImpact, setDelImpact] = useState<any>(null);
  const [showAdvanced, setShowAdvanced] = useState(false);

  async function openDelete(a: ApplianceRow) {
    setDelApp(a); setDelImpact(null);
    try {
      setDelImpact(await api.get<any>(`/cloud/v1/appliances-admin/${a.id}/delete-impact`));
    } catch { /* preview is best-effort */ }
  }

  // ---- Advanced Support: elevated technical actions (step-up + reason + audit) ----
  const [adv, setAdv] = useState<AdvancedAction | null>(null);

  async function runAdvanced({ reason }: { reason: string }) {
    const x = adv; if (!x) return;
    setActBusy(true); setActErr(null); setRowBusy(x.a.id);
    try {
      await withStepUp(() => api.post(x.url, { reason }));
      setAdv(null);
      toast.success(`${x.a.serial}: ${x.verb} done`);
      await loadRegistered();
    } catch (e) { setActErr(e instanceof Error ? e.message : `${x.verb} failed`); }
    finally { setActBusy(false); setRowBusy(null); }
  }

  useEffect(() => {
    if (tenantMode !== "existing" || !tenantId) { setSites([]); return; }
    api.get<{ data: Site[] }>(`/v1/sites?tenant_id=${tenantId}`).then((r) => setSites(r.data ?? [])).catch(() => setSites([]));
  }, [tenantMode, tenantId]);

  async function resolveTargets(): Promise<{ tenant: string; site: string }> {
    let tid = tenantId;
    if (tenantMode === "new") {
      const name = newCustomer.trim();
      if (!name) throw new Error("Enter a customer name.");
      const slug = slugify(name);
      await api.post("/v1/tenants", { slug, name });
      const r = await api.get<{ data: Tenant[] }>("/v1/tenants");
      tid = (r.data ?? []).find((x) => x.slug === slug)?.id ?? "";
    }
    if (!tid) throw new Error("Pick or create a customer.");
    let sid = siteMode === "existing" ? siteId : "";
    if (siteMode === "new") {
      const name = newSite.trim();
      if (!name) throw new Error("Enter a site name.");
      const code = slugify(name);
      const tz = Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
      await api.post(`/v1/sites?tenant_id=${tid}`, { code, name, timezone: tz });
      const r = await api.get<{ data: Site[] }>(`/v1/sites?tenant_id=${tid}`);
      sid = (r.data ?? []).find((x) => x.code === code)?.id ?? "";
    }
    if (!sid) throw new Error("Pick or create a site.");
    return { tenant: tid, site: sid };
  }

  async function onActivate() {
    setFormErr(null);
    if (!sel) { setFormErr("Select a pending appliance."); return; }
    if (!password.trim()) { setFormErr("Enter your password to authorize activation."); return; }
    setBusy(true);
    try {
      await reauth(password.trim());
      setPassword("");
      const t = await resolveTargets();
      trackId.current = sel.id;
      setActivatedSerial(sel.serial);
      setPhase("activating");
      const body: any = {
        tenant_id: t.tenant, site_id: t.site,
        max_concurrent_online_guests: Number(maxGuests) || 0,
        grace_period_days: Number(graceDays) || 30,
      };
      if (validUntil) body.valid_until = new Date(validUntil + "T23:59:59Z").toISOString();
      else body.valid_days = 365;
      const res = await withStepUp(() =>
        api.post<{ status: string }>(`/cloud/v1/appliances-admin/${sel.id}/activate`, body));
      if (res.status !== "activated") throw new Error("activation did not complete");
      setPhase("converging");
    } catch (e) {
      if (e instanceof ApiError && (e.status === 401 || e.code === "reauth_required")) { setFormErr("Password confirmation failed."); }
      else setFormErr(e instanceof Error ? e.message : "Activation failed.");
      setPhase("");
    } finally {
      setBusy(false);
    }
  }

  // Once activating, poll the assignment adoption to flip to Active.
  const [convergeAt, setConvergeAt] = useState<number | null>(null);
  const advance = useCallback(async () => {
    const id = trackId.current;
    if (!id || phase === "" || phase === "active") return;
    try {
      const a = await api.get<AssignmentStatus>(`/cloud/v1/appliances-admin/${id}/assignment`);
      setConvergeAt(Date.now());
      // "Active" = the appliance has converged: it is online (heartbeating over mTLS) and holds an active
      // license. (adopted_at is legacy/never populated.)
      if (a.converged || a.adopted_at) setPhase("active");
    } catch { /* keep polling */ }
  }, [phase]);
  useEffect(() => {
    if (phase !== "converging") return;
    advance();
    const t = setInterval(advance, 4000);
    return () => clearInterval(t);
  }, [phase, advance]);

  function reset() {
    setPhase(""); setRunErr(null); trackId.current = null; setSel(null);
    setNewCustomer(""); setNewSite(""); setActivatedSerial(""); setConvergeAt(null);
    loadPending();
  }

  const phaseIdx = STEPS.findIndex((s) => s.key === phase);
  const stepperCurrent = phase === "active" ? STEPS.length : Math.max(phaseIdx, 1);

  return (
    <PageShell>
      <PageHeader
        eyebrow="Infrastructure"
        title="Onboarding"
        icon={<PlugZap />}
        description="Connect an appliance. A factory-clean appliance with internet appears here by itself: select it, choose its customer, site and license terms, and activate it once. Assignment, certificate and the signed license all happen for you."
      />

      {!canRead ? (
        <RoleRestricted what="Onboarding is the vendor's appliance activation queue." />
      ) : phase ? (
        <Card>
          <CardHeader>
            <div className="flex items-center gap-2.5">
              {phase === "active"
                ? <ShieldCheck className="size-5 text-success" aria-hidden />
                : <Loader2 className="size-5 animate-spin text-primary motion-reduce:animate-none" aria-hidden />}
              <CardTitle>
                {phase === "active" ? `${activatedSerial} is active` : `Activating ${activatedSerial}`}
              </CardTitle>
            </div>
            {phase === "converging" && <LiveStatus updatedAt={convergeAt} intervalSeconds={4} />}
          </CardHeader>
          <CardBody className="space-y-6">
            <Stepper steps={STEPS.map((s) => s.label)} current={stepperCurrent} />
            <ol className="space-y-3" aria-label="Activation progress">
              {STEPS.map((s, i) => {
                const done = phaseIdx > i || phase === "active" || i === 0;
                const active = s.key === phase && phase !== "active";
                return (
                  <li key={s.key} className="flex items-start gap-3 text-sm" aria-current={active ? "step" : undefined}>
                    {done
                      ? <Check className="mt-0.5 size-5 shrink-0 text-success" aria-hidden />
                      : active
                        ? <Loader2 className="mt-0.5 size-5 shrink-0 animate-spin text-primary motion-reduce:animate-none" aria-hidden />
                        : <Circle className="mt-0.5 size-5 shrink-0 text-muted-foreground/50" aria-hidden />}
                    <div>
                      <div className={cn(active ? "font-semibold" : done ? "font-medium" : "text-muted-foreground")}>
                        {s.label}
                        <span className="sr-only">{done ? " — done" : active ? " — in progress" : " — waiting"}</span>
                      </div>
                      <div className="text-caption text-muted-foreground">{s.detail}</div>
                    </div>
                  </li>
                );
              })}
            </ol>
            <ErrorBanner err={runErr} />
          </CardBody>
          {phase === "active" && (
            <CardFooter className="justify-between">
              <Badge tone="ok" dot>Activated</Badge>
              <Button onClick={reset}><RotateCcw /> Activate another</Button>
            </CardFooter>
          )}
        </Card>
      ) : (
        <>
          {readOnly && <ReadOnlyNotice />}
          <Card>
            <CardHeader>
              <div className="space-y-0.5">
                <CardTitle>Pending activation</CardTitle>
                <CardDescription>
                  Appliances that registered themselves and wait for activation.{canActivate ? " Select one to activate it." : ""}
                </CardDescription>
              </div>
              <LiveStatus
                updatedAt={pendingAt}
                intervalSeconds={POLL_SECONDS}
                error={pendingFailed}
                onRefresh={loadPending}
              />
            </CardHeader>
            {pending === null ? (
              <SkeletonRows rows={3} cols={6} />
            ) : pending.length === 0 ? (
              <EmptyState
                icon={<PlugZap />}
                title="No appliances waiting"
                hint="A factory-clean appliance that can reach Central registers itself and appears here within seconds. For an appliance with no route here, use Offline activation below."
              />
            ) : (
              <Table aria-label="Pending activation">
                <THead>
                  <TR>
                    {canActivate && <TH className="w-10"><span className="sr-only">Selected</span></TH>}
                    <TH>Serial</TH><TH>WAN MAC</TH><TH className="hidden md:table-cell">Model</TH>
                    <TH className="hidden md:table-cell">Source IP</TH><TH className="hidden sm:table-cell">First seen</TH>
                  </TR>
                </THead>
                <tbody>
                  {pending.map((p) => {
                    const selected = sel?.id === p.id;
                    if (!canActivate) {
                      return (
                        <TR key={p.id}>
                          <TD className="font-mono font-medium">{p.serial}</TD>
                          <TD className="font-mono text-xs">{p.wan_mac || "—"}</TD>
                          <TD className="hidden text-muted-foreground md:table-cell">{p.model || "—"}</TD>
                          <TD className="hidden font-mono text-xs text-muted-foreground md:table-cell">{p.source_ip || "—"}</TD>
                          <TD className="hidden text-muted-foreground sm:table-cell">{p.first_seen ? formatRelative(p.first_seen) : "—"}</TD>
                        </TR>
                      );
                    }
                    return (
                      <TR
                        key={p.id}
                        tabIndex={0}
                        aria-selected={selected}
                        onClick={() => { setSel(p); setFormErr(null); }}
                        onKeyDown={(e) => {
                          if (e.key === "Enter" || e.key === " ") { e.preventDefault(); setSel(p); setFormErr(null); }
                        }}
                        className={cn(
                          "cursor-pointer focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring",
                          selected && "bg-primary-subtle/60 [tbody_&]:hover:bg-primary-subtle/70",
                        )}
                      >
                        <TD>
                          {selected
                            ? <Check className="size-4 text-primary" aria-hidden />
                            : <Circle className="size-4 text-muted-foreground/40" aria-hidden />}
                        </TD>
                        <TD className="font-mono font-medium">{p.serial}</TD>
                        <TD className="font-mono text-xs">{p.wan_mac || "—"}</TD>
                        <TD className="hidden text-muted-foreground md:table-cell">{p.model || "—"}</TD>
                        <TD className="hidden font-mono text-xs text-muted-foreground md:table-cell">{p.source_ip || "—"}</TD>
                        <TD className="hidden text-muted-foreground sm:table-cell">{p.first_seen ? formatRelative(p.first_seen) : "—"}</TD>
                      </TR>
                    );
                  })}
                </tbody>
              </Table>
            )}
          </Card>

          {sel && canActivate && (
            <Card aria-labelledby="activate-title" className="border-primary/40">
              <CardHeader>
                <div className="space-y-0.5">
                  <CardTitle id="activate-title">Activate <span className="font-mono">{sel.serial}</span></CardTitle>
                  <CardDescription>
                    {sel.wan_mac ? <>WAN MAC <span className="font-mono">{sel.wan_mac}</span>. </> : null}
                    Choose who owns it, where it is, and its license terms.
                  </CardDescription>
                </div>
                <Button variant="ghost" size="sm" onClick={() => setSel(null)}>Cancel</Button>
              </CardHeader>
              <CardBody>
                <form
                  className="space-y-6"
                  onSubmit={(e) => { e.preventDefault(); void onActivate(); }}
                >
                  <ErrorBanner err={formErr} className="mb-0" />
                  <div className="grid gap-6 lg:grid-cols-2">
                    <fieldset className="space-y-2.5">
                      <div className="flex flex-wrap items-center justify-between gap-2">
                        <legend className="text-label">Customer</legend>
                        <Segmented
                          size="sm"
                          label="Customer"
                          value={tenantMode}
                          onChange={(v) => { setTenantMode(v); if (v === "new") setSiteMode("new"); }}
                          options={[{ value: "existing", label: "Existing" }, { value: "new", label: "New" }]}
                        />
                      </div>
                      {tenantMode === "existing" ? (
                        <Field hint={tenants.length === 0 ? "No customers yet — choose New." : undefined}>
                          <Select aria-label="Customer" value={tenantId} onChange={(e) => { setTenantId(e.target.value); setSiteId(""); }}>
                            <option value="">Select a customer…</option>
                            {tenants.map((t) => <option key={t.id} value={t.id}>{t.name}</option>)}
                          </Select>
                        </Field>
                      ) : (
                        <Field hint="A new customer is created with this name.">
                          <Input aria-label="New customer name" placeholder="Customer name" value={newCustomer} onChange={(e) => setNewCustomer(e.target.value)} />
                        </Field>
                      )}
                    </fieldset>

                    <fieldset className="space-y-2.5">
                      <div className="flex flex-wrap items-center justify-between gap-2">
                        <legend className="text-label">Site</legend>
                        {tenantMode === "existing" ? (
                          <Segmented
                            size="sm"
                            label="Site"
                            value={siteMode}
                            onChange={setSiteMode}
                            options={[{ value: "existing", label: "Existing" }, { value: "new", label: "New" }]}
                          />
                        ) : (
                          <span className="text-caption text-muted-foreground">A new customer needs a new site</span>
                        )}
                      </div>
                      {siteMode === "existing" && tenantMode === "existing" ? (
                        <Field hint={!tenantId ? "Choose the customer first." : sites.length === 0 ? "This customer has no site yet — choose New." : undefined}>
                          <Select aria-label="Site" value={siteId} onChange={(e) => setSiteId(e.target.value)} disabled={!tenantId}>
                            <option value="">Select a site…</option>
                            {sites.map((s) => <option key={s.id} value={s.id}>{s.name}</option>)}
                          </Select>
                        </Field>
                      ) : (
                        <Field hint="A new site is created in your browser's time zone.">
                          <Input aria-label="New site name" placeholder="Site name" value={newSite} onChange={(e) => setNewSite(e.target.value)} />
                        </Field>
                      )}
                    </fieldset>
                  </div>

                  <div className="grid gap-4 sm:grid-cols-3">
                    <Field label="Max concurrent online guests" hint="0 = unlimited. Across the whole appliance, all guest networks.">
                      <Input type="number" min={0} value={maxGuests} onChange={(e) => setMaxGuests(e.target.value)} />
                    </Field>
                    <Field label="Valid until" hint="Empty = 365 days from now.">
                      <Input type="date" value={validUntil} onChange={(e) => setValidUntil(e.target.value)} />
                    </Field>
                    <Field label="Grace period (days)" hint="After expiry, guests are still served with warnings.">
                      <Input type="number" min={0} value={graceDays} onChange={(e) => setGraceDays(e.target.value)} />
                    </Field>
                  </div>

                  <div className="flex flex-wrap items-end gap-4 border-t border-border pt-5">
                    <Field label="Confirm your password" required className="w-full sm:w-72">
                      <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
                    </Field>
                    <Button type="submit" size="lg" disabled={busy}>
                      {busy ? <><Loader2 className="animate-spin motion-reduce:animate-none" /> Activating…</> : "Activate"}
                    </Button>
                  </div>
                </form>
              </CardBody>
            </Card>
          )}

          {canImport && (
          <Card>
            <CardHeader>
              <div className="space-y-0.5">
                <CardTitle>Offline activation</CardTitle>
                <CardDescription>
                  For an appliance with no route to Central. Import the activation request it produced, activate it
                  above as usual, then download its activation package from Registered appliances to carry back.
                  The file only registers the appliance — customer, site and license terms are chosen here, by you.
                </CardDescription>
              </div>
            </CardHeader>
            <CardBody>
              <label
                className={cn(
                  "flex cursor-pointer flex-col items-center justify-center gap-2 rounded-lg border border-dashed border-border-strong px-4 py-6 text-center transition-colors",
                  "hover:bg-accent/50 has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-ring",
                  offlineBusy && "pointer-events-none opacity-60",
                )}
              >
                <FileUp className="size-5 text-muted-foreground" aria-hidden />
                <span className="text-sm font-medium">{offlineBusy ? "Importing…" : "Choose an activation request (.json)"}</span>
                <span className="text-caption text-muted-foreground">The file the appliance saved under Appliance & licence → Offline.</span>
                <input
                  type="file"
                  accept=".json,application/json"
                  disabled={offlineBusy}
                  onChange={(e) => { void importActivationRequest(e.target.files?.[0] ?? null); e.target.value = ""; }}
                  className="sr-only"
                />
              </label>
            </CardBody>
          </Card>
          )}

          {registered.length > 0 && (
            <Card>
              <CardHeader>
                <div className="space-y-0.5">
                  <CardTitle>Registered appliances</CardTitle>
                  <CardDescription>Appliances already activated, for resets and recovery.</CardDescription>
                </div>
                {(canManage || canCert) && (
                  <label className="flex items-center gap-2 text-sm">
                    <Switch checked={showAdvanced} onCheckedChange={setShowAdvanced} label="Advanced Support" />
                    <span>Advanced Support</span>
                  </label>
                )}
              </CardHeader>
              {showAdvanced && (canManage || canCert) && (
                <div className="px-5 pt-4">
                  <Callout tone="warning">
                    Advanced Support shows elevated technical actions. Each asks for a reason, which is written to the
                    audit log, and may ask for your password.
                  </Callout>
                </div>
              )}
              <Table>
                <THead>
                  <TR><TH>Serial</TH><TH>State</TH><TH className="hidden md:table-cell">WAN MAC</TH><TH><span className="sr-only">Actions</span></TH></TR>
                </THead>
                <tbody>
                  {registered.map((a) => (
                    <TR key={a.id}>
                      <TD className="font-mono">{a.serial}</TD>
                      <TD>
                        <Badge tone={a.lifecycle_state === "activated" || a.lifecycle_state === "online" ? "ok" : "default"} dot>
                          {statusWord(a.lifecycle_state)}
                        </Badge>
                      </TD>
                      <TD className="hidden font-mono text-xs md:table-cell">{a.wan_mac || "—"}</TD>
                      <TD>
                        <div className="flex flex-wrap justify-end gap-1">
                          {canManage && (
                            <Button size="sm" variant="ghost" disabled={rowBusy === a.id} onClick={() => { setActErr(null); setDeactivate(a); }}>
                              <PowerOff /> Deactivate
                            </Button>
                          )}
                          {canCert && (
                            <Button size="sm" variant="ghost" disabled={offlineBusy}
                              title="Offline first activation: one signed file carrying the assignment, trust material and license."
                              onClick={() => void generateActivationPackage(a.id, a.serial)}>
                              <Download /> Activation package
                            </Button>
                          )}
                          {showAdvanced && (
                            <>
                              {canCert && (
                                <Button size="sm" variant="ghost" disabled={rowBusy === a.id}
                                  onClick={() => { setActErr(null); setAdv({ a, verb: "reissue certificate", title: "Reissue certificate", url: `/cloud/v1/certificates/${a.id}/issue`, consequence: "Issues a new certificate for this appliance." }); }}>
                                  <Wrench /> Reissue cert
                                </Button>
                              )}
                              {canManage && (
                                <Button size="sm" variant="ghost" disabled={rowBusy === a.id}
                                  onClick={() => { setActErr(null); setAdv({ a, verb: "force reconcile", title: "Force reconcile", url: `/cloud/v1/appliances-admin/${a.id}/force-reconcile`, consequence: "Forces a reconcile of this appliance's assignment and license." }); }}>
                                  <Wrench /> Reconcile
                                </Button>
                              )}
                              {canManage && (
                                <Button size="sm" variant="ghost" className="text-destructive hover:text-destructive" disabled={rowBusy === a.id}
                                  onClick={() => {
                                    setActErr(null);
                                    setAdv({
                                      a, verb: "decommission", title: "Decommission", url: `/cloud/v1/appliances-admin/${a.id}/decommission`,
                                      consequence: "This permanently retires the appliance.",
                                      consequences: [
                                        "The appliance moves to the retired state and its credentials are withdrawn.",
                                        "Any license bound to this appliance is revoked.",
                                        "It cannot be undone.",
                                      ],
                                    });
                                  }}>
                                  <Wrench /> Decommission
                                </Button>
                              )}
                            </>
                          )}
                          {canManage && (
                            <Button size="sm" variant="ghost" className="text-destructive hover:text-destructive" disabled={rowBusy === a.id} onClick={() => openDelete(a)}>
                              <Trash2 /> Delete
                            </Button>
                          )}
                        </div>
                      </TD>
                    </TR>
                  ))}
                </tbody>
              </Table>
              {canManage && (
              <CardFooter className="block text-caption text-muted-foreground">
                <strong className="font-semibold text-foreground">Deactivate</strong> revokes the license (activate again to
                restore). <strong className="font-semibold text-foreground">Delete</strong> removes the appliance, its license,
                assignment and certificate — the appliance then reappears above as Pending. Delete the site or customer
                from their own pages if you also want those gone.
              </CardFooter>
              )}
            </Card>
          )}
        </>
      )}

      <ConfirmDialog
        open={!!deactivate}
        onOpenChange={(v) => { if (!v) setDeactivate(null); }}
        title={`Deactivate ${deactivate?.serial ?? ""}?`}
        description="The appliance can be activated again later."
        confirmLabel="Deactivate"
        confirmVariant="danger"
        busy={actBusy}
        error={actErr}
        consequences={[
          "Its license is revoked: the appliance refuses new guest sign-ins.",
          "Existing guest sessions are not dropped.",
        ]}
        onConfirm={onDeactivate}
      />

      <ConfirmDialog
        open={!!adv}
        onOpenChange={(v) => { if (!v) setAdv(null); }}
        title={adv ? `${adv.title}: ${adv.a.serial}` : ""}
        description={adv?.consequence}
        consequences={adv?.consequences}
        confirmLabel={adv?.title ?? "Confirm"}
        confirmVariant={adv?.verb === "decommission" ? "danger" : "primary"}
        busy={actBusy}
        error={actErr}
        requireReason
        reasonLabel="Reason"
        reasonPlaceholder="Why is this needed?"
        onConfirm={runAdvanced}
      />

      <DeleteDialog
        open={!!delApp}
        onClose={() => { setDelApp(null); setDelImpact(null); }}
        onDeleted={() => { toast.success(`${delApp?.serial ?? "Appliance"} deleted`, "It will register again as Pending."); loadRegistered(); loadPending(); }}
        title={`Delete appliance ${delApp?.serial ?? ""}`}
        what="Appliance"
        expected={delApp?.serial ?? ""}
        confirmHint="Type the appliance serial"
        deleteUrl={`/cloud/v1/appliances-admin/${delApp?.id}`}
        consequences={[
          "The appliance, its assignment and its certificates are removed; its secure access ends.",
          "Any license bound to this appliance is revoked.",
          "The physical appliance registers again as Pending.",
          "It cannot be undone.",
        ]}
        extraImpact={delImpact && (
          <div className="rounded-md border border-border bg-surface p-3.5 text-sm">
            <div className="mb-1.5 font-semibold">This will remove and terminate:</div>
            <ul className="list-disc space-y-0.5 ps-5 text-muted-foreground">
              {(delImpact.terminates ?? []).map((t: string) => <li key={t}>{t}</li>)}
              {Object.entries(delImpact.technical_records ?? {}).map(([k, v]) => (
                <li key={k}>{String(v)} {k.replace(/_/g, " ")}</li>
              ))}
              {(delImpact.licenses_revoked ?? []).length > 0 && <li>{delImpact.licenses_revoked.length} site license(s) revoked</li>}
            </ul>
          </div>
        )}
      />
    </PageShell>
  );
}
