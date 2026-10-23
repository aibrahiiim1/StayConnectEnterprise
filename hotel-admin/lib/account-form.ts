// Match the server's character limits.
export const ACCOUNT_NAME_MAX = 120;
export const ACCOUNT_NOTES_MAX = 1000;

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

// Send null for cleared fields because omitting them preserves stored values; self-registered usernames stay server-owned.
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
