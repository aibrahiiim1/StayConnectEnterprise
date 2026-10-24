"use client";

// A service plan is the technical service (speed, data, time, devices); a package is the guest offer that
// hands out one plan. The operator picks a plan by name; the pinned plan revision is resolved in
// lib/package-save. Plan fields are read-only context here and edited on Service Plans, since other packages
// share the plan.

import { useEffect, useId, useState } from "react";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { Switch } from "@/components/ui/misc";
import { Callout } from "@/components/ui/error-banner";
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
  SPEED_STEPS_REFUSAL,
  tiersArePlain,
  plainTiers,
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
  type PreservedRule,
} from "@/lib/commerce-form";
import { moduleLicensedIn } from "@/lib/commerce-form";
import { cardCurrencyProblem, type CardAccountView } from "@/lib/card-currency";
import { AcquisitionSection } from "./acquisition-section";
import { TravelAgentPicker } from "./travel-agent-picker";

export type PackageFormValue = {
  payload: PublishPayload;
  /** The caller turns the selected plan into the revision that gets pinned. */
  selectedPlanID: string;
};

export type PlanOption = {
  plan_id: string;
  code: string;
  name?: string | null;
  current_revision_id: string;
  down_kbps?: number | null; up_kbps?: number | null;
  data_quota_bytes?: number | null; time_quota_seconds?: number | null;
  max_concurrent_devices?: number | null; speed_allocation?: string | null;
};

export type ClientGroupOption = { id: string; name: string; enabled?: boolean; priority?: number };

// Four choices map to one CLIENT_GROUP rule (or none).
type Audience = "everyone" | "groups" | "public" | "groups_public";
type AudienceRule = Extract<EligibilityRuleForm, { type: "CLIENT_GROUP" }>;
function audienceOf(r: AudienceRule | undefined): Audience {
  if (!r) return "everyone";
  if (r.public) return r.group_ids.length ? "groups_public" : "public";
  return "groups";
}

// No limit (no rule), once ever, or once every N hours.
type FreeAccess = "unlimited" | "once" | "every";
type FreeRule = Extract<EligibilityRuleForm, { type: "PRIOR_PURCHASE" }>;
function freeAccessOf(r: FreeRule | undefined): FreeAccess {
  if (!r) return "unlimited";
  return r.within_hours !== undefined && r.within_hours !== "" ? "every" : "once";
}

export type PackageFormInitial = {
  code: string;
  name: string;
  planID: string;
  planRevisionID: string;
  rules: EligibilityRuleForm[];
  tiers: GrantTierForm[];
  duration: DurationForm;
  visibleFrom?: string;
  visibleUntil?: string;
  /** Absent means the plan's flat allowance. */
  allocation?: AllocationForm;
  /** Price, currency, methods and room-charge mappings, handed back unchanged. */
  acquisition?: AcquisitionForm;
  /** Conditions of a type this form does not edit: shown read-only and republished as loaded. */
  preservedRules?: PreservedRule[];
  /** The edited name is merged into it so nothing else in it is dropped. */
  display?: Record<string, unknown> | null;
};

function emptyRule(type: RuleType): EligibilityRuleForm {
  switch (type) {
    case "AUTH_METHOD": return { type, methods: "" };
    case "SUBJECT_KIND": return { type, kinds: "" };
    case "DATE_WINDOW": return { type, from: "", until: "" };
    // forbids_prior has its own control above the rows.
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

export function PackageForm({
  mode, initial, plans, busy, onSave, onCancel, modules = null, roomChargeInterfaces = null, clientGroups = null,
  cardAccount = null,
}: {
  mode: "add" | "edit";
  initial?: PackageFormInitial;
  plans: PlanOption[];
  busy?: boolean;
  onSave: (v: PackageFormValue) => void | Promise<void>;
  onCancel?: () => void;
  /** null while loading; "error" fails closed. */
  modules?: ModulesReport | "error" | null;
  /** null while loading; "error" when they cannot be listed. */
  roomChargeInterfaces?: RoomChargeInterface[] | "error" | null;
  /** null while loading; "error" when they cannot be read. */
  clientGroups?: ClientGroupOption[] | "error" | null;
  /** The default active payment account; "none" when there is none; null = unknown (no card-currency claim). */
  cardAccount?: CardAccountView | "none" | null;
}) {
  const hotel = moduleLicensedIn("hospitality", modules ?? null);
  const [code, setCode] = useState(initial?.code ?? "");
  const [name, setName] = useState(initial?.name ?? "");
  // A new Free package defaults to "once per client", or it would be handed out again whenever the allowance
  // runs out; an existing package keeps exactly its rules.
  const [rules, setRules] = useState<EligibilityRuleForm[]>(
    initial ? (initial.rules ?? []) : [{ type: "PRIOR_PURCHASE", mode: "forbids_prior", also_by_device: true }],
  );
  // The default follows the price until the operator chooses a limit: once per client when Free, repeatable
  // when priced (a second purchase is a sale).
  const [freeDefaulted, setFreeDefaulted] = useState(!initial);
  // A package with no grant tier is offered to nobody, so a new one starts with the single open tier.
  const [tiers, setTiers] = useState<GrantTierForm[]>(initial?.tiers?.length ? initial.tiers : [{ order: 10 }]);
  const [duration, setDuration] = useState<DurationForm>(initial?.duration ?? { end_mode: "MANUAL_END" });
  // FIXED means the service plan's allowance; loaded from the package so an unrelated save cannot reset it.
  const [alloc, setAlloc] = useState<AllocationForm>(
    initial?.allocation ?? { mode: "FIXED", gb_per_night: "", min_gb: "", max_gb: "" });
  // Loaded on Edit because a save republishes everything: a rename must not make a priced package free.
  const [acq, setAcq] = useState<AcquisitionForm>(initial?.acquisition ?? newAcquisitionForm());
  const [visFrom, setVisFrom] = useState(initial?.visibleFrom ?? "");
  const [visUntil, setVisUntil] = useState(initial?.visibleUntil ?? "");
  const [durationHours, setDurationHours] = useState(
    initial?.duration?.end_mode === "VALIDITY_WINDOW" && initial.duration.duration_seconds
      ? String(Number(initial.duration.duration_seconds) / 3600)
      : "");

  const [planID, setPlanID] = useState(initial?.planID ?? "");
  const [advanced, setAdvanced] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const ids = useId();
  const planHeadingID = `${ids}-plan`;
  const endHeadingID = `${ids}-end`;
  const allocHeadingID = `${ids}-alloc`;

  const selected = plans.find((p) => p.plan_id === planID);
  const notice = allowanceNotice(alloc, selected?.data_quota_bytes, !!selected);

  function submit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    if (!planID || !selected) {
      // A package with no plan grants nothing; covers the only plan being removed mid-edit.
      setError("Choose a service plan. It decides the speed and allowances this package gives.");
      return;
    }
    const res = buildPublishPayload({
      code, name,
      // The caller replaces this with the revision resolved from the selected plan.
      service_plan_revision_id: selected.current_revision_id || "pending",
      rules, tiers, duration,
      visible_from: visFrom || undefined, visible_until: visUntil || undefined,
      preserved_rules: initial?.preservedRules,
      display: initial?.display,
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

  // The audience (CLIENT_GROUP) and free allowance (forbids-prior PRIOR_PURCHASE) are rules in the same list
  // the generic rows edit; their dedicated controls edit them in place so nothing is stated twice, and the
  // generic rows skip them.
  const audienceRule = rules.find((r): r is AudienceRule => r.type === "CLIENT_GROUP");
  // Held as state: "groups and public" with no group ticked is the same rule as "public only", and deriving
  // it would snap the picker shut as it is chosen.
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
  useEffect(() => {
    if (initial || !freeDefaulted) return;
    if (paid && freeRule) setFreeRule(null);
    if (!paid && !freeRule) setFreeRule({ type: "PRIOR_PURCHASE", mode: "forbids_prior", also_by_device: true });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [paid]);
  const setFreeAccess = (v: FreeAccess) => {
    setFreeDefaulted(false);
    if (v === "unlimited") { setFreeRule(null); return; }
    // Counts the device too on a Free package, not on a priced one (a second purchase is a sale).
    const base: FreeRule = freeRule ?? { type: "PRIOR_PURCHASE", mode: "forbids_prior", also_by_device: !paid };
    if (v === "once") { const { within_hours: _drop, ...rest } = base; setFreeRule(rest); }
    else setFreeRule({ ...base, within_hours: base.within_hours && base.within_hours !== "" ? base.within_hours : "24" });
  };
  // Also lists stored ids missing from the site's groups (since removed) so they can still be unticked.
  const groupOptions: ClientGroupOption[] = Array.isArray(clientGroups) ? clientGroups : [];
  const unknownGroupIDs = (audienceRule?.group_ids ?? []).filter((id) => !groupOptions.some((g) => g.id === id));


  return (
    <form onSubmit={submit} className="space-y-6 [&>div+div]:border-t [&>div+div]:border-border [&>div+div]:pt-5" data-testid="package-form">
      {error && <div role="alert" className="text-sm text-destructive">{error}</div>}

      <div className="grid gap-4 sm:grid-cols-2">
        {/* Accessible names are the visible labels; test hooks live in data-testid, not aria-label. */}
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
        <h3 id={planHeadingID} className="mb-1.5 text-label">Service plan</h3>
        {plans.length === 0 ? (
          // Says what to do rather than showing an empty dropdown that looks like loading.
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
            <p className="mt-1.5 text-xs text-muted-foreground" data-testid="plan-summary">
              {selected
                ? planSummary(selected)
                : "Choose a plan to see the speed and allowances it gives."}
            </p>
            <p className="mt-1 text-xs text-muted-foreground">
              <a href="/service-plans" className="underline">Change speed and limits on Service plans</a>.
            </p>
          </>
        )}
      </div>

      <div>
        <h3 id={endHeadingID} className="mb-1.5 text-label">How long access lasts</h3>
        <div className="grid gap-4 sm:grid-cols-2">
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

      {/* The preview exists because the clamp order is not obvious and the published revision is immutable. */}
      <div>
        <h3 id={allocHeadingID} className="mb-1.5 text-label">Data allowance</h3>
        <Select data-testid="allocation-mode" aria-labelledby={allocHeadingID}
          value={alloc.mode}
          onChange={(e) => setAlloc((a) => ({ ...a, mode: e.target.value as AllocationForm["mode"] }))}>
          {(Object.keys(ALLOCATION_MODE_LABELS) as (keyof typeof ALLOCATION_MODE_LABELS)[])
            // Per-night needs a stay: offered with Hospitality, and kept for a package that already uses it.
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
            <p className="text-xs text-muted-foreground" data-testid="per-night-audience-note">
              This allowance is given only to clients who signed in with their room, because it is counted from
              their stay. The package is not offered to clients who signed in with a voucher, an account, an email
              or phone code, a social account or open access, and it cannot be issued as vouchers.
            </p>
            {acq.methods.includes("PREPAID") && (
              <p className="text-xs text-warning-subtle-foreground" role="status" data-testid="per-night-voucher-note">
                Voucher is ticked below, but vouchers cannot be issued for a package with a per-night allowance.
                Untick Voucher, or choose the plan&rsquo;s allowance instead.
              </p>
            )}
          </div>
        )}

        {/* Says which allowance applies; informational only, never blocks Save. */}
        {notice && (
          <div
            data-testid="allowance-notice"
            role="note"
            className={
              notice.emphasis
                ? "mt-2 text-sm rounded-md border border-border bg-surface px-3 py-2 space-y-1"
                : "mt-2 text-xs text-muted-foreground"
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
      {/* Whether card is offered follows scd's rule (lib/card-currency), shown here rather than discovered by a client. */}
      {paid && acq.methods.includes("ONLINE_PAYMENT") && cardCurrencyProblem(cardAccount, acq.currency) && (
        <p className="text-xs text-warning-subtle-foreground" role="status" data-testid="card-currency-note">
          {cardCurrencyProblem(cardAccount, acq.currency)}
        </p>
      )}

      {/* The audience is about the client (employees, partners, public), never the sign-in method. */}
      <div>
        <h3 id={`${ids}-audience`} className="mb-1.5 text-label">Audience</h3>
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

      {/* The free allowance is keyed on the package, not the revision, so a wording change does not reset it.
          It also applies to priced packages, but the device switch defaults ON only for Free ones. */}
      <div>
        <h3 id={`${ids}-free`} className="mb-1.5 text-label">Free access</h3>
        <div className="grid gap-4 sm:grid-cols-2">
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
        {!freeRule && paid && (
          <p className="mt-1.5 text-xs text-muted-foreground" data-testid="free-access-repeatable-note">
            A priced package may be bought again by the same client. Choose a limit above if you want one.
          </p>
        )}
      </div>

      <div>
        <div className="flex items-center justify-between mb-1">
          <h3 className="text-label">More conditions</h3>
          <Button type="button" variant="ghost" onClick={() => setRules((rs) => [...rs, emptyRule("AUTH_METHOD")])}>
            <Plus size={14} /> Add condition
          </Button>
        </div>
        {rules.filter((r) => !isDedicatedRule(r)).length === 0 && <p className="text-xs text-muted-foreground">No further conditions. Add one to narrow the audience by sign-in method, date, network or stay.</p>}
        {/* A voucher guest has no Stay to test, so a PMS condition excludes them entirely rather than narrowing. */}
        {rules.some((r) => isPMSRuleType(r.type)) && (
          <p className="mb-2 text-xs text-muted-foreground" data-testid="pms-rule-note">
            Conditions about the stay only apply to clients who signed in with their room. Clients using a
            voucher or an account are not offered this package while any of them is set.
          </p>
        )}
        {/* Rows have no visible labels, so each control is named for its row ("Condition 1: type"). */}
        {rules.map((r, i) => {
          if (isDedicatedRule(r)) return null;
          const vi = rules.slice(0, i).filter((x) => !isDedicatedRule(x)).length;
          // Numbered by visible row; the dedicated rules are not rows.
          const n = `Condition ${vi + 1}`;
          return (
          <div key={i} className="flex gap-2 items-center mb-2" data-testid={`rule-${vi}`}>
            <Select data-testid={`rule-type-${vi}`} aria-label={`${n}: type`} className="h-9 w-auto shrink-0 ps-2.5"
              value={r.type} onChange={(e) => setRules((rs) => rs.map((x, j) => (j === i ? emptyRule(e.target.value as RuleType) : x)))}>
              <optgroup label="General">
                {SUPPORTED_RULE_TYPES.filter((t) => !isPMSRuleType(t) && t !== "CLIENT_GROUP").map((t) => <option key={t} value={t}>{RULE_TYPE_LABELS[t]}</option>)}
              </optgroup>
              {/* Offered only with Hospitality; a stored condition of this kind stays visible. */}
              {(hotel || isPMSRuleType(r.type)) && (
                <optgroup label="Hotel (PMS stay)">
                  {SUPPORTED_RULE_TYPES.filter((t) => isPMSRuleType(t) && (hotel || t === r.type)).map((t) => <option key={t} value={t}>{RULE_TYPE_LABELS[t]}</option>)}
                </optgroup>
              )}
            </Select>
            {r.type === "AUTH_METHOD" && <Input data-testid={`rule-methods-${vi}`} aria-label={`${n}: sign-in methods`} placeholder="account, voucher" value={r.methods} onChange={(e) => setRule(i, { methods: e.target.value })} />}
            {r.type === "SUBJECT_KIND" && <Input data-testid={`rule-kinds-${vi}`} aria-label={`${n}: client kinds`} placeholder="ACCOUNT, VOUCHER" value={r.kinds} onChange={(e) => setRule(i, { kinds: e.target.value })} />}
            {r.type === "DATE_WINDOW" && <>
              <Input data-testid={`rule-from-${vi}`} aria-label={`${n}: from`} type="datetime-local" value={r.from} onChange={(e) => setRule(i, { from: e.target.value })} />
              <Input data-testid={`rule-until-${vi}`} aria-label={`${n}: until`} type="datetime-local" value={r.until} onChange={(e) => setRule(i, { until: e.target.value })} />
            </>}
            {r.type === "PRIOR_PURCHASE" && (
              // The other mode is the Free access control above.
              <Select data-testid={`rule-mode-${vi}`} aria-label={`${n}: earlier purchase`} className="h-9 w-auto shrink-0 ps-2.5"
                value={r.mode} onChange={(e) => setRule(i, { mode: e.target.value as "requires_prior" | "forbids_prior" })}>
                <option value="requires_prior">Only clients who already had a package here</option>
              </Select>
            )}
            {r.type === "SITE_NETWORK" && <Input data-testid={`rule-networks-${vi}`} aria-label={`${n}: client networks`} placeholder="uuid,uuid" value={r.guest_network_ids} onChange={(e) => setRule(i, { guest_network_ids: e.target.value })} />}
            {/* Either bound may be empty; an empty one is omitted rather than sent as 0. */}
            {r.type === "STAY_LENGTH" && <>
              <Input data-testid={`rule-min-nights-${vi}`} aria-label={`${n}: from nights`} type="number" min={0} placeholder="from (nights)"
                value={r.min_nights} onChange={(e) => setRule(i, { min_nights: e.target.value })} />
              <Input data-testid={`rule-max-nights-${vi}`} aria-label={`${n}: to nights`} type="number" min={0} placeholder="to (nights)"
                value={r.max_nights} onChange={(e) => setRule(i, { max_nights: e.target.value })} />
            </>}
            {r.type === "ROOM_TYPE" && <Input data-testid={`rule-room-types-${vi}`} aria-label={`${n}: room types`} placeholder="DLX, SUITE" value={r.room_types} onChange={(e) => setRule(i, { room_types: e.target.value })} />}
            {r.type === "RATE_PLAN" && <Input data-testid={`rule-rate-plans-${vi}`} aria-label={`${n}: rate plans`} placeholder="BAR, CORP" value={r.rate_plans} onChange={(e) => setRule(i, { rate_plans: e.target.value })} />}
            {r.type === "VIP" && (
              <Select data-testid={`rule-vip-${vi}`} aria-label={`${n}: VIP status`} className="h-9 w-auto shrink-0 ps-2.5"
                value={r.is_vip} onChange={(e) => setRule(i, { is_vip: e.target.value as "true" | "false" })}>
                <option value="true">VIP guests only</option>
                <option value="false">Non-VIP guests only</option>
              </Select>
            )}
            {r.type === "TRAVEL_AGENT" && <TravelAgentPicker testId={`rule-travel-agents-${vi}`} label={`${n}: travel agents`} value={r.travel_agents} onChange={(v) => setRule(i, { travel_agents: v })} />}
            {r.type === "PMS_INTERFACE" && <Input data-testid={`rule-pms-interfaces-${vi}`} aria-label={`${n}: PMS interfaces`} placeholder="interface id" value={r.pms_interface_ids} onChange={(e) => setRule(i, { pms_interface_ids: e.target.value })} />}
            <Button type="button" variant="ghost" data-testid={`remove-rule-${vi}`} aria-label={`Remove condition ${vi + 1}`} onClick={() => setRules((rs) => rs.filter((_, j) => j !== i))}><Trash2 size={14} /></Button>
          </div>
          );
        })}
        {(initial?.preservedRules ?? []).length > 0 && (
          <div className="mt-2 space-y-1" data-testid="preserved-rules">
            {(initial?.preservedRules ?? []).map((r, i) => (
              <div key={i} className="rounded-md border border-border bg-surface/50 px-3 py-1.5 text-xs">
                <span className="font-mono">{r.type}</span>{" "}
                <span className="font-mono text-muted-foreground">{JSON.stringify(r.value)}</span>{" "}
                <span className="text-muted-foreground">— kept as is</span>
              </div>
            ))}
            <p className="text-xs text-muted-foreground">
              These conditions cannot be changed here. They are saved again unchanged.
            </p>
          </div>
        )}
      </div>

      {/* Speed steps are never applied; the appliance refuses to save them, so one click replaces them with the plain step. */}
      {!tiersArePlain(tiers) && (
        <div data-testid="speed-steps-notice">
          <Callout tone="warning" title="Speed steps are not applied">
            <p>{SPEED_STEPS_REFUSAL}</p>
            <Button type="button" variant="secondary" size="sm" className="mt-2" data-testid="remove-speed-steps"
              onClick={() => setTiers((ts) => plainTiers(ts))}>Remove speed steps</Button>
          </Callout>
        </div>
      )}

      <div>
        <button type="button" className="flex items-center gap-1 rounded-md text-label text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          onClick={() => setAdvanced((v) => !v)} aria-expanded={advanced}>
          {advanced ? <ChevronDown size={14} /> : <ChevronRight size={14} />} Advanced
        </button>
        {advanced && (
          <div className="mt-3 space-y-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Offer from"><Input data-testid="visible-from" type="datetime-local" value={visFrom} onChange={(e) => setVisFrom(e.target.value)} /></Field>
              <Field label="Offer until"><Input data-testid="visible-until" type="datetime-local" value={visUntil} onChange={(e) => setVisUntil(e.target.value)} /></Field>
            </div>
          </div>
        )}
      </div>

      <div className="flex flex-wrap justify-end gap-2">
        {onCancel && <Button type="button" variant="secondary" onClick={onCancel}>Cancel</Button>}
        <Button type="submit" disabled={busy}>{busy ? "Saving…" : mode === "edit" ? "Save changes" : "Add package"}</Button>
      </div>
    </form>
  );
}
