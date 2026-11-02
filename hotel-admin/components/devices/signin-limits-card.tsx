"use client";

// Sign-in limits and capacity (audit decisions AD-30/31/34/51/58). The values live on one appliance endpoint, but each
// group is shown where an operator looks for it: one-time codes and sign-in protection under Sign-in methods, the After
// check-out limits on that page, uploaded images under Portal settings, device retention under Devices. Every value
// has a default, a unit, an explanation and bounds that come from the appliance (never from this file); a group saves
// only its own values, with a reason and the operator's password, and the appliance records who changed what.

import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Field, Input, QuietHints } from "@/components/ui/input";
import { ErrorBanner } from "@/components/ui/error-banner";
import { ConfirmDialog } from "@/components/ui/dialog";
import { InfoHint } from "@/components/ui/quiet";
import { useToast } from "@/components/ui/toast";
import { useEditFocus } from "@/lib/use-edit-focus";

type Bound = { min: number; max: number; default: number | null; unit: string; help: string };
type Payload = { settings: Record<string, number | null>; bounds: Record<string, Bound> };

export const SIGNIN_SETTING_LABELS: Record<string, string> = {
  otp_code_lifetime_minutes: "Code lifetime",
  otp_max_attempts: "Wrong guesses per code",
  otp_device_hourly_cap: "Codes per device, per number",
  otp_destination_hourly_cap: "Codes per number",
  otp_source_hourly_cap: "Codes per network source",
  signin_ceiling_per_minute: "Sign-ins per minute (appliance)",
  network_pause_exempt_hours: "Room pause: recent guests exempt",
  device_retention_days: "Keep unused devices",
  portal_image_max_edge_px: "Longest side",
  portal_image_quality: "Quality",
  post_stay_device_attempts_per_minute: "Attempts per device",
  post_stay_ip_attempts_per_minute: "Attempts per network address",
  post_stay_wrong_answers_before_lockout: "Wrong answers before lockout",
  post_stay_lockout_minutes: "Lockout length",
  post_stay_new_device_room_attempts_per_minute: "New-device attempts per room",
  post_stay_appliance_attempts_per_minute: "Attempts, whole appliance",
};

/** The groups and where they are shown. Every key the appliance reports belongs to exactly one group. */
export const SIGNIN_SETTING_GROUPS = {
  oneTimeCodes: ["otp_code_lifetime_minutes", "otp_max_attempts", "otp_device_hourly_cap", "otp_destination_hourly_cap", "otp_source_hourly_cap"],
  protection: ["signin_ceiling_per_minute", "network_pause_exempt_hours"],
  afterCheckout: [
    "post_stay_device_attempts_per_minute", "post_stay_ip_attempts_per_minute", "post_stay_wrong_answers_before_lockout",
    "post_stay_lockout_minutes", "post_stay_new_device_room_attempts_per_minute", "post_stay_appliance_attempts_per_minute",
  ],
  portalImages: ["portal_image_max_edge_px", "portal_image_quality"],
  devices: ["device_retention_days"],
} as const;

export function SigninSettingsGroup({
  title, hint, keys, canEdit, bare = false,
}: {
  title: string;
  hint?: string;
  keys: readonly string[];
  canEdit: boolean;
  /** Without its own card, e.g. inside a dialog. */
  bare?: boolean;
}) {
  const toast = useToast();
  const [data, setData] = useState<Payload | null>(null);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [editing, setEditing] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const { triggerRef, panelRef } = useEditFocus(editing);
  const [err, setErr] = useState<unknown>(null);
  const [saveErr, setSaveErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const r = await api.get<Payload>("/signin-capacity-settings/");
      if (r && r.bounds && r.settings) setData(r);
    } catch (e) {
      setErr(e);
    }
  }, []);
  useEffect(() => { void load(); }, [load]);

  // Only the keys this appliance reports are shown: an older appliance without a setting simply has no row for it.
  const shown = data ? keys.filter((k) => k in data.bounds) : [];

  function startEdit() {
    if (!data) return;
    const d: Record<string, string> = {};
    for (const k of shown) d[k] = data.settings[k] == null ? "" : String(data.settings[k]);
    setDraft(d);
    setEditing(true);
  }

  function parsed(): { patch: Record<string, number | null>; ok: boolean } {
    const patch: Record<string, number | null> = {};
    let ok = true;
    if (!data) return { patch, ok: false };
    for (const k of shown) {
      const b = data.bounds[k];
      const raw = (draft[k] ?? "").trim();
      const cur = data.settings[k];
      if (raw === "") {
        if (k !== "signin_ceiling_per_minute") ok = false;
        else if (cur != null) patch[k] = null;
        continue;
      }
      const n = Number(raw);
      if (!Number.isInteger(n) || n < b.min || n > b.max) { ok = false; continue; }
      if (n !== cur) patch[k] = n;
    }
    return { patch, ok };
  }

  async function save({ reason, password }: { reason: string; password: string }) {
    const { patch } = parsed();
    setBusy(true); setSaveErr(null);
    try {
      await api.put("/signin-capacity-settings/", { settings: patch, reason: reason.trim(), password });
      toast.success("Saved.");
      setConfirming(false); setEditing(false);
      await load();
    } catch (e) {
      setSaveErr(e instanceof ApiError && e.code === "reauth_required" ? new Error("Your password was not accepted.") : e);
    } finally { setBusy(false); }
  }

  const { patch, ok } = parsed();
  const changed = Object.keys(patch).length > 0;
  if (data && shown.length === 0) return null;

  const body = (
    <QuietHints>
      <div className="space-y-3" data-testid={`settings-group-${title}`}>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 items-center gap-1">
            <span className="text-emphasis">{title}</span>
            <InfoHint>{hint}</InfoHint>
          </div>
          {canEdit && !editing && data && (
            <Button ref={triggerRef} size="sm" variant="secondary" onClick={startEdit}>Change</Button>
          )}
        </div>
        <ErrorBanner err={err} />
        {data && !editing && (
          <dl className="grid gap-x-6 gap-y-1.5 sm:grid-cols-2">
            {shown.map((k) => (
              <div key={k} className="flex min-h-7 items-center justify-between gap-3">
                <dt className="flex items-center gap-1 text-caption text-muted-foreground">
                  {SIGNIN_SETTING_LABELS[k] ?? k}
                  <InfoHint>{data.bounds[k].help}</InfoHint>
                </dt>
                <dd>
                  <Badge tone="neutral">
                    {data.settings[k] == null ? "follows licence" : `${data.settings[k]} ${data.bounds[k].unit}`}
                  </Badge>
                </dd>
              </div>
            ))}
          </dl>
        )}
        {data && editing && (
          <div ref={panelRef} className="grid gap-3 sm:grid-cols-2">
            {shown.map((k) => {
              const b = data.bounds[k];
              return (
                <Field key={k} label={`${SIGNIN_SETTING_LABELS[k] ?? k} (${b.unit})`} hint={`${b.help} ${b.min} to ${b.max} ${b.unit}.`}>
                  <Input type="number" min={b.min} max={b.max} value={draft[k] ?? ""}
                    placeholder={b.default == null ? "follow licence" : String(b.default)}
                    onChange={(e) => setDraft({ ...draft, [k]: e.target.value })} />
                </Field>
              );
            })}
            <div className="flex justify-end gap-2 sm:col-span-2">
              <Button size="sm" variant="secondary" disabled={busy} onClick={() => setEditing(false)}>Cancel</Button>
              <Button size="sm" variant="primary" disabled={busy || !ok || !changed}
                onClick={() => { setSaveErr(null); setConfirming(true); }}>Save</Button>
            </div>
          </div>
        )}
        <ConfirmDialog
          open={confirming}
          onOpenChange={(v) => !busy && setConfirming(v)}
          title={`Save ${title.toLowerCase()}`}
          description={Object.entries(patch).map(([k, v]) => `${SIGNIN_SETTING_LABELS[k] ?? k}: ${v == null ? "follow licence" : v}`).join(" · ")}
          confirmLabel="Save"
          busy={busy}
          error={saveErr}
          // Unchanged from before the move: these are rate limits and lockouts, so a reason and the password are asked.
          requireReason reasonStrict
          requirePassword strictPassword
          onConfirm={save}
        />
      </div>
    </QuietHints>
  );
  return bare ? body : <Card><CardBody>{body}</CardBody></Card>;
}
