import { LIVE_REASONS } from "./guest-data-words";

const NOTHING_CHANGED = "Nothing was changed.";

type Failure = { status?: number; code?: string; text: string; live: string[] };

function failure(e: unknown): Failure {
  const o = (e ?? {}) as { status?: unknown; code?: unknown; message?: unknown; body?: { live?: unknown } };
  return {
    status: typeof o.status === "number" ? o.status : undefined,
    code: typeof o.code === "string" ? o.code : undefined,
    text: typeof o.message === "string" ? o.message.trim() : "",
    live: Array.isArray(o.body?.live) ? o.body.live.filter((x): x is string => typeof x === "string") : [],
  };
}

function isSentence(text: string): boolean {
  return /^[A-Z]/.test(text) && /[.!?]$/.test(text);
}

export const REASON_NAMES_GUEST =
  "The reason names the guest or gives a contact, room, reservation or device. It is kept after the guest is erased, so leave those out. Nothing was changed.";

const CODE_WORDS: Record<string, string> = {
  reauth_required: `Your password was not accepted. ${NOTHING_CHANGED}`,
  reauth_unavailable: `Your password could not be checked just now. Try again in a moment. ${NOTHING_CHANGED}`,
  reason_required: `Give a reason of 4 to 500 characters. ${NOTHING_CHANGED}`,
  invalid_reason: `Give a reason of 4 to 500 characters. ${NOTHING_CHANGED}`,
  invalid_search: "Enter 2 to 200 characters to search.",
  invalid_requester: `Say who is asking in 2 to 120 characters. ${NOTHING_CHANGED}`,
  invalid_date: `Give the day the request was received. ${NOTHING_CHANGED}`,
  invalid_note: `Keep the note to 500 characters. ${NOTHING_CHANGED}`,
  not_verified: `Verify the requester before you open a request. ${NOTHING_CHANGED}`,
  confirmation_required: `Type the request reference to confirm. ${NOTHING_CHANGED}`,
  nothing_selected: `Select at least one record. ${NOTHING_CHANGED}`,
  not_found: `That record is no longer on this site. Search again. ${NOTHING_CHANGED}`,
  awaiting_assignment: `This appliance is not activated for a site yet, so this cannot be done. ${NOTHING_CHANGED}`,
  not_available: `This is not available on this appliance just now. ${NOTHING_CHANGED}`,
  forbidden: `Your role cannot do this. ${NOTHING_CHANGED}`,
};

export function guestDataError(e: unknown, fallback: string, lead = "Nothing was done"): string {
  const f = failure(e);
  if (f.code === "guest_data_live" && f.live.length > 0) {
    const reasons = f.live.map((x) => LIVE_REASONS[x] ?? "the guest's records are still in use");
    return `${lead}: ${Array.from(new Set(reasons)).join("; ")}.`;
  }
  if (f.code === "invalid" && /must not name the guest/.test(f.text)) return REASON_NAMES_GUEST;
  if (f.code && CODE_WORDS[f.code]) return CODE_WORDS[f.code];
  if (f.status === 429) return `Too many attempts. Wait a minute, then try again. ${NOTHING_CHANGED}`;
  if (isSentence(f.text)) return /nothing/i.test(f.text) ? f.text : `${f.text} ${NOTHING_CHANGED}`;
  return fallback;
}

const COUNT_WORDS: Record<string, [string, string]> = {
  client_accounts: ["client account", "client accounts"],
  principals: ["sign-in identity", "sign-in identities"],
  stays: ["reservation", "reservations"],
  stay_guests: ["guest on a shared stay", "guests on a shared stay"],
  sessions: ["internet session", "internet sessions"],
  devices: ["device", "devices"],
  shared_devices: ["shared device", "shared devices"],
  devices_seen_by_that_access: ["device seen on that access", "devices seen on that access"],
  sign_in_attempts: ["sign-in attempt", "sign-in attempts"],
  sign_in_passes: ["sign-in pass", "sign-in passes"],
  sign_in_redirects: ["social sign-in redirect", "social sign-in redirects"],
  sign_in_restrictions: ["sign-in restriction", "sign-in restrictions"],
  oauth_states: ["unfinished social sign-in", "unfinished social sign-ins"],
  verification_codes: ["verification code", "verification codes"],
  verification_records: ["verification record", "verification records"],
  audit_entries: ["activity log entry", "activity log entries"],
  activity_log_entries_cleaned: ["activity log entry cleaned", "activity log entries cleaned"],
  exports: ["hand-over", "hand-overs"],
  records_disclosed: ["record handed over", "records handed over"],
};

export function countWords(counts: Record<string, unknown> | null | undefined): string {
  return Object.entries(counts ?? {})
    .filter((entry): entry is [string, number] => typeof entry[1] === "number" && entry[1] > 0)
    .map(([k, n]) => {
      const w = COUNT_WORDS[k];
      return `${n} ${w ? w[n === 1 ? 0 : 1] : k.replace(/_/g, " ")}`;
    })
    .join(", ");
}

const STATUS_WORDS: Record<string, string> = {
  IN_HOUSE: "In-house",
  POST_STAY_ACTIVE: "After Check-out Access",
  NO_SHOW: "No-show",
};

export function statusWords(status: string | null | undefined): string {
  const s = (status ?? "").trim();
  if (!s) return "";
  const known = STATUS_WORDS[s.toUpperCase()];
  if (known) return known;
  const words = s.replace(/_/g, " ").toLowerCase();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

export function formatDay(day: string | null | undefined): string {
  const m = /^(\d{4})-(\d{2})-(\d{2})/.exec(day ?? "");
  if (!m) return day ?? "";
  return new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]))
    .toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" });
}
