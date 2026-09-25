import type { License } from "./api";

export type Tone = "ok" | "warn" | "err" | "info" | "default";

/**
 * The state a license is in, in the words the product uses (handoff §7, "License states").
 *
 * The logic is exactly the one the Licenses page always had — the server's status wins for revoked,
 * superseded and suspended; otherwise the validity window and grace days decide; an unbound license is
 * "Awaiting appliance binding" — only the wording and colour language moved here so the page, its filter
 * chips and the tests share one answer.
 */
export function licenseState(l: License, now: number = Date.now()): { key: string; label: string; tone: Tone } {
  const bound = (l.appliance_ids?.length ?? 0) > 0;
  const vu = new Date(l.valid_until).getTime();
  const graceEnd = vu + (l.grace_period_days || 0) * 86400000;
  if (l.status === "revoked") return { key: "revoked", label: "Revoked", tone: "err" };
  if (l.status === "superseded") return { key: "superseded", label: "Superseded", tone: "default" };
  if (l.status === "suspended") return { key: "suspended", label: "Suspended", tone: "warn" };
  if (now > graceEnd) return { key: "expired", label: "Expired", tone: "err" };
  if (now > vu) return { key: "grace", label: "Grace", tone: "warn" };
  if (!bound) return { key: "awaiting", label: "Awaiting appliance binding", tone: "warn" };
  return { key: "active", label: "Active", tone: "ok" };
}

export function graceEndOf(l: License): Date {
  return new Date(new Date(l.valid_until).getTime() + (l.grace_period_days || 0) * 86400000);
}

/** Sentence-case a machine status ("false_positive" → "False positive"). The word is shown, never only a colour. */
export function statusWord(s?: string | null): string {
  if (!s) return "—";
  const t = s.replace(/[_-]+/g, " ").trim();
  return t.charAt(0).toUpperCase() + t.slice(1);
}
