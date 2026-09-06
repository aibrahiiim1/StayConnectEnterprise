"use client";

// ONE FORM FOR A PACKAGE — what it is called, WHICH SERVICE PLAN it uses, who gets it, how long it lasts.
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

import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Trash2, Plus, ChevronDown, ChevronRight } from "lucide-react";
import { durationToSeconds } from "@/lib/units";
import { planSummary } from "@/lib/package-save";
import {
  SUPPORTED_RULE_TYPES,
  SUPPORTED_END_MODES,
  END_MODE_LABELS,
  RULE_TYPE_LABELS,
  buildPublishPayload,
  type EligibilityRuleForm,
  type GrantTierForm,
  type DurationForm,
  type PublishPayload,
  type RuleType,
} from "@/lib/commerce-form";

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
};

function emptyRule(type: RuleType): EligibilityRuleForm {
  switch (type) {
    case "AUTH_METHOD": return { type, methods: "" };
    case "SUBJECT_KIND": return { type, kinds: "" };
    case "DATE_WINDOW": return { type, from: "", until: "" };
    case "PRIOR_PURCHASE": return { type, mode: "forbids_prior" };
    case "SITE_NETWORK": return { type, guest_network_ids: "" };
  }
}

const num = (v: unknown): number | null => {
  if (v === "" || v === null || v === undefined) return null;
  const n = Number(v);
  return Number.isFinite(n) ? n : null;
};

export function PackageForm({
  mode, initial, plans, busy, onSave, onCancel,
}: {
  mode: "add" | "edit";
  initial?: PackageFormInitial;
  /** Every Service Plan the site has. A package cannot exist without one. */
  plans: PlanOption[];
  busy?: boolean;
  onSave: (v: PackageFormValue) => void | Promise<void>;
  onCancel?: () => void;
}) {
  const [code, setCode] = useState(initial?.code ?? "");
  const [name, setName] = useState(initial?.name ?? "");
  const [rules, setRules] = useState<EligibilityRuleForm[]>(initial?.rules ?? []);
  // A package with no grant tier is offered to nobody, so a new one starts with the single open tier that
  // means "everyone who is eligible". The operator never has to know that.
  const [tiers, setTiers] = useState<GrantTierForm[]>(initial?.tiers?.length ? initial.tiers : [{ order: 10 }]);
  const [duration, setDuration] = useState<DurationForm>(initial?.duration ?? { end_mode: "MANUAL_END" });
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

  const selected = plans.find((p) => p.plan_id === planID);

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
    onSave({ payload: res.payload, selectedPlanID: planID });
  }

  const setRule = (i: number, patch: Partial<EligibilityRuleForm>) =>
    setRules((rs) => rs.map((r, j) => (j === i ? ({ ...r, ...patch } as EligibilityRuleForm) : r)));
  const setTier = (i: number, patch: Partial<GrantTierForm>) =>
    setTiers((ts) => ts.map((t, j) => (j === i ? { ...t, ...patch } : t)));


  return (
    <form onSubmit={submit} className="space-y-5" aria-label="package-form">
      {error && <div role="alert" className="text-sm text-red-500">{error}</div>}

      <div className="grid gap-3 sm:grid-cols-2">
        <div>
          <Label>Name</Label>
          <Input aria-label="name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Free WiFi" />
          <p className="text-xs text-muted mt-1">What the guest sees on the portal.</p>
        </div>
        <div>
          <Label>Short code</Label>
          <Input aria-label="code" value={code} onChange={(e) => setCode(e.target.value)}
            readOnly={mode === "edit"} placeholder="FREEWIFI" />
          <p className="text-xs text-muted mt-1">
            {mode === "edit" ? "The code identifies this package and cannot be changed." : "A short identifier. It cannot be changed later."}
          </p>
        </div>
      </div>

      <div>
        <h3 className="text-sm font-medium mb-2">Service plan</h3>
        {plans.length === 0 ? (
          // A PACKAGE CANNOT BE CREATED WITHOUT ONE, so this says what to do rather than presenting an empty
          // dropdown that looks like a loading state.
          <div className="text-sm rounded-md border border-border bg-panel2 px-3 py-2" role="status">
            There are no service plans yet. A service plan defines the speed, allowances and device limit a
            package hands out, so one has to exist first.{" "}
            <a href="/service-plans" className="underline">Create a service plan</a>, then come back.
          </div>
        ) : (
          <>
            <select aria-label="service-plan" required value={planID}
              onChange={(e) => setPlanID(e.target.value)}
              className="w-full bg-panel2 border border-border rounded-md px-2 py-2 text-sm">
              <option value="">Choose a service plan…</option>
              {plans.filter((p) => p.current_revision_id).map((p) => (
                <option key={p.plan_id} value={p.plan_id}>{p.name || p.code}</option>
              ))}
            </select>
            {/* WHAT IT GRANTS, READ-ONLY. These belong to the plan and are edited on the Service Plans
                screen; showing them here is context for the choice, not a second place to change them. */}
            <p className="text-xs text-muted mt-2" data-testid="plan-summary">
              {selected
                ? planSummary(selected)
                : "Choose a plan to see the speed and allowances it gives."}
            </p>
            <p className="text-xs text-muted mt-1">
              Speed, data, time and device limits are part of the service plan.{" "}
              <a href="/service-plans" className="underline">Change them on Service plans</a>.
            </p>
          </>
        )}
      </div>

      <div>
        <h3 className="text-sm font-medium mb-2">How long access lasts</h3>
        <div className="grid gap-3 sm:grid-cols-2">
          <div>
            <select aria-label="end-mode" className="w-full bg-panel2 border border-border rounded-md px-2 py-2 text-sm"
              value={duration.end_mode} onChange={(e) => setDuration({ end_mode: e.target.value as DurationForm["end_mode"] })}>
              {SUPPORTED_END_MODES.map((m) => <option key={m} value={m}>{END_MODE_LABELS[m]}</option>)}
            </select>
          </div>
          {duration.end_mode === "VALIDITY_WINDOW" && (
            <div>
              <Label>Length of access (hours)</Label>
              <Input aria-label="duration-hours" type="number" min={0} step="0.5" value={durationHours}
                onChange={(e) => {
                  setDurationHours(e.target.value);
                  const secs = durationToSeconds(e.target.value, "hours");
                  setDuration((d) => ({ ...d, duration_seconds: secs ?? "" }));
                }} />
            </div>
          )}
          {duration.end_mode === "FIXED_AT" && (
            <div>
              <Label>Ends at</Label>
              <Input aria-label="ends-at" type="datetime-local" value={duration.ends_at ?? ""}
                onChange={(e) => setDuration((d) => ({ ...d, ends_at: e.target.value }))} />
            </div>
          )}
        </div>
      </div>

      <div>
        <div className="flex items-center justify-between mb-1">
          <h3 className="text-sm font-medium">Who this package is offered to</h3>
          <Button type="button" variant="ghost" onClick={() => setRules((rs) => [...rs, emptyRule("AUTH_METHOD")])}>
            <Plus size={14} /> Add condition
          </Button>
        </div>
        {rules.length === 0 && <p className="text-xs text-muted">Everyone who signs in. Add a condition to narrow it.</p>}
        {rules.map((r, i) => (
          <div key={i} className="flex gap-2 items-center mb-2" data-testid={`rule-${i}`}>
            <select aria-label={`rule-type-${i}`} className="bg-panel2 border border-border rounded-md px-2 py-1.5 text-sm"
              value={r.type} onChange={(e) => setRules((rs) => rs.map((x, j) => (j === i ? emptyRule(e.target.value as RuleType) : x)))}>
              {SUPPORTED_RULE_TYPES.map((t) => <option key={t} value={t}>{RULE_TYPE_LABELS[t]}</option>)}
            </select>
            {r.type === "AUTH_METHOD" && <Input aria-label={`rule-methods-${i}`} placeholder="account, voucher" value={r.methods} onChange={(e) => setRule(i, { methods: e.target.value })} />}
            {r.type === "SUBJECT_KIND" && <Input aria-label={`rule-kinds-${i}`} placeholder="ACCOUNT, VOUCHER" value={r.kinds} onChange={(e) => setRule(i, { kinds: e.target.value })} />}
            {r.type === "DATE_WINDOW" && <>
              <Input aria-label={`rule-from-${i}`} type="datetime-local" value={r.from} onChange={(e) => setRule(i, { from: e.target.value })} />
              <Input aria-label={`rule-until-${i}`} type="datetime-local" value={r.until} onChange={(e) => setRule(i, { until: e.target.value })} />
            </>}
            {r.type === "PRIOR_PURCHASE" && (
              <select aria-label={`rule-mode-${i}`} className="bg-panel2 border border-border rounded-md px-2 py-1.5 text-sm"
                value={r.mode} onChange={(e) => setRule(i, { mode: e.target.value as "requires_prior" | "forbids_prior" })}>
                <option value="forbids_prior">forbids prior</option>
                <option value="requires_prior">requires prior</option>
              </select>
            )}
            {r.type === "SITE_NETWORK" && <Input aria-label={`rule-networks-${i}`} placeholder="uuid,uuid" value={r.guest_network_ids} onChange={(e) => setRule(i, { guest_network_ids: e.target.value })} />}
            <Button type="button" variant="ghost" aria-label={`remove-rule-${i}`} onClick={() => setRules((rs) => rs.filter((_, j) => j !== i))}><Trash2 size={14} /></Button>
          </div>
        ))}
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
              <div><Label>Offer from</Label><Input aria-label="visible-from" type="datetime-local" value={visFrom} onChange={(e) => setVisFrom(e.target.value)} /></div>
              <div><Label>Offer until</Label><Input aria-label="visible-until" type="datetime-local" value={visUntil} onChange={(e) => setVisUntil(e.target.value)} /></div>
            </div>
            <div>
              <div className="flex items-center justify-between mb-1">
                <Label>Speed steps (applied in order, kbps)</Label>
                <Button type="button" variant="ghost" onClick={() => setTiers((ts) => [...ts, { order: (ts.length + 1) * 10 }])}><Plus size={14} /> Add step</Button>
              </div>
              {tiers.map((t, i) => (
                <div key={i} className="flex gap-2 items-center mb-2" data-testid={`tier-${i}`}>
                  <Input aria-label={`tier-order-${i}`} type="number" className="w-24" value={String(t.order)} onChange={(e) => setTier(i, { order: e.target.value })} />
                  <Input aria-label={`tier-down-${i}`} type="number" min={0} placeholder="down kbps" value={String(t.down_kbps ?? "")} onChange={(e) => setTier(i, { down_kbps: e.target.value })} />
                  <Input aria-label={`tier-up-${i}`} type="number" min={0} placeholder="up kbps" value={String(t.up_kbps ?? "")} onChange={(e) => setTier(i, { up_kbps: e.target.value })} />
                  <Button type="button" variant="ghost" aria-label={`remove-tier-${i}`} onClick={() => setTiers((ts) => ts.filter((_, j) => j !== i))}><Trash2 size={14} /></Button>
                </div>
              ))}
              <p className="text-xs text-muted">
                Leave a single step with no speeds unless you need different speeds for different guests.
              </p>
            </div>
          </div>
        )}
      </div>

      <div className="text-xs text-muted">
        This package is <strong>free to the guest</strong>. Selling packages to guests is not enabled on this
        appliance, so there is no price to set here; the package is granted rather than sold.
      </div>

      <div className="flex gap-2">
        <Button type="submit" disabled={busy}>{busy ? "Saving…" : mode === "edit" ? "Save changes" : "Add package"}</Button>
        {onCancel && <Button type="button" variant="ghost" onClick={onCancel}>Cancel</Button>}
      </div>
    </form>
  );
}
