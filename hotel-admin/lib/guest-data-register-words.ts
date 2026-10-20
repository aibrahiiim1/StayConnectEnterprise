// Words for the request register (migration 0147). Kept apart from the page so a test can hold them.

export const REGISTER_KIND_WORDS: Record<string, string> = {
  ACCESS: "A copy of their data",
  ERASURE: "Erasure of their data",
  OTHER: "Another data-protection request",
};

// The results a request can be closed with.
export const REGISTER_STATUS_OPTIONS: { value: string; label: string }[] = [
  { value: "ANSWERED", label: "Answered" },
  { value: "PARTLY_REFUSED", label: "Partly refused" },
  { value: "REFUSED", label: "Refused" },
  { value: "WITHDRAWN", label: "Withdrawn by the person" },
];

export const REGISTER_STATUS_LABEL: Record<string, string> = {
  OPEN: "Open",
  ...Object.fromEntries(REGISTER_STATUS_OPTIONS.map((o) => [o.value, o.label])),
};

export const REGISTER_STATUS_TONE: Record<string, "neutral" | "ok" | "warn" | "err"> = {
  OPEN: "warn",
  ANSWERED: "ok",
  PARTLY_REFUSED: "neutral",
  REFUSED: "neutral",
  WITHDRAWN: "neutral",
};

// The ground for what was kept or refused, in words the person can be given.
export const OUTCOME_OPTIONS: { value: string; label: string }[] = [
  { value: "NONE", label: "Nothing kept or refused" },
  { value: "LEGAL_RETENTION", label: "Kept because the law requires it (invoices, payments)" },
  { value: "NOT_IDENTIFIED", label: "The person could not be identified" },
  { value: "SHARED_ACCESS", label: "Shared internet access: it cannot be attributed to one person" },
  { value: "THIRD_PARTY_RIGHTS", label: "It would expose another person’s data" },
  { value: "MANIFESTLY_UNFOUNDED", label: "Manifestly unfounded or excessive" },
  { value: "OTHER", label: "Another ground, explained to the person" },
];

export function dueWords(daysLeft: number | null, overdue: boolean): string {
  if (daysLeft === null) return "";
  if (overdue) return `${Math.abs(daysLeft)} day${Math.abs(daysLeft) === 1 ? "" : "s"} overdue`;
  if (daysLeft === 0) return "due today";
  return `${daysLeft} day${daysLeft === 1 ? "" : "s"} left`;
}
