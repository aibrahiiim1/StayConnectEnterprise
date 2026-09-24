"use client";

// SERVICE PLANS — the technical service behind a guest-facing Internet package.
//
// A plan says what a guest GETS: speed, devices, data and time. Packages are what a guest sees and chooses, and
// each one hands out one plan. This screen shows what every plan grants, which packages use it, whether any of
// those packages is still on older settings (with the one-click "apply current settings" fix), and the plan's
// saved versions — in a side sheet per plan.
//
// THERE IS NO ENABLE/DISABLE SWITCH HERE, ON PURPOSE. A plan row carries an `enabled` flag, but nothing that
// decides what a guest receives reads it for an operator plan: the portal offers PACKAGES, and a package hands
// out the plan version it pinned whatever that flag says. A switch that changes nothing a guest experiences
// would be a control that lies, so the screen does not offer one. A plan stops reaching guests when no active
// package uses it — the sheet lists those packages and says so.
//
// The form collects operator units (Mbps, GB, hours, days) and converts on submit; the API and wire contract
// are unchanged. "Add plan" sends create_only, so a code that already exists is refused instead of silently
// becoming a new version of that plan.

import { useCallback, useEffect, useMemo, useState, Fragment } from "react";
import { AlertTriangle, CircleSlash, Gauge, Layers, Pencil, Plus, RefreshCw, Trash2 } from "lucide-react";
import { api, ApiError, ListResp } from "@/lib/api";
import { deletePlan, getPlanDeletability, type RevisionInfo } from "@/lib/api/commerce";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { PageShell, PageHeader, StatCard, Toolbar } from "@/components/ui/page";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogBody } from "@/components/ui/dialog";
import { Sheet, SheetContent, SheetHeader, SheetBody, SheetFooter, SheetSection } from "@/components/ui/sheet";
import { FilterChips, KeyValueGrid, MetricStrip, SearchInput, Timeline } from "@/components/ui/data";
import { MonoId, SkeletonRows } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { DeleteDialog } from "@/components/commerce/delete-dialog";
import { focusSheetItself } from "@/components/commerce/sheet-focus";
import {
  formatSpeed, formatData, formatDuration, formatDevices, mbpsToKbps, gbToBytes,
  durationToSeconds, secondsToDurationField, bytesToGb,
  DEVICE_LIMIT_POLICIES, TIME_ACCOUNTING_MODES,
} from "@/lib/units";
import {
  stalePackagesFor, repinPayload, repinOutcomeMessage, type PackageCurrentDTO,
} from "@/lib/repin";

type PlanSummary = {
  plan_id: string; code: string; enabled: boolean;
  current_revision_id: string; revision_count: number;
  name?: string | null;
  down_kbps?: number | null; up_kbps?: number | null;
  max_concurrent_devices?: number | null; device_limit_policy?: string | null;
  idle_timeout_seconds?: number | null; max_continuous_session_seconds?: number | null;
  time_quota_seconds?: number | null; data_quota_bytes?: number | null;
  time_accounting_mode?: string | null;
  speed_allocation?: string | null;
  used_by_active_packages?: number;
};
type PackageRef = {
  package_id: string; code: string; name?: string | null; active: boolean;
  service_plan_id?: string | null; service_plan_revision_id?: string | null;
};
type Chip = "all" | "used" | "unused";

// The server's bounds, in the units this form collects. edged is the authority and refuses anything past
// these; repeating them here is what lets the browser refuse it at the field that caused it.
//   maxKbps 10_000_000 · maxIdleSeconds 30d · maxSessionSeconds 365d · maxTimeQuotaSecond 10y
const LIMITS = { mbps: 10000, idleMinutes: 43200, sessionHours: 8760, timeQuotaDays: 3650 } as const;

const selectClass = "w-full rounded-md border border-input bg-background px-2 py-2 text-sm";

export default function ServicePlansPage() {
  const toast = useToast();
  const [rows, setRows] = useState<PlanSummary[] | null>(null);
  const [packages, setPackages] = useState<PackageRef[]>([]);
  const [err, setErr] = useState<unknown>(null);
  const [formErr, setFormErr] = useState<unknown>(null);
  const [unavailable, setUnavailable] = useState(false);
  const writable = true; // edged enforces write permission server-side and answers 403 if the role lacks it.
  const [showNew, setShowNew] = useState(false);
  const [prefill, setPrefill] = useState<PlanSummary | null>(null);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  const [chip, setChip] = useState<Chip>("all");
  const [q, setQ] = useState("");
  const [selected, setSelected] = useState<PlanSummary | null>(null);
  const [revs, setRevs] = useState<RevisionInfo[] | null>(null);
  const [revsErr, setRevsErr] = useState<unknown>(null);
  const [deleting, setDeleting] = useState<PlanSummary | null>(null);
  // THE REPIN STEP. Publishing new plan settings does NOT move any package onto them — packages pin a
  // specific revision, and that is what keeps a guest's terms stable. So after saving, the operator is asked
  // which packages should use the new settings for FUTURE guests. Nothing is repinned unless they say so,
  // and no existing Entitlement changes either way. The same step is reachable on its own from the plan row.
  const [repin, setRepin] = useState<{
    planRevisionID: string; planLabel: string; packages: PackageRef[];
    chosen: Record<string, boolean>; standing: boolean;
  } | null>(null);

  // The plan's existing time allowance, expressed in the largest unit that divides it exactly.
  const timeQuota = secondsToDurationField(prefill?.time_quota_seconds, ["hours", "days"]);

  const load = useCallback(async () => {
    try {
      const [r, pk] = await Promise.all([
        api.get<ListResp<PlanSummary>>("/commercial-packages/plans"),
        api.get<ListResp<PackageRef>>("/commercial-packages"),
      ]);
      const plans = r.data ?? [];
      setRows(plans); setPackages(pk.data ?? []); setUnavailable(false);
      setSelected((s) => (s ? plans.find((p) => p.plan_id === s.plan_id) ?? null : s));
    } catch (e) {
      if (e instanceof ApiError && e.status === 503) { setUnavailable(true); setRows([]); return; }
      setRows([]);
      setErr(e);
    }
  }, []);
  useEffect(() => { load(); }, [load]);

  useEffect(() => {
    if (!selected) { setRevs(null); setRevsErr(null); return; }
    let live = true;
    setRevs(null); setRevsErr(null);
    api.get<ListResp<RevisionInfo>>(`/commercial-packages/plans/${selected.plan_id}/revisions`)
      .then((r) => { if (live) setRevs(r.data ?? []); })
      .catch((e) => { if (live) setRevsErr(e); });
    return () => { live = false; };
  }, [selected?.plan_id]); // eslint-disable-line react-hooks/exhaustive-deps

  const stats = useMemo(() => {
    const all = rows ?? [];
    const used = all.filter((p) => (p.used_by_active_packages ?? 0) > 0).length;
    const stale = all.reduce((n, p) => n + stalePackagesFor(p, packages).length, 0);
    return { all: all.length, used, unused: all.length - used, stale };
  }, [rows, packages]);

  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return (rows ?? []).filter((p) => {
      const used = (p.used_by_active_packages ?? 0) > 0;
      return (chip === "all" || (chip === "used" ? used : !used)) &&
        (!needle || (p.name ?? "").toLowerCase().includes(needle) || p.code.toLowerCase().includes(needle));
    });
  }, [rows, chip, q]);

  function startNew(from?: PlanSummary) {
    setPrefill(from ?? null);
    setFormErr(null);
    setShowNew(true);
  }

  async function onPublish(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault(); setBusy(true); setFormErr(null); setErr(null);
    const el = e.currentTarget; const f = new FormData(el);
    const str = (k: string) => ((f.get(k) as string) || "").trim();
    const int = (k: string) => { const v = str(k); return v === "" ? undefined : Number(v); };
    const editingPlan = prefill;
    try {
      const res = await api.post<{ current_revision_id: string }>("/commercial-packages/plans", {
        code: str("code"),
        name: str("name"),
        // Operator units in, wire units out.
        down_kbps: mbpsToKbps(str("down_mbps")),
        up_kbps: mbpsToKbps(str("up_mbps")),
        max_concurrent_devices: int("max_concurrent_devices") ?? 1,
        device_limit_policy: str("device_limit_policy") || "REJECT_NEW_DEVICE",
        idle_timeout_seconds: durationToSeconds(str("idle_timeout"), "minutes"),
        max_continuous_session_seconds: durationToSeconds(str("max_session"), "hours"),
        time_quota_seconds: durationToSeconds(str("time_quota"), (str("time_quota_unit") as "hours" | "days") || "hours"),
        data_quota_bytes: gbToBytes(str("data_quota_gb")),
        time_accounting_mode: str("time_accounting_mode") || "VALIDITY_WINDOW",
        speed_allocation: str("speed_allocation") || "PER_DEVICE",
        // ADD refuses a code that already exists; EDIT publishes a new version of this plan.
        ...(editingPlan ? {} : { create_only: true }),
      });
      el.reset(); setShowNew(false);
      toast.success(editingPlan ? "Plan saved" : "Plan added");
      // Which packages are now behind: active packages of this plan not already pinned to what was just published.
      const affected = editingPlan && res?.current_revision_id
        ? stalePackagesFor(
            { plan_id: editingPlan.plan_id, code: editingPlan.code, current_revision_id: res.current_revision_id },
            packages,
          )
        : [];
      if (affected.length > 0 && res?.current_revision_id) {
        setRepin({
          planRevisionID: res.current_revision_id,
          planLabel: editingPlan?.name || editingPlan?.code || "this plan",
          packages: affected,
          chosen: Object.fromEntries(affected.map((k) => [k.package_id, false])),
          standing: false,
        });
      } else {
        setNotice("Changes saved. Existing guest access is unchanged; the new settings apply to future grants.");
      }
      setPrefill(null); await load();
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  // startRepin opens the same prompt from a plan row, for packages that are ALREADY behind. It publishes no plan
  // revision, and nothing at all happens until the operator ticks a package and applies.
  function startRepin(p: PlanSummary) {
    const stale = stalePackagesFor(p, packages);
    if (stale.length === 0) return;
    setNotice(null); setErr(null); setSelected(null);
    setRepin({
      planRevisionID: p.current_revision_id,
      planLabel: p.name || p.code,
      packages: stale,
      chosen: Object.fromEntries(stale.map((k) => [k.package_id, false])),
      standing: true,
    });
  }

  // applyRepin republishes ONLY the chosen packages, each from its own CURRENT configuration, pinned to the plan
  // revision the prompt was opened with. It posts only to /commercial-packages.
  async function applyRepin() {
    if (!repin) return;
    const chosen = repin.packages.filter((k) => repin.chosen[k.package_id]);
    setBusy(true); setErr(null);
    try {
      for (const k of chosen) {
        const cur = await api.get<PackageCurrentDTO>(`/commercial-packages/${k.package_id}/current`);
        await api.post("/commercial-packages", repinPayload(cur, repin.planRevisionID, k.name || k.code));
      }
      setRepin(null);
      const msg = repinOutcomeMessage(chosen.length);
      setNotice(msg); toast.success("Packages updated", msg);
      await load();
    } catch (e) { setErr(e); }
    finally { setBusy(false); }
  }

  const packagesOf = (p: PlanSummary) => packages.filter((k) => k.service_plan_id === p.plan_id);

  return (
    <PageShell width="wide">
      <PageHeader
        icon={<Gauge />}
        eyebrow="Internet offering"
        title="Service plans"
        description="A service plan is the technical service a guest receives: how fast it is, how many devices it covers, and how much data and time it includes. Each internet package hands out one plan."
        actions={writable && !unavailable && <Button onClick={() => startNew()}><Plus /> Add plan</Button>}
      />

      <ErrorBanner err={err} />
      {notice && <Callout tone="success">{notice}</Callout>}

      {!unavailable && (
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
          <StatCard label="Service plans" value={rows ? stats.all : "—"} icon={<Layers />} />
          <StatCard label="Used by active packages" value={rows ? stats.used : "—"} icon={<Gauge />} tone="ok"
            hint="Plans that guests can currently receive" />
          <StatCard label="Packages on older settings" value={rows ? stats.stale : "—"} icon={<AlertTriangle />}
            tone={stats.stale > 0 ? "warn" : "default"}
            hint={stats.stale > 0 ? "Apply current settings from the plan" : "Every package is up to date"} />
          <StatCard label="Not used by any package" value={rows ? stats.unused : "—"} icon={<CircleSlash />}
            hint="These reach no guest until a package uses them" />
        </div>
      )}

      {repin && (
        <Card>
          <CardHeader><CardTitle data-testid="repin-title">
            {repin.standing ? "Apply current settings to packages" : "Apply these settings to packages?"}
          </CardTitle></CardHeader>
          <CardBody>
            <p className="mb-3 text-sm text-muted" data-testid="repin-intro">
              {repin.standing ? (
                <>
                  These packages still give guests older settings than <strong>{repin.planLabel}</strong> has
                  now. Choose which of them should use the current settings for <strong>future</strong> guests.
                </>
              ) : (
                <>
                  The new settings for <strong>{repin.planLabel}</strong> are saved. Choose which packages
                  should give them to <strong>future</strong> guests.
                </>
              )}{" "}
              Guests already connected are not affected either way, and any package you leave unticked keeps
              the settings it has now.
            </p>
            <div className="mb-4 space-y-2">
              {repin.packages.map((k) => (
                <label key={k.package_id} className="flex items-center gap-2 text-sm">
                  <input type="checkbox" aria-label={`repin-${k.code}`}
                    checked={!!repin.chosen[k.package_id]}
                    onChange={(e) => setRepin((s2) => s2 && ({ ...s2, chosen: { ...s2.chosen, [k.package_id]: e.target.checked } }))} />
                  <span>{k.name || k.code}</span>
                  <span className="text-xs text-muted-foreground">{k.code}</span>
                </label>
              ))}
            </div>
            <div className="flex gap-2">
              <Button onClick={applyRepin} disabled={busy}>{busy ? "Applying…" : "Apply to selected packages"}</Button>
              <Button variant="ghost" disabled={busy} onClick={() => {
                const wasStanding = repin.standing;
                setRepin(null);
                setNotice(wasStanding
                  ? "Nothing was changed. Those packages keep the settings they have now."
                  : "Changes saved. No packages were updated, so guests continue to receive the settings they do now.");
              }}>Not now</Button>
            </div>
          </CardBody>
        </Card>
      )}

      {unavailable ? (
        <Card><CardBody>
          <EmptyState
            icon={<Gauge />}
            title="The internet offering is not switched on for this appliance"
            hint="Service plans and internet packages become available once this capability is enabled for the site. Contact your StayConnect administrator." />
        </CardBody></Card>
      ) : (
        <Card>
          <div className="border-b border-border px-4 py-3">
            <Toolbar>
              <FilterChips<Chip>
                label="Show plans"
                value={chip}
                onChange={setChip}
                options={[
                  { value: "all", label: "All", count: stats.all },
                  { value: "used", label: "In use", count: stats.used, tone: "ok" },
                  { value: "unused", label: "Not used", count: stats.unused },
                ]}
              />
              <SearchInput value={q} onChange={setQ} placeholder="Search plans" label="Search plans by name or code" />
            </Toolbar>
          </div>
          <CardBody className="p-0">
            {rows === null ? (
              <SkeletonRows rows={4} cols={7} />
            ) : rows.length === 0 ? (
              <EmptyState
                icon={<Gauge />}
                title="No service plans yet"
                hint="A service plan defines the speed, device count and allowances a guest receives. Create one, then attach it to an internet package."
                action={writable ? <Button onClick={() => startNew()}><Plus /> Add the first plan</Button> : undefined} />
            ) : shown.length === 0 ? (
              <EmptyState icon={<Gauge />} title="No plan matches" hint="Clear the search or choose another filter." />
            ) : (
              <Table>
                <THead><TR>
                  <TH>Plan</TH><TH>Speed</TH><TH>Devices</TH><TH>Time</TH><TH>Data</TH><TH>Used by</TH>
                  <TH><span className="sr-only">Actions</span></TH>
                </TR></THead>
                <TBody>
                  {shown.map((p) => {
                    // Packages of this plan that still give guests older settings, per row, because the action is
                    // per plan and the operator needs to see WHICH plan has drifted without opening anything.
                    const stale = stalePackagesFor(p, packages);
                    return (
                      <Fragment key={p.plan_id}>
                        <TR className="cursor-pointer" onClick={() => setSelected(p)}>
                          <TD>
                            <button type="button" className="text-left font-medium hover:underline"
                              onClick={(e) => { e.stopPropagation(); setSelected(p); }}>
                              {p.name || p.code}
                            </button>
                            <div className="text-xs text-muted-foreground">{p.code}</div>
                          </TD>
                          <TD>
                            <div>{formatSpeed(p.down_kbps)} down{p.speed_allocation === "SHARED" ? " (shared)" : ""}</div>
                            <div className="text-xs text-muted-foreground">{formatSpeed(p.up_kbps)} up</div>
                          </TD>
                          <TD>
                            <div>{formatDevices(p.max_concurrent_devices)}</div>
                            <div className="text-xs text-muted-foreground">
                              {DEVICE_LIMIT_POLICIES[p.device_limit_policy ?? ""] ?? "—"}
                            </div>
                          </TD>
                          <TD>{formatDuration(p.time_quota_seconds)}</TD>
                          <TD>{formatData(p.data_quota_bytes)}</TD>
                          <TD className="text-xs text-muted-foreground">
                            {(p.used_by_active_packages ?? 0) === 0
                              ? "No active packages"
                              : `${p.used_by_active_packages} active package${p.used_by_active_packages === 1 ? "" : "s"}`}
                            {stale.length > 0 && (
                              <div className="mt-0.5 text-warning-subtle-foreground" data-testid={`stale-count-${p.code}`}>
                                {stale.length} still on older settings
                              </div>
                            )}
                          </TD>
                          <TD className="whitespace-nowrap text-right" onClick={(e) => e.stopPropagation()}>
                            {writable && stale.length > 0 && (
                              <Button size="sm" variant="ghost" data-testid={`apply-current-${p.code}`}
                                onClick={() => startRepin(p)}>
                                Apply current settings to packages
                              </Button>
                            )}
                            {writable && <Button size="sm" variant="ghost" onClick={() => startNew(p)}>Edit</Button>}
                          </TD>
                        </TR>
                      </Fragment>
                    );
                  })}
                </TBody>
              </Table>
            )}
          </CardBody>
        </Card>
      )}

      {/* THE PLAN RECORD. */}
      <Sheet open={selected !== null} onOpenChange={(v) => !v && setSelected(null)}>
        <SheetContent width="md" onOpenAutoFocus={focusSheetItself}>
          {selected && (() => {
            const p = selected;
            const stale = stalePackagesFor(p, packages);
            const using = packagesOf(p);
            return (
              <>
                <SheetHeader icon={<Gauge />} eyebrow="Service plan" title={p.name || p.code}
                  description={`Code ${p.code}`}
                  badges={(p.used_by_active_packages ?? 0) > 0
                    ? <Badge tone="ok" dot>Used by {p.used_by_active_packages} active package{p.used_by_active_packages === 1 ? "" : "s"}</Badge>
                    : <Badge dot>Not used by an active package</Badge>} />
                <SheetBody>
                  <MetricStrip items={[
                    { label: "Download", value: formatSpeed(p.down_kbps) },
                    { label: "Upload", value: formatSpeed(p.up_kbps) },
                    { label: "Data", value: formatData(p.data_quota_bytes) },
                    { label: "Devices", value: formatDevices(p.max_concurrent_devices) },
                  ]} />
                  {stale.length > 0 && (
                    <Callout tone="warning" title="Some packages still give older settings">
                      {stale.length} package{stale.length === 1 ? "" : "s"} using this plan still
                      {stale.length === 1 ? " gives" : " give"} guests an earlier version of it.
                    </Callout>
                  )}
                  {!p.enabled && (
                    <Callout tone="neutral" title="Marked inactive in its records">
                      This mark does not change what guests receive: any active package using this plan still hands
                      it out. To stop guests receiving it, disable or edit those packages.
                    </Callout>
                  )}
                  <SheetSection title="What it grants">
                    <KeyValueGrid items={[
                      { label: "Speed", value: `${formatSpeed(p.down_kbps)} down · ${formatSpeed(p.up_kbps)} up` },
                      { label: "Speed sharing", value: p.speed_allocation === "SHARED"
                        ? "Shared by the guest's devices" : "Full speed on every device" },
                      { label: "Devices at once", value: formatDevices(p.max_concurrent_devices),
                        hint: DEVICE_LIMIT_POLICIES[p.device_limit_policy ?? ""] },
                      { label: "Time allowance", value: formatDuration(p.time_quota_seconds),
                        hint: TIME_ACCOUNTING_MODES[p.time_accounting_mode ?? ""] },
                      { label: "Data allowance", value: formatData(p.data_quota_bytes) },
                      { label: "Disconnect after inactivity", value: p.idle_timeout_seconds ? formatDuration(p.idle_timeout_seconds) : "Never" },
                      { label: "Longest single session", value: p.max_continuous_session_seconds ? formatDuration(p.max_continuous_session_seconds) : "No limit" },
                    ]} />
                  </SheetSection>
                  <SheetSection title="Packages using it"
                    description="A plan reaches guests only through the packages that hand it out.">
                    {using.length === 0 ? (
                      <p className="text-sm text-muted-foreground">No package uses this plan.</p>
                    ) : (
                      <ul className="space-y-1.5">
                        {using.map((k) => (
                          <li key={k.package_id} className="flex items-center justify-between gap-2 text-sm">
                            <span>{k.name || k.code}</span>
                            <span className="flex gap-1.5">
                              {k.active ? <Badge tone="ok" dot>Active</Badge> : <Badge dot>Disabled</Badge>}
                              {stale.some((x) => x.package_id === k.package_id) && <Badge tone="warn">Older settings</Badge>}
                            </span>
                          </li>
                        ))}
                      </ul>
                    )}
                  </SheetSection>
                  <SheetSection title="Saved versions"
                    description="Every saved change is kept permanently. A guest keeps the terms that applied when they connected.">
                    <ErrorBanner err={revsErr} />
                    {revs === null && !revsErr ? <SkeletonRows rows={2} cols={1} /> : (
                      <Timeline emptyLabel="No saved versions yet"
                        items={(revs ?? []).map((r) => ({
                          key: r.revision_id,
                          title: <span>Version {r.revision_no}{" "}
                            {r.is_current ? <Badge tone="info">in force</Badge> : <span className="text-muted-foreground">superseded</span>}
                          </span>,
                          body: r.label || undefined,
                          tone: r.is_current ? "ok" : "neutral",
                        }))} />
                    )}
                  </SheetSection>
                  <SheetSection title="For support">
                    <KeyValueGrid columns={1} items={[{ label: "Plan reference", value: <MonoId value={p.plan_id} /> }]} />
                  </SheetSection>
                </SheetBody>
                <SheetFooter>
                  <Button variant="ghost" className="mr-auto text-destructive" onClick={() => setDeleting(p)}>
                    <Trash2 /> Delete…
                  </Button>
                  {writable && stale.length > 0 && (
                    <Button variant="secondary" onClick={() => startRepin(p)}><RefreshCw /> Apply current settings</Button>
                  )}
                  {writable && <Button onClick={() => { setSelected(null); startNew(p); }}><Pencil /> Edit</Button>}
                </SheetFooter>
              </>
            );
          })()}
        </SheetContent>
      </Sheet>

      {deleting && (
        <DeleteDialog open onOpenChange={(v) => !v && setDeleting(null)} kind="plan"
          name={deleting.name || deleting.code} load={() => getPlanDeletability(deleting.plan_id)}
          onDelete={async ({ reason, password }) => {
            const p = deleting;
            await deletePlan(p.plan_id, reason, password);
            const msg = `${p.name || p.code} and its saved versions were removed permanently.`;
            setDeleting(null); setSelected((s) => (s?.plan_id === p.plan_id ? null : s));
            toast.success("Service plan deleted", msg);
            await load();
          }} />
      )}

      {/* THE FORM IS A DIALOG: saving it publishes settings that decide how fast every guest on that plan gets. */}
      <Dialog open={showNew} onOpenChange={(v) => { if (!v && !busy) { setShowNew(false); setPrefill(null); } }}>
        <DialogContent size="lg">
          <DialogHeader>
            <DialogTitle>{prefill ? `Edit ${prefill.name || prefill.code}` : "Add a service plan"}</DialogTitle>
            {/* Editing means publishing a NEW version — the previous one stays intact so anything already given
                under it keeps its terms. Saying so prevents the assumption that this edits the plan in place. */}
            <DialogDescription>
              {prefill
                ? "Saving records these as the plan's current settings. Guests already connected keep the terms they were given, and packages using this plan are only updated if you choose them afterwards."
                : "This creates the plan and its first settings."}
            </DialogDescription>
          </DialogHeader>
          <DialogBody className="space-y-4">
            <ErrorBanner err={formErr} />
            {/* Keyed on the plan being edited: these are uncontrolled inputs, and switching straight from one plan
                to another would otherwise leave the first plan's numbers on screen — and publish them. */}
            <form key={prefill?.plan_id ?? "new"} onSubmit={onPublish} className="grid gap-3 sm:grid-cols-2">
              <div>
                <Label htmlFor="plan-code">Plan code</Label>
                <Input id="plan-code" name="code" required defaultValue={prefill?.code ?? ""} readOnly={!!prefill}
                  placeholder="GOLD" />
                <p className="mt-1 text-xs text-muted">
                  {prefill ? "The code identifies this plan and cannot be changed." : "A short identifier that no other plan uses. It cannot be changed later."}
                </p>
              </div>
              <div>
                <Label htmlFor="plan-name">Display name</Label>
                <Input id="plan-name" name="name" defaultValue={prefill?.name ?? ""} placeholder="Premium Wi-Fi" />
              </div>

              {/* max= on every numeric field mirrors the server's bound, so an over-limit value is refused at the
                  field that caused it instead of as a rejected save that names a wire field. */}
              <div>
                <Label htmlFor="plan-down">Download speed (Mbps)</Label>
                <Input id="plan-down" name="down_mbps" type="number" min={0} max={LIMITS.mbps} step="0.1"
                  defaultValue={prefill?.down_kbps ? prefill.down_kbps / 1000 : ""} placeholder="Leave empty for unlimited" />
                <p className="mt-1 text-xs text-muted">Up to {LIMITS.mbps} Mbps.</p>
              </div>
              <div>
                <Label htmlFor="plan-up">Upload speed (Mbps)</Label>
                <Input id="plan-up" name="up_mbps" type="number" min={0} max={LIMITS.mbps} step="0.1"
                  defaultValue={prefill?.up_kbps ? prefill.up_kbps / 1000 : ""} placeholder="Leave empty for unlimited" />
                <p className="mt-1 text-xs text-muted">Up to {LIMITS.mbps} Mbps.</p>
              </div>

              <div>
                <Label htmlFor="plan-devices">Devices at once</Label>
                <Input id="plan-devices" name="max_concurrent_devices" type="number" min={1}
                  defaultValue={prefill?.max_concurrent_devices ?? 1} required />
              </div>
              <div>
                <Label htmlFor="plan-device-policy">When the device limit is reached</Label>
                <select id="plan-device-policy" name="device_limit_policy" defaultValue={prefill?.device_limit_policy ?? "REJECT_NEW_DEVICE"}
                  className={selectClass}>
                  {Object.entries(DEVICE_LIMIT_POLICIES).map(([v, label]) => (
                    <option key={v} value={v}>{label}</option>
                  ))}
                </select>
              </div>

              {/* PRE-FILLED, and that is a bug fix rather than a nicety: the form publishes a complete new version
                  from whatever the fields hold, so a field left blank because it was never loaded PUBLISHED a blank. */}
              <div>
                <Label htmlFor="plan-time">Total time allowance</Label>
                <div className="flex gap-2">
                  <Input id="plan-time" name="time_quota" type="number" min={0} step="0.01" placeholder="Unlimited"
                    className="flex-1" defaultValue={timeQuota.value} />
                  <select name="time_quota_unit" aria-label="Time allowance unit" defaultValue={timeQuota.unit}
                    className="rounded-md border border-input bg-background px-2 text-sm">
                    <option value="hours">hours</option>
                    <option value="days">days</option>
                  </select>
                </div>
                <p className="mt-1 text-xs text-muted">Up to {LIMITS.timeQuotaDays} days.</p>
              </div>
              <div>
                <Label htmlFor="plan-data">Data allowance (GB)</Label>
                <Input id="plan-data" name="data_quota_gb" type="number" min={0} step="0.1" placeholder="Unlimited"
                  defaultValue={bytesToGb(prefill?.data_quota_bytes)} />
              </div>

              <div>
                <Label htmlFor="plan-idle">Disconnect after inactivity (minutes)</Label>
                <Input id="plan-idle" name="idle_timeout" type="number" min={0} max={LIMITS.idleMinutes} placeholder="Never"
                  defaultValue={secondsToDurationField(prefill?.idle_timeout_seconds, ["minutes"]).value} />
              </div>
              <div>
                <Label htmlFor="plan-session">Maximum single session (hours)</Label>
                <Input id="plan-session" name="max_session" type="number" min={0} max={LIMITS.sessionHours} placeholder="No limit"
                  defaultValue={secondsToDurationField(prefill?.max_continuous_session_seconds, ["hours"]).value} />
              </div>

              <div className="sm:col-span-2">
                <Label htmlFor="plan-sharing">How the speed is shared</Label>
                <select id="plan-sharing" name="speed_allocation" defaultValue={prefill?.speed_allocation ?? "PER_DEVICE"}
                  className={selectClass}>
                  <option value="PER_DEVICE">Per device — every device gets the full speed</option>
                  <option value="SHARED">Shared — all the guest&rsquo;s devices share the speed</option>
                </select>
                <p className="mt-1 text-xs text-muted">
                  Shared gives the whole allowance to whichever devices are actually using it, so one device alone
                  still gets the full speed. It is not divided into fixed portions. Shared needs a download and
                  upload speed to share.
                </p>
              </div>

              <div className="sm:col-span-2">
                <Label htmlFor="plan-time-mode">How time is counted</Label>
                <select id="plan-time-mode" name="time_accounting_mode" defaultValue="VALIDITY_WINDOW" className={selectClass}>
                  {/* Only VALIDITY_WINDOW is implemented end to end; offering the other would be a control that
                      silently does nothing. */}
                  <option value="VALIDITY_WINDOW">{TIME_ACCOUNTING_MODES.VALIDITY_WINDOW}</option>
                </select>
              </div>

              <div className="flex gap-2 sm:col-span-2">
                <Button type="submit" disabled={busy}>{busy ? "Saving…" : prefill ? "Save changes" : "Add plan"}</Button>
                <Button type="button" variant="ghost" disabled={busy} onClick={() => { setShowNew(false); setPrefill(null); }}>Cancel</Button>
              </div>
            </form>
          </DialogBody>
        </DialogContent>
      </Dialog>
    </PageShell>
  );
}
