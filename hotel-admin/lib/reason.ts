// ONE RULE FOR EVERY MANDATORY STAFF REASON, mirroring edged's reason_bounds.go: 4 to 500 characters of the
// trimmed text, counted as characters (code points), not bytes. A reason typed in Arabic used to pass here and be
// refused by edged, which counted bytes. data-plane/cmd/edged/reason_bounds_test.go pins the same numbers.

export const REASON_MIN_CHARS = 4;
export const REASON_MAX_CHARS = 500;

/** The number of characters in a reason, as edged counts them. */
export function reasonLength(reason: string): number {
  return [...reason.trim()].length;
}

export function reasonOk(reason: string): boolean {
  const n = reasonLength(reason);
  return n >= REASON_MIN_CHARS && n <= REASON_MAX_CHARS;
}

export const REASON_HINT = `${REASON_MIN_CHARS} to ${REASON_MAX_CHARS} characters. Recorded with your name.`;
