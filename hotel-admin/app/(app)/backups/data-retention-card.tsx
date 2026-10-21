"use client";

// HOW LONG GUEST PERSONAL DATA IS KEPT (migration 0127; GDPR plan C1; Product-Owner decisions D-C1, D-C1a, D-C3).
//
// Eight periods. When one ends, the appliance's hourly run ANONYMISES the data it covers: names, email addresses,
// phone numbers, room and reservation numbers, MAC and IP addresses are replaced or cleared, and nothing that is
// still in use is touched. Records and totals stay, so reports keep working; the person behind them cannot be
// identified any more. Sign-in attempts are the one exception: they are deleted, as before. The bounds and
// defaults come from the appliance, so this form cannot disagree with what the server enforces.

import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { ErrorBanner } from "@/components/ui/error-banner";
import { useToast } from "@/components/ui/toast";
import { formatDate } from "@/lib/utils";
import { useEditFocus } from "@/lib/use-edit-focus";

type Key = "pms_stay_days" | "client_activity_days" | "inactive_identity_days" | "expired_record_days"
  | "audit_log_days" | "sign_in_attempt_days" | "log_days" | "staff_note_days";
type Bound = { min: number; max: number; default: number; unit: string };
type Run = { ran_at: string; category: string; rows_anonymised: number };
type Resp = { settings: Record<Key, number>; bounds: Record<Key, Bound>; runs?: Run[] };

export const RETENTION_ROWS: { key: Key; label: string; what: string }[] = [
  { key: "pms_stay_days", label: "Hotel stay data", what: "Guest names, room and reservation from the PMS, counted from checkout." },
  { key: "client_activity_days", label: "Devices and sessions", what: "MAC and IP addresses of devices and their sessions, counted from when the device was last seen." },
  { key: "inactive_identity_days", label: "Inactive guest identities", what: "Email addresses, phone numbers, social sign-ins and client accounts nobody has used for this long." },
  { key: "expired_record_days", label: "Expired one-time records", what: "Verification codes, sign-in links and similar records, counted from when they expired." },
  { key: "audit_log_days", label: "Audit log details", what: "Addresses and guest details inside audit entries. What happened, when and which staff member did it are kept." },
  { key: "sign_in_attempt_days", label: "Sign-in attempts", what: "What guests typed when signing in. These are deleted, not anonymised." },
  { key: "log_days", label: "System logs", what: "The appliance's technical logs, which contain device addresses and sign-in names. They are deleted. Keep them longer only if your country's law requires connection logs to be kept." },
  { key: "staff_note_days", label: "Staff notes", what: "What staff typed as a note or reason: PMS case decisions, released or replaced devices, revoked post-stay access. The note is removed; what was done, when and by whom is kept." },
];

export function DataRetentionCard({ writable }: { writable: boolean }) {
  const toast = useToast();
  const [data, setData] = useState<Resp | null>(null);
  const [unavailable, setUnavailable] = useState(false);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState<Record<Key, string>>({} as Record<Key, string>);
  const [reason, setReason] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const { triggerRef, panelRef } = useEditFocus(editing);

  const load = useCallback(async () => {
    try {
      const r = await api.get<Resp>("/data-retention/");
      if (r && r.settings && r.bounds && RETENTION_ROWS.every((x) => x.key in r.settings && x.key in r.bounds)) setData(r);
      else setUnavailable(true);
    } catch {
      setUnavailable(true);
    }
  }, []);
  useEffect(() => { void load(); }, [load]);

  if (unavailable) return null;

  const inBounds = (k: Key) => {
    const n = Number(draft[k]);
    const b = data?.bounds[k];
    return !!b && Number.isInteger(n) && n >= b.min && n <= b.max;
  };
  const valid = !!data && RETENTION_ROWS.every((x) => inBounds(x.key)) && reason.trim().length >= 4 && password !== "";

  async function save() {
    setBusy(true); setErr(null);
    try {
      const settings = Object.fromEntries(RETENTION_ROWS.map((x) => [x.key, Number(draft[x.key])]));
      await api.put("/data-retention/", { settings, reason: reason.trim(), password });
      toast.success("Saved. The next hourly run uses the new periods.");
      setEditing(false); setPassword(""); setReason("");
      await load();
    } catch (e) {
      setErr(e instanceof ApiError && e.code === "reauth_required" ? new Error("Your password was not accepted.") : e);
    } finally { setBusy(false); }
  }

  const lastRun = data?.runs?.[0];
  return (
    <Card>
      <CardBody className="space-y-3">
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0 space-y-1">
            <div className="text-emphasis">Guest data kept for</div>
            <p className="text-caption text-muted-foreground">
              When a period ends, the appliance anonymises that data within the hour: the person behind it can no
              longer be identified, while totals and reports keep working. Data still in use, such as a guest who is
              online or a package that is still valid, is never touched. Anonymised data cannot be brought back.
            </p>
            {lastRun && (
              <p className="text-caption text-muted-foreground">
                Last run {formatDate(lastRun.ran_at)}: {lastRun.rows_anonymised} records anonymised.
              </p>
            )}
          </div>
          {writable && !editing && data && (
            <Button ref={triggerRef} size="sm" variant="outline" onClick={() => {
              setDraft(Object.fromEntries(RETENTION_ROWS.map((x) => [x.key, String(data.settings[x.key])])) as Record<Key, string>);
              setEditing(true);
            }}>Change</Button>
          )}
        </div>
        <ErrorBanner err={err} />
        {!editing && data && (
          <dl className="grid gap-x-6 gap-y-2 sm:grid-cols-2">
            {RETENTION_ROWS.map((x) => (
              <div key={x.key} className="min-w-0">
                <dt className="text-sm">{x.label}</dt>
                <dd className="text-caption text-muted-foreground">
                  <span className="text-foreground">{data.settings[x.key]} days</span> · {x.what}
                </dd>
              </div>
            ))}
          </dl>
        )}
        {editing && data && (
          <div ref={panelRef} className="grid gap-3 sm:grid-cols-2">
            {RETENTION_ROWS.map((x) => {
              const b = data.bounds[x.key];
              return (
                <Field key={x.key} label={`${x.label} (days)`}
                  hint={`${b.unit}. ${b.min} to ${b.max}, default ${b.default}.`}
                  error={draft[x.key] !== undefined && !inBounds(x.key) ? `Enter ${b.min} to ${b.max}.` : undefined}>
                  <Input type="number" min={b.min} max={b.max} value={draft[x.key] ?? ""}
                    onChange={(e) => setDraft({ ...draft, [x.key]: e.target.value })} />
                </Field>
              );
            })}
            <Field label="Reason" required>
              <Input value={reason} onChange={(e) => setReason(e.target.value)} maxLength={500} />
            </Field>
            <Field label="Your password" required hint="Confirms the change.">
              <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
            </Field>
            <p className="text-caption text-muted-foreground sm:col-span-2">
              A shorter period anonymises older data on the next hourly run. A longer one cannot restore data that
              was already anonymised.
            </p>
            <div className="flex gap-2 sm:col-span-2">
              <Button size="sm" variant="primary" disabled={busy || !valid} onClick={() => void save()}>{busy ? "Saving…" : "Save"}</Button>
              <Button size="sm" variant="ghost" disabled={busy} onClick={() => setEditing(false)}>Cancel</Button>
            </div>
          </div>
        )}
      </CardBody>
    </Card>
  );
}
