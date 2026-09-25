// VOUCHER API TYPES AND THE PURE HELPERS THE SCREEN IS BUILT ON.
//
// Kept out of lib/api.ts on purpose (it is shared by every screen) and kept free of React so each rule here --
// what status a card is really in, how a price in minor units is written, what a CSV contains -- is testable
// on its own.

import type { Voucher, VoucherCodeFormat, VoucherReveal } from "@/lib/api";

export type { Voucher, VoucherCodeFormat, VoucherReveal };

/**
 * What a card IS right now, as opposed to what its row says.
 *
 * Nothing writes REDEMPTION_EXPIRED: expiry is enforced at sign-in from the validity window. So a card past
 * its valid-until stays UNUSED in the table forever, and a screen reading `state` alone says "not used yet"
 * and offers to cancel a card no guest can redeem. The server computes this with the authenticator's own
 * [from, until) rule; `effectiveStatus` below is the same rule, used only when a row predates that field.
 */
export type EffectiveStatus = "available" | "not_yet_valid" | "expired" | "redeemed" | "cancelled";

export type VoucherRow = Voucher & {
  effective_state?: EffectiveStatus;
  // Labels edged adds beside the ids. Absent when a lookup could not name the row.
  package_name?: string;
  package_code?: string;
  package_revision_no?: number;
  issued_by_label?: string;
};

export type VoucherListResp = { vouchers?: VoucherRow[]; total?: number };

export type VoucherSummaryFull = {
  unused: number;
  redeemed: number;
  revoked: number;
  redemption_expired: number;
  // Effective counts. Optional so an older server that sends only the stored ones still renders.
  available?: number;
  expired_unused?: number;
  not_yet_valid?: number;
  total?: number;
  issued_last_7_days?: number;
  batches?: number;
};

export type VoucherBatch = {
  batch_id: string;
  package_revision_id: string;
  count: number;
  redeemed: number;
  cancelled: number;
  available: number;
  expired: number;
  not_yet_valid: number;
  /** Stored UNUSED: available + expired + not yet valid. What an "unused only" export returns. */
  unused: number;
  created_at: string;
  issued_by: string | null;
  notes: string | null;
  redemption_valid_from: string | null;
  redemption_valid_until: string | null;
  package_name?: string;
  package_code?: string;
  package_revision_no?: number;
  issued_by_label?: string;
};

export type VoucherBatchesResp = { batches?: VoucherBatch[]; total?: number; unbatched_vouchers?: number };

export type Grantable = {
  id: string;
  package_code: string;
  revision_no: number;
  package_type: string;
  name: string;
  price_minor: number;
  currency: string;
  currency_exponent?: number | null;
};

export type KeyGeneration = {
  id: string;
  generation_no: number;
  superseded_at: string | null;
  supersede_reason: string | null;
  vouchers: number;
  unused_vouchers: number;
  active: boolean;
};

export type FormatChange = {
  changed_at: string;
  changed_by: string;
  change_reason: string | null;
  old_code_mode: "numbers" | "mixed" | null;
  old_code_length: number | null;
  new_code_mode: "numbers" | "mixed";
  new_code_length: number;
  new_config_version: number;
};

export type VoucherHistory = {
  voucher_id: string;
  entitlements: {
    activated_at: string | null;
    status: string;
    window_ends_at: string | null;
    terminated_at: string | null;
    terminal_reason: string | null;
  }[];
  redemption_available: boolean;
  cancelled: { at: string; by: string | null; reason: string | null } | null;
  cancellation_available: boolean;
};

/** The one-time response of a print run. It exists only in memory: nothing stores a plaintext code. */
export type IssuedBatch = { batch_id: string; count: number; codes: string[]; package_revision_id?: string };

/** An export's response: the codes of one batch, recovered under the step-up. */
export type ExportedBatch = { batch_id: string; count: number; vouchers: { id: string; code: string }[] };

// ---------------------------------------------------------------------------------------------------------

export function effectiveStatus(v: Pick<VoucherRow, "state" | "redemption_valid_from" | "redemption_valid_until" | "effective_state">, now = Date.now()): EffectiveStatus {
  if (v.effective_state) return v.effective_state;
  if (v.state === "REDEEMED") return "redeemed";
  if (v.state === "REVOKED") return "cancelled";
  if (v.state === "REDEMPTION_EXPIRED") return "expired";
  if (v.redemption_valid_until && Date.parse(v.redemption_valid_until) <= now) return "expired";
  if (v.redemption_valid_from && Date.parse(v.redemption_valid_from) > now) return "not_yet_valid";
  return "available";
}

export const STATUS_WORDS: Record<EffectiveStatus, string> = {
  available: "Available",
  not_yet_valid: "Not yet valid",
  expired: "Expired (never used)",
  redeemed: "Used",
  cancelled: "Cancelled",
};

export const STATUS_TONE: Record<EffectiveStatus, "ok" | "info" | "warn" | "neutral" | "err"> = {
  available: "ok",
  not_yet_valid: "info",
  expired: "warn",
  redeemed: "neutral",
  cancelled: "err",
};

export const STATUS_EXPLAIN: Record<EffectiveStatus, string> = {
  available: "Printed, not used, and inside its validity window: a guest can sign in with it now.",
  not_yet_valid: "Printed and not used, but its validity window has not opened yet. Sign-in refuses it until then.",
  expired:
    "Never used, and its valid-until has passed. Expiry is enforced when a guest tries to sign in, so the card is refused there; the stored record still says unused.",
  redeemed: "A guest has signed in with this card. Ending that guest's access is done from their session, not here.",
  cancelled: "Cancelled by an operator before it was used. It no longer grants access.",
};

/** A card can be cancelled only while it is unused AND still redeemable (or not yet valid). */
export function canCancel(status: EffectiveStatus): boolean {
  return status === "available" || status === "not_yet_valid";
}

/** `•••• AB12` — the display hint, masked. The last four characters match a card in a hand; they are not one. */
export function maskedCode(last4: string): string {
  return `•••• ${last4}`;
}

/**
 * A price in MINOR units, written the way the currency is written. `price_minor` 1500 in EUR is €15.00; in
 * JPY it is ¥1,500. The revision's own exponent wins when it has one; otherwise the currency's convention.
 */
export function formatPrice(minor: number, currency: string, exponent?: number | null): string {
  if (!minor || minor <= 0 || !currency) return "Free";
  let digits = exponent ?? null;
  if (digits == null) {
    try {
      digits = new Intl.NumberFormat("en", { style: "currency", currency }).resolvedOptions().maximumFractionDigits ?? 2;
    } catch {
      digits = 2;
    }
  }
  const major = minor / Math.pow(10, digits);
  try {
    return new Intl.NumberFormat(undefined, {
      style: "currency",
      currency,
      minimumFractionDigits: digits,
      maximumFractionDigits: digits,
    }).format(major);
  } catch {
    return `${major.toFixed(digits)} ${currency}`;
  }
}

/** Human time: "3 days ago", "in 2 hours". The absolute time goes in a tooltip beside it. */
export function relativeTime(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return "—";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "—";
  const diff = t - now;
  const abs = Math.abs(diff);
  const units: [Intl.RelativeTimeFormatUnit, number][] = [
    ["year", 365 * 86_400_000],
    ["month", 30 * 86_400_000],
    ["week", 7 * 86_400_000],
    ["day", 86_400_000],
    ["hour", 3_600_000],
    ["minute", 60_000],
  ];
  if (abs < 60_000) return diff < 0 ? "just now" : "in under a minute";
  const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
  for (const [unit, ms] of units) {
    if (abs >= ms) return rtf.format(Math.round(diff / ms), unit);
  }
  return rtf.format(Math.round(diff / 60_000), "minute");
}

export function absoluteTime(iso: string | null | undefined): string {
  if (!iso) return "";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  return new Date(t).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
}

export function shortDate(iso: string | null | undefined): string {
  if (!iso) return "";
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return "";
  return new Date(t).toLocaleDateString(undefined, { dateStyle: "medium" });
}

/** "Valid 12 Oct – 19 Oct 2026", "Valid until 19 Oct", "No expiry". */
export function validityWords(from: string | null | undefined, until: string | null | undefined): string {
  if (from && until) return `${shortDate(from)} – ${shortDate(until)}`;
  if (until) return `Until ${shortDate(until)}`;
  if (from) return `From ${shortDate(from)}`;
  return "No expiry";
}

/** A short, human batch label: its date plus the first characters of its id. */
export function batchLabel(b: { batch_id: string; created_at?: string | null }): string {
  const d = b.created_at ? shortDate(b.created_at) : "";
  return d ? `${d} · ${b.batch_id.slice(0, 8)}` : b.batch_id.slice(0, 8);
}

/**
 * The CSV of codes the operator holds right now. Built in the browser, never sent anywhere, never stored:
 * the server has already done the only privileged part (issuing, or recovering and recording the export).
 */
export function codesCsv(rows: { code: string; id?: string }[], meta: { package?: string; validity?: string } = {}): string {
  const esc = (s: string) => (/[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s);
  const header = ["code", "package", "validity", ...(rows.some((r) => r.id) ? ["voucher_id"] : [])];
  const lines = rows.map((r) =>
    [r.code, meta.package ?? "", meta.validity ?? "", ...(rows.some((x) => x.id) ? [r.id ?? ""] : [])].map(esc).join(","),
  );
  return [header.join(","), ...lines].join("\n");
}

export function downloadCsv(filename: string, csv: string) {
  const blob = new Blob([csv], { type: "text/csv;charset=utf-8" });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  URL.revokeObjectURL(url);
}

/**
 * An ILLUSTRATIVE code for a format, drawn only from characters the generator actually uses (ambiguous ones
 * such as 0/O, 1/I/L, 5/S are never printed). It is an example of the shape, not a real code.
 */
export function sampleCode(mode: "numbers" | "mixed", length: number): string {
  const src = mode === "numbers" ? "4827396248" : "K7PX4R9MHT";
  return src.slice(0, Math.max(1, Math.min(length, src.length)));
}

export function formatWords(mode: "numbers" | "mixed", length: number): string {
  return `${length} ${mode === "numbers" ? "digits" : "letters and digits"}`;
}

/** The selection an export recorded, in words. `selection` is the JSON the server stored. */
export function selectionWords(selection: string | null): string {
  if (!selection) return "";
  try {
    const s = JSON.parse(selection) as { batch_id?: string; state?: string };
    if (s.batch_id) {
      const st = s.state ? (s.state === "UNUSED" ? " (unused cards)" : ` (${s.state.toLowerCase()})`) : "";
      return `Batch ${s.batch_id.slice(0, 8)}${st}`;
    }
  } catch {
    /* not JSON: shown as nothing rather than as raw text */
  }
  return "";
}
