"use client";

import { useEffect, useState } from "react";
import { api, withStepUp } from "@/lib/api";
import { SectionCard } from "@/components/ui/card";
import { Field, FieldGrid, Input, useFieldErrors } from "@/components/ui/input";
import { Skeleton } from "@/components/ui/misc";
import { Button } from "@/components/ui/button";
import { ErrorBanner } from "@/components/ui/error-banner";
import { useToast } from "@/components/ui/toast";
import { LastChanged } from "@/components/session-lifetime";

export type RegistrationSettings = {
  max_waiting_appliances: number;
  max_waiting_appliances_bounds: { default: number; min: number; max: number; unit: string };
  /** Appliances waiting for activation right now. */
  waiting_appliances: number;
  updated_at?: string | null;
  updated_by?: string | null;
};

/** The cap on appliances waiting for activation; a new registration past it is refused until some are activated or deleted. */
export function RegistrationLimitsSetting({ canManage }: { canManage: boolean }) {
  const toast = useToast();
  const [s, setS] = useState<RegistrationSettings | null>(null);
  const [max, setMax] = useState("");
  const [reason, setReason] = useState("");
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const fields = useFieldErrors<"max" | "reason" | "unchanged">();

  function load(v: RegistrationSettings) {
    setS(v);
    setMax(String(v.max_waiting_appliances));
  }

  useEffect(() => {
    api.get<RegistrationSettings>("/cloud/v1/settings/registration").then(load).catch(setErr);
  }, []);

  const unchanged = !!s && max === String(s.max_waiting_appliances);

  async function save() {
    if (!s) return;
    const b = s.max_waiting_appliances_bounds;
    const n = Number(max);
    if (!/^\d+$/.test(max.trim()) || n < b.min || n > b.max) {
      setErr(null);
      fields.fail({ max: `Enter a whole number of ${b.unit} from ${b.min} to ${b.max}.` });
      return;
    }
    if (!reason.trim()) {
      setErr(null);
      fields.fail({ reason: "Enter a reason; it is recorded in the audit log." });
      return;
    }
    fields.clear();
    setBusy(true);
    setErr(null);
    try {
      const v = await withStepUp(() =>
        api.patch<RegistrationSettings>("/cloud/v1/settings/registration", { max_waiting_appliances: n, reason: reason.trim() }),
      );
      load(v);
      setReason("");
      toast.success("Registration limit saved", `Up to ${v.max_waiting_appliances} appliances may wait for activation`);
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  }

  const b = s?.max_waiting_appliances_bounds;
  return (
    <SectionCard
      title="Appliance registration"
      description="Appliances register themselves without a token, so anyone who can reach Central could add a waiting appliance. Past this limit a new appliance is refused and an alert is raised; appliances already waiting, and known ones registering again, are not affected."
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
      {!s && !err && <Skeleton className="h-24" />}
      {s && b && (
        <>
          <p className="text-sm" data-testid="waiting-count">
            <strong>{s.waiting_appliances}</strong> of {s.max_waiting_appliances} {b.unit} are waiting for activation now.
          </p>
          <FieldGrid cols={2}>
            <Field
              label="Most appliances waiting for activation"
              required
              hint={`From ${b.min} to ${b.max} ${b.unit}. Default ${b.default}.`}
              error={fields.error("max")}
            >
              <Input id={fields.id("max")} inputMode="numeric" aria-label="Most appliances waiting for activation" value={max}
                disabled={!canManage} onChange={(e) => setMax(e.target.value)} />
            </Field>
            {canManage && (
              <Field label="Reason" required hint="Recorded in the audit log." error={fields.error("reason")}>
                <Input id={fields.id("reason")} value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)}
                  placeholder="e.g. opening week, many new appliances" />
              </Field>
            )}
          </FieldGrid>
        </>
      )}
      {s && !canManage && <LastChanged at={s.updated_at} by={s.updated_by} />}
    </SectionCard>
  );
}
