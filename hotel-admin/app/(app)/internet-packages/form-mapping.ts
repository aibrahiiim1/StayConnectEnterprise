// Shaping the API's CURRENT configuration of a package into the authoring form's shape.
//
// Moved out of the page unchanged. Edit loads the current configuration — including the eligibility rules and
// grant tiers the form does not display prominently — because saving republishes all of it, and anything not
// loaded would be silently dropped.

import type { EligibilityRuleForm, GrantTierForm, DurationForm } from "@/lib/commerce-form";

export type PackageCurrent = {
  package_id: string; code: string; revision_id: string; revision_no: number;
  service_plan_revision_id: string;
  display?: Record<string, unknown> | null;
  duration_policy?: Record<string, unknown> | null;
  data_allocation_policy?: Record<string, unknown> | null;
  eligibility_rules?: { Type?: string; type?: string; Value?: Record<string, unknown>; value?: Record<string, unknown> }[] | null;
  grant_tiers?: { Order?: number; order?: number; Value?: Record<string, unknown>; value?: Record<string, unknown> }[] | null;
  visible_from?: string | null; visible_until?: string | null;
};

// Go marshals these structs with capitalised keys (the fields carry no json tags), so both spellings are
// accepted rather than assuming one. Getting this wrong would drop rules on save.
const pick = <T,>(a: T | undefined, b: T | undefined): T | undefined => (a !== undefined ? a : b);

export function rulesToForm(rules: PackageCurrent["eligibility_rules"]): EligibilityRuleForm[] {
  const out: EligibilityRuleForm[] = [];
  for (const r of rules ?? []) {
    const type = pick(r.type, r.Type);
    const value = (pick(r.value, r.Value) ?? {}) as Record<string, unknown>;
    const list = (k: string) => (Array.isArray(value[k]) ? (value[k] as string[]).join(", ") : "");
    const num = (v: unknown) => (typeof v === "number" && Number.isFinite(v) ? String(v) : "");
    switch (type) {
      case "AUTH_METHOD": out.push({ type: "AUTH_METHOD", methods: list("methods") }); break;
      case "SUBJECT_KIND": out.push({ type: "SUBJECT_KIND", kinds: list("kinds") }); break;
      case "DATE_WINDOW": out.push({
        type: "DATE_WINDOW",
        from: toLocalInput(value.from as string | undefined) ?? "",
        until: toLocalInput(value.until as string | undefined) ?? "",
      }); break;
      case "PRIOR_PURCHASE": out.push({
        type: "PRIOR_PURCHASE", mode: value.requires_prior ? "requires_prior" : "forbids_prior",
      }); break;
      case "SITE_NETWORK": out.push({ type: "SITE_NETWORK", guest_network_ids: list("guest_network_ids") }); break;
      // THE STAY DIMENSIONS. A bound that is absent stays absent: reading a missing max_nights back as "0"
      // would republish "up to zero nights", i.e. a package for nobody.
      case "STAY_LENGTH": out.push({
        type: "STAY_LENGTH",
        min_nights: num(value.min_nights),
        max_nights: num(value.max_nights),
      }); break;
      case "ROOM_TYPE": out.push({ type: "ROOM_TYPE", room_types: list("room_types") }); break;
      case "RATE_PLAN": out.push({ type: "RATE_PLAN", rate_plans: list("rate_plans") }); break;
      case "VIP": out.push({ type: "VIP", is_vip: value.is_vip === false ? "false" : "true" }); break;
      case "TRAVEL_AGENT": out.push({ type: "TRAVEL_AGENT", travel_agents: list("travel_agents") }); break;
      case "PMS_INTERFACE": out.push({
        type: "PMS_INTERFACE", pms_interface_ids: list("pms_interface_ids"),
      }); break;
      default: break; // an unknown/unsupported type is not re-emitted; the form only offers supported ones
    }
  }
  return out;
}

export function tiersToForm(tiers: PackageCurrent["grant_tiers"]): GrantTierForm[] {
  const out: GrantTierForm[] = [];
  for (const t of tiers ?? []) {
    const value = (pick(t.value, t.Value) ?? {}) as Record<string, unknown>;
    out.push({
      order: pick(t.order, t.Order) ?? 10,
      down_kbps: (value.down_kbps as number | undefined) ?? "",
      up_kbps: (value.up_kbps as number | undefined) ?? "",
    });
  }
  return out;
}

export function durationToForm(d: PackageCurrent["duration_policy"]): DurationForm {
  const mode = (d?.end_mode as string) ?? "MANUAL_END";
  if (mode === "VALIDITY_WINDOW") return { end_mode: "VALIDITY_WINDOW", duration_seconds: Number(d?.duration_seconds ?? 0) };
  if (mode === "FIXED_AT") return { end_mode: "FIXED_AT", ends_at: toLocalInput(d?.ends_at as string | undefined) ?? "" };
  return { end_mode: "MANUAL_END" };
}

// datetime-local wants "YYYY-MM-DDTHH:mm" in local time; the API speaks RFC3339.
export function toLocalInput(iso?: string | null): string | undefined {
  if (!iso) return undefined;
  const t = new Date(iso);
  if (!Number.isFinite(t.getTime())) return undefined;
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${t.getFullYear()}-${pad(t.getMonth() + 1)}-${pad(t.getDate())}T${pad(t.getHours())}:${pad(t.getMinutes())}`;
}
