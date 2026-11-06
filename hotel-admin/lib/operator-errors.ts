const NOTHING_CHANGED = "Nothing was changed.";

function failure(e: unknown): { status?: number; code?: string; text: string } {
  if (typeof e === "string") return { text: e.trim() };
  const o = (e ?? {}) as { status?: unknown; code?: unknown; message?: unknown };
  return {
    status: typeof o.status === "number" ? o.status : undefined,
    code: typeof o.code === "string" ? o.code : undefined,
    text: typeof o.message === "string" ? o.message.trim() : "",
  };
}

function isSentence(text: string): boolean {
  return /^[A-Z]/.test(text) && /[.!?]$/.test(text);
}

function withNothingChanged(text: string): string {
  return /nothing/i.test(text) ? text : `${text} ${NOTHING_CHANGED}`;
}

const TEXT_WORDS: [RegExp, string][] = [
  [/email already exists/i, `That email or username is already an operator. ${NOTHING_CHANGED}`],
  [/cannot disable yourself/i, `You cannot disable your own account. ${NOTHING_CHANGED}`],
  [/cannot remove your own site_admin role/i, `You cannot remove your own Site admin role. ${NOTHING_CHANGED}`],
  [/^invalid role$/i, `That role no longer exists. Reload the page and choose another. ${NOTHING_CHANGED}`],
  [/^operator not found$/i, `That operator no longer exists. Reload the page. ${NOTHING_CHANGED}`],
  [/^role assignment not found$/i, `They no longer have that role. Reload the page. ${NOTHING_CHANGED}`],
];

const CODE_WORDS: Record<string, string> = {
  reauth_required: `Your password was not accepted. ${NOTHING_CHANGED}`,
  reauth_unavailable: `Your password could not be checked just now. Try again in a moment. ${NOTHING_CHANGED}`,
  limit_exceeded: `Your licence allows no more operators. Disable one first, or extend the licence. ${NOTHING_CHANGED}`,
  forbidden: `Your role cannot do this. ${NOTHING_CHANGED}`,
};

export function operatorError(e: unknown, fallback: string): string {
  const f = failure(e);
  for (const [pattern, words] of TEXT_WORDS) if (pattern.test(f.text)) return words;
  if (f.code && CODE_WORDS[f.code]) return CODE_WORDS[f.code];
  if (isSentence(f.text)) return withNothingChanged(f.text);
  if (f.status === 429) return `Too many attempts. Wait a minute, then try again. ${NOTHING_CHANGED}`;
  return fallback;
}

export function periodWords(days: number): string {
  if (days > 0 && days % 365 === 0) {
    const years = days / 365;
    return years === 1 ? "1 year" : `${years} years`;
  }
  return days === 1 ? "1 day" : `${days} days`;
}

export function lockoutWords(failures: number, seconds: number): string {
  const tries = failures === 1 ? "1 wrong password locks" : `${failures} wrong passwords lock`;
  const span = seconds < 60
    ? `${seconds} s`
    : `${Math.round(seconds / 60)} min`;
  return `${tries} an account for ${span}`;
}
