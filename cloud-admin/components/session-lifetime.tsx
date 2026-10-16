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
  const [reason, setReason] = useState("");
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api.get<SecuritySettings>("/cloud/v1/settings/security")
      .then((v) => { setS(v); setHours(String(v.operator_session_max_hours)); })
      .catch(setErr);
  }, []);

  async function save() {
    if (!s) return;
    const n = Number(hours);
    if (!/^\d+$/.test(hours.trim()) || n < s.min || n > s.max) {
      setErr(`Enter a whole number of hours from ${s.min} to ${s.max}.`);
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
        api.patch<SecuritySettings>("/cloud/v1/settings/security", { operator_session_max_hours: n, reason: reason.trim() }),
      );
      setS(v);
      setHours(String(v.operator_session_max_hours));
      setReason("");
      toast.success("Sign-in lifetime saved", `${v.operator_session_max_hours} hours`);
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
          <CardTitle>Sign-in lifetime</CardTitle>
          <CardDescription>
            A console sign-in ends this many hours after it began, however active the person is. Disabling a person,
            changing their role or resetting their password ends their sign-ins at once.
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
          <Button size="sm" disabled={busy || hours === String(s.operator_session_max_hours)} onClick={save}>
            {busy ? "Saving…" : "Save"}
          </Button>
        </CardFooter>
      )}
    </Card>
  );
}
