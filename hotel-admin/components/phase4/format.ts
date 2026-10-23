// Format amounts from API minor units and exponent to avoid assuming a currency scale.

export function money(minor: number | null, currency: string, exponent = 2): string {
  if (minor === null) return "—";
  if (!currency) return String(minor);
  return `${(minor / Math.pow(10, exponent)).toFixed(exponent)} ${currency}`;
}

export function humanize(code: string | null | undefined): string {
  if (!code) return "—";
  const s = code.replace(/_/g, " ").toLowerCase();
  return s.charAt(0).toUpperCase() + s.slice(1);
}
