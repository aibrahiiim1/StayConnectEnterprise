"use client";

import { useEffect, useState } from "react";
import { api, withStepUp } from "@/lib/api";
import { Card, CardBody, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { ErrorBanner } from "@/components/ui/error-banner";
import { useToast } from "@/components/ui/toast";
import { formatDateTime } from "@/lib/status";

export type SecuritySettings = {
  operator_session_max_hours: number;
  /** Signed out after this many minutes without activity (0052). */
  operator_idle_minutes: number;
  idle_bounds: { default: number; min: number; max: number; unit: string };
  default: number;
  min: number;
  max: number;
  unit: string;
  updated_at?: string | null;
  updated_by?: string | null;
};

/**
 * The absolute console sign-in lifetime (System → Team). A sign-in ends this many hours after it began, however
 * active the person is. Shortening it ends the sign-ins that are already older at their next request.
 */
export function SessionLifetimeSetting({ canManage }: { canManage: boolean }) {
  const toast = useToast();
  const [s, setS] = useState<SecuritySettings | null>(null);
  const [hours, setHours] = useState("");
  const [idle, setIdle] = useState("");
  const [reason, setReason] = useState("");
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api.get<SecuritySettings>("/cloud/v1/settings/security")
      .then((v) => { setS(v); setHours(String(v.operator_session_max_hours)); setIdle(String(v.operator_idle_minutes)); })
      .catch(setErr);
  }, []);

  async function save() {
    if (!s) return;
    const n = Number(hours);
    if (!/^\d+$/.test(hours.trim()) || n < s.min || n > s.max) {
      setErr(`Enter a whole number of hours from ${s.min} to ${s.max}.`);
      return;
    }
    const m = Number(idle);
    if (!/^\d+$/.test(idle.trim()) || m < s.idle_bounds.min || m > s.idle_bounds.max) {
      setErr(`Enter a whole number of minutes from ${s.idle_bounds.min} to ${s.idle_bounds.max}.`);
      return;
    }
    if (!reason.trim()) {
      setErr("Enter a reason; it is recorded in the audit log.");
      return;
    }
    setBusy(true);
    setErr(null);
    try {
      const v = await withStepUp(() =>
        api.patch<SecuritySettings>("/cloud/v1/settings/security", { operator_session_max_hours: n, operator_idle_minutes: m, reason: reason.trim() }),
      );
      setS(v);
      setHours(String(v.operator_session_max_hours));
      setIdle(String(v.operator_idle_minutes));
      setReason("");
      toast.success("Sign-in limits saved", `${v.operator_idle_minutes} minutes idle, ${v.operator_session_max_hours} hours at most`);
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <div className="space-y-0.5">
          <CardTitle>Sign-in limits</CardTitle>
          <CardDescription>
            A console sign-in ends after this many minutes without activity, and this many hours after it began however
            active the person is. A tab that is only open counts as inactive. Disabling a person, changing their role
            or resetting their password or two-step sign-in ends their sign-ins at once.
          </CardDescription>
        </div>
      </CardHeader>
      <CardBody className="space-y-4">
        <ErrorBanner err={err} />
        {s && (
          <div className="grid gap-4 sm:grid-cols-2">
            <Field
              label="Hours"
              required
              hint={`From ${s.min} to ${s.max} hours. Default ${s.default}.`}
            >
              <Input inputMode="numeric" aria-label="Sign-in lifetime in hours" value={hours} disabled={!canManage}
                onChange={(e) => setHours(e.target.value)} />
            </Field>
            <Field
              label="Minutes without activity"
              required
              hint={`From ${s.idle_bounds.min} to ${s.idle_bounds.max} minutes. Default ${s.idle_bounds.default}.`}
            >
              <Input inputMode="numeric" aria-label="Sign out after this many minutes without activity" value={idle}
                disabled={!canManage} onChange={(e) => setIdle(e.target.value)} />
            </Field>
            {canManage && (
              <Field label="Reason" required hint="Recorded in the audit log.">
                <Input value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} placeholder="e.g. shared workstation" />
              </Field>
            )}
          </div>
        )}
        {s?.updated_at && (
          <p className="text-caption text-muted-foreground">
            Last changed {formatDateTime(s.updated_at)}{s.updated_by ? ` by ${s.updated_by}` : ""}.
          </p>
        )}
      </CardBody>
      {canManage && s && (
        <CardFooter>
          <Button size="sm" disabled={busy || (hours === String(s.operator_session_max_hours) && idle === String(s.operator_idle_minutes))} onClick={save}>
            {busy ? "Saving…" : "Save"}
          </Button>
        </CardFooter>
      )}
    </Card>
  );
}
