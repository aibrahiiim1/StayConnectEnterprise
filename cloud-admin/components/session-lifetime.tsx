"use client";

import { useEffect, useState } from "react";
import { api, withStepUp } from "@/lib/api";
import { SectionCard } from "@/components/ui/card";
import { Field, FieldGrid, Input, useFieldErrors } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/misc";
import { Button } from "@/components/ui/button";
import { ErrorBanner } from "@/components/ui/error-banner";
import { useToast } from "@/components/ui/toast";
import { formatDateTime } from "@/lib/status";

export type SecuritySettings = {
  operator_session_max_hours: number;
  /** Minutes without activity before sign-out. */
  operator_idle_minutes: number;
  idle_bounds: { default: number; min: number; max: number; unit: string };
  /** Wrong attempts within the lock window that trigger account lockout. */
  signin_lock_failures: number;
  signin_lock_failures_bounds: { default: number; min: number; max: number; unit: string };
  signin_lock_minutes: number;
  signin_lock_minutes_bounds: { default: number; min: number; max: number; unit: string };
  /** Hours an appliance marked for replacement stays licensed until its replacement is activated. */
  replacement_window_hours?: number;
  replacement_window_hours_bounds?: { default: number; min: number; max: number; unit: string };
  /** Minutes Central waits for a retiring appliance's signed acknowledgement before it raises an alert. */
  retirement_ack_minutes?: number;
  retirement_ack_minutes_bounds?: { default: number; min: number; max: number; unit: string };
  default: number;
  min: number;
  max: number;
  unit: string;
  updated_at?: string | null;
  updated_by?: string | null;
};

/** Shorter absolute lifetimes end already-older sessions at their next request. */
export function SessionLifetimeSetting({ canManage }: { canManage: boolean }) {
  const toast = useToast();
  const [s, setS] = useState<SecuritySettings | null>(null);
  const [hours, setHours] = useState("");
  const [idle, setIdle] = useState("");
  const [lockN, setLockN] = useState("");
  const [lockMins, setLockMins] = useState("");
  const [replHours, setReplHours] = useState("");
  const [ackMins, setAckMins] = useState("");
  const [reason, setReason] = useState("");
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const fields = useFieldErrors<"hours" | "idle" | "lockN" | "lockMins" | "replHours" | "ackMins" | "reason" | "unchanged">();

  useEffect(() => {
    api.get<SecuritySettings>("/cloud/v1/settings/security")
      .then((v) => {
        setS(v); setHours(String(v.operator_session_max_hours)); setIdle(String(v.operator_idle_minutes));
        setLockN(String(v.signin_lock_failures)); setLockMins(String(v.signin_lock_minutes));
        setReplHours(v.replacement_window_hours === undefined ? "" : String(v.replacement_window_hours));
        setAckMins(v.retirement_ack_minutes === undefined ? "" : String(v.retirement_ack_minutes));
      })
      .catch(setErr);
  }, []);

  async function save() {
    if (!s) return;
    const fail = (f: Parameters<typeof fields.fail>[0]) => { setErr(null); fields.fail(f); };
    const n = Number(hours);
    if (!/^\d+$/.test(hours.trim()) || n < s.min || n > s.max) {
      fail({ hours: `Enter a whole number of hours from ${s.min} to ${s.max}.` });
      return;
    }
    const m = Number(idle);
    if (!/^\d+$/.test(idle.trim()) || m < s.idle_bounds.min || m > s.idle_bounds.max) {
      fail({ idle: `Enter a whole number of minutes from ${s.idle_bounds.min} to ${s.idle_bounds.max}.` });
      return;
    }
    const ln = Number(lockN), lm = Number(lockMins);
    const lb = s.signin_lock_failures_bounds, mb = s.signin_lock_minutes_bounds;
    if (!/^\d+$/.test(lockN.trim()) || ln < lb.min || ln > lb.max) {
      fail({ lockN: `Enter a whole number of wrong attempts from ${lb.min} to ${lb.max}.` });
      return;
    }
    if (!/^\d+$/.test(lockMins.trim()) || lm < mb.min || lm > mb.max) {
      fail({ lockMins: `Enter a whole number of minutes from ${mb.min} to ${mb.max} for the lock.` });
      return;
    }
    const rb = s.replacement_window_hours_bounds, ab = s.retirement_ack_minutes_bounds;
    if (rb && (!/^\d+$/.test(replHours.trim()) || Number(replHours) < rb.min || Number(replHours) > rb.max)) {
      fail({ replHours: `Enter a whole number of hours from ${rb.min} to ${rb.max}.` });
      return;
    }
    if (ab && (!/^\d+$/.test(ackMins.trim()) || Number(ackMins) < ab.min || Number(ackMins) > ab.max)) {
      fail({ ackMins: `Enter a whole number of minutes from ${ab.min} to ${ab.max}.` });
      return;
    }
    if (!reason.trim()) {
      fail({ reason: "Enter a reason; it is recorded in the audit log." });
      return;
    }
    fields.clear();
    setBusy(true);
    setErr(null);
    try {
      const v = await withStepUp(() =>
        api.patch<SecuritySettings>("/cloud/v1/settings/security", {
          operator_session_max_hours: n, operator_idle_minutes: m, signin_lock_failures: ln, signin_lock_minutes: lm,
          ...(rb ? { replacement_window_hours: Number(replHours) } : {}),
          ...(ab ? { retirement_ack_minutes: Number(ackMins) } : {}),
          reason: reason.trim(),
        }),
      );
      setS(v);
      setHours(String(v.operator_session_max_hours));
      setIdle(String(v.operator_idle_minutes));
      setLockN(String(v.signin_lock_failures));
      setLockMins(String(v.signin_lock_minutes));
      setReplHours(v.replacement_window_hours === undefined ? "" : String(v.replacement_window_hours));
      setAckMins(v.retirement_ack_minutes === undefined ? "" : String(v.retirement_ack_minutes));
      setReason("");
      toast.success("Sign-in limits saved", `${v.operator_idle_minutes} minutes idle, ${v.operator_session_max_hours} hours at most`);
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  }

  const unchanged = !!s && hours === String(s.operator_session_max_hours) && idle === String(s.operator_idle_minutes)
    && lockN === String(s.signin_lock_failures) && lockMins === String(s.signin_lock_minutes)
    && replHours === (s.replacement_window_hours === undefined ? "" : String(s.replacement_window_hours))
    && ackMins === (s.retirement_ack_minutes === undefined ? "" : String(s.retirement_ack_minutes));

  return (
    <SectionCard
      title="Sign-in limits"
      description="How long a console sign-in lasts, and when wrong attempts lock an account. Disabling a person, or changing their role, password or two-step sign-in, ends their sign-ins at once."
      footer={canManage && s ? (
        <>
          <LastChanged at={s.updated_at} by={s.updated_by} />
          {unchanged && !busy && <span id={fields.id("unchanged")} className="text-xs text-muted-foreground">No changes to save.</span>}
          <Button size="sm" disabled={unchanged} loading={busy} onClick={save}
            aria-describedby={unchanged && !busy ? fields.id("unchanged") : undefined}>
            Save
          </Button>
        </>
      ) : undefined}
      bodyClassName="space-y-4"
    >
      <ErrorBanner err={err} />
      {!s && !err && <Skeleton className="h-32" />}
      {s && (
        <FieldGrid cols={2}>
          <Field
            label="Hours"
            required
            hint={`From ${s.min} to ${s.max} hours. Default ${s.default}.`}
            error={fields.error("hours")}
          >
            <Input id={fields.id("hours")} inputMode="numeric" aria-label="Sign-in lifetime in hours" value={hours} disabled={!canManage}
              onChange={(e) => setHours(e.target.value)} />
          </Field>
          <Field
            label="Minutes without activity"
            required
            hint={`From ${s.idle_bounds.min} to ${s.idle_bounds.max} minutes. Default ${s.idle_bounds.default}.`}
            error={fields.error("idle")}
          >
            <Input id={fields.id("idle")} inputMode="numeric" aria-label="Sign out after this many minutes without activity" value={idle}
              disabled={!canManage} onChange={(e) => setIdle(e.target.value)} />
          </Field>
          <Field
            label="Wrong attempts before a lock"
            required
            hint={`From ${s.signin_lock_failures_bounds.min} to ${s.signin_lock_failures_bounds.max}. Default ${s.signin_lock_failures_bounds.default}.`}
            error={fields.error("lockN")}
          >
            <Input id={fields.id("lockN")} inputMode="numeric" aria-label="Wrong passwords or codes before the account is locked" value={lockN}
              disabled={!canManage} onChange={(e) => setLockN(e.target.value)} />
          </Field>
          <Field
            label="Lock time (minutes)"
            required
            hint={`From ${s.signin_lock_minutes_bounds.min} to ${s.signin_lock_minutes_bounds.max} minutes. Default ${s.signin_lock_minutes_bounds.default}. Wrong attempts are counted over the same time.`}
            error={fields.error("lockMins")}
          >
            <Input id={fields.id("lockMins")} inputMode="numeric" aria-label="Minutes an account stays locked" value={lockMins}
              disabled={!canManage} onChange={(e) => setLockMins(e.target.value)} />
          </Field>
          {s.replacement_window_hours_bounds && (
            <Field
              label="Replacement window (hours)"
              required
              hint={`How long an appliance marked for replacement stays licensed until its replacement is activated. From ${s.replacement_window_hours_bounds.min} to ${s.replacement_window_hours_bounds.max} hours. Default ${s.replacement_window_hours_bounds.default}.`}
              error={fields.error("replHours")}
            >
              <Input id={fields.id("replHours")} inputMode="numeric" aria-label="Hours an appliance marked for replacement stays licensed" value={replHours}
                disabled={!canManage} onChange={(e) => setReplHours(e.target.value)} />
            </Field>
          )}
          {s.retirement_ack_minutes_bounds && (
            <Field
              label="Retirement acknowledgement wait (minutes)"
              required
              hint={`How long Central waits for a retiring appliance to confirm before it raises an alert. From ${s.retirement_ack_minutes_bounds.min} to ${s.retirement_ack_minutes_bounds.max} minutes. Default ${s.retirement_ack_minutes_bounds.default}.`}
              error={fields.error("ackMins")}
            >
              <Input id={fields.id("ackMins")} inputMode="numeric" aria-label="Minutes to wait for a retiring appliance to acknowledge" value={ackMins}
                disabled={!canManage} onChange={(e) => setAckMins(e.target.value)} />
            </Field>
          )}
          {canManage && (
            <Field label="Reason" required hint="Recorded in the audit log." error={fields.error("reason")}>
              <Input id={fields.id("reason")} value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} placeholder="e.g. shared workstation" />
            </Field>
          )}
        </FieldGrid>
      )}
      {s && !canManage && <LastChanged at={s.updated_at} by={s.updated_by} />}
    </SectionCard>
  );
}

/** "Last changed … by …", shown with a setting's save button. */
export function LastChanged({ at, by }: { at?: string | null; by?: string | null }) {
  if (!at) return null;
  return (
    <span className="me-auto text-xs text-muted-foreground">
      Last changed {formatDateTime(at)}{by ? ` by ${by}` : ""}
    </span>
  );
}
