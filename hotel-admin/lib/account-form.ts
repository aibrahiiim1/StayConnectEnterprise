// What the Client accounts create and edit forms send (edged guest_accounts_iamv2_lifecycle.go).

// The free-text bounds edged enforces, in characters.
export const ACCOUNT_NAME_MAX = 120;
export const ACCOUNT_NOTES_MAX = 1000;

// validityWindow reads the two dates and refuses a window that ends before it starts, before anything is sent.
export function validityWindow(form: FormData): { from: string | null; until: string | null; error?: string } {
  const f = (form.get("valid_from") as string) || "";
  const u = (form.get("valid_until") as string) || "";
  const from = f ? new Date(f).toISOString() : null;
  const until = u ? new Date(u).toISOString() : null;
  if (from && until && new Date(until) <= new Date(from)) {
    return { from, until, error: "Valid until must be later than Valid from." };
  }
  return { from, until };
}

// accountEditBody is the PATCH an edit sends. Every optional field is sent: a value sets it, an emptied field is
// null and CLEARS it -- edged keeps the stored value only for a field that is absent, so sending `undefined` for an
// emptied box used to bring the old value back. A self-service account's username is its owner's address and is
// never sent.
export function accountEditBody(form: FormData, selfRegistered: boolean): Record<string, unknown> {
  const text = (k: string) => {
    const v = ((form.get(k) as string) ?? "").trim();
    return v === "" ? null : v;
  };
  const w = validityWindow(form);
  const body: Record<string, unknown> = {
    display_name: text("display_name"),
    notes: text("notes"),
    valid_from: w.from,
    valid_until: w.until,
  };
  if (!selfRegistered) body.username = ((form.get("username") as string) ?? "").trim();
  return body;
}
