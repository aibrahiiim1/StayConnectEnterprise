"use client";

// THE PACKAGE CATALOGUE — what guests are offered, how many are using each package now, and every action on a
// package in one place.
//
// The list shows what each package GIVES (not a code and a revision count). A row opens the package's record
// in a side sheet: what it gives, its saved versions, and Edit / Disable / Delete…. The revision chain underneath
// is unchanged and still immutable — lib/package-save decides which revision a save pins, and a save always
// records a new permanent version.

import { useCallback, useEffect, useMemo, useState } from "react";
import { AlertTriangle, Ban, CheckCircle2, Gauge, Package, Pencil, Plus, Trash2, Users } from "lucide-react";
import { api, ListResp } from "@/lib/api";
import {
  getActivity, getPackageDeletability, priceText, type PackageSummary, type RevisionInfo,
} from "@/lib/api/commerce";
import { StatCard, Toolbar } from "@/components/ui/page";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogBody, ConfirmDialog,
} from "@/components/ui/dialog";
import { Sheet, SheetContent, SheetHeader, SheetBody, SheetFooter, SheetSection } from "@/components/ui/sheet";
import { FilterChips, KeyValueGrid, MetricStrip, SearchInput, Timeline } from "@/components/ui/data";
import { MonoId, SkeletonRows } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { DeleteDialog } from "@/components/commerce/delete-dialog";
import { focusSheetItself } from "@/components/commerce/sheet-focus";
import { PackageForm, type PackageFormInitial, type PackageFormValue, type PlanOption } from "./package-form";
import { decideSave, saveOutcomeMessage } from "@/lib/package-save";
import { formatSpeed, formatData, formatDuration, formatDevices, DEVICE_LIMIT_POLICIES } from "@/lib/units";
import { allocationFromPolicy, stayLengthWarnings, readStayLength, type PackageStayRange } from "@/lib/stay-packages";
import { formatDate } from "@/lib/utils";
import { rulesToForm, tiersToForm, durationToForm, toLocalInput, type PackageCurrent } from "./form-mapping";

type PlanSummary = PlanOption & { used_by_active_packages?: number };
type Chip = "active" | "disabled" | "all";

export type TabProps = { guard: (e: unknown) => boolean; setErr: (s: string | null) => void };

export function PackagesTab({
  guard, setErr, addRequest = 0, onAddHandled,
}: TabProps & { addRequest?: number; onAddHandled?: () => void }) {
  const toast = useToast();
  const [rows, setRows] = useState<PackageSummary[] | null>(null);
  const [plans, setPlans] = useState<PlanSummary[]>([]);
  // Guests on each package right now, from the activity summary. null = that read failed, which is shown as
  // "—" rather than as zero: an unknown count is not an empty package.
  const [activeBy, setActiveBy] = useState<Record<string, number> | null>(null);
  const [activeNow, setActiveNow] = useState<number | null>(null);
  const [stayWarnings, setStayWarnings] = useState<string[]>([]);
  const [chip, setChip] = useState<Chip>("all");
  const [q, setQ] = useState("");

  const [selected, setSelected] = useState<PackageSummary | null>(null);
  const [history, setHistory] = useState<RevisionInfo[] | null>(null);
  const [historyErr, setHistoryErr] = useState<unknown>(null);

  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<PackageFormInitial | null>(null);
  const [formErr, setFormErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  // THE DISABLE CONFIRMATION — a reason and a password in one dialog, never two window.prompt() calls.
  const [disabling, setDisabling] = useState<PackageSummary | null>(null);
  const [deleting, setDeleting] = useState<PackageSummary | null>(null);
  const [actionErr, setActionErr] = useState<unknown>(null);

  const load = useCallback(async () => {
    try {
      const [pk, pl] = await Promise.all([
        api.get<ListResp<PackageSummary>>("/commercial-packages"),
        api.get<ListResp<PlanSummary>>("/commercial-packages/plans"),
      ]);
      const pkgs = pk.data ?? [];
      setRows(pkgs); setPlans(pl.data ?? []);
      setSelected((s) => (s ? pkgs.find((p) => p.package_id === s.package_id) ?? null : s));

      // Guests on each package now. A failure here must not take the catalogue down with it.
      getActivity({ range: "24h", limit: 1 })
        .then((a) => {
          const m: Record<string, number> = {};
          for (const x of a?.summary?.active_by_package ?? []) m[x.package_id] = x.grants;
          setActiveBy(m); setActiveNow(a?.summary?.active_now ?? null);
        })
        .catch(() => { setActiveBy(null); setActiveNow(null); });

      // THE STAY-LENGTH OVERLAP CHECK. The list does not carry eligibility rules — edged authors them and
      // cannot read the table back — so they come one package at a time through the scoped reader. It is a
      // WARNING: overlapping ranges are legitimate when the operator wants the guest to choose.
      const active = pkgs.filter((p) => p.active);
      const ranges = await Promise.all(active.map(async (p): Promise<PackageStayRange> => {
        try {
          const cur = await api.get<PackageCurrent>(`/commercial-packages/${p.package_id}/current`);
          return {
            package_id: p.package_id, name: p.name || p.code, active: true,
            range: readStayLength((cur.eligibility_rules ?? []).map((r) => ({
              type: r.type ?? r.Type, value: r.value ?? r.Value,
            }))),
          };
        } catch {
          // A package whose conditions cannot be read is not evidence of an overlap.
          return { package_id: p.package_id, name: p.name || p.code, active: true, range: null };
        }
      }));
      setStayWarnings(stayLengthWarnings(ranges));
    } catch (e) {
      if (!guard(e)) { setRows([]); setErr((e as Error)?.message ?? "Could not load packages"); }
    }
  }, [guard, setErr]);
  useEffect(() => { load(); }, [load]);

  // "Add package" lives in the page header; each press arrives here as a new request number.
  useEffect(() => {
    if (addRequest > 0) { setFormErr(null); setEditing(null); setAdding(true); onAddHandled?.(); }
  }, [addRequest]); // eslint-disable-line react-hooks/exhaustive-deps

  // The record's history loads when its sheet opens.
  useEffect(() => {
    if (!selected) { setHistory(null); setHistoryErr(null); return; }
    let live = true;
    setHistory(null); setHistoryErr(null);
    api.get<ListResp<RevisionInfo>>(`/commercial-packages/${selected.package_id}/revisions`)
      .then((r) => { if (live) setHistory(r.data ?? []); })
      .catch((e) => { if (live) setHistoryErr(e); });
    return () => { live = false; };
  }, [selected?.package_id]); // eslint-disable-line react-hooks/exhaustive-deps

  const counts = useMemo(() => {
    const all = rows ?? [];
    return { all: all.length, active: all.filter((p) => p.active).length, disabled: all.filter((p) => !p.active).length };
  }, [rows]);

  const shown = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return (rows ?? []).filter((p) =>
      (chip === "all" || (chip === "active" ? p.active : !p.active)) &&
      (!needle || (p.name ?? "").toLowerCase().includes(needle) || p.code.toLowerCase().includes(needle)));
  }, [rows, chip, q]);

  // EDIT loads the CURRENT configuration so nothing the form does not display is dropped on save.
  async function startEdit(p: PackageSummary) {
    setErr(null); setNotice(null); setFormErr(null);
    try {
      const cur = await api.get<PackageCurrent>(`/commercial-packages/${p.package_id}/current`);
      setEditing({
        code: p.code,
        name: (cur.display?.name as string) ?? p.name ?? p.code,
        planID: p.service_plan_id ?? "",
        planRevisionID: cur.service_plan_revision_id,
        rules: rulesToForm(cur.eligibility_rules),
        tiers: tiersToForm(cur.grant_tiers),
        duration: durationToForm(cur.duration_policy),
        visibleFrom: toLocalInput(cur.visible_from),
        visibleUntil: toLocalInput(cur.visible_until),
        // Loaded, not defaulted: an edit meant only to rename must not flatten a per-night allowance.
        allocation: allocationFromPolicy(cur.data_allocation_policy),
      });
      setAdding(false);
    } catch (e) { if (!guard(e)) setErr((e as Error)?.message ?? "Could not open this package"); }
  }

  // SAVE publishes a new immutable package revision pinned to the selected plan's current revision, and never
  // creates or edits a service plan. ADD sends create_only, so a code that is already taken is refused
  // (409 code_exists) instead of quietly becoming a new version of the package that owns it.
  async function save(v: PackageFormValue, mode: "add" | "edit") {
    setBusy(true); setFormErr(null); setNotice(null);
    try {
      const chosen = plans.find((x) => x.plan_id === v.selectedPlanID);
      if (!chosen?.current_revision_id) {
        setFormErr("That service plan has no settings published yet. Open Service plans and save it first.");
        return;
      }
      const decision = decideSave({
        pinnedPlanRevisionID: mode === "edit" ? editing?.planRevisionID ?? "" : "",
        selected: chosen,
        currentPlanID: mode === "edit" ? editing?.planID : undefined,
      });
      await api.post("/commercial-packages", {
        ...v.payload,
        service_plan_revision_id: decision.pinPlanRevisionID,
        ...(mode === "add" ? { create_only: true } : {}),
      });
      setAdding(false); setEditing(null);
      const msg = saveOutcomeMessage(decision);
      setNotice(msg);
      toast.success(mode === "add" ? "Package added" : "Package saved", msg);
      await load();
    } catch (e) { if (!guard(e)) setFormErr(e); }
    finally { setBusy(false); }
  }

  async function enable(p: PackageSummary) {
    setBusy(true); setErr(null); setNotice(null);
    try {
      await api.post(`/commercial-packages/${p.package_id}/active`, { active: true });
      const msg = `${p.name || p.code} is being offered to guests again.`;
      setNotice(msg); toast.success("Package enabled", msg);
      await load();
    } catch (e) { if (!guard(e)) setErr((e as Error)?.message ?? "Could not update this package"); }
    finally { setBusy(false); }
  }

  async function disable({ reason, password }: { reason: string; password: string }) {
    if (!disabling) return;
    setBusy(true); setActionErr(null); setNotice(null);
    try {
      await api.post(`/commercial-packages/${disabling.package_id}/active`, { active: false, reason, password });
      const msg = `${disabling.name || disabling.code} is no longer offered to guests. Anyone already online keeps what they have.`;
      setNotice(msg); toast.success("Package disabled", msg);
      setDisabling(null);
      await load();
    } catch (e) { setActionErr(e); }
    finally { setBusy(false); }
  }

  const planOf = (p: PackageSummary) => plans.find((x) => x.plan_id === p.service_plan_id);

  return (
    <div className="space-y-4">
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard label="Offered to guests" value={rows ? counts.active : "—"} icon={<CheckCircle2 />} tone="ok"
          hint={rows ? `${counts.all} package${counts.all === 1 ? "" : "s"} in total` : undefined} />
        <StatCard label="Disabled" value={rows ? counts.disabled : "—"} icon={<Ban />}
          hint="Kept with their history, not offered" />
        <StatCard label="Guests on a package now" value={activeNow ?? "—"} icon={<Users />} tone="info"
          hint={activeNow === null ? "Could not be counted right now" : "Across every package"} />
        <StatCard label="Service plans" value={plans.length} icon={<Gauge />} href="/service-plans"
          hint="The speed and allowances packages hand out" />
      </div>

      {notice && <Callout tone="success">{notice}</Callout>}
      {stayWarnings.length > 0 && (
        <div data-testid="stay-length-warnings">
          <Callout tone="warning" title="Overlapping stay lengths">
            <ul className="list-disc space-y-0.5 pl-4">
              {stayWarnings.map((w, i) => <li key={i}>{w}</li>)}
            </ul>
            <p className="mt-1 text-xs">
              Nothing has been changed. If you meant each length to have one package, adjust the night ranges.
            </p>
          </Callout>
        </div>
      )}

      <Card>
        <div className="border-b border-border px-4 py-3">
          <Toolbar>
            <FilterChips<Chip>
              label="Show packages"
              value={chip}
              onChange={setChip}
              options={[
                { value: "all", label: "All", count: counts.all },
                { value: "active", label: "Active", count: counts.active, tone: "ok" },
                { value: "disabled", label: "Disabled", count: counts.disabled },
              ]}
            />
            <SearchInput value={q} onChange={setQ} placeholder="Search packages" label="Search packages by name or code" />
          </Toolbar>
        </div>
        <CardBody className="p-0">
          {rows === null ? (
            <SkeletonRows rows={4} cols={7} />
          ) : rows.length === 0 ? (
            <EmptyState icon={<Package />} title="No internet packages yet"
              hint="Until a package exists, a verified guest has nothing to be given and cannot get online."
              action={<Button onClick={() => { setFormErr(null); setAdding(true); }}><Plus /> Add the first package</Button>} />
          ) : shown.length === 0 ? (
            <EmptyState icon={<Package />} title="No package matches"
              hint="Clear the search or choose another filter to see the rest." />
          ) : (
            <Table>
              <THead><TR>
                <TH>Package</TH><TH>Status</TH><TH>Price</TH><TH>Speed</TH><TH>Data</TH><TH>Time</TH>
                <TH>Devices</TH><TH>Guests now</TH><TH><span className="sr-only">Actions</span></TH>
              </TR></THead>
              <TBody>
                {shown.map((p) => (
                  <TR key={p.package_id} className="cursor-pointer" onClick={() => setSelected(p)}>
                    <TD>
                      <button type="button" className="text-left font-medium hover:underline"
                        onClick={(e) => { e.stopPropagation(); setSelected(p); }}>
                        {p.name || p.code}
                      </button>
                      {/* Only when it adds something: a package with no separate name printed its code twice. */}
                      {p.name && p.name !== p.code && <div className="text-xs text-muted-foreground">{p.code}</div>}
                      {p.plan_has_newer_revision && (
                        <div className="mt-0.5 inline-flex items-center gap-1 text-xs text-warning-subtle-foreground">
                          <AlertTriangle className="size-3" /> Plan has newer settings
                        </div>
                      )}
                    </TD>
                    <TD>{p.active ? <Badge tone="ok" dot>Active</Badge> : <Badge tone="default" dot>Disabled</Badge>}</TD>
                    <TD className="whitespace-nowrap">{priceText(p.price_minor, p.currency, p.currency_exponent)}</TD>
                    <TD>
                      <div>{formatSpeed(p.down_kbps)} down{p.speed_allocation === "SHARED" ? " (shared)" : ""}</div>
                      <div className="text-xs text-muted-foreground">{formatSpeed(p.up_kbps)} up</div>
                    </TD>
                    <TD>{formatData(p.data_quota_bytes)}</TD>
                    <TD>{formatDuration(p.time_quota_seconds)}</TD>
                    <TD>{formatDevices(p.max_concurrent_devices)}</TD>
                    <TD className="tabular">{activeBy ? (activeBy[p.package_id] ?? 0) : "—"}</TD>
                    <TD className="whitespace-nowrap text-right" onClick={(e) => e.stopPropagation()}>
                      <Button size="sm" variant="ghost" disabled={busy} onClick={() => startEdit(p)}>Edit</Button>
                      <Button size="sm" variant="ghost" disabled={busy}
                        onClick={() => { setActionErr(null); if (p.active) setDisabling(p); else void enable(p); }}>
                        {p.active ? "Disable" : "Enable"}
                      </Button>
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          )}
        </CardBody>
      </Card>

      {/* THE PACKAGE RECORD. */}
      <Sheet open={selected !== null} onOpenChange={(v) => !v && setSelected(null)}>
        <SheetContent width="md" onOpenAutoFocus={focusSheetItself}>
          {selected && (() => {
            const p = selected;
            const plan = planOf(p);
            return (
              <>
                <SheetHeader
                  icon={<Package />}
                  eyebrow="Internet package"
                  title={p.name || p.code}
                  description={p.name && p.name !== p.code ? `Code ${p.code}` : undefined}
                  badges={<>
                    {p.active ? <Badge tone="ok" dot>Offered to guests</Badge> : <Badge dot>Disabled</Badge>}
                    {p.package_type && <Badge tone="neutral">{p.package_type.replace(/_/g, " ").toLowerCase()}</Badge>}
                  </>}
                />
                <SheetBody>
                  <MetricStrip items={[
                    { label: "Price", value: priceText(p.price_minor, p.currency, p.currency_exponent) },
                    { label: "Download", value: formatSpeed(p.down_kbps) },
                    { label: "Data", value: formatData(p.data_quota_bytes) },
                    { label: "Guests now", value: activeBy ? (activeBy[p.package_id] ?? 0) : "—" },
                  ]} />

                  {p.plan_has_newer_revision && (
                    <Callout tone="warning" title="The service plan has newer settings">
                      The <strong>{p.service_plan_code}</strong> service plan has newer settings that this package
                      does not use yet. Choose Edit and save to bring it up to date.
                    </Callout>
                  )}

                  <SheetSection title="What it gives">
                    <KeyValueGrid items={[
                      { label: "Service plan", value: plan?.name || p.service_plan_code || "—",
                        hint: p.service_plan_revision_no ? `Using version ${p.service_plan_revision_no} of the plan` : undefined },
                      { label: "Speed", value: `${formatSpeed(p.down_kbps)} down · ${formatSpeed(p.up_kbps)} up` },
                      { label: "Data allowance", value: formatData(p.data_quota_bytes) },
                      { label: "Time allowance", value: formatDuration(p.time_quota_seconds) },
                      { label: "Devices", value: formatDevices(p.max_concurrent_devices),
                        hint: DEVICE_LIMIT_POLICIES[p.device_limit_policy ?? ""] },
                      { label: "Speed sharing", value: p.speed_allocation === "SHARED"
                        ? "Shared by the guest's devices" : "Full speed on every device" },
                      { label: "Offered from", value: p.visible_from ? formatDate(p.visible_from) : "Always" },
                      { label: "Offered until", value: p.visible_until ? formatDate(p.visible_until) : "No end date" },
                    ]} />
                  </SheetSection>

                  <SheetSection title="Saved versions"
                    description="Each saved change is kept permanently. A guest keeps the terms that applied when they connected.">
                    <ErrorBanner err={historyErr} />
                    {history === null && !historyErr ? <SkeletonRows rows={2} cols={1} /> : (
                      <Timeline
                        emptyLabel="No saved versions yet"
                        items={(history ?? []).map((r) => ({
                          key: r.revision_id,
                          title: <span>Version {r.revision_no}{" "}
                            {r.is_current ? <Badge tone="info">in force</Badge> : <span className="text-muted-foreground">superseded</span>}
                          </span>,
                          body: priceText(r.price_minor, r.currency, r.currency_exponent),
                          tone: r.is_current ? "ok" : "neutral",
                        }))}
                      />
                    )}
                  </SheetSection>

                  <SheetSection title="For support">
                    <KeyValueGrid columns={1} items={[
                      { label: "Package reference", value: <MonoId value={p.package_id} /> },
                    ]} />
                  </SheetSection>
                </SheetBody>
                <SheetFooter>
                  <Button variant="ghost" className="mr-auto text-destructive"
                    onClick={() => { setDeleting(p); }}>
                    <Trash2 /> Delete…
                  </Button>
                  <Button variant="secondary" disabled={busy}
                    onClick={() => { setActionErr(null); if (p.active) setDisabling(p); else void enable(p); }}>
                    {p.active ? <><Ban /> Disable</> : <><CheckCircle2 /> Enable</>}
                  </Button>
                  <Button disabled={busy} onClick={() => startEdit(p)}><Pencil /> Edit</Button>
                </SheetFooter>
              </>
            );
          })()}
        </SheetContent>
      </Sheet>

      {/* ADD and EDIT are dialogs over the list, so the operator never loses their place. */}
      <Dialog open={adding} onOpenChange={(v) => !v && !busy && setAdding(false)}>
        <DialogContent size="xl">
          <DialogHeader>
            <DialogTitle>Add an internet package</DialogTitle>
            <DialogDescription>What the guest is offered on the portal, and the service plan it hands out.</DialogDescription>
          </DialogHeader>
          <DialogBody className="space-y-4">
            <ErrorBanner err={formErr} />
            <PackageForm mode="add" plans={plans} busy={busy} onSave={(v) => save(v, "add")} onCancel={() => setAdding(false)} />
          </DialogBody>
        </DialogContent>
      </Dialog>

      <Dialog open={editing !== null} onOpenChange={(v) => { if (!v && !busy) setEditing(null); }}>
        <DialogContent size="xl">
          <DialogHeader>
            <DialogTitle>{editing ? `Edit ${editing.name || editing.code}` : "Edit package"}</DialogTitle>
            <DialogDescription>
              Saving records a new permanent version of this package. Guests already online keep the terms they
              connected under.
            </DialogDescription>
          </DialogHeader>
          <DialogBody className="space-y-4">
            <ErrorBanner err={formErr} />
            {editing && (
              <PackageForm mode="edit" initial={editing} plans={plans} busy={busy} onSave={(v) => save(v, "edit")}
                onCancel={() => setEditing(null)} />
            )}
          </DialogBody>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={disabling !== null}
        onOpenChange={(v) => !v && setDisabling(null)}
        title={`Stop offering ${disabling?.name || disabling?.code || "this package"}?`}
        description="Guests will stop being offered it immediately. Anyone already online keeps the access they were given, its history is kept, and it can be switched back on at any time."
        confirmLabel="Stop offering it"
        confirmVariant="danger"
        busy={busy}
        error={actionErr}
        requireReason
        reasonLabel="Why are you disabling it?"
        reasonPlaceholder="Replaced by the new summer package"
        requirePassword
        onConfirm={disable}
      />

      {deleting && (
        <DeleteDialog
          open
          onOpenChange={(v) => !v && setDeleting(null)}
          kind="package"
          name={deleting.name || deleting.code}
          load={() => getPackageDeletability(deleting.package_id)}
          onDisableInstead={deleting.active ? () => {
            const p = deleting;
            setDeleting(null); setActionErr(null); setDisabling(p);
          } : undefined}
        />
      )}
    </div>
  );
}
