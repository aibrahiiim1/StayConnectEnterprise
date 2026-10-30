"use client";

// Sign-in limits and capacity (audit decisions AD-30/31/34/51/58): one-time-code caps and lifetime, the appliance-wide
// sign-in ceiling, the room sign-in pause exemption, device retention and the uploaded-image limits. Every value has a
// default, a unit, an explanation and bounds, comes from the appliance (never from this file) and is saved with a
// reason and the operator's password; the appliance records who changed what.

import { useCallback, useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Field, Input } from "@/components/ui/input";
import { ErrorBanner } from "@/components/ui/error-banner";
import { useToast } from "@/components/ui/toast";
import { REASON_HINT, REASON_MAX_CHARS, reasonOk } from "@/lib/reason";
import { useEditFocus } from "@/lib/use-edit-focus";

type Bound = { min: number; max: number; default: number | null; unit: string; help: string };
type Payload = { settings: Record<string, number | null>; bounds: Record<string, Bound> };

const LABELS: Record<string, string> = {
  otp_code_lifetime_minutes: "One-time code lifetime",
  otp_max_attempts: "Wrong guesses per code",
  otp_device_hourly_cap: "Codes per device, per number",
  otp_destination_hourly_cap: "Codes per number, all devices",
  otp_source_hourly_cap: "Codes per network source",
  signin_ceiling_per_minute: "Sign-in attempts per minute (whole appliance)",
  network_pause_exempt_hours: "Room pause: recent guests exempt for",
  device_retention_days: "Keep unused devices for",
  portal_image_max_edge_px: "Portal images: longest side",
  portal_image_quality: "Portal images: quality",
  post_stay_signin_days: "Post-stay sign-in: days after check-out",
  post_stay_device_attempts_per_minute: "Post-stay sign-in: attempts per device",
  post_stay_ip_attempts_per_minute: "Post-stay sign-in: attempts per network address",
  post_stay_wrong_answers_before_lockout: "Post-stay sign-in: answers before lockout",
  post_stay_lockout_minutes: "Post-stay sign-in: lockout length",
  post_stay_new_device_room_attempts_per_minute: "Post-stay sign-in: new-device attempts per room",
  post_stay_appliance_attempts_per_minute: "Post-stay sign-in: attempts, whole appliance",
};

export function SigninLimitsCard({ canEdit }: { canEdit: boolean }) {
  const toast = useToast();
  const [data, setData] = useState<Payload | null>(null);
  const [draft, setDraft] = useState<Record<string, string>>({});
  const [reason, setReason] = useState("");
  const [password, setPassword] = useState("");
  const [editing, setEditing] = useState(false);
  const { triggerRef, panelRef } = useEditFocus(editing);
  const [err, setErr] = useState<unknown>(null);
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

  function startEdit() {
    if (!data) return;
    const d: Record<string, string> = {};
    for (const k of Object.keys(data.bounds)) d[k] = data.settings[k] == null ? "" : String(data.settings[k]);
    setDraft(d);
    setEditing(true);
  }

  function parsed(): { patch: Record<string, number | null>; ok: boolean } {
    const patch: Record<string, number | null> = {};
    let ok = true;
    if (!data) return { patch, ok: false };
    for (const [k, b] of Object.entries(data.bounds)) {
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

  async function save() {
    const { patch } = parsed();
    setBusy(true); setErr(null);
    try {
      await api.put("/signin-capacity-settings/", { settings: patch, reason: reason.trim(), password });
      toast.success("Saved.");
      setEditing(false); setPassword(""); setReason("");
      await load();
    } catch (e) {
      setErr(e instanceof ApiError && e.code === "reauth_required" ? new Error("Your password was not accepted.") : e);
    } finally { setBusy(false); }
  }

  const { patch, ok } = parsed();
  const changed = Object.keys(patch).length > 0;
  const valid = editing && ok && changed && reasonOk(reason) && password !== "";
  return (
    <Card>
      <CardBody className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="min-w-0 space-y-1">
            <div className="text-emphasis">Sign-in limits and capacity</div>
            <p className="text-caption text-muted-foreground">
              One-time codes, the sign-in ceiling, the room sign-in pause, unused devices and uploaded portal images.
            </p>
          </div>
          {canEdit && !editing && data && (
            <Button ref={triggerRef} size="sm" variant="secondary" onClick={startEdit}>Change</Button>
          )}
        </div>
        <ErrorBanner err={err} />
        {data && !editing && (
          <dl className="grid gap-x-6 gap-y-1 sm:grid-cols-2">
            {Object.keys(data.bounds).map((k) => (
              <div key={k} className="flex items-center justify-between gap-3">
                <dt className="text-caption text-muted-foreground">{LABELS[k] ?? k}</dt>
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
            {Object.entries(data.bounds).map(([k, b]) => (
              <Field key={k} label={LABELS[k] ?? k} hint={`${b.help} ${b.min} to ${b.max} ${b.unit}.`}>
                <Input type="number" min={b.min} max={b.max} value={draft[k] ?? ""}
                  placeholder={b.default == null ? "follow licence" : String(b.default)}
                  onChange={(e) => setDraft({ ...draft, [k]: e.target.value })} />
              </Field>
            ))}
            <Field label="Reason" required hint={REASON_HINT}>
              <Input value={reason} onChange={(e) => setReason(e.target.value)} maxLength={REASON_MAX_CHARS} />
            </Field>
            <Field label="Your password" required hint="Confirms the change.">
              <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
            </Field>
            <div className="flex justify-end gap-2 sm:col-span-2">
              <Button size="sm" variant="secondary" disabled={busy} onClick={() => setEditing(false)}>Cancel</Button>
              <Button size="sm" variant="primary" disabled={busy || !valid} onClick={() => void save()}>{busy ? "Saving…" : "Save"}</Button>
            </div>
          </div>
        )}
      </CardBody>
    </Card>
  );
}
