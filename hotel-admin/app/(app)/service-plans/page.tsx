"use client";

// No enable/disable switch on purpose: nothing that decides what a guest receives reads a plan's `enabled`
// flag (packages hand out the plan version they pinned), so a switch would be a control that lies.

import { useCallback, useEffect, useMemo, useState } from "react";
import { AlertTriangle, CircleSlash, Gauge, Layers, Pencil, Plus, RefreshCw, Trash2 } from "lucide-react";
import { api, ApiError, ListResp, Whoami } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { deletePlan, getPlanDeletability, type RevisionInfo } from "@/lib/api/commerce";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Field, Input, InputGroup, Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { PageShell, PageHeader, StatCard, StatGrid } from "@/components/ui/page";
import { FilterBar } from "@/components/ui/toolbar";
import { RowActions } from "@/components/ui/row-actions";
import { HistoryList } from "@/components/ui/history";
import { formatSpeedPair } from "@/components/commerce/speed";
import { HelpList, HelpSection } from "@/components/help";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogBody, DialogFooter } from "@/components/ui/dialog";
import { Sheet, SheetContent, SheetHeader, SheetBody, SheetFooter, SheetSection } from "@/components/ui/sheet";
import { FilterChips, KeyValueGrid, MetricStrip, SearchInput } from "@/components/ui/data";
import { MonoId, SkeletonRows } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { DeleteDialog } from "@/components/commerce/delete-dialog";
import { focusSheetItself } from "@/components/commerce/sheet-focus";
import {
  formatSpeed, formatData, formatDuration, formatDevices, mbpsToKbps, gbToBytes,
  durationToSeconds, secondsToDurationField, bytesToGb,
  DEVICE_LIMIT_POLICIES, SELECTABLE_DEVICE_LIMIT_POLICIES, TIME_ACCOUNTING_MODES,
} from "@/lib/units";
import {
  stalePackagesFor, repinPayload, repinOutcomeMessage, repinSkippedMessage, commercialTermsOf,
  type PackageCurrentDTO,
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

// Mirrors edged's bounds (maxKbps 10_000_000, idle 30d, session 365d, time quota 10y) in form units.
const LIMITS = { mbps: 10000, idleMinutes: 43200, sessionHours: 8760, timeQuotaDays: 3650 } as const;

export default function ServicePlansPage() {
  const toast = useToast();
  const [rows, setRows] = useState<PlanSummary[] | null>(null);
  const [packages, setPackages] = useState<PackageRef[]>([]);
  const [err, setErr] = useState<unknown>(null);
  const [formErr, setFormErr] = useState<unknown>(null);
  const [unavailable, setUnavailable] = useState(false);
  // Fails closed while loading; edged enforces the real gate.
  const [roles, setRoles] = useState<string[] | null>(null);
  useEffect(() => {
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => setRoles([]));
  }, []);
  const writable = roles !== null && canWrite("commercial-packages", roles);
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
  // Publishing plan settings does not move packages (they pin a revision); the operator chooses which to repin for future guests.
  const [repin, setRepin] = useState<{
    planRevisionID: string; planLabel: string; packages: PackageRef[];
    chosen: Record<string, boolean>; standing: boolean;
  } | null>(null);
  const [repinPassword, setRepinPassword] = useState("");
  useEffect(() => { if (!repin) setRepinPassword(""); }, [repin]);

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
        down_kbps: mbpsToKbps(str("down_mbps")),
        up_kbps: mbpsToKbps(str("up_mbps")),
        max_concurrent_devices: int("max_concurrent_devices") ?? 1,
        device_limit_policy: str("device_limit_policy") || "REJECT_NEW_DEVICE",
        idle_timeout_seconds: durationToSeconds(str("idle_timeout"), "minutes"),
        max_continuous_session_seconds: durationToSeconds(str("max_session"), "hours"),
        time_quota_seconds: durationToSeconds(str("time_quota"), (str("time_quota_unit") as "hours" | "days") || "hours"),
        data_quota_bytes: gbToBytes(str("data_quota_gb")),
        // On edit keep the plan's mode so an online-time plan never becomes validity-window; none sent means the server keeps it.
        ...(editingPlan
          ? (editingPlan.time_accounting_mode ? { time_accounting_mode: editingPlan.time_accounting_mode } : {})
          : { time_accounting_mode: str("time_accounting_mode") || "VALIDITY_WINDOW" }),
        speed_allocation: str("speed_allocation") || "PER_DEVICE",
        // create_only refuses an existing code; an edit is a step-up and carries the password.
        ...(editingPlan ? { password: (f.get("password") as string) ?? "" } : { create_only: true }),
      });
      el.reset(); setShowNew(false);
      toast.success(editingPlan ? "Plan saved" : "Plan added");
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
        setNotice("Changes saved. Existing client access is unchanged; the new settings apply to future grants.");
      }
      setPrefill(null); await load();
    } catch (e) {
      setFormErr(e instanceof ApiError && (e as ApiError).code === "reauth_required"
        ? new Error("That password is not correct. The plan was not saved.")
        : e);
    }
    finally { setBusy(false); }
  }

  // Publishes no plan revision; nothing happens until the operator ticks a package and applies.
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

  // Republishes only the chosen packages with their full stored terms and the password. A package whose price
  // details cannot be read in full is skipped and named, so nothing is ever republished as Free by omission.
  async function applyRepin() {
    if (!repin) return;
    const chosen = repin.packages.filter((k) => repin.chosen[k.package_id]);
    if (chosen.length > 0 && !repinPassword) {
      setErr(new Error("Enter your password to apply the settings."));
      return;
    }
    setBusy(true); setErr(null);
    let moved = 0;
    const skipped: { name: string; missing: string[] }[] = [];
    try {
      for (const k of chosen) {
        const cur = await api.get<PackageCurrentDTO>(`/commercial-packages/${k.package_id}/current`);
        const { missing } = commercialTermsOf(cur);
        if (missing.length > 0) { skipped.push({ name: k.name || k.code, missing }); continue; }
        await api.post("/commercial-packages", {
          ...repinPayload(cur, repin.planRevisionID, k.name || k.code),
          password: repinPassword,
        });
        moved++;
      }
      setRepin(null); setRepinPassword("");
      const skippedMsg = repinSkippedMessage(skipped);
      const msg = [moved > 0 || skipped.length === 0 ? repinOutcomeMessage(moved) : null, skippedMsg].filter(Boolean).join(" ");
      setNotice(msg);
      if (moved > 0) toast.success("Packages updated", repinOutcomeMessage(moved));
      await load();
    } catch (e) {
      if (e instanceof ApiError && (e as ApiError).code === "reauth_required") {
        setErr(new Error(moved > 0
          ? `That password was not accepted. ${moved} package${moved === 1 ? " was" : "s were"} already moved; the rest were not changed.`
          : "That password is not correct. Nothing was changed."));
      } else {
        setErr(e);
      }
      if (moved > 0) await load();
    }
    finally { setBusy(false); }
  }

  const packagesOf = (p: PlanSummary) => packages.filter((k) => k.service_plan_id === p.plan_id);

  return (
    <PageShell width="wide">
      <PageHeader
        title="Service plans"
        description="The speed, devices, data and time a package hands out."
        help={
          <>
            <HelpSection title="What a service plan is">
              <p>
                A service plan is the technical service a client receives: how fast it is, how many devices it
                covers, and how much data and time it includes. Each internet package hands out one plan.
              </p>
              <p>A plan reaches clients only through the packages that hand it out.</p>
            </HelpSection>
            <HelpSection title="Saved versions">
              <p>
                Every saved change is kept permanently. A client keeps the terms that applied when they connected.
                When a plan changes, you choose which packages give the new settings to future clients.
              </p>
            </HelpSection>
            <HelpSection title="How the speed is shared">
              <HelpList items={[
                <><strong>Per device</strong>: every device gets the full speed.</>,
                <><strong>Shared</strong>: the whole allowance goes to whichever devices are actually using it, so one device alone still gets the full speed. It is not divided into fixed portions.</>,
              ]} />
            </HelpSection>
          </>
        }
        actions={writable && !unavailable && <Button onClick={() => startNew()}><Plus /> Add plan</Button>}
      />

      {!unavailable && roles !== null && !writable && (
        <ReadOnlyNotice>Your role can see the service plans but not change them.</ReadOnlyNotice>
      )}
      <ErrorBanner err={err} className="mb-0" />
      {notice && <Callout tone="success">{notice}</Callout>}

      {!unavailable && (
        <StatGrid cols={4}>
          <StatCard label="Service plans" value={rows ? stats.all : "—"} icon={<Layers />} />
          <StatCard label="Used by active packages" value={rows ? stats.used : "—"} icon={<Gauge />}
            hint="Clients can receive these" />
          <StatCard label="Packages on older settings" value={rows ? stats.stale : "—"} icon={<AlertTriangle />}
            tone={stats.stale > 0 ? "warn" : "default"}
            hint={stats.stale > 0 ? "Apply current settings from the plan" : "Every package is up to date"} />
          <StatCard label="Not used by any package" value={rows ? stats.unused : "—"} icon={<CircleSlash />}
            hint="Reach no client yet" />
        </StatGrid>
      )}

      {repin && (
        <Card className="border-primary/40">
          <CardHeader><CardTitle data-testid="repin-title">
            {repin.standing ? "Apply current settings to packages" : "Apply these settings to packages?"}
          </CardTitle></CardHeader>
          <CardBody>
            <p className="mb-3 max-w-3xl text-sm text-muted-foreground" data-testid="repin-intro">
              {repin.standing ? (
                <>
                  These packages still give clients older settings than <strong>{repin.planLabel}</strong> has
                  now. Choose which of them should use the current settings for <strong>future</strong> clients.
                </>
              ) : (
                <>
                  The new settings for <strong>{repin.planLabel}</strong> are saved. Choose which packages
                  should give them to <strong>future</strong> clients.
                </>
              )}{" "}
              Clients already connected are not affected either way, and any package you leave unticked keeps
              the settings it has now.
            </p>
            <div className="mb-4 space-y-2">
              {repin.packages.map((k) => (
                <label key={k.package_id} className="flex items-center gap-2 text-sm">
                  <input type="checkbox" className="size-4 accent-primary" aria-label={`repin-${k.code}`}
                    checked={!!repin.chosen[k.package_id]}
                    onChange={(e) => setRepin((s2) => s2 && ({ ...s2, chosen: { ...s2.chosen, [k.package_id]: e.target.checked } }))} />
                  <span>{k.name || k.code}</span>
                  <span className="text-xs text-muted-foreground">{k.code}</span>
                </label>
              ))}
            </div>
            <Field label="Confirm your password" className="mb-4 max-w-sm"
              hint="Asked once for all the packages you move.">
              <Input id="repin-password" type="password" autoComplete="current-password" value={repinPassword}
                onChange={(e) => setRepinPassword(e.target.value)} />
            </Field>
            <div className="flex gap-2">
              <Button onClick={applyRepin}
                disabled={busy || (repin.packages.some((k) => repin.chosen[k.package_id]) && repinPassword === "")}>
                {busy ? "Applying…" : "Apply to selected packages"}
              </Button>
              <Button variant="ghost" disabled={busy} onClick={() => {
                const wasStanding = repin.standing;
                setRepin(null);
                setNotice(wasStanding
                  ? "Nothing was changed. Those packages keep the settings they have now."
                  : "Changes saved. No packages were updated, so clients continue to receive the settings they do now.");
              }}>Not now</Button>
            </div>
          </CardBody>
        </Card>
      )}

      {unavailable ? (
        <Card>
          <EmptyState
            icon={<Gauge />}
            title="The internet offering is not switched on for this appliance"
            hint="Service plans and internet packages become available once this capability is enabled for the site. Contact your Semantics administrator." />
        </Card>
      ) : (
        <Card className="overflow-hidden">
          <FilterBar
            inset
            filters={
              <FilterChips<Chip>
                label="Show plans"
                value={chip}
                onChange={setChip}
                options={[
                  { value: "all", label: "All", count: rows ? stats.all : undefined },
                  { value: "used", label: "In use", count: rows ? stats.used : undefined, tone: "ok" },
                  { value: "unused", label: "Not used", count: rows ? stats.unused : undefined },
                ]}
              />
            }
            search={<SearchInput value={q} onChange={setQ} placeholder="Search plans" label="Search plans by name or code" />}
          />
          {rows === null ? (
            <SkeletonRows rows={4} cols={7} />
          ) : rows.length === 0 ? (
            <EmptyState
              size="sm"
              icon={<Gauge />}
              title="No service plans yet"
              hint="A service plan defines the speed, devices and allowances a client receives. Create one, then attach it to an internet package."
              action={writable ? <Button onClick={() => startNew()}><Plus /> Add the first plan</Button> : undefined} />
          ) : shown.length === 0 ? (
            <EmptyState size="sm" icon={<Gauge />} title="No plan matches" hint="Clear the search or choose another filter." />
          ) : (
            <Table label="Service plans">
              <THead><TR>
                <TH>Plan</TH><TH>Speed (down / up)</TH><TH>Devices</TH><TH className="hidden md:table-cell">Time</TH>
                <TH className="hidden md:table-cell">Data</TH><TH>Used by</TH>
                <TH><span className="sr-only">Actions</span></TH>
              </TR></THead>
              <TBody>
                {shown.map((p) => {
                  const stale = stalePackagesFor(p, packages);
                  const name = p.name || p.code;
                  const used = p.used_by_active_packages ?? 0;
                  return (
                    <TR key={p.plan_id} onClick={() => setSelected(p)}>
                      <TD truncate title={name} className="max-w-[16rem]">
                        <span className="font-medium">{name}</span>
                        <div className="truncate text-xs text-muted-foreground">{p.code}</div>
                      </TD>
                      <TD nowrap>
                        {formatSpeedPair(p.down_kbps, p.up_kbps)}
                        {p.speed_allocation === "SHARED" && <span className="text-muted-foreground"> · shared</span>}
                      </TD>
                      <TD nowrap title={DEVICE_LIMIT_POLICIES[p.device_limit_policy ?? ""] ?? undefined}>
                        {formatDevices(p.max_concurrent_devices)}
                      </TD>
                      <TD nowrap className="hidden md:table-cell">{formatDuration(p.time_quota_seconds)}</TD>
                      <TD nowrap className="hidden md:table-cell">{formatData(p.data_quota_bytes)}</TD>
                      <TD nowrap>
                        <span className={used === 0 ? "text-muted-foreground" : undefined}>
                          {used === 0 ? "No active packages" : `${used} active package${used === 1 ? "" : "s"}`}
                        </span>
                        {stale.length > 0 && (
                          <div className="text-xs text-warning-subtle-foreground" data-testid={`stale-count-${p.code}`}>
                            {stale.length} still on older settings
                          </div>
                        )}
                      </TD>
                      <TD className="w-0">
                        {writable && (
                          <div className="flex items-center justify-end gap-1">
                            {stale.length > 0 && (
                              <Button size="xs" variant="ghost" className="text-foreground" data-testid={`apply-current-${p.code}`}
                                onClick={(e) => { e.stopPropagation(); startRepin(p); }}>
                                <RefreshCw /> Apply current settings
                              </Button>
                            )}
                            <RowActions
                              subject={name}
                              primary={{ label: "Edit", onSelect: () => startNew(p) }}
                              items={[{ label: "Delete…", danger: true, icon: <Trash2 />, onSelect: () => setDeleting(p) }]}
                            />
                          </div>
                        )}
                      </TD>
                    </TR>
                  );
                })}
              </TBody>
            </Table>
          )}
        </Card>
      )}

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
                      {stale.length === 1 ? " gives" : " give"} clients an earlier version of it.
                    </Callout>
                  )}
                  {!p.enabled && (
                    <Callout tone="neutral" title="Marked inactive in its records">
                      This mark does not change what clients receive: any active package using this plan still hands
                      it out. To stop clients receiving it, disable or edit those packages.
                    </Callout>
                  )}
                  <SheetSection title="What it grants">
                    <KeyValueGrid items={[
                      { label: "Speed (down / up)", value: formatSpeedPair(p.down_kbps, p.up_kbps) },
                      { label: "Speed sharing", value: p.speed_allocation === "SHARED"
                        ? "Shared by the client's devices" : "Full speed on every device" },
                      { label: "Devices at once", value: formatDevices(p.max_concurrent_devices),
                        hint: DEVICE_LIMIT_POLICIES[p.device_limit_policy ?? ""] },
                      { label: "Time allowance", value: formatDuration(p.time_quota_seconds),
                        hint: TIME_ACCOUNTING_MODES[p.time_accounting_mode ?? ""] },
                      { label: "Data allowance", value: formatData(p.data_quota_bytes) },
                      { label: "Disconnect after inactivity", value: p.idle_timeout_seconds ? formatDuration(p.idle_timeout_seconds) : "Never" },
                      { label: "Longest single session", value: p.max_continuous_session_seconds ? formatDuration(p.max_continuous_session_seconds) : "No limit" },
                    ]} />
                  </SheetSection>
                  <SheetSection title="Packages using it">
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
                  <SheetSection title="Saved versions">
                    <ErrorBanner err={revsErr} />
                    {revs === null && !revsErr ? <SkeletonRows rows={2} cols={1} /> : (
                      <HistoryList label="Saved versions" emptyLabel="No saved versions yet"
                        className="rounded-md border border-border"
                        items={(revs ?? []).map((r) => ({
                          id: r.revision_id,
                          title: `Version ${r.revision_no}`,
                          badge: r.is_current ? <Badge tone="accent">in force</Badge> : <span className="text-xs text-muted-foreground">superseded</span>,
                          summary: r.label || undefined,
                          tone: r.is_current ? "ok" : "neutral",
                        }))} />
                    )}
                  </SheetSection>
                  <SheetSection title="For support">
                    <KeyValueGrid columns={1} items={[{ label: "Plan reference", value: <MonoId value={p.plan_id} /> }]} />
                  </SheetSection>
                </SheetBody>
                {writable && (
                  <SheetFooter>
                    <Button variant="ghost" className="me-auto text-destructive" onClick={() => setDeleting(p)}>
                      <Trash2 /> Delete…
                    </Button>
                    {stale.length > 0 && (
                      <Button variant="secondary" onClick={() => startRepin(p)}><RefreshCw /> Apply current settings</Button>
                    )}
                    <Button onClick={() => { setSelected(null); startNew(p); }}><Pencil /> Edit</Button>
                  </SheetFooter>
                )}
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

      <Dialog open={showNew} onOpenChange={(v) => { if (!v && !busy) { setShowNew(false); setPrefill(null); } }}>
        <DialogContent size="lg">
          <DialogHeader>
            <DialogTitle>{prefill ? `Edit ${prefill.name || prefill.code}` : "Add a service plan"}</DialogTitle>
            <DialogDescription>
              {prefill
                ? "Saving records these as the plan's current settings. Clients already connected keep the terms they were given, and packages using this plan are only updated if you choose them afterwards."
                : "This creates the plan and its first settings."}
            </DialogDescription>
          </DialogHeader>
          <DialogBody className="space-y-4">
            <ErrorBanner err={formErr} />
            {/* Keyed on the plan: uncontrolled inputs would otherwise keep the previous plan's numbers and publish them. */}
            <form id="service-plan-form" key={prefill?.plan_id ?? "new"} onSubmit={onPublish} className="grid gap-x-4 gap-y-4 sm:grid-cols-2">
              <Field label="Plan code" required
                hint={prefill ? "Identifies this plan; cannot be changed." : "A short identifier no other plan uses. It cannot be changed later."}>
                <Input id="plan-code" name="code" required defaultValue={prefill?.code ?? ""} readOnly={!!prefill}
                  placeholder="GOLD" />
              </Field>
              <Field label="Display name" optional>
                <Input id="plan-name" name="name" defaultValue={prefill?.name ?? ""} placeholder="Premium Wi-Fi" />
              </Field>

              <Field label="Download speed" htmlFor="plan-down" hint={`Empty for unlimited. Up to ${LIMITS.mbps} Mbps.`}>
                <InputGroup suffix="Mbps">
                  <Input id="plan-down" name="down_mbps" type="number" min={0} max={LIMITS.mbps} step="0.1"
                    defaultValue={prefill?.down_kbps ? prefill.down_kbps / 1000 : ""} placeholder="Unlimited" />
                </InputGroup>
              </Field>
              <Field label="Upload speed" htmlFor="plan-up" hint={`Empty for unlimited. Up to ${LIMITS.mbps} Mbps.`}>
                <InputGroup suffix="Mbps">
                  <Input id="plan-up" name="up_mbps" type="number" min={0} max={LIMITS.mbps} step="0.1"
                    defaultValue={prefill?.up_kbps ? prefill.up_kbps / 1000 : ""} placeholder="Unlimited" />
                </InputGroup>
              </Field>

              <Field label="Devices at once" required>
                <Input id="plan-devices" name="max_concurrent_devices" type="number" min={1}
                  defaultValue={prefill?.max_concurrent_devices ?? 1} required />
              </Field>
              <Field label="When the device limit is reached"
                hint="Disconnecting the oldest device frees its place for the new one.">
                <Select id="plan-device-policy" name="device_limit_policy"
                  defaultValue={(SELECTABLE_DEVICE_LIMIT_POLICIES as readonly string[]).includes(prefill?.device_limit_policy ?? "")
                    ? (prefill?.device_limit_policy ?? "REJECT_NEW_DEVICE") : "REJECT_NEW_DEVICE"}>
                  {SELECTABLE_DEVICE_LIMIT_POLICIES.map((v) => (
                    <option key={v} value={v}>{DEVICE_LIMIT_POLICIES[v]}</option>
                  ))}
                </Select>
              </Field>

              {/* Fields must be pre-filled: the form publishes a complete version from whatever they hold. */}
              <Field label="Total time allowance" htmlFor="plan-time"
                hint={<>Time online, added up across the client&rsquo;s devices (two devices online for an hour use two
                  hours). Up to {LIMITS.timeQuotaDays} days.</>}>
                <div className="flex gap-2">
                  <Input id="plan-time" name="time_quota" type="number" min={0} step="0.01" placeholder="Unlimited"
                    className="flex-1" defaultValue={timeQuota.value} />
                  <Select name="time_quota_unit" aria-label="Time allowance unit" defaultValue={timeQuota.unit}
                    className="w-28 shrink-0">
                    <option value="hours">hours</option>
                    <option value="days">days</option>
                  </Select>
                </div>
              </Field>
              <Field label="Data allowance" htmlFor="plan-data" hint="Empty for unlimited.">
                <InputGroup suffix="GB">
                  <Input id="plan-data" name="data_quota_gb" type="number" min={0} step="0.1" placeholder="Unlimited"
                    defaultValue={bytesToGb(prefill?.data_quota_bytes)} />
                </InputGroup>
              </Field>

              <Field label="Disconnect after inactivity" htmlFor="plan-idle"
                hint="The device can sign in again; the package is not used up.">
                <InputGroup suffix="minutes">
                  <Input id="plan-idle" name="idle_timeout" type="number" min={0} max={LIMITS.idleMinutes} placeholder="Never"
                    defaultValue={secondsToDurationField(prefill?.idle_timeout_seconds, ["minutes"]).value} />
                </InputGroup>
              </Field>
              <Field label="Maximum single session" htmlFor="plan-session"
                hint="The device can sign in again straight away.">
                <InputGroup suffix="hours">
                  <Input id="plan-session" name="max_session" type="number" min={0} max={LIMITS.sessionHours} placeholder="No limit"
                    defaultValue={secondsToDurationField(prefill?.max_continuous_session_seconds, ["hours"]).value} />
                </InputGroup>
              </Field>

              <Field label="How the speed is shared" className="sm:col-span-2" hint="Shared needs a download and upload speed to share.">
                <Select id="plan-sharing" name="speed_allocation" defaultValue={prefill?.speed_allocation ?? "PER_DEVICE"}>
                  <option value="PER_DEVICE">Per device — every device gets the full speed</option>
                  <option value="SHARED">Shared — all the client&rsquo;s devices share the speed</option>
                </Select>
              </Field>

              {prefill ? (
                <Field label="How time is counted" className="sm:col-span-2" hint="Kept as this plan has it. Saving does not change how time is counted.">
                  <Select id="plan-time-mode" disabled value={prefill.time_accounting_mode || "VALIDITY_WINDOW"} onChange={() => {}}>
                    <option value={prefill.time_accounting_mode || "VALIDITY_WINDOW"}>
                      {TIME_ACCOUNTING_MODES[prefill.time_accounting_mode || "VALIDITY_WINDOW"] ?? prefill.time_accounting_mode}
                    </option>
                  </Select>
                </Field>
              ) : (
                <Field label="How time is counted" className="sm:col-span-2">
                  <Select id="plan-time-mode" name="time_accounting_mode" defaultValue="VALIDITY_WINDOW">
                    <option value="VALIDITY_WINDOW">{TIME_ACCOUNTING_MODES.VALIDITY_WINDOW}</option>
                  </Select>
                </Field>
              )}

              {prefill && (
                <Field label="Confirm your password" required className="sm:col-span-2"
                  hint="Saving changes what every package using this plan can give future clients.">
                  <Input id="plan-password" name="password" type="password" autoComplete="current-password" required />
                </Field>
              )}

            </form>
          </DialogBody>
          <DialogFooter>
            <Button type="button" variant="secondary" disabled={busy} onClick={() => { setShowNew(false); setPrefill(null); }}>Cancel</Button>
            <Button type="submit" form="service-plan-form" disabled={busy}>{busy ? "Saving…" : prefill ? "Save changes" : "Add plan"}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </PageShell>
  );
}
