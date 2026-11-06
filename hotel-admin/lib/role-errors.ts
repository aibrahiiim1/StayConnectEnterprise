const NOTHING_CHANGED = "Nothing was changed.";

const CODE_WORDS: Record<string, string> = {
  reauth_required: `Your password was not accepted. ${NOTHING_CHANGED}`,
  reauth_unavailable: `Your password could not be checked just now. Try again in a moment. ${NOTHING_CHANGED}`,
  not_found: `That role no longer exists. Reload the page. ${NOTHING_CHANGED}`,
  role_in_use: `Staff still hold this role. Take it away from them in Operators first. ${NOTHING_CHANGED}`,
  awaiting_assignment: `This appliance is not activated for a site yet, so roles cannot be changed. ${NOTHING_CHANGED}`,
  forbidden: `Your role cannot do this. ${NOTHING_CHANGED}`,
  bad_request: `Give the role a name and at least one feature. ${NOTHING_CHANGED}`,
};

export function roleError(e: unknown, fallback: string): string {
  const o = (e ?? {}) as { status?: unknown; code?: unknown; message?: unknown };
  const code = typeof o.code === "string" ? o.code : "";
  const text = typeof o.message === "string" ? o.message.trim() : "";
  if (CODE_WORDS[code]) return CODE_WORDS[code];
  if (/^[A-Z]/.test(text) && /[.!?]$/.test(text)) return /nothing/i.test(text) ? text : `${text} ${NOTHING_CHANGED}`;
  if (o.status === 429) return `Too many attempts. Wait a minute, then try again. ${NOTHING_CHANGED}`;
  return fallback;
}
