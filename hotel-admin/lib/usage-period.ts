// The usage explorer period: list and detail share one [from, to) window (edged resources_usage.go parseWindow).

export const PERIODS = [
  { value: "7", label: "Last 7 days" },
  { value: "30", label: "Last 30 days" },
  { value: "90", label: "Last 90 days" },
  { value: "365", label: "Last 12 months" },
  { value: "custom", label: "Dates…" },
] as const;
export type Period = (typeof PERIODS)[number]["value"];

export function periodQuery(period: Period, fromDay: string, toDay: string, now = new Date()): { qs: string; error?: string } {
  if (period !== "custom") {
    const from = new Date(now.getTime() - Number(period) * 86_400_000);
    return { qs: `from=${encodeURIComponent(from.toISOString())}&to=${encodeURIComponent(now.toISOString())}` };
  }
  if (!fromDay || !toDay) return { qs: "", error: "Choose both dates." };
  const from = new Date(`${fromDay}T00:00:00`);
  const to = new Date(`${toDay}T23:59:59`);
  if (!(to > from)) return { qs: "", error: "The end date must be on or after the start date." };
  return { qs: `from=${encodeURIComponent(from.toISOString())}&to=${encodeURIComponent(to.toISOString())}` };
}
