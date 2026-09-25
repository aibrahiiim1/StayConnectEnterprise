// Shared wording helpers for the four Charges screens. Presentation only: amounts are formatted from the
// minor units and exponent the API sends, and a fixed backend code is turned into words for a badge.

export function money(minor: number | null, currency: string, exponent = 2): string {
  if (minor === null) return "—";
  if (!currency) return String(minor);
  return `${(minor / Math.pow(10, exponent)).toFixed(exponent)} ${currency}`;
}

/** "MANUAL_REVIEW" → "Manual review". For badges; the raw code stays in the data. */
export function humanize(code: string | null | undefined): string {
  if (!code) return "—";
  const s = code.replace(/_/g, " ").toLowerCase();
  return s.charAt(0).toUpperCase() + s.slice(1);
}
