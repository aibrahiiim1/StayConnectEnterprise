"use client";

// THE PACKAGE CATALOGUE — what guests are offered, how many are using each package now, and every action on a
// package in one place.
//
// The list shows what each package GIVES (not a code and a revision count). A row opens the package's record
// in a side sheet: what it gives, its saved versions, and Edit / Disable / Delete…. The revision chain underneath
// is unchanged and still immutable — lib/package-save decides which revision a save pins, and a save always
// records a new permanent version.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import { AlertTriangle, Ban, CheckCircle2, Gauge, Package, Pencil, Plus, Trash2, Users } from "lucide-react";
import { api, ApiError, ListResp } from "@/lib/api";
import { defaultCardAccount, type CardAccountView } from "@/lib/card-currency";
import type { AccountsResp } from "@/lib/payment-admin";
import {
  getActivity, getPackageDeletability, deletePackage, priceText, type PackageSummary, type RevisionInfo,
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
import { PackageForm, type ClientGroupOption, type PackageFormInitial, type PackageFormValue, type PlanOption } from "./package-form";
import { decideSave, saveOutcomeMessage } from "@/lib/package-save";
import { formatSpeed, formatData, formatDuration, formatDevices, DEVICE_LIMIT_POLICIES } from "@/lib/units";
import { allocationFromPolicy, stayLengthWarnings, readStayLength, type PackageStayRange } from "@/lib/stay-packages";
import { formatDate } from "@/lib/utils";
import {
  rulesToForm, tiersToForm, durationToForm, toLocalInput, acquisitionToForm, preservedRulesOf,
  revisionCarryOf, revisionTermsOf, type PackageCurrent, type RevisionCarry,
} from "./form-mapping";
import {
  describePublishRefusal, revisionNeedsPassword, type ModulesReport, type RevisionTerms, type RoomChargeInterface,
} from "@/lib/commerce-form";

type PlanSummary = PlanOption & { used_by_active_packages?: number };

// A refusal of the price section arrives as "invalid_currency: …" (and a duration one as
// "invalid_duration_policy: …"); it is shown naming the part of the form it concerns, followed by the server's
// own reason. Every other refusal -- module_not_enabled, commercial_fields_required, a validation sentence --
// already reads as a sentence and is shown exactly as the server wrote it, never swallowed.
function readableRefusal(e: unknown): unknown {
  const readable = e instanceof Error ? describePublishRefusal(e.message) : null;
  return readable ?? e;
}
type Chip = "active" | "disabled" | "all";

export type TabProps = { guard: (e: unknown) => boolean; setErr: (s: string | null) => void };

export function PackagesTab({
  guard, setErr, addRequest = 0, onAddHandled, writable = true,
}: TabProps & { addRequest?: number; onAddHandled?: () => void; writable?: boolean }) {
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
  // WHAT THE PRICE SECTION MAY OFFER: the site's module state and the PMS interfaces room charge can be mapped
  // to. Read each time the form opens, because an operator may have just switched a module on in another tab.
  // null = loading; "error" = could not be read, which the form treats as not selectable (fail closed).
  const [modules, setModules] = useState<ModulesReport | "error" | null>(null);
  const [roomIfaces, setRoomIfaces] = useState<RoomChargeInterface[] | "error" | null>(null);
  // THE CLIENT GROUPS AN AUDIENCE CAN NAME, read with the module state for the same reason: a group created in
  // another tab must be offered the next time the form opens, not the next time the page loads.
  const [clientGroups, setClientGroups] = useState<ClientGroupOption[] | "error" | null>(null);
  // THE DEFAULT ACTIVE PAYMENT ACCOUNT -- the one every card checkout uses. A package is offered by card only in
  // its currency, or in one its provider converts when it is marked provider-converts (lib/card-currency, the
  // rule scd applies). null = unknown (not licensed, or not readable): no claim is made.
  const [cardAccount, setCardAccount] = useState<CardAccountView | "none" | null>(null);
  // WHAT THE PACKAGE BEING EDITED CHARGES AND GRANTS TODAY, and the fields the form does not edit. A save that
  // changes the terms needs the operator's password; the carried fields go back exactly as loaded.
  const [editBase, setEditBase] = useState<{ terms: RevisionTerms; carry: RevisionCarry } | null>(null);
  // A SAVE WAITING FOR THE PASSWORD. Set when the revision changes price, currency, methods or plan (or when
  // the server asks for the password anyway); the dialog then sends the same body with the password.
  const [pendingSave, setPendingSave] = useState<{ body: Record<string, unknown>; planChanged: boolean } | null>(null);
  const [pendingErr, setPendingErr] = useState<unknown>(null);
  // REACTIVATION is a step-up too: switching a package back on offers it to clients again.
  const [enabling, setEnabling] = useState<PackageSummary | null>(null);
  const formOpen = adding || editing !== null;
  useEffect(() => {
    if (!formOpen) return;
    let live = true;
    setModules(null); setRoomIfaces(null); setClientGroups(null); setCardAccount(null);
    api.get<ListResp<ClientGroupOption>>("/client-groups")
      .then((g) => { if (live) setClientGroups(g?.data ?? []); })
      .catch(() => { if (live) setClientGroups("error"); });
    // The PMS interfaces room charge can map to are asked for only when Room charge is licensed: a site
    // without it never calls the room-charge surface.
    api.get<ModulesReport>("/modules")
      .then((m) => {
        if (!live) return;
        setModules(m ?? {});
        if (m?.modules?.room_charge?.licensed) {
          api.get<{ interfaces?: RoomChargeInterface[] | null }>("/pms-financial-onboarding")
            .then((r) => { if (live) setRoomIfaces(r?.interfaces ?? []); })
            .catch(() => { if (live) setRoomIfaces("error"); });
        } else {
          setRoomIfaces([]);
        }
        if (m?.modules?.card_payment?.licensed) {
          api.get<AccountsResp>("/payment-providers/accounts")
            .then((a) => { if (live) setCardAccount(defaultCardAccount(a?.accounts ?? [])); })
            .catch(() => { /* unknown: the form makes no claim about card currencies */ });
        }
      })
      .catch(() => { if (live) { setModules("error"); setRoomIfaces([]); } });
    return () => { live = false; };
  }, [formOpen]);

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
      // A package with NO current revision has no conditions to read: asking the server for them returns a
      // correct 404 ("no current configuration") that the browser then logs as an error on every load. Such a
      // package is shown as not configured instead, and cannot overlap anything.
      const active = pkgs.filter((p) => p.active && !!p.current_revision_id);
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
    if (addRequest > 0 && writable) { setFormErr(null); setEditing(null); setAdding(true); onAddHandled?.(); }
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
      setEditBase({ terms: revisionTermsOf(cur), carry: revisionCarryOf(cur) });
      setEditing({
        code: p.code,
        name: (cur.display?.name as string) ?? p.name ?? p.code,
        planID: p.service_plan_id ?? "",
        planRevisionID: cur.service_plan_revision_id,
        rules: rulesToForm(cur.eligibility_rules),
        // Kept verbatim: conditions of a type the form has no control for, and the whole display object.
        preservedRules: preservedRulesOf(cur.eligibility_rules),
        display: cur.display ?? null,
        tiers: tiersToForm(cur.grant_tiers),
        duration: durationToForm(cur.duration_policy),
        visibleFrom: toLocalInput(cur.visible_from),
        visibleUntil: toLocalInput(cur.visible_until),
        // Loaded, not defaulted: an edit meant only to rename must not flatten a per-night allowance.
        allocation: allocationFromPolicy(cur.data_allocation_policy),
        // Loaded, not defaulted, for the same reason: a rename must not make a priced package free.
        acquisition: acquisitionToForm(cur),
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
      const body: Record<string, unknown> = {
        ...v.payload,
        service_plan_revision_id: decision.pinPlanRevisionID,
        ...(mode === "add" ? { create_only: true } : {}),
      };
      if (mode === "edit") {
        // A REVISION OF AN EXISTING PACKAGE STATES ITS COMMERCIAL TERMS IN FULL. The server refuses one that
        // leaves any out (commercial_fields_required) rather than defaulting it to Free; room-charge mappings
        // are sent as [] when the package has none.
        body.room_charge_mappings = v.payload.room_charge_mappings ?? [];
        Object.assign(body, editBase?.carry ?? {});
        const after: RevisionTerms = {
          price_minor: v.payload.price_minor ?? 0,
          currency: v.payload.currency ?? "",
          currency_exponent: v.payload.currency_exponent ?? 2,
          acquisition_methods: v.payload.acquisition_methods ?? [],
          service_plan_revision_id: decision.pinPlanRevisionID,
        };
        if (revisionNeedsPassword(editBase?.terms, after)) {
          // Changing what clients pay or receive is a step-up: ask for the password, then send this body.
          setPendingErr(null); setPendingSave({ body, planChanged: decision.planChanged });
          return;
        }
      }
      await publish(body, mode, decision.planChanged);
    } catch (e) {
      if (guard(e)) return;
      if (mode === "edit" && e instanceof ApiError && e.code === "reauth_required" && lastBody.current) {
        // The server judged this save a change of terms after all: ask for the password and send it again.
        setPendingErr(null); setPendingSave({ body: lastBody.current, planChanged: lastPlanChanged.current });
        return;
      }
      setFormErr(readableRefusal(e));
    }
    finally { setBusy(false); }
  }

  // The body of the last publish attempt, so a step-up the server asks for can resend exactly the same thing.
  const lastBody = useRef<Record<string, unknown> | null>(null);
  const lastPlanChanged = useRef(false);

  async function publish(body: Record<string, unknown>, mode: "add" | "edit", planChanged: boolean) {
    lastBody.current = body; lastPlanChanged.current = planChanged;
    await api.post("/commercial-packages", body);
    lastBody.current = null;
    setAdding(false); setEditing(null); setEditBase(null); setPendingSave(null);
    const msg = saveOutcomeMessage({ pinPlanRevisionID: String(body.service_plan_revision_id ?? ""), planChanged, publishesPackageRevision: true });
    setNotice(msg);
    toast.success(mode === "add" ? "Package added" : "Package saved", msg);
    await load();
  }

  async function confirmPendingSave({ password }: { password: string }) {
    if (!pendingSave) return;
    const { body, planChanged } = pendingSave;
    setBusy(true); setPendingErr(null);
    try {
      await publish({ ...body, password }, "edit", planChanged);
    } catch (e) {
      if (guard(e)) return;
      if (e instanceof ApiError && e.code === "reauth_required") {
        setPendingErr(new Error("That password is not correct. Nothing was saved."));
        return;
      }
      // Any other refusal concerns the form, not the password: close the prompt and show it there.
      setPendingSave(null);
      setFormErr(readableRefusal(e));
    } finally { setBusy(false); }
  }

  async function enable({ password }: { password: string }) {
    if (!enabling) return;
    const p = enabling;
    setBusy(true); setActionErr(null); setNotice(null);
    try {
      await api.post(`/commercial-packages/${p.package_id}/active`, { active: true, password });
      const msg = `${p.name || p.code} is being offered to clients again.`;
      setNotice(msg); toast.success("Package enabled", msg);
      setEnabling(null);
      await load();
    } catch (e) {
      if (guard(e)) return;
      setActionErr(e instanceof ApiError && e.code === "reauth_required"
        ? new Error("That password is not correct. The package was not switched on.")
        : e);
    }
    finally { setBusy(false); }
  }

  async function disable({ reason, password }: { reason: string; password: string }) {
    if (!disabling) return;
    setBusy(true); setActionErr(null); setNotice(null);
    try {
      await api.post(`/commercial-packages/${disabling.package_id}/active`, { active: false, reason, password });
      const msg = `${disabling.name || disabling.code} is no longer offered to clients. Anyone already online keeps what they have.`;
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
        <StatCard label="Offered to clients" value={rows ? counts.active : "—"} icon={<CheckCircle2 />} tone="ok"
          hint={rows ? `${counts.all} package${counts.all === 1 ? "" : "s"} in total` : undefined} />
        <StatCard label="Disabled" value={rows ? counts.disabled : "—"} icon={<Ban />}
          hint="Kept with their history, not offered" />
        <StatCard label="Clients on a package now" value={activeNow ?? "—"} icon={<Users />} tone="info"
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
              hint="Until a package exists, a verified client has nothing to be given and cannot get online."
              action={writable ? <Button onClick={() => { setFormErr(null); setAdding(true); }}><Plus /> Add the first package</Button> : undefined} />
          ) : shown.length === 0 ? (
            <EmptyState icon={<Package />} title="No package matches"
              hint="Clear the search or choose another filter to see the rest." />
          ) : (
            <Table>
              <THead><TR>
                <TH>Package</TH><TH>Status</TH><TH>Price</TH><TH>Speed</TH><TH>Data</TH><TH>Time</TH>
                <TH>Devices</TH><TH>Clients now</TH><TH><span className="sr-only">Actions</span></TH>
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
                    <TD>
                      {!p.current_revision_id ? (
                        <Badge tone="warn" dot>Not configured</Badge>
                      ) : p.active ? (
                        <Badge tone="ok" dot>Active</Badge>
                      ) : (
                        <Badge tone="default" dot>Disabled</Badge>
                      )}
                    </TD>
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
                      {writable && (
                        <>
                          <Button size="sm" variant="ghost" disabled={busy} aria-label={`Edit ${p.name || p.code}`} onClick={() => startEdit(p)}>Edit</Button>
                          <Button size="sm" variant="ghost" disabled={busy} aria-label={`${p.active ? "Disable" : "Enable"} ${p.name || p.code}`}
                            onClick={() => { setActionErr(null); if (p.active) setDisabling(p); else setEnabling(p); }}>
                            {p.active ? "Disable" : "Enable"}
                          </Button>
                        </>
                      )}
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
                    {/* A package with no published configuration cannot be offered, whatever its active flag says. */}
                    {!p.current_revision_id ? (
                      <Badge tone="warn" dot>Not configured — not offered to clients</Badge>
                    ) : p.active ? (
                      <Badge tone="ok" dot>Offered to clients</Badge>
                    ) : (
                      <Badge dot>Disabled</Badge>
                    )}
                    {p.package_type && <Badge tone="neutral">{p.package_type.replace(/_/g, " ").toLowerCase()}</Badge>}
                  </>}
                />
                <SheetBody>
                  <MetricStrip items={[
                    { label: "Price", value: priceText(p.price_minor, p.currency, p.currency_exponent) },
                    { label: "Download", value: formatSpeed(p.down_kbps) },
                    { label: "Data", value: formatData(p.data_quota_bytes) },
                    { label: "Clients now", value: activeBy ? (activeBy[p.package_id] ?? 0) : "—" },
                  ]} />

                  {/* THIS USED TO TELL THE OPERATOR TO DO SOMETHING THE PRODUCT DELIBERATELY REFUSES.
                      It said "Choose Edit and save to bring it up to date". Saving a package KEEPS the service-plan
                      revision it is pinned to when the plan selection has not changed -- decideSave does that on
                      purpose, because advancing the pin just because someone renamed a package would apply a
                      technical change nobody approved on this screen. So the operator would edit, save, read
                      "Changes saved", and the package would still be on the old revision with nothing to say the
                      instruction had not worked. The supported action already exists on Service plans, where it
                      asks which packages should move, one by one. */}
                  {p.plan_has_newer_revision && (
                    <Callout tone="warning" title="The service plan has newer settings">
                      The <strong>{p.service_plan_code}</strong> service plan has newer settings that this package
                      does not use yet. Editing and saving this package will not move it: a package keeps the
                      version of the plan it was pinned to.{" "}
                      To move it, open <Link href="/service-plans" className="font-medium underline">Service
                      plans</Link>, choose <strong>{p.service_plan_code}</strong> and use <strong>Apply current
                      settings</strong> — that asks which packages should move. Clients already online keep what
                      they have; the change applies to clients who connect afterwards.
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
                        ? "Shared by the client's devices" : "Full speed on every device" },
                      { label: "Offered from", value: p.visible_from ? formatDate(p.visible_from) : "Always" },
                      { label: "Offered until", value: p.visible_until ? formatDate(p.visible_until) : "No end date" },
                    ]} />
                  </SheetSection>

                  <SheetSection title="Saved versions">
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
                {writable && (
                  <SheetFooter>
                    <Button variant="ghost" className="me-auto text-destructive"
                      onClick={() => { setDeleting(p); }}>
                      <Trash2 /> Delete…
                    </Button>
                    <Button variant="secondary" disabled={busy}
                      onClick={() => { setActionErr(null); if (p.active) setDisabling(p); else setEnabling(p); }}>
                      {p.active ? <><Ban /> Disable</> : <><CheckCircle2 /> Enable</>}
                    </Button>
                    <Button disabled={busy} onClick={() => startEdit(p)}><Pencil /> Edit</Button>
                  </SheetFooter>
                )}
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
            <DialogDescription>What the client is offered on the portal, and the service plan it hands out.</DialogDescription>
          </DialogHeader>
          <DialogBody className="space-y-4">
            <ErrorBanner err={formErr} />
            <PackageForm mode="add" plans={plans} busy={busy} onSave={(v) => save(v, "add")} onCancel={() => setAdding(false)}
              modules={modules} roomChargeInterfaces={roomIfaces} clientGroups={clientGroups} cardAccount={cardAccount} />
          </DialogBody>
        </DialogContent>
      </Dialog>

      <Dialog open={editing !== null} onOpenChange={(v) => { if (!v && !busy) { setEditing(null); setEditBase(null); } }}>
        <DialogContent size="xl">
          <DialogHeader>
            <DialogTitle>{editing ? `Edit ${editing.name || editing.code}` : "Edit package"}</DialogTitle>
            <DialogDescription>
              Saving records a new permanent version of this package. Clients already online keep the terms they
              connected under.
            </DialogDescription>
          </DialogHeader>
          <DialogBody className="space-y-4">
            <ErrorBanner err={formErr} />
            {editing && (
              <PackageForm mode="edit" initial={editing} plans={plans} busy={busy} onSave={(v) => save(v, "edit")}
                onCancel={() => setEditing(null)} modules={modules} roomChargeInterfaces={roomIfaces} clientGroups={clientGroups}
                cardAccount={cardAccount} />
            )}
          </DialogBody>
        </DialogContent>
      </Dialog>

      {/* THE STEP-UP FOR A CHANGE OF TERMS. Changing the price, currency, how clients get the package or the
          service plan it hands out changes what future clients pay or receive, so the server asks for the
          operator's password. Asked once, here, and sent with the same body the form produced. */}
      <ConfirmDialog
        open={pendingSave !== null}
        onOpenChange={(v) => { if (!v && !busy) { setPendingSave(null); setPendingErr(null); } }}
        title="Confirm the change of terms"
        description="This change alters what future clients pay for this package or what it gives them (its price, currency, how clients get it, or its service plan). Clients already online keep the terms they connected under."
        confirmLabel="Save changes"
        busy={busy}
        error={pendingErr}
        requirePassword
        onConfirm={confirmPendingSave}
      />

      <ConfirmDialog
        open={enabling !== null}
        onOpenChange={(v) => !v && !busy && setEnabling(null)}
        title={`Offer ${enabling?.name || enabling?.code || "this package"} again?`}
        description="Clients will be offered it again straight away, on the terms of its current version."
        confirmLabel="Offer it again"
        busy={busy}
        error={actionErr}
        requirePassword
        onConfirm={enable}
      />

      <ConfirmDialog
        open={disabling !== null}
        onOpenChange={(v) => !v && setDisabling(null)}
        title={`Stop offering ${disabling?.name || disabling?.code || "this package"}?`}
        description="Clients will stop being offered it immediately. Anyone already online keeps the access they were given, its history is kept, and it can be switched back on at any time."
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
          onDelete={async ({ reason, password }) => {
            const p = deleting;
            await deletePackage(p.package_id, reason, password);
            const msg = `${p.name || p.code} and its saved versions were removed permanently.`;
            setDeleting(null); setNotice(msg); toast.success("Package deleted", msg);
            await load();
          }}
          onDisableInstead={deleting.active ? () => {
            const p = deleting;
            setDeleting(null); setActionErr(null); setDisabling(p);
          } : undefined}
        />
      )}
    </div>
  );
}
