"use client";

// ONE FORM FOR A PACKAGE — what it is called, WHICH SERVICE PLAN it uses, what it costs and how clients get
// it, who gets it, how long it lasts.
//
// The two concepts stay distinct, because they are. A SERVICE PLAN is the technical service: speed, data,
// time, devices, how the speed is shared. An INTERNET PACKAGE is the guest offer: a name, a duration, who is
// eligible — and which service plan it hands out.
//
// What is hidden is the REVISION, not the relationship. The operator picks a plan by name and sees what it
// grants; the service-plan revision that pins is worked out in lib/package-save. An earlier version of this
// form carried the speed and allowance fields itself and published a plan revision behind the operator's
// back, which hid the wrong thing: it let a package silently author technical settings belonging to a plan
// that other packages also use. Those fields are shown here as read-only context and edited on Service Plans.

import { useId, useState } from "react";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { Switch } from "@/components/ui/misc";
import { Trash2, Plus, ChevronDown, ChevronRight } from "lucide-react";
import { durationToSeconds } from "@/lib/units";
import {
  ALLOCATION_MODE_LABELS, validateAllocation, serializeAllocation, previewAllocation,
  allowanceNotice, type AllocationForm,
} from "@/lib/stay-packages";
import { planSummary } from "@/lib/package-save";
import {
  SUPPORTED_RULE_TYPES,
  SUPPORTED_END_MODES,
  END_MODE_LABELS,
  RULE_TYPE_LABELS,
  buildPublishPayload,
  buildAcquisition,
  newAcquisitionForm,
  type AcquisitionForm,
  type ModulesReport,
  type RoomChargeInterface,
  type EligibilityRuleForm,
  isPMSRuleType,
  type GrantTierForm,
  type DurationForm,
  type PublishPayload,
  type RuleType,
  isDedicatedRule,
  isPaidPrice,
  WITHIN_HOURS_MIN,
  WITHIN_HOURS_MAX,
} from "@/lib/commerce-form";
import { moduleLicensedIn } from "@/lib/commerce-form";
import { AcquisitionSection } from "./acquisition-section";
import { TravelAgentPicker } from "./travel-agent-picker";

export type PackageFormValue = {
  payload: PublishPayload;
  /** The Service Plan the operator selected. The caller turns it into the revision that gets pinned. */
  selectedPlanID: string;
};

/** PlanOption is one selectable Service Plan, described by what it grants rather than by an id. */
export type PlanOption = {
  plan_id: string;
  code: string;
  name?: string | null;
  current_revision_id: string;
  down_kbps?: number | null; up_kbps?: number | null;
  data_quota_bytes?: number | null; time_quota_seconds?: number | null;
  max_concurrent_devices?: number | null; speed_allocation?: string | null;
};

/** One Client group as GET /client-groups lists it (only what the audience picker shows). */
export type ClientGroupOption = { id: string; name: string; enabled?: boolean; priority?: number };

// THE AUDIENCE, as the operator chooses it. Four words for one CLIENT_GROUP rule (or none).
type Audience = "everyone" | "groups" | "public" | "groups_public";
type AudienceRule = Extract<EligibilityRuleForm, { type: "CLIENT_GROUP" }>;
function audienceOf(r: AudienceRule | undefined): Audience {
  if (!r) return "everyone";
  if (r.public) return r.group_ids.length ? "groups_public" : "public";
  return "groups";
}

// THE FREE ALLOWANCE, as the operator chooses it: no limit (no rule), once ever, or once every N hours.
type FreeAccess = "unlimited" | "once" | "every";
type FreeRule = Extract<EligibilityRuleForm, { type: "PRIOR_PURCHASE" }>;
function freeAccessOf(r: FreeRule | undefined): FreeAccess {
  if (!r) return "unlimited";
  return r.within_hours !== undefined && r.within_hours !== "" ? "every" : "once";
}

export type PackageFormInitial = {
  code: string;
  name: string;
  /** The Service Plan this package currently uses, preselected on Edit. */
  planID: string;
  planRevisionID: string;
  rules: EligibilityRuleForm[];
  tiers: GrantTierForm[];
  duration: DurationForm;
  visibleFrom?: string;
  visibleUntil?: string;
  /** The package's existing data-allowance policy. Absent means the plan's flat allowance. */
  allocation?: AllocationForm;
  /** The package's price, currency, acquisition methods and room-charge mappings, handed back unchanged. */
  acquisition?: AcquisitionForm;
};

function emptyRule(type: RuleType): EligibilityRuleForm {
  switch (type) {
    case "AUTH_METHOD": return { type, methods: "" };
    case "SUBJECT_KIND": return { type, kinds: "" };
    case "DATE_WINDOW": return { type, from: "", until: "" };
    // A generic row starts as "requires prior": the free allowance (forbids_prior) has its own control above
    // the rows, and a row that started there would simply move into it.
    case "PRIOR_PURCHASE": return { type, mode: "requires_prior" };
    case "SITE_NETWORK": return { type, guest_network_ids: "" };
    case "CLIENT_GROUP": return { type, group_ids: [], public: false };
    case "STAY_LENGTH": return { type, min_nights: "", max_nights: "" };
    case "ROOM_TYPE": return { type, room_types: "" };
    case "RATE_PLAN": return { type, rate_plans: "" };
    case "VIP": return { type, is_vip: "true" };
    case "TRAVEL_AGENT": return { type, travel_agents: [] };
    case "PMS_INTERFACE": return { type, pms_interface_ids: "" };
  }
}

const num = (v: unknown): number | null => {
  if (v === "" || v === null || v === undefined) return null;
  const n = Number(v);
  return Number.isFinite(n) ? n : null;
};

export function PackageForm({
  mode, initial, plans, busy, onSave, onCancel, modules = null, roomChargeInterfaces = null, clientGroups = null,
}: {
  mode: "add" | "edit";
  initial?: PackageFormInitial;
  /** Every Service Plan the site has. A package cannot exist without one. */
  plans: PlanOption[];
  busy?: boolean;
  onSave: (v: PackageFormValue) => void | Promise<void>;
  onCancel?: () => void;
  /** GET /modules: which acquisition methods this site can offer. null while loading; "error" fails closed. */
  modules?: ModulesReport | "error" | null;
  /** The PMS interfaces a room charge can be mapped to. null while loading; "error" when they cannot be listed. */
  roomChargeInterfaces?: RoomChargeInterface[] | "error" | null;
  /** GET /client-groups: the groups an audience can name. null while loading; "error" when they cannot be read. */
  clientGroups?: ClientGroupOption[] | "error" | null;
}) {
  // HOSPITALITY decides whether hotel conditions and the per-night allowance are offered at all.
  const hotel = moduleLicensedIn("hospitality", modules ?? null);
  const [code, setCode] = useState(initial?.code ?? "");
  const [name, setName] = useState(initial?.name ?? "");
  const [rules, setRules] = useState<EligibilityRuleForm[]>(initial?.rules ?? []);
  // A package with no grant tier is offered to nobody, so a new one starts with the single open tier that
  // means "everyone who is eligible". The operator never has to know that.
  const [tiers, setTiers] = useState<GrantTierForm[]>(initial?.tiers?.length ? initial.tiers : [{ order: 10 }]);
  const [duration, setDuration] = useState<DurationForm>(initial?.duration ?? { end_mode: "MANUAL_END" });
  // The data allowance. FIXED is the default and means "whatever the service plan says", which is what every
  // package did before this existed. It is loaded from the package rather than reset, so saving an unrelated
  // change cannot quietly turn a per-night package back into a flat one.
  const [alloc, setAlloc] = useState<AllocationForm>(
    initial?.allocation ?? { mode: "FIXED", gb_per_night: "", min_gb: "", max_gb: "" });
  // PRICE AND HOW CLIENTS GET IT. Loaded on Edit for the same reason as the allowance: a save republishes
  // everything, so a rename must not quietly make a priced package free or drop its room-charge posting codes.
  // A new package starts Free, which is what the server publishes when these fields are absent.
  const [acq, setAcq] = useState<AcquisitionForm>(initial?.acquisition ?? newAcquisitionForm());
  const [visFrom, setVisFrom] = useState(initial?.visibleFrom ?? "");
  const [visUntil, setVisUntil] = useState(initial?.visibleUntil ?? "");
  const [durationHours, setDurationHours] = useState(
    initial?.duration?.end_mode === "VALIDITY_WINDOW" && initial.duration.duration_seconds
      ? String(Number(initial.duration.duration_seconds) / 3600)
      : "");

  // WHICH SERVICE PLAN. On Edit this starts at the plan the package already uses.
  const [planID, setPlanID] = useState(initial?.planID ?? "");
  const [advanced, setAdvanced] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // The section headings are the visible names of the control each one introduces; these ids bind them.
  const ids = useId();
  const planHeadingID = `${ids}-plan`;
  const endHeadingID = `${ids}-end`;
  const allocHeadingID = `${ids}-alloc`;
  const stepsHeadingID = `${ids}-steps`;

  const selected = plans.find((p) => p.plan_id === planID);
  // Recomputed on every render, so it follows the plan, the mode and the three numbers immediately.
  const notice = allowanceNotice(alloc, selected?.data_quota_bytes, !!selected);

  function submit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    if (!planID || !selected) {
      // A package with no service plan grants nothing, so this is refused here rather than discovered by a
      // guest. The dropdown is required; this covers the case where the only plan was removed mid-edit.
      setError("Choose a service plan. It decides the speed and allowances this package gives.");
      return;
    }
    const res = buildPublishPayload({
      code, name,
      // The caller replaces this with the revision it resolves from the selected plan.
      service_plan_revision_id: selected.current_revision_id || "pending",
      rules, tiers, duration,
      visible_from: visFrom || undefined, visible_until: visUntil || undefined,
    });
    if (res.error || !res.payload) { setError(res.error ?? "Please check the form"); return; }
    const allocErr = validateAllocation(alloc);
    if (allocErr) { setError(allocErr); return; }
    const policy = serializeAllocation(alloc);
    if (policy) res.payload.data_allocation_policy = policy;
    const acqRes = buildAcquisition(acq);
    if (acqRes.error || !acqRes.fields) { setError(acqRes.error ?? "Please check the price section"); return; }
    Object.assign(res.payload, acqRes.fields);
    onSave({ payload: res.payload, selectedPlanID: planID });
  }

  const setRule = (i: number, patch: Partial<EligibilityRuleForm>) =>
    setRules((rs) => rs.map((r, j) => (j === i ? ({ ...r, ...patch } as EligibilityRuleForm) : r)));
  const setTier = (i: number, patch: Partial<GrantTierForm>) =>
    setTiers((ts) => ts.map((t, j) => (j === i ? { ...t, ...patch } : t)));

  // THE AUDIENCE AND THE FREE ALLOWANCE ARE RULES LIKE ANY OTHER -- one CLIENT_GROUP rule and one
  // forbids-prior PRIOR_PURCHASE rule in the same list the generic rows edit. Each dedicated control finds its
  // rule in that list and edits it in place, so a save republishes exactly one set of conditions and nothing
  // can be stated twice. The generic rows below skip the two rules these controls own.
  const audienceRule = rules.find((r): r is AudienceRule => r.type === "CLIENT_GROUP");
  // The choice is held as state rather than derived from the rule: "These groups and public clients" with no
  // group ticked yet is the same rule as "Public clients only", and deriving it would snap the picker shut the
  // moment the operator chose it.
  const [audience, setAudienceChoice] = useState<Audience>(() => audienceOf(audienceRule));
  const setAudienceRule = (next: AudienceRule | null) =>
    setRules((rs) => {
      const rest = rs.filter((r) => r.type !== "CLIENT_GROUP");
      return next ? [next, ...rest] : rest;
    });
  const setAudience = (v: Audience) => {
    setAudienceChoice(v);
    const ids = audienceRule?.group_ids ?? [];
    if (v === "everyone") setAudienceRule(null);
    else if (v === "public") setAudienceRule({ type: "CLIENT_GROUP", group_ids: [], public: true });
    else setAudienceRule({ type: "CLIENT_GROUP", group_ids: ids, public: v === "groups_public" });
  };
  const toggleGroup = (id: string, on: boolean) => {
    const cur = audienceRule ?? { type: "CLIENT_GROUP" as const, group_ids: [], public: false };
    const ids = on ? Array.from(new Set([...cur.group_ids, id])) : cur.group_ids.filter((x) => x !== id);
    setAudienceRule({ ...cur, group_ids: ids });
  };

  const freeRule = rules.find((r): r is FreeRule => r.type === "PRIOR_PURCHASE" && r.mode === "forbids_prior");
  const freeAccess = freeAccessOf(freeRule);
  const paid = isPaidPrice(acq.price, acq.currency, acq.currency_exponent);
  const setFreeRule = (next: FreeRule | null) =>
    setRules((rs) => {
      const rest = rs.filter((r) => !(r.type === "PRIOR_PURCHASE" && r.mode === "forbids_prior"));
      return next ? [...rest.filter((r) => r.type === "CLIENT_GROUP"), next, ...rest.filter((r) => r.type !== "CLIENT_GROUP")] : rest;
    });
  const setFreeAccess = (v: FreeAccess) => {
    if (v === "unlimited") { setFreeRule(null); return; }
    // A rule created here counts the device too on a Free package (the contract's default), and not on a
    // priced one, where a second purchase from the same phone is a sale rather than a loophole.
    const base: FreeRule = freeRule ?? { type: "PRIOR_PURCHASE", mode: "forbids_prior", also_by_device: !paid };
    if (v === "once") { const { within_hours: _drop, ...rest } = base; setFreeRule(rest); }
    else setFreeRule({ ...base, within_hours: base.within_hours && base.within_hours !== "" ? base.within_hours : "24" });
  };
  // Which groups the picker lists: every group the site has, plus any id the stored rule names that the list
  // does not (a group removed since), so it can still be unticked rather than silently kept.
  const groupOptions: ClientGroupOption[] = Array.isArray(clientGroups) ? clientGroups : [];
  const unknownGroupIDs = (audienceRule?.group_ids ?? []).filter((id) => !groupOptions.some((g) => g.id === id));


  return (
    <form onSubmit={submit} className="space-y-5" data-testid="package-form">
      {error && <div role="alert" className="text-sm text-destructive">{error}</div>}

      <div className="grid gap-3 sm:grid-cols-2">
        {/* THE VISIBLE LABELS ARE THE NAMES. These controls used to carry aria-labels such as "code" and
            "service-plan" -- test hooks that overrode the words on screen, so a screen reader announced
            "service-plan, combo box". The hooks survive as data-testid; the names are the labels the operator reads. */}
        <Field label="Name" hint="What the client sees on the portal.">
          <Input data-testid="name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Free WiFi" />
        </Field>
        <Field
          label="Short code"
          hint={mode === "edit" ? "The code identifies this package and cannot be changed." : "A short identifier. It cannot be changed later."}
        >
          <Input data-testid="code" value={code} onChange={(e) => setCode(e.target.value)}
            readOnly={mode === "edit"} placeholder="FREEWIFI" />
        </Field>
      </div>

      <div>
        <h3 id={planHeadingID} className="text-sm font-medium mb-2">Service plan</h3>
        {plans.length === 0 ? (
          // A PACKAGE CANNOT BE CREATED WITHOUT ONE, so this says what to do rather than presenting an empty
          // dropdown that looks like a loading state.
          <div className="text-sm rounded-md border border-border bg-surface px-3 py-2" role="status">
            There are no service plans yet. A service plan defines the speed, allowances and device limit a
            package hands out, so one has to exist first.{" "}
            <a href="/service-plans" className="underline">Create a service plan</a>, then come back.
          </div>
        ) : (
          <>
            <Select data-testid="service-plan" aria-labelledby={planHeadingID} required value={planID}
              onChange={(e) => setPlanID(e.target.value)}>
              <option value="">Choose a service plan…</option>
              {plans.filter((p) => p.current_revision_id).map((p) => (
                <option key={p.plan_id} value={p.plan_id}>{p.name || p.code}</option>
              ))}
            </Select>
            {/* WHAT IT GRANTS, READ-ONLY. These belong to the plan and are edited on the Service Plans
                screen; showing them here is context for the choice, not a second place to change them. */}
            <p className="text-xs text-muted mt-2" data-testid="plan-summary">
              {selected
                ? planSummary(selected)
                : "Choose a plan to see the speed and allowances it gives."}
            </p>
            <p className="text-xs text-muted mt-1">
              <a href="/service-plans" className="underline">Change speed and limits on Service plans</a>.
            </p>
          </>
        )}
      </div>

      <div>
        <h3 id={endHeadingID} className="text-sm font-medium mb-2">How long access lasts</h3>
        <div className="grid gap-3 sm:grid-cols-2">
          <div>
            <Select data-testid="end-mode" aria-labelledby={endHeadingID}
              value={duration.end_mode} onChange={(e) => setDuration({ end_mode: e.target.value as DurationForm["end_mode"] })}>
              {SUPPORTED_END_MODES.map((m) => <option key={m} value={m}>{END_MODE_LABELS[m]}</option>)}
            </Select>
          </div>
          {duration.end_mode === "VALIDITY_WINDOW" && (
            <Field label="Length of access (hours)">
              <Input data-testid="duration-hours" type="number" min={0} step="0.5" value={durationHours}
                onChange={(e) => {
                  setDurationHours(e.target.value);
                  const secs = durationToSeconds(e.target.value, "hours");
                  setDuration((d) => ({ ...d, duration_seconds: secs ?? "" }));
                }} />
            </Field>
          )}
          {duration.end_mode === "FIXED_AT" && (
            <Field label="Ends at">
              <Input data-testid="ends-at" type="datetime-local" value={duration.ends_at ?? ""}
                onChange={(e) => setDuration((d) => ({ ...d, ends_at: e.target.value }))} />
            </Field>
          )}
        </div>
      </div>

      {/* THE DATA ALLOWANCE.
          A flat allowance treats a two-night guest and a three-week guest identically: generous to one and
          exhausted on day four for the other. Per-night scales it with the stay, between a floor and a
          ceiling, and the preview below is there because the clamp order is not obvious from three boxes —
          and the revision this publishes cannot be edited afterwards. */}
      <div>
        <h3 id={allocHeadingID} className="mb-1.5 text-sm font-semibold">Data allowance</h3>
        <Select data-testid="allocation-mode" aria-labelledby={allocHeadingID}
          value={alloc.mode}
          onChange={(e) => setAlloc((a) => ({ ...a, mode: e.target.value as AllocationForm["mode"] }))}>
          {(Object.keys(ALLOCATION_MODE_LABELS) as (keyof typeof ALLOCATION_MODE_LABELS)[])
            // A per-night allowance needs a stay to count nights of: offered with Hospitality, and kept for a
            // stored package that already uses it.
            .filter((m) => m !== "PER_STAY_NIGHT" || hotel || alloc.mode === "PER_STAY_NIGHT")
            .map((m) => (
              <option key={m} value={m}>{ALLOCATION_MODE_LABELS[m]}</option>
            ))}
        </Select>
        {alloc.mode === "PER_STAY_NIGHT" && (
          <div className="mt-2 space-y-2">
            <div className="grid gap-2 sm:grid-cols-3">
              <Field label="GB per night">
                <Input data-testid="gb-per-night" type="number" min={0} step="0.1" value={alloc.gb_per_night}
                  onChange={(e) => setAlloc((a) => ({ ...a, gb_per_night: e.target.value }))} />
              </Field>
              <Field label="Minimum GB">
                <Input data-testid="min-gb" type="number" min={0} step="0.1" placeholder="none" value={alloc.min_gb}
                  onChange={(e) => setAlloc((a) => ({ ...a, min_gb: e.target.value }))} />
              </Field>
              <Field label="Maximum GB">
                <Input data-testid="max-gb" type="number" min={0} step="0.1" placeholder="no cap" value={alloc.max_gb}
                  onChange={(e) => setAlloc((a) => ({ ...a, max_gb: e.target.value }))} />
              </Field>
            </div>
            {previewAllocation(alloc, [2, 5, 8, 12, 25]).length > 0 && (
              <div className="text-xs text-muted-foreground" data-testid="allocation-preview">
                {previewAllocation(alloc, [2, 5, 8, 12, 25])
                  .map((r) => `${r.nights} nights → ${r.gb} GB`).join(" · ")}
              </div>
            )}
            <p className="text-xs text-muted-foreground">
              Clients who did not sign in with their room are not offered this package.
            </p>
          </div>
        )}

        {/* WHICH ALLOWANCE ACTUALLY APPLIES.
            Two numbers are visible at once -- the plan's flat quota and the package's per-night one -- and
            nothing said which a guest receives. Informational only: it never blocks Save, because both
            configurations are legitimate. */}
        {notice && (
          <div
            data-testid="allowance-notice"
            role="note"
            className={
              notice.emphasis
                ? "mt-2 text-sm rounded-md border border-border bg-surface px-3 py-2 space-y-1"
                : "mt-2 text-xs text-muted"
            }
          >
            {notice.kind === "FIXED_USES_PLAN" && (
              <p>This package uses the service plan&rsquo;s {notice.planQuotaGB} GB data allowance.</p>
            )}
            {notice.kind === "FIXED_PLAN_HAS_NO_QUOTA" && (
              <p>
                The selected service plan has no data-usage quota, so this package does not impose a
                data-volume limit.
              </p>
            )}
            {notice.kind === "PER_STAY_NIGHT_PLAN_HAS_NO_QUOTA" && (
              <p>
                The selected service plan sets no data allowance of its own, so this package&rsquo;s{" "}
                {notice.gbPerNight} GB per stay night is the client&rsquo;s data allowance for this package.
              </p>
            )}
            {notice.kind === "PER_STAY_NIGHT_OVERRIDES_PLAN" && (
              <>
                <p>Service plan allowance: <strong>{notice.planQuotaGB} GB</strong></p>
                <p>
                  This package gives <strong>{notice.gbPerNight} GB per stay night</strong> instead.
                  {notice.minGB !== undefined && <> Minimum {notice.minGB} GB.</>}
                  {notice.maxGB !== undefined && <> Maximum {notice.maxGB} GB.</>}
                </p>
                {notice.exampleGB !== undefined && (
                  <p data-testid="allowance-example">
                    For a stay of {notice.exampleNights} nights, the client receives{" "}
                    <strong>{notice.exampleGB} GB</strong>.
                  </p>
                )}
                <p className="text-muted-foreground">
                  For clients receiving this package, the stay-based allowance takes precedence over the
                  service plan allowance. The service plan itself is unchanged, and its {notice.planQuotaGB} GB
                  still applies to other packages that use the plan&rsquo;s own allowance.
                </p>
              </>
            )}
          </div>
        )}
      </div>

      <AcquisitionSection value={acq} onChange={setAcq} modules={modules} interfaces={roomChargeInterfaces} />

      {/* THE AUDIENCE. Who a package is for is a question about the CLIENT -- employees, partners, the public --
          not about how they signed in. A site that wants "employees free, everyone else paid" says so here
          with a Client group; the sign-in method never decides the offer (ONEGATE_CLIENT_IDENTITY_AND_ACCESS_POLICY.md §4). */}
      <div>
        <h3 id={`${ids}-audience`} className="mb-1.5 text-sm font-semibold">Audience</h3>
        <Select data-testid="audience" aria-labelledby={`${ids}-audience`} value={audience}
          onChange={(e) => setAudience(e.target.value as Audience)}>
          <option value="everyone">Everyone</option>
          <option value="groups">Only these groups</option>
          <option value="public">Public clients only</option>
          <option value="groups_public">These groups and public clients</option>
        </Select>
        <p className="mt-1.5 text-xs text-muted-foreground">
          {audience === "everyone" && "Offered to every client who is otherwise eligible."}
          {audience === "groups" && "Offered only to clients who are in one of the groups ticked below."}
          {audience === "public" && "Offered only to clients who are in no client group at all — the public, as opposed to employees or partners."}
          {audience === "groups_public" && "Offered to the groups ticked below and to clients who are in no group; other groups are not offered it."}
        </p>
        {(audience === "groups" || audience === "groups_public") && (
          <div className="mt-2 space-y-1.5" role="group" aria-label="Client groups in the audience">
            {clientGroups === null && <p className="text-xs text-muted-foreground">Loading client groups…</p>}
            {clientGroups === "error" && (
              <p className="text-xs text-warning-subtle-foreground" role="status">
                The client groups could not be read. The groups already chosen are kept; try again to change them.
              </p>
            )}
            {Array.isArray(clientGroups) && clientGroups.length === 0 && unknownGroupIDs.length === 0 && (
              <p className="text-xs text-muted-foreground" role="status">
                This site has no client groups yet.{" "}
                <a href="/client-groups" className="underline">Create one under Client access → Client groups</a>, then come back.
              </p>
            )}
            {groupOptions.map((g) => {
              const on = (audienceRule?.group_ids ?? []).includes(g.id);
              return (
                <label key={g.id} className="flex cursor-pointer items-center gap-2 text-sm">
                  <input type="checkbox" className="size-4 accent-primary" data-testid={`audience-group-${g.id}`}
                    checked={on} onChange={(e) => toggleGroup(g.id, e.target.checked)} />
                  <span>{g.name}</span>
                  {g.enabled === false && <span className="text-xs text-muted-foreground">(switched off — matches nobody)</span>}
                </label>
              );
            })}
            {unknownGroupIDs.map((id) => (
              <label key={id} className="flex cursor-pointer items-center gap-2 text-sm">
                <input type="checkbox" className="size-4 accent-primary" data-testid={`audience-group-${id}`}
                  checked onChange={() => toggleGroup(id, false)} />
                <span className="font-mono text-xs">{id}</span>
                <span className="text-xs text-muted-foreground">(group no longer exists)</span>
              </label>
            ))}
          </div>
        )}
      </div>

      {/* FREE ACCESS. The free allowance is the forbids-prior rule, keyed on the package rather than the
          revision so a wording change does not hand everyone a fresh allowance (§5). It is offered on a priced
          package too -- "one discounted purchase per client" is a real offer -- but it is Free packages that
          need it, which is why the device switch defaults ON only for those. */}
      <div>
        <h3 id={`${ids}-free`} className="mb-1.5 text-sm font-semibold">Free access</h3>
        <div className="grid gap-3 sm:grid-cols-2">
          <Select data-testid="free-access" aria-labelledby={`${ids}-free`} value={freeAccess}
            onChange={(e) => setFreeAccess(e.target.value as FreeAccess)}>
            <option value="unlimited">No limit — a client may get this package again</option>
            <option value="once">Once per client, ever</option>
            <option value="every">Once per client every N hours</option>
          </Select>
          {freeAccess === "every" && freeRule && (
            <Field label="Every (hours)" hint={`Whole hours, ${WITHIN_HOURS_MIN}–${WITHIN_HOURS_MAX} (one year). Default 24.`}>
              <Input data-testid="free-access-hours" type="number" min={WITHIN_HOURS_MIN} max={WITHIN_HOURS_MAX} step={1}
                value={freeRule.within_hours ?? ""}
                onChange={(e) => setFreeRule({ ...freeRule, within_hours: e.target.value })} />
            </Field>
          )}
        </div>
        {freeRule && (
          <div className="mt-2 flex items-start justify-between gap-3 rounded-md border border-border bg-surface/50 px-3.5 py-2.5">
            <div>
              <div className="text-sm font-medium">Count the device too</div>
              <div className="text-xs text-muted-foreground">
                Prevents a second free allowance by signing in with another email on the same phone. Turn it off
                for a shared device, such as a family tablet.
              </div>
            </div>
            <Switch
              checked={freeRule.also_by_device ?? true}
              onCheckedChange={(v) => setFreeRule({ ...freeRule, also_by_device: v })}
              label="Count the device too"
            />
          </div>
        )}
        {freeRule && paid && (
          <p className="mt-1.5 text-xs text-muted-foreground" data-testid="free-access-paid-note">
            This package has a price. The limit still applies — a client may buy it the stated number of times.
          </p>
        )}
      </div>

      <div>
        <div className="flex items-center justify-between mb-1">
          <h3 className="text-sm font-medium">More conditions</h3>
          <Button type="button" variant="ghost" onClick={() => setRules((rs) => [...rs, emptyRule("AUTH_METHOD")])}>
            <Plus size={14} /> Add condition
          </Button>
        </div>
        {rules.filter((r) => !isDedicatedRule(r)).length === 0 && <p className="text-xs text-muted-foreground">No further conditions. Add one to narrow the audience by sign-in method, date, network or stay.</p>}
        {/* A PMS condition is only answerable for a guest who signed in through the PMS. Saying so here stops
            the reasonable assumption that adding "VIP guests only" merely narrows the audience — for a voucher
            guest there is no Stay to test, so the package is not offered to them at all. */}
        {rules.some((r) => isPMSRuleType(r.type)) && (
          <p className="text-xs text-muted mb-2" data-testid="pms-rule-note">
            Conditions about the stay only apply to clients who signed in with their room. Clients using a
            voucher or an account are not offered this package while any of them is set.
          </p>
        )}
        {/* A condition row has no room for visible labels, so each control is named for the row it sits in:
            "Condition 1: type", not the test hook "rule-type-0" it used to announce. */}
        {rules.map((r, i) => {
          // The audience and the free allowance have their own controls above; their rules are not rows.
          if (isDedicatedRule(r)) return null;
          const n = `Condition ${rules.slice(0, i).filter((x) => !isDedicatedRule(x)).length + 1}`;
          return (
          <div key={i} className="flex gap-2 items-center mb-2" data-testid={`rule-${i}`}>
            <Select data-testid={`rule-type-${i}`} aria-label={`${n}: type`} className="h-9 w-auto shrink-0 ps-2.5"
              value={r.type} onChange={(e) => setRules((rs) => rs.map((x, j) => (j === i ? emptyRule(e.target.value as RuleType) : x)))}>
              {/* Conditions about a PMS stay only mean something at a hotel, so they are grouped as such. The
                  audience (CLIENT_GROUP) is set by its own control and is not offered as a row. */}
              <optgroup label="General">
                {SUPPORTED_RULE_TYPES.filter((t) => !isPMSRuleType(t) && t !== "CLIENT_GROUP").map((t) => <option key={t} value={t}>{RULE_TYPE_LABELS[t]}</option>)}
              </optgroup>
              {/* Offered only with Hospitality; a stored condition of this kind stays visible so it can be changed. */}
              {(hotel || isPMSRuleType(r.type)) && (
                <optgroup label="Hotel (PMS stay)">
                  {SUPPORTED_RULE_TYPES.filter((t) => isPMSRuleType(t) && (hotel || t === r.type)).map((t) => <option key={t} value={t}>{RULE_TYPE_LABELS[t]}</option>)}
                </optgroup>
              )}
            </Select>
            {r.type === "AUTH_METHOD" && <Input data-testid={`rule-methods-${i}`} aria-label={`${n}: sign-in methods`} placeholder="account, voucher" value={r.methods} onChange={(e) => setRule(i, { methods: e.target.value })} />}
            {r.type === "SUBJECT_KIND" && <Input data-testid={`rule-kinds-${i}`} aria-label={`${n}: client kinds`} placeholder="ACCOUNT, VOUCHER" value={r.kinds} onChange={(e) => setRule(i, { kinds: e.target.value })} />}
            {r.type === "DATE_WINDOW" && <>
              <Input data-testid={`rule-from-${i}`} aria-label={`${n}: from`} type="datetime-local" value={r.from} onChange={(e) => setRule(i, { from: e.target.value })} />
              <Input data-testid={`rule-until-${i}`} aria-label={`${n}: until`} type="datetime-local" value={r.until} onChange={(e) => setRule(i, { until: e.target.value })} />
            </>}
            {r.type === "PRIOR_PURCHASE" && (
              // Only "requires prior" is a row: the other mode is the Free access control above, and a row set
              // to it would simply move there.
              <Select data-testid={`rule-mode-${i}`} aria-label={`${n}: earlier purchase`} className="h-9 w-auto shrink-0 ps-2.5"
                value={r.mode} onChange={(e) => setRule(i, { mode: e.target.value as "requires_prior" | "forbids_prior" })}>
                <option value="requires_prior">Only clients who already had a package here</option>
              </Select>
            )}
            {r.type === "SITE_NETWORK" && <Input data-testid={`rule-networks-${i}`} aria-label={`${n}: client networks`} placeholder="uuid,uuid" value={r.guest_network_ids} onChange={(e) => setRule(i, { guest_network_ids: e.target.value })} />}
            {/* STAY LENGTH. Either bound may be left empty — "8 nights or more" and "up to 7 nights" are both
                real rules — so neither input is required and an empty one is omitted rather than sent as 0. */}
            {r.type === "STAY_LENGTH" && <>
              <Input data-testid={`rule-min-nights-${i}`} aria-label={`${n}: from nights`} type="number" min={0} placeholder="from (nights)"
                value={r.min_nights} onChange={(e) => setRule(i, { min_nights: e.target.value })} />
              <Input data-testid={`rule-max-nights-${i}`} aria-label={`${n}: to nights`} type="number" min={0} placeholder="to (nights)"
                value={r.max_nights} onChange={(e) => setRule(i, { max_nights: e.target.value })} />
            </>}
            {r.type === "ROOM_TYPE" && <Input data-testid={`rule-room-types-${i}`} aria-label={`${n}: room types`} placeholder="DLX, SUITE" value={r.room_types} onChange={(e) => setRule(i, { room_types: e.target.value })} />}
            {r.type === "RATE_PLAN" && <Input data-testid={`rule-rate-plans-${i}`} aria-label={`${n}: rate plans`} placeholder="BAR, CORP" value={r.rate_plans} onChange={(e) => setRule(i, { rate_plans: e.target.value })} />}
            {r.type === "VIP" && (
              <Select data-testid={`rule-vip-${i}`} aria-label={`${n}: VIP status`} className="h-9 w-auto shrink-0 ps-2.5"
                value={r.is_vip} onChange={(e) => setRule(i, { is_vip: e.target.value as "true" | "false" })}>
                <option value="true">VIP guests only</option>
                <option value="false">Non-VIP guests only</option>
              </Select>
            )}
            {r.type === "TRAVEL_AGENT" && <TravelAgentPicker testId={`rule-travel-agents-${i}`} label={`${n}: travel agents`} value={r.travel_agents} onChange={(v) => setRule(i, { travel_agents: v })} />}
            {r.type === "PMS_INTERFACE" && <Input data-testid={`rule-pms-interfaces-${i}`} aria-label={`${n}: PMS interfaces`} placeholder="interface id" value={r.pms_interface_ids} onChange={(e) => setRule(i, { pms_interface_ids: e.target.value })} />}
            <Button type="button" variant="ghost" data-testid={`remove-rule-${i}`} aria-label={`Remove condition ${i + 1}`} onClick={() => setRules((rs) => rs.filter((_, j) => j !== i))}><Trash2 size={14} /></Button>
          </div>
          );
        })}
      </div>

      {/* ADVANCED. Sale window and speed steps are real controls and stay available, but they are not part of
          creating an ordinary package, and putting them in the main flow is what made this form feel like
          configuration rather than administration. */}
      <div>
        <button type="button" className="text-sm text-muted flex items-center gap-1"
          onClick={() => setAdvanced((v) => !v)} aria-expanded={advanced}>
          {advanced ? <ChevronDown size={14} /> : <ChevronRight size={14} />} Advanced
        </button>
        {advanced && (
          <div className="mt-3 space-y-4 border-l-2 border-border pl-3">
            <div className="grid gap-3 sm:grid-cols-2">
              <Field label="Offer from"><Input data-testid="visible-from" type="datetime-local" value={visFrom} onChange={(e) => setVisFrom(e.target.value)} /></Field>
              <Field label="Offer until"><Input data-testid="visible-until" type="datetime-local" value={visUntil} onChange={(e) => setVisUntil(e.target.value)} /></Field>
            </div>
            <div role="group" aria-labelledby={stepsHeadingID}>
              <div className="flex items-center justify-between mb-1">
                <span id={stepsHeadingID} className="block text-label text-foreground">Speed steps (applied in order, kbps)</span>
                <Button type="button" variant="ghost" onClick={() => setTiers((ts) => [...ts, { order: (ts.length + 1) * 10 }])}><Plus size={14} /> Add step</Button>
              </div>
              {tiers.map((t, i) => (
                <div key={i} className="flex gap-2 items-center mb-2" data-testid={`tier-${i}`}>
                  <Input data-testid={`tier-order-${i}`} aria-label={`Step ${i + 1}: order`} type="number" className="w-24" value={String(t.order)} onChange={(e) => setTier(i, { order: e.target.value })} />
                  <Input data-testid={`tier-down-${i}`} aria-label={`Step ${i + 1}: download kbps`} type="number" min={0} placeholder="down kbps" value={String(t.down_kbps ?? "")} onChange={(e) => setTier(i, { down_kbps: e.target.value })} />
                  <Input data-testid={`tier-up-${i}`} aria-label={`Step ${i + 1}: upload kbps`} type="number" min={0} placeholder="up kbps" value={String(t.up_kbps ?? "")} onChange={(e) => setTier(i, { up_kbps: e.target.value })} />
                  <Button type="button" variant="ghost" data-testid={`remove-tier-${i}`} aria-label={`Remove step ${i + 1}`} onClick={() => setTiers((ts) => ts.filter((_, j) => j !== i))}><Trash2 size={14} /></Button>
                </div>
              ))}
              <p className="text-xs text-muted-foreground">
                Leave a single step with no speeds unless you need different speeds for different clients.
              </p>
            </div>
          </div>
        )}
      </div>

      <div className="flex gap-2">
        <Button type="submit" disabled={busy}>{busy ? "Saving…" : mode === "edit" ? "Save changes" : "Add package"}</Button>
        {onCancel && <Button type="button" variant="ghost" onClick={onCancel}>Cancel</Button>}
      </div>
    </form>
  );
}
