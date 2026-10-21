"use client";

import { useEffect, useState } from "react";
import { api, withStepUp } from "@/lib/api";
import { Card, CardBody, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Field, Input, useFieldErrors } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { ErrorBanner } from "@/components/ui/error-banner";
import { useToast } from "@/components/ui/toast";
import { formatDateTime } from "@/lib/status";

type Bounds = { default: number; min: number; max: number; unit: string };

export type RetentionSettings = {
  audit_log_months: number;
  audit_log_months_bounds: Bounds;
  history_personal_months: number;
  history_personal_months_bounds: Bounds;
  history_years: number;
  history_years_bounds: Bounds;
  disabled_account_months: number;
  disabled_account_months_bounds: Bounds;
  updated_at?: string | null;
  updated_by?: string | null;
};

type Key = "audit_log_months" | "history_personal_months" | "history_years" | "disabled_account_months";

const FIELDS: { key: Key; label: string; aria: string; explain: string }[] = [
  {
    key: "audit_log_months", label: "Audit log (months)", aria: "Months the audit log is kept",
    explain: "Who did what in Central, with their IP address and browser.",
  },
  {
    key: "history_personal_months", label: "People in the history (months)",
    aria: "Months before people are removed from the history",
    explain: "After this, alerts and appliance history keep the event but lose the person's email and IP address.",
  },
  {
    key: "history_years", label: "History and licences (years)", aria: "Years the history and licences are kept",
    explain: "Closed alerts, appliance and certificate history and ended licences are deleted. A retired appliance loses its MAC addresses, host name and IP address.",
  },
  {
    key: "disabled_account_months", label: "Disabled accounts (months)", aria: "Months before a disabled account is anonymised",
    explain: "After this, a disabled account's email, name, password and authenticator are removed for good.",
  },
];

/**
 * How long Central keeps its own records (System → Team, migration 0054). Applied every hour; a shorter period
 * removes the older records at the next run.
 */
export function DataRetentionSetting({ canManage }: { canManage: boolean }) {
  const toast = useToast();
  const [s, setS] = useState<RetentionSettings | null>(null);
  const [vals, setVals] = useState<Record<Key, string>>({
    audit_log_months: "", history_personal_months: "", history_years: "", disabled_account_months: "",
  });
  const [reason, setReason] = useState("");
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const fields = useFieldErrors<string>();

  function load(v: RetentionSettings) {
    setS(v);
    setVals({
      audit_log_months: String(v.audit_log_months), history_personal_months: String(v.history_personal_months),
      history_years: String(v.history_years), disabled_account_months: String(v.disabled_account_months),
    });
  }

  useEffect(() => {
    api.get<RetentionSettings>("/cloud/v1/settings/retention").then(load).catch(setErr);
  }, []);

  const changed = s !== null && FIELDS.some((f) => vals[f.key] !== String(s[f.key]));

  async function save() {
    if (!s) return;
    const body: Record<string, number | string> = {};
    for (const f of FIELDS) {
      const b = s[`${f.key}_bounds`];
      const n = Number(vals[f.key]);
      if (!/^\d+$/.test(vals[f.key].trim()) || n < b.min || n > b.max) {
        // Reported under its own field, which takes focus.
        setErr(null);
        fields.fail({ [f.key]: `${f.label}: enter a whole number from ${b.min} to ${b.max}.` });
        return;
      }
      if (n !== s[f.key]) body[f.key] = n;
    }
    if (!reason.trim()) {
      setErr(null);
      fields.fail({ reason: "Enter a reason; it is recorded in the audit log." });
      return;
    }
    fields.clear();
    body.reason = reason.trim();
    setBusy(true);
    setErr(null);
    try {
      const v = await withStepUp(() => api.patch<RetentionSettings>("/cloud/v1/settings/retention", body));
      load(v);
      setReason("");
      toast.success("Retention periods saved", "They apply within the hour.");
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
          <CardTitle>Data retention</CardTitle>
          <CardDescription>
            How long Central keeps its own records. Every hour, records older than these periods are deleted or
            have the people in them removed. Open security alerts and current licences are never removed.
          </CardDescription>
        </div>
      </CardHeader>
      <CardBody className="space-y-4">
        <ErrorBanner err={err} />
        {s && (
          <div className="grid gap-4 sm:grid-cols-2">
            {FIELDS.map((f) => {
              const b = s[`${f.key}_bounds`];
              return (
                <Field key={f.key} label={f.label} required
                  hint={`${f.explain} From ${b.min} to ${b.max} ${b.unit}. Default ${b.default}.`}
                  error={fields.error(f.key)}>
                  <Input id={fields.id(f.key)} inputMode="numeric" aria-label={f.aria} value={vals[f.key]} disabled={!canManage}
                    onChange={(e) => setVals((v) => ({ ...v, [f.key]: e.target.value }))} />
                </Field>
              );
            })}
            {canManage && (
              <Field label="Reason" required hint="Recorded in the audit log." error={fields.error("reason")}>
                <Input id={fields.id("reason")} value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)}
                  placeholder="e.g. auditor's request" />
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
          {!changed && !busy && <span id={fields.id("unchanged")} className="text-caption text-muted-foreground">No changes to save.</span>}
          <Button size="sm" disabled={busy || !changed} onClick={save}
            aria-describedby={!changed && !busy ? fields.id("unchanged") : undefined}>
            {busy ? "Saving…" : "Save"}
          </Button>
        </CardFooter>
      )}
    </Card>
  );
}
