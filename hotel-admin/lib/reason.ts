// Mandatory staff reasons mirror edged's reason_bounds.go: 4 to 500 characters of trimmed text, counted as code points,
// not bytes.

export const REASON_MIN_CHARS = 4;
export const REASON_MAX_CHARS = 500;

export function reasonLength(reason: string): number {
  return [...reason.trim()].length;
}

export function reasonOk(reason: string): boolean {
  const n = reasonLength(reason);
  return n >= REASON_MIN_CHARS && n <= REASON_MAX_CHARS;
}

export const REASON_HINT = `${REASON_MIN_CHARS} to ${REASON_MAX_CHARS} characters. Recorded with your name.`;
