"use client";

// Typed publish form for a free, non-PMS commercial-package revision. All serialization/validation lives
// in lib/commerce-form (unit-tested); this file is the React editor. The eligibility rule-type dropdown
// offers ONLY supported Phase-2 types (no PMS), the grant-tier editor is ordered, the duration editor
// offers only capability-enabled end-modes, and there is NO price/settlement field (free-only).

import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Trash2, Plus } from "lucide-react";
import {
  SUPPORTED_RULE_TYPES,
  SUPPORTED_END_MODES,
  buildPublishPayload,
  type EligibilityRuleForm,
  type GrantTierForm,
  type DurationForm,
  type PublishPayload,
  type RuleType,
} from "@/lib/commerce-form";

type PlanOption = { plan_id: string; code: string; current_revision_id: string };

function emptyRule(type: RuleType): EligibilityRuleForm {
  switch (type) {
    case "AUTH_METHOD": return { type, methods: "" };
    case "SUBJECT_KIND": return { type, kinds: "" };
    case "DATE_WINDOW": return { type, from: "", until: "" };
    case "PRIOR_PURCHASE": return { type, mode: "forbids_prior" };
    case "SITE_NETWORK": return { type, guest_network_ids: "" };
  }
}

export function PublishPackageForm({
  plans, busy, onPublish,
}: {
  plans: PlanOption[];
  busy?: boolean;
  onPublish: (payload: PublishPayload) => void | Promise<void>;
}) {
  const [code, setCode] = useState("");
  const [name, setName] = useState("");
  const [planRev, setPlanRev] = useState("");
  const [rules, setRules] = useState<EligibilityRuleForm[]>([]);
  const [tiers, setTiers] = useState<GrantTierForm[]>([{ order: 10, down_kbps: "" }]);
  const [duration, setDuration] = useState<DurationForm>({ end_mode: "MANUAL_END" });
  const [visFrom, setVisFrom] = useState("");
  const [visUntil, setVisUntil] = useState("");
  const [error, setError] = useState<string | null>(null);

  function submit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    const res = buildPublishPayload({
      code, name, service_plan_revision_id: planRev, rules, tiers, duration,
      visible_from: visFrom || undefined, visible_until: visUntil || undefined,
    });
    if (res.error || !res.payload) { setError(res.error ?? "invalid form"); return; }
    onPublish(res.payload);
  }

  const setRule = (i: number, patch: Partial<EligibilityRuleForm>) =>
    setRules((rs) => rs.map((r, j) => (j === i ? ({ ...r, ...patch } as EligibilityRuleForm) : r)));
  const setTier = (i: number, patch: Partial<GrantTierForm>) =>
    setTiers((ts) => ts.map((t, j) => (j === i ? { ...t, ...patch } : t)));

  return (
    <form onSubmit={submit} className="space-y-4" aria-label="publish-package-form">
      {error && <div role="alert" className="text-sm text-red-500">{error}</div>}
      <div className="grid grid-cols-2 gap-3">
        <div><Label>Code</Label><Input aria-label="code" value={code} onChange={(e) => setCode(e.target.value)} placeholder="FREEWIFI" /></div>
        <div><Label>Display name</Label><Input aria-label="name" value={name} onChange={(e) => setName(e.target.value)} placeholder="Free WiFi" /></div>
        <div className="col-span-2">
          <Label>Service plan</Label>
          <select aria-label="service-plan" className="w-full bg-panel2 border border-border rounded-md px-2 py-2 text-sm" value={planRev} onChange={(e) => setPlanRev(e.target.value)}>
            <option value="">Select a plan…</option>
            {plans.filter((p) => p.current_revision_id).map((p) => (
              <option key={p.plan_id} value={p.current_revision_id}>{p.code} (current revision)</option>
            ))}
          </select>
        </div>
      </div>

      {/* Duration policy — only capability-enabled end modes */}
      <div className="grid grid-cols-2 gap-3">
        <div>
          <Label>End mode</Label>
          <select aria-label="end-mode" className="w-full bg-panel2 border border-border rounded-md px-2 py-2 text-sm"
            value={duration.end_mode} onChange={(e) => setDuration({ end_mode: e.target.value as DurationForm["end_mode"] })}>
            {SUPPORTED_END_MODES.map((m) => <option key={m} value={m}>{m}</option>)}
          </select>
        </div>
        {duration.end_mode === "VALIDITY_WINDOW" && (
          <div><Label>Duration seconds</Label><Input aria-label="duration-seconds" type="number" min={1} value={String(duration.duration_seconds ?? "")} onChange={(e) => setDuration((d) => ({ ...d, duration_seconds: e.target.value }))} /></div>
        )}
        {duration.end_mode === "FIXED_AT" && (
          <div><Label>Ends at</Label><Input aria-label="ends-at" type="datetime-local" value={duration.ends_at ?? ""} onChange={(e) => setDuration((d) => ({ ...d, ends_at: e.target.value }))} /></div>
        )}
      </div>

      {/* Sale window */}
      <div className="grid grid-cols-2 gap-3">
        <div><Label>Visible from</Label><Input aria-label="visible-from" type="datetime-local" value={visFrom} onChange={(e) => setVisFrom(e.target.value)} /></div>
        <div><Label>Visible until</Label><Input aria-label="visible-until" type="datetime-local" value={visUntil} onChange={(e) => setVisUntil(e.target.value)} /></div>
      </div>

      {/* Eligibility rules — typed, supported types only */}
      <div>
        <div className="flex items-center justify-between mb-1">
          <Label>Eligibility rules</Label>
          <Button type="button" variant="ghost" onClick={() => setRules((rs) => [...rs, emptyRule("AUTH_METHOD")])}><Plus size={14} /> Add rule</Button>
        </div>
        {rules.map((r, i) => (
          <div key={i} className="flex gap-2 items-center mb-2" data-testid={`rule-${i}`}>
            <select aria-label={`rule-type-${i}`} className="bg-panel2 border border-border rounded-md px-2 py-1.5 text-sm"
              value={r.type} onChange={(e) => setRules((rs) => rs.map((x, j) => (j === i ? emptyRule(e.target.value as RuleType) : x)))}>
              {SUPPORTED_RULE_TYPES.map((t) => <option key={t} value={t}>{t}</option>)}
            </select>
            {r.type === "AUTH_METHOD" && <Input aria-label={`rule-methods-${i}`} placeholder="account, voucher" value={r.methods} onChange={(e) => setRule(i, { methods: e.target.value })} />}
            {r.type === "SUBJECT_KIND" && <Input aria-label={`rule-kinds-${i}`} placeholder="ACCOUNT, VOUCHER" value={r.kinds} onChange={(e) => setRule(i, { kinds: e.target.value })} />}
            {r.type === "DATE_WINDOW" && <>
              <Input aria-label={`rule-from-${i}`} type="datetime-local" value={r.from} onChange={(e) => setRule(i, { from: e.target.value })} />
              <Input aria-label={`rule-until-${i}`} type="datetime-local" value={r.until} onChange={(e) => setRule(i, { until: e.target.value })} />
            </>}
            {r.type === "PRIOR_PURCHASE" && (
              <select aria-label={`rule-mode-${i}`} className="bg-panel2 border border-border rounded-md px-2 py-1.5 text-sm" value={r.mode} onChange={(e) => setRule(i, { mode: e.target.value as "requires_prior" | "forbids_prior" })}>
                <option value="forbids_prior">forbids prior</option>
                <option value="requires_prior">requires prior</option>
              </select>
            )}
            {r.type === "SITE_NETWORK" && <Input aria-label={`rule-networks-${i}`} placeholder="uuid,uuid" value={r.guest_network_ids} onChange={(e) => setRule(i, { guest_network_ids: e.target.value })} />}
            <Button type="button" variant="ghost" aria-label={`remove-rule-${i}`} onClick={() => setRules((rs) => rs.filter((_, j) => j !== i))}><Trash2 size={14} /></Button>
          </div>
        ))}
      </div>

      {/* Ordered grant tiers */}
      <div>
        <div className="flex items-center justify-between mb-1">
          <Label>Grant tiers (ordered)</Label>
          <Button type="button" variant="ghost" onClick={() => setTiers((ts) => [...ts, { order: (ts.length + 1) * 10, down_kbps: "" }])}><Plus size={14} /> Add tier</Button>
        </div>
        {tiers.map((t, i) => (
          <div key={i} className="flex gap-2 items-center mb-2" data-testid={`tier-${i}`}>
            <Input aria-label={`tier-order-${i}`} type="number" className="w-24" value={String(t.order)} onChange={(e) => setTier(i, { order: e.target.value })} />
            <Input aria-label={`tier-down-${i}`} type="number" min={0} placeholder="down kbps" value={String(t.down_kbps ?? "")} onChange={(e) => setTier(i, { down_kbps: e.target.value })} />
            <Input aria-label={`tier-up-${i}`} type="number" min={0} placeholder="up kbps" value={String(t.up_kbps ?? "")} onChange={(e) => setTier(i, { up_kbps: e.target.value })} />
            <Button type="button" variant="ghost" aria-label={`remove-tier-${i}`} onClick={() => setTiers((ts) => ts.filter((_, j) => j !== i))}><Trash2 size={14} /></Button>
          </div>
        ))}
      </div>

      <div className="text-xs text-muted">This publishes a <strong>free</strong> package revision (price 0, settlement NOT_REQUIRED); paid/PMS settlement is not configurable in Phase 2.</div>
      <Button type="submit" disabled={busy}>{busy ? "Publishing…" : "Publish"}</Button>
    </form>
  );
}
