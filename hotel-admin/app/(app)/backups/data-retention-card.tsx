"use client";

// When a period ends the hourly run anonymises (sign-in attempts and logs are deleted instead). Bounds and
// defaults come from the appliance so this form cannot disagree with the server.

import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { SectionCard } from "@/components/ui/card";
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
// What a shortening would remove, as the appliance counted it with the retention rules themselves (audit AD-67).
type Impact = { total: number; byCategory: Record<string, number>; message: string };

const CATEGORY_LABELS: Record<string, string> = {
  pms_stays: "Hotel stay data", client_activity: "Devices and sessions", inactive_identities: "Inactive guest identities",
  expired_records: "Expired one-time records", audit_log: "Audit log details", staff_notes: "Staff notes", sign_in_attempts: "Sign-in attempts",
};

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
  const [impact, setImpact] = useState<Impact | null>(null);
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

  // confirm is the number of additional records the appliance showed for a shortening; sending it back is the second confirmation.
  async function save(confirm?: number) {
    setBusy(true); setErr(null);
    try {
      const settings = Object.fromEntries(RETENTION_ROWS.map((x) => [x.key, Number(draft[x.key])]));
      await api.put("/data-retention/", {
        settings, reason: reason.trim(), password, ...(confirm !== undefined ? { confirm_records: confirm } : {}),
      });
      toast.success("Saved. The next hourly run uses the new periods.");
      setEditing(false); setPassword(""); setReason(""); setImpact(null);
      await load();
    } catch (e) {
      if (e instanceof ApiError && e.code === "impact_confirmation_required") {
        setImpact({ total: Number(e.body?.additional_records ?? 0), byCategory: e.body?.additional_by_category ?? {}, message: e.message });
      } else {
        setImpact(null);
        setErr(e instanceof ApiError && e.code === "reauth_required" ? new Error("Your password was not accepted.") : e);
      }
    } finally { setBusy(false); }
  }

  const lastRun = data?.runs?.[0];
  return (
    <SectionCard
      title="Guest data kept for"
      description="When a period ends, that data is anonymised within the hour; totals and reports keep working."
      actions={writable && !editing && data && (
        <Button ref={triggerRef} size="sm" variant="secondary" onClick={() => {
          setDraft(Object.fromEntries(RETENTION_ROWS.map((x) => [x.key, String(data.settings[x.key])])) as Record<Key, string>);
          setEditing(true);
        }}>Change</Button>
      )}
      bodyClassName="space-y-3"
    >
        <p className="text-caption text-muted-foreground">
          Data still in use, such as a guest who is online or a valid package, is never touched. Anonymised data cannot
          be brought back.
          {lastRun && <> Last run {formatDate(lastRun.ran_at)}: {lastRun.rows_anonymised} records anonymised.</>}
        </p>
        <ErrorBanner err={err} />
        {!editing && data && (
          <dl className="grid gap-x-6 gap-y-3 sm:grid-cols-2">
            {RETENTION_ROWS.map((x) => (
              <div key={x.key} className="min-w-0 border-b border-border pb-2">
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
            {impact && (
              <div role="alert" className="rounded-md border border-warning/40 bg-warning/10 p-3 text-sm sm:col-span-2">
                <p className="font-medium">
                  These shorter periods will remove {impact.total} more record{impact.total === 1 ? "" : "s"} on the next hourly run.
                </p>
                <ul className="mt-1 list-disc pl-5 text-caption text-muted-foreground">
                  {Object.entries(impact.byCategory).map(([c, n]) => (
                    <li key={c}>{CATEGORY_LABELS[c] ?? c}: {n}</li>
                  ))}
                </ul>
                <p className="mt-1 text-caption text-muted-foreground">
                  This cannot be undone. System logs are removed by the appliance separately and are not in this count.
                </p>
                <div className="mt-2 flex gap-2">
                  <Button size="sm" variant="danger" disabled={busy} onClick={() => void save(impact.total)}>
                    {busy ? "Saving…" : `Remove ${impact.total} record${impact.total === 1 ? "" : "s"} and save`}
                  </Button>
                  <Button size="sm" variant="ghost" disabled={busy} onClick={() => setImpact(null)}>Go back</Button>
                </div>
              </div>
            )}
            <div className="flex gap-2 sm:col-span-2">
              <Button size="sm" variant="primary" disabled={busy || !valid || impact !== null} onClick={() => void save()}>{busy ? "Saving…" : "Save"}</Button>
              <Button size="sm" variant="ghost" disabled={busy} onClick={() => { setEditing(false); setImpact(null); }}>Cancel</Button>
            </div>
          </div>
        )}
    </SectionCard>
  );
}
