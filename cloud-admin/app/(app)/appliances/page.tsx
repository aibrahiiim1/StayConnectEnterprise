"use client";

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { Eye, Key, Plus, Server, Trash2 } from "lucide-react";
import {
  api, ApiError, Appliance, ListResp, Site,
  BootstrapToken, BootstrapTokenCreated, EffectiveConfig,
} from "@/lib/api";
import { useCustomer } from "@/lib/customer-context";
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { ConfirmDialog, DetailDialog, DialogForm } from "@/components/ui/dialog";
import { PageHeader, PageShell, StatCard, Toolbar } from "@/components/ui/page";
import { SearchInput } from "@/components/ui/data";
import { MonoId, Skeleton, SkeletonRows } from "@/components/ui/misc";
import { LiveStatus, OneTimeReveal, refreshingClass } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { CustomerScope } from "@/components/customer-scope";
import { DeleteDialog } from "@/components/delete-dialog";
import { RoleRestricted } from "@/components/role-restricted";
import { usePermissions } from "@/lib/permissions";
import { statusWord } from "@/lib/license-state";
import { cn, formatRelative, errMsg } from "@/lib/utils";

const toneFor = (status: string) =>
  status === "online" ? "ok" :
  status === "enrolled" ? "info" :
  status === "pending" ? "info" :
  status === "retired" ? "default" : "warn";

// LivePulse — solid green for a fresh online heartbeat, amber when last_seen is older than ~25 s (about to be
// flipped to offline by the sweeper). The word beside it carries the state; the dot only adds liveness.
function LivePulse({ status, lastSeen }: { status: string; lastSeen?: string }) {
  if (status !== "online" || !lastSeen) return null;
  const ageMs = Date.now() - new Date(lastSeen).getTime();
  const stale = ageMs > 25_000; // ahead of the 30s sweeper threshold
  return (
    <span className="relative inline-flex size-2" title={`Last heartbeat ${Math.round(ageMs / 1000)} s ago`}>
      {!stale && <span className="absolute inline-flex size-full animate-ping rounded-full bg-success/60 motion-reduce:hidden" aria-hidden />}
      <span className={cn("relative inline-flex size-2 rounded-full", stale ? "bg-warning" : "bg-success")} aria-hidden />
      <span className="sr-only">{stale ? "Heartbeat is late" : "Heartbeat is fresh"}</span>
    </span>
  );
}

export default function AppliancesPage() {
  // Appliances are owned via Site → Customer. The Customer context scopes the list; "All customers" ("") fans
  // out. Manual create / token mint require a concrete customer (and a Site under it).
  const { selectedTenantId, selectedTenantName, ready, tenants } = useCustomer();
  // Roles (lib/permissions.ts): the /v1/appliances routes apply only the tenant-scope check, so manual create,
  // Config and Delete stay for every role that can list. Enrollment tokens are catalog-gated
  // (platform.enrollment_tokens.create / .revoke, api/enrollment.go TokenRoutes) and are hidden otherwise.
  const { can } = usePermissions();
  const canWrite = can["appliances.write"];
  const canMint = can["enrollmentTokens.create"];
  const canRevokeToken = can["enrollmentTokens.revoke"];
  const allCustomers = selectedTenantId === "";
  const custName = (tid?: string) => tenants.find((t) => t.id === tid)?.name ?? tid ?? "—";
  const toast = useToast();
  const [rows, setRows] = useState<Appliance[] | null>(null);
  const [sites, setSites] = useState<Site[]>([]);
  const [tokens, setTokens] = useState<BootstrapToken[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [loadedAt, setLoadedAt] = useState<number | null>(null);
  const [loading, setLoading] = useState(false);
  const [query, setQuery] = useState("");

  // Re-render every 10s so the LivePulse reflects updated last_seen ages without an API round trip.
  const [, setTick] = useState(0);
  useEffect(() => {
    const i = setInterval(() => setTick((n) => n + 1), 10_000);
    return () => clearInterval(i);
  }, []);

  async function load(keep = false) {
    if (!ready) return;
    if (!keep) setRows(null);
    setErr(null); setLoading(true);
    try {
      // Appliances + sites fan out across customers (super-admin, no tenant_id). Enrollment tokens are strictly
      // customer-scoped and only used by the per-customer forms, so skip them in All-customers mode and never let
      // a tokens failure blank the appliance list.
      const [apps, st, tk] = await Promise.all([
        api.get<ListResp<Appliance>>(`/v1/appliances?tenant_id=${selectedTenantId}`),
        api.get<ListResp<Site>>(`/v1/sites?tenant_id=${selectedTenantId}`).catch(() => ({ data: [] as Site[] })),
        allCustomers
          ? Promise.resolve({ data: [] as BootstrapToken[] })
          : api.get<ListResp<BootstrapToken>>(`/v1/appliance-bootstrap-tokens?tenant_id=${selectedTenantId}`).catch(() => ({ data: [] as BootstrapToken[] })),
      ]);
      setRows(apps.data ?? []);
      setSites(st.data ?? []);
      setTokens(tk.data ?? []);
      setLoadedAt(Date.now());
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load");
    } finally {
      setLoading(false);
    }
  }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { setShowNew(false); setShowMint(false); load(); }, [ready, selectedTenantId]);

  // ---- mint enrollment token ----
  const [showMint, setShowMint] = useState(false);
  const [mintedToken, setMintedToken] = useState<BootstrapTokenCreated | null>(null);
  const [busy, setBusy] = useState(false);
  const [formErr, setFormErr] = useState<string | null>(null);

  async function onMintToken(e: React.FormEvent<HTMLFormElement>) {
    if (allCustomers) return;
    setBusy(true); setFormErr(null);
    const form = new FormData(e.currentTarget);
    try {
      const res = await api.post<BootstrapTokenCreated>(
        `/v1/appliance-bootstrap-tokens?tenant_id=${selectedTenantId}`,
        {
          site_id: form.get("site_id"),
          expected_serial: (form.get("expected_serial") as string) || undefined,
          ttl_hours: Number(form.get("ttl_hours")) || 24,
        },
      );
      setShowMint(false);
      setMintedToken(res);
      load(true);
    } catch (e) { setFormErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  // ---- revoke token ----
  const [revokeTok, setRevokeTok] = useState<BootstrapToken | null>(null);
  const [actBusy, setActBusy] = useState(false);
  const [actErr, setActErr] = useState<string | null>(null);

  async function onRevokeToken() {
    const t = revokeTok; if (!t) return;
    setActBusy(true); setActErr(null);
    try {
      await api.del(`/v1/appliance-bootstrap-tokens/${t.id}?tenant_id=${selectedTenantId}`);
      setRevokeTok(null);
      toast.success("Enrollment token revoked");
      load(true);
    } catch (e) { setActErr(errMsg(e)); }
    finally { setActBusy(false); }
  }

  // ---- effective config ----
  const [effOpen, setEffOpen] = useState<Appliance | null>(null);
  const [effData, setEffData] = useState<EffectiveConfig | null>(null);

  async function onShowEffective(a: Appliance) {
    setEffOpen(a);
    setEffData(null);
    try {
      const res = await api.get<EffectiveConfig>(
        `/v1/appliances/${a.id}/effective-config?tenant_id=${a.tenant_id ?? selectedTenantId}`,
      );
      setEffData(res);
    } catch (e) { setErr(errMsg(e)); setEffOpen(null); }
  }

  // ---- create ----
  const [showNew, setShowNew] = useState(false);

  async function onCreate(e: React.FormEvent<HTMLFormElement>) {
    if (allCustomers) return;
    setBusy(true); setFormErr(null);
    const form = new FormData(e.currentTarget);
    try {
      await api.post(`/v1/appliances?tenant_id=${selectedTenantId}`, {
        site_id: form.get("site_id"),
        serial: form.get("serial"),
        name: form.get("name"),
        model: (form.get("model") as string) || undefined,
      });
      setShowNew(false);
      toast.success("Appliance created", String(form.get("serial") ?? ""));
      load(true);
    } catch (e: any) {
      if (e instanceof ApiError && e.body?.error === "limit_exceeded") {
        setFormErr(`License limit reached: ${e.body.limit_key} (${e.body.current}/${e.body.limit})`);
      } else setFormErr(e?.message ?? "Create failed");
    } finally { setBusy(false); }
  }

  // ---- delete ----
  // DELETE /v1/appliances/{id} takes no body and no password step-up (api/appliances.go deleteAppliance), so the
  // shared DeleteDialog runs without a reason field and without withStepUp — same request as before.
  const [delApp, setDelApp] = useState<Appliance | null>(null);

  const siteName = (sid: string) => sites.find((s) => s.id === sid)?.name ?? sid.slice(0, 8);
  const canCreate = !allCustomers && sites.length > 0;

  const counts = useMemo(() => {
    const all = rows ?? [];
    const online = all.filter((a) => a.status === "online").length;
    const waiting = all.filter((a) => a.status === "enrolled" || a.status === "pending").length;
    const retired = all.filter((a) => a.status === "retired").length;
    return { total: all.length, online, waiting, other: all.length - online - waiting - retired };
  }, [rows]);

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (rows ?? []).filter((a) =>
      !q || [a.name, a.serial, a.status, a.version ?? "", a.site_id ? siteName(a.site_id) : "", allCustomers ? custName(a.tenant_id) : ""]
        .join(" ").toLowerCase().includes(q));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rows, query, sites, tenants, allCustomers]);

  return (
    <PageShell>
      <PageHeader
        eyebrow="Infrastructure"
        title="Appliances"
        icon={<Server />}
        description="Every appliance, where it is and whether it is online. Appliances normally arrive by themselves under Onboarding; the tools here are for recovery."
        actions={
          can["appliances.read"] ? (
            <>
              {canMint && (
                <Button variant="secondary" onClick={() => { setFormErr(null); setShowMint(true); }} disabled={!canCreate}>
                  <Key /> Enrollment token
                </Button>
              )}
              {canWrite && (
                <Button onClick={() => { setFormErr(null); setShowNew(true); }} disabled={!canCreate}>
                  <Plus /> New appliance
                </Button>
              )}
            </>
          ) : undefined
        }
      >
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
          <CustomerScope />
          <LiveStatus updatedAt={loadedAt} refreshing={loading && rows !== null} onRefresh={() => load(true)} />
        </div>
      </PageHeader>

      {!can["appliances.read"] ? (
        <RoleRestricted what="Appliances belong to a customer, and your sign-in has none." />
      ) : (
      <>
      <Callout tone="info" title="Most appliances install zero-touch">
        A factory-clean appliance with internet registers itself and appears under{" "}
        <Link href="/onboarding" className="font-medium underline underline-offset-2">Onboarding</Link> as Pending
        activation, where you choose its customer, site and license terms and click Activate — no token. Enrollment
        tokens are only a recovery lever: an appliance that cannot register itself, or one being deliberately
        re-attached.
      </Callout>
      {allCustomers && (canWrite || canMint) && (
        <Callout tone="neutral">
          Select a customer in the sidebar to add or enroll an appliance — an appliance is created under a site that
          belongs to one customer.
        </Callout>
      )}
      {!allCustomers && rows !== null && sites.length === 0 && (
        <Callout tone="warning">Create a site under <strong>{selectedTenantName}</strong> first — appliances belong to a site.</Callout>
      )}
      <ErrorBanner err={err} />

      <section aria-label="Appliance counts" className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <StatCard label="Appliances" value={rows ? counts.total : <Skeleton className="h-7 w-10" />} icon={<Server />} />
        <StatCard label="Online" value={rows ? counts.online : <Skeleton className="h-7 w-10" />} tone="ok" hint="Heard from recently" />
        <StatCard label="Enrolled or pending" value={rows ? counts.waiting : <Skeleton className="h-7 w-10" />} tone="info" />
        <StatCard label="Offline or other" value={rows ? counts.other : <Skeleton className="h-7 w-10" />} tone="warn" />
      </section>

      <Card className={cn(loading && rows !== null && refreshingClass)}>
        <Toolbar className="border-b border-border px-4 py-3">
          <SearchInput value={query} onChange={setQuery} placeholder="Search name, serial, site" label="Search appliances" />
        </Toolbar>
        {rows === null ? (
          <SkeletonRows rows={4} cols={allCustomers ? 8 : 7} />
        ) : rows.length === 0 ? (
          <EmptyState
            icon={<Server />}
            title="No appliances yet"
            hint="Factory-clean appliances register themselves under Onboarding; activate them there."
            action={<Link href="/onboarding" className="text-sm font-medium text-primary hover:underline">Go to Onboarding</Link>}
          />
        ) : visible.length === 0 ? (
          <EmptyState title="No appliances match" hint="Nothing matches the search."
            action={<Button variant="secondary" onClick={() => setQuery("")}>Clear search</Button>} />
        ) : (
          <Table>
            <THead>
              <TR>
                {allCustomers && <TH>Customer</TH>}
                <TH>Name</TH><TH>Site</TH><TH>Serial</TH>
                <TH>Status</TH><TH className="hidden md:table-cell">Version</TH><TH className="hidden md:table-cell">Last seen</TH>
                <TH><span className="sr-only">Actions</span></TH>
              </TR>
            </THead>
            <tbody>
              {visible.map((a) => (
                <TR key={a.id}>
                  {allCustomers && <TD className="text-muted-foreground">{custName(a.tenant_id)}</TD>}
                  <TD>
                    <div className="font-medium">{a.name}</div>
                    <MonoId value={a.id} title="Appliance id" />
                  </TD>
                  <TD className="text-muted-foreground">{a.site_id ? siteName(a.site_id) : <Badge tone="warn">Unassigned</Badge>}</TD>
                  <TD className="font-mono text-xs">{a.serial}</TD>
                  <TD>
                    <span className="inline-flex items-center gap-2">
                      <LivePulse status={a.status} lastSeen={a.last_seen_at} />
                      <Badge tone={toneFor(a.status) as any}>{statusWord(a.status)}</Badge>
                    </span>
                  </TD>
                  <TD className="hidden font-mono text-xs text-muted-foreground md:table-cell">{a.version || "—"}</TD>
                  <TD className="hidden text-muted-foreground md:table-cell">{a.last_seen_at ? formatRelative(a.last_seen_at) : "—"}</TD>
                  <TD>
                    <div className="flex justify-end gap-1">
                      <Button size="sm" variant="ghost" onClick={() => onShowEffective(a)}><Eye /> Config</Button>
                      {canWrite && (
                        <Button size="sm" variant="ghost" className="text-destructive hover:text-destructive"
                          onClick={() => setDelApp(a)} aria-label={`Delete appliance ${a.serial}`}>
                          <Trash2 /> <span className="hidden sm:inline">Delete</span>
                        </Button>
                      )}
                    </div>
                  </TD>
                </TR>
              ))}
            </tbody>
          </Table>
        )}
      </Card>

      {tokens && tokens.length > 0 && (
        <Card>
          <CardHeader>
            <div className="space-y-0.5">
              <CardTitle>Enrollment tokens</CardTitle>
              <CardDescription>Recovery tokens minted for this customer. The full token is shown only once, when it is created.</CardDescription>
            </div>
          </CardHeader>
          <Table>
            <THead>
              <TR>
                <TH>Hint</TH><TH>Site</TH><TH className="hidden md:table-cell">Serial lock</TH>
                <TH>Status</TH><TH className="hidden sm:table-cell">Expires</TH><TH className="hidden md:table-cell">Created</TH>
                <TH><span className="sr-only">Actions</span></TH>
              </TR>
            </THead>
            <tbody>
              {tokens.map((t) => {
                const consumed = !!t.consumed_at;
                const expired = !consumed && new Date(t.expires_at) < new Date();
                const tone = consumed ? "default" : expired ? "warn" : "info";
                const label = consumed ? "Consumed" : expired ? "Expired" : "Pending";
                return (
                  <TR key={t.id}>
                    <TD className="font-mono text-xs">…{t.token_hint}</TD>
                    <TD className="text-muted-foreground">{siteName(t.site_id)}</TD>
                    <TD className="hidden font-mono text-xs md:table-cell">{t.expected_serial || "—"}</TD>
                    <TD><Badge tone={tone as any}>{label}</Badge></TD>
                    <TD className="hidden text-xs text-muted-foreground sm:table-cell">{formatRelative(t.expires_at)}</TD>
                    <TD className="hidden text-xs text-muted-foreground md:table-cell">{formatRelative(t.created_at)}</TD>
                    <TD className="text-end">
                      {!consumed && canRevokeToken && (
                        <Button size="sm" variant="ghost" className="text-destructive hover:text-destructive"
                          onClick={() => { setActErr(null); setRevokeTok(t); }}>
                          <Trash2 /> Revoke
                        </Button>
                      )}
                    </TD>
                  </TR>
                );
              })}
            </tbody>
          </Table>
        </Card>
      )}
      </>
      )}

      <DialogForm
        open={showMint}
        onOpenChange={setShowMint}
        title="Mint enrollment token"
        description="Only for an appliance that cannot register itself. The token is shown once."
        submitLabel="Mint token"
        busyLabel="Minting…"
        busy={busy}
        error={formErr}
        onSubmit={onMintToken}
      >
        <Field label="Site" required>
          <Select name="site_id" required>
            {sites.map((s) => <option key={s.id} value={s.id}>{s.name} — {s.code}</option>)}
          </Select>
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Serial" hint="Optional. Locks the token to this serial.">
            <Input name="expected_serial" placeholder="APP-HQ-0001" />
          </Field>
          <Field label="Valid for (hours)" hint="1 to 168. Default 24.">
            <Input name="ttl_hours" type="number" defaultValue={24} min={1} max={168} />
          </Field>
        </div>
      </DialogForm>

      <DialogForm
        open={showNew}
        onOpenChange={setShowNew}
        title="New appliance"
        description="Manual registration. Most appliances register themselves under Onboarding instead."
        submitLabel="Create appliance"
        busyLabel="Creating…"
        busy={busy}
        error={formErr}
        onSubmit={onCreate}
      >
        <Field label="Site" required>
          <Select name="site_id" required>
            {sites.map((s) => <option key={s.id} value={s.id}>{s.name} — {s.code}</option>)}
          </Select>
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Serial" required><Input name="serial" required placeholder="APP-HQ-0001" /></Field>
          <Field label="Name" required><Input name="name" required placeholder="hq-gateway" /></Field>
          <Field label="Model" className="sm:col-span-2"><Input name="model" placeholder="Protectli VP2410" /></Field>
        </div>
      </DialogForm>

      <OneTimeReveal
        open={!!mintedToken}
        title="New enrollment token"
        description="Enter it in the appliance's Hotel Admin, under Appliance & licence → Advanced / recovery."
        valueLabel="Enrollment token"
        value={mintedToken?.token ?? ""}
        onAcknowledge={() => setMintedToken(null)}
      >
        {mintedToken && (
          <div className="space-y-2 text-sm text-muted-foreground">
            <p>
              Never edit files on the appliance: a factory unit has no customer identity and adopts its customer and
              site only from the signed assignment issued after it is claimed.
            </p>
            <p className="text-xs">
              Site: {siteName(mintedToken.row.site_id)}
              {mintedToken.row.expected_serial ? <> · Serial lock: <span className="font-mono">{mintedToken.row.expected_serial}</span></> : null}
              {" "}· Expires: {formatRelative(mintedToken.row.expires_at)}
            </p>
          </div>
        )}
      </OneTimeReveal>

      <ConfirmDialog
        open={!!revokeTok}
        onOpenChange={(v) => { if (!v) setRevokeTok(null); }}
        title="Revoke this enrollment token?"
        description={revokeTok ? <>Token ending <span className="font-mono">…{revokeTok.token_hint}</span>.</> : undefined}
        confirmLabel="Revoke token"
        confirmVariant="danger"
        busy={actBusy}
        error={actErr}
        consequences={["An appliance can no longer enroll with this token."]}
        onConfirm={onRevokeToken}
      />

      <DeleteDialog
        open={!!delApp}
        onClose={() => setDelApp(null)}
        onDeleted={() => { toast.success("Appliance deleted", delApp?.serial); load(true); }}
        title={`Delete appliance ${delApp?.serial ?? ""}`}
        what="Appliance"
        expected={delApp?.serial ?? ""}
        confirmHint="Type the appliance serial"
        deleteUrl={`/v1/appliances/${delApp?.id}?tenant_id=${delApp?.tenant_id ?? selectedTenantId}`}
        takesReason={false}
        stepUp={false}
        consequences={[
          "The appliance record is removed from this customer.",
          "Any license bound to this appliance is revoked.",
          "It cannot be undone.",
        ]}
      />

      <DetailDialog
        open={!!effOpen}
        onOpenChange={(v) => { if (!v) setEffOpen(null); }}
        title={`Effective config — ${effOpen?.name ?? ""}`}
        description={effOpen ? <>What the appliance at site <span className="font-mono">{siteName(effOpen.site_id)}</span> should be enforcing.</> : undefined}
        size="xl"
      >
        {!effData ? (
          <div className="space-y-3" aria-busy="true"><span className="sr-only">Loading</span><Skeleton className="h-24" /><Skeleton className="h-24" /></div>
        ) : (
          <>
            <section className="space-y-2">
              <h3 className="text-micro uppercase tracking-[0.06em] text-muted-foreground">
                PMS connections ({effData.pms_providers?.length ?? 0})
              </h3>
              {!effData.pms_providers || effData.pms_providers.length === 0 ? (
                <p className="text-sm text-muted-foreground">No PMS connections configured.</p>
              ) : (
                <div className="overflow-hidden rounded-md border border-border">
                  <Table>
                    <THead><TR><TH>Name</TH><TH>Kind</TH><TH>Scope</TH><TH>Status</TH></TR></THead>
                    <tbody>
                      {effData.pms_providers.map((p) => (
                        <TR key={p.id}>
                          <TD>{p.display_name || p.name}</TD>
                          <TD className="text-muted-foreground">{p.kind}</TD>
                          <TD>{p.site_id ? <Badge tone="info">Site override</Badge> : <Badge>Customer-wide</Badge>}</TD>
                          <TD><Badge tone={(p.status === "connected" ? "ok" : p.status === "down" ? "err" : "warn") as any}>{statusWord(p.status)}</Badge></TD>
                        </TR>
                      ))}
                    </tbody>
                  </Table>
                </div>
              )}
            </section>
            <section className="space-y-2">
              <h3 className="text-micro uppercase tracking-[0.06em] text-muted-foreground">
                Allowed sites ({effData.walled_garden?.length ?? 0})
              </h3>
              {!effData.walled_garden || effData.walled_garden.length === 0 ? (
                <p className="text-sm text-muted-foreground">No allowed-site rules configured.</p>
              ) : (
                <div className="overflow-hidden rounded-md border border-border">
                  <Table>
                    <THead><TR><TH>Kind</TH><TH>Value</TH><TH>Ports</TH><TH>Scope</TH></TR></THead>
                    <tbody>
                      {effData.walled_garden.map((w) => (
                        <TR key={w.id}>
                          <TD className="text-muted-foreground">{w.kind}</TD>
                          <TD className="font-mono text-xs">{w.value}</TD>
                          <TD className="text-xs text-muted-foreground">{w.ports?.join(", ") || "any"}</TD>
                          <TD>{w.site_id ? <Badge tone="info">Site</Badge> : <Badge>Customer-wide</Badge>}</TD>
                        </TR>
                      ))}
                    </tbody>
                  </Table>
                </div>
              )}
            </section>
          </>
        )}
      </DetailDialog>
    </PageShell>
  );
}
