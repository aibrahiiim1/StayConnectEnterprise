"use client";

// One editor for the switch and the terms. Saves with the config version that was read; on a conflict the page
// reloads the setting without discarding the operator's entries.

import * as React from "react";
import { ArrowRight, LogOut } from "lucide-react";
import { api } from "@/lib/api";
import {
  type AccessBounds,
  type AccessDraft,
  type AccessState,
  type DraftField,
  OTHER_REASON,
  REASON_CHOICES,
  describePublishFailure,
  devicePolicyTitle,
  DEVICE_POLICY_TEXT,
  draftFromState,
  draftToTerms,
  fmtData,
  fmtHours,
  fmtSpeed,
  guestReceivesSentence,
  isValidReasonCode,
  normaliseReasonCode,
  validateDraft,
  MB,
} from "@/lib/api/after-checkout-access";
import { Sheet, SheetBody, SheetContent, SheetFooter, SheetHeader, SheetSection } from "@/components/ui/sheet";
import { Stepper, OptionCard } from "@/components/ui/data";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { Switch } from "@/components/ui/misc";
import { Badge } from "@/components/ui/badge";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { cn } from "@/lib/utils";

const STEPS = ["Settings", "Review and save"];

type Row = { key: string; label: string; from: string; to: string; changed: boolean };

function reviewRows(state: AccessState, draft: AccessDraft, terms: ReturnType<typeof draftToTerms>): Row[] {
  const p = state.policy;
  const row = (key: string, label: string, from: string | undefined, to: string): Row => ({
    key, label, from: from ?? "—", to, changed: (from ?? "—") !== to,
  });
  return [
    row("enabled", "After Check-out Access", state.enabled ? "On" : "Off", draft.enabled ? "On" : "Off"),
    row("hours", "Duration after check-out", p ? fmtHours(p.duration_hours) : undefined, fmtHours(terms.duration_hours)),
    row("down", "Download speed", p ? fmtSpeed(p.down_kbps) : undefined, fmtSpeed(terms.down_kbps)),
    row("up", "Upload speed", p ? fmtSpeed(p.up_kbps) : undefined, fmtSpeed(terms.up_kbps)),
    row("quota", "Shared data quota", p ? fmtData(p.data_quota_bytes) : undefined, fmtData(terms.data_quota_bytes)),
    row("limit", "Device allowance", p ? String(p.device_limit) : undefined, String(terms.device_limit)),
    row("policy", "New devices", p ? devicePolicyTitle(p.device_policy) : undefined, devicePolicyTitle(terms.device_policy)),
  ];
}

export function AccessEditorSheet({
  open, onOpenChange, state, bounds, supportedPolicies, canWrite, onSaved, reload,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  state: AccessState;
  bounds: AccessBounds;
  supportedPolicies: string[];
  canWrite: boolean;
  onSaved: (newVersion: number) => void | Promise<void>;
  reload: () => Promise<void>;
}) {
  const [step, setStep] = React.useState(0);
  const [draft, setDraft] = React.useState<AccessDraft>(() => draftFromState(state, supportedPolicies));
  const [touched, setTouched] = React.useState(false);
  const defaultReason = state.configured ? "POLICY_CHANGE" : "INITIAL_SETUP";
  const [reasonChoice, setReasonChoice] = React.useState<string>(defaultReason);
  const [otherReason, setOtherReason] = React.useState("");
  const [password, setPassword] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);
  const [fieldError, setFieldError] = React.useState<"password" | "reason" | null>(null);

  // Seed only on open so a conflict reload cannot erase the draft.
  React.useEffect(() => {
    if (open) {
      setDraft(draftFromState(state, supportedPolicies));
      setStep(0);
      setTouched(false);
      setReasonChoice(defaultReason);
      setOtherReason("");
      setError(null);
      setFieldError(null);
    } else {
      setPassword("");
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const errors = validateDraft(draft, bounds, supportedPolicies);
  const valid = Object.keys(errors).length === 0;
  const terms = draftToTerms(draft);
  const rows = reviewRows(state, draft, terms);
  const termsChanged = !state.configured || rows.filter((r) => r.key !== "enabled").some((r) => r.changed);
  const switchChanged = draft.enabled !== state.enabled;
  const nothingChanged = !termsChanged && !switchChanged;
  const changedCount = rows.filter((r) => r.changed).length + (!state.configured && !rows.some((r) => r.changed) ? 1 : 0);

  const reasonCode = reasonChoice === OTHER_REASON ? normaliseReasonCode(otherReason) : reasonChoice;
  const reasonOk = isValidReasonCode(reasonCode);

  const set = (k: keyof AccessDraft, v: string | boolean) => setDraft((d) => ({ ...d, [k]: v }));
  const err = (k: DraftField) => (touched || draft[k] === "" ? errors[k] : undefined);

  function toReview(e?: React.FormEvent) {
    e?.preventDefault();
    setTouched(true);
    if (!valid) return;
    setError(null);
    setStep(1);
  }

  async function save() {
    if (!valid || !reasonOk || !password || nothingChanged || !canWrite) return;
    setBusy(true);
    setError(null);
    setFieldError(null);
    try {
      const body: Record<string, unknown> = {
        expected_config_version: state.config_version,
        reason_code: reasonCode,
        password,
      };
      // Terms go whole or not at all; the switch only when it changed.
      if (termsChanged) Object.assign(body, terms);
      if (switchChanged) body.enabled = draft.enabled;
      const r = await api.put<{ config_version: number }>("/after-checkout-access", body);
      setPassword("");
      await onSaved(r.config_version);
    } catch (e: any) {
      const f = describePublishFailure(e);
      setError(f.message);
      setFieldError(f.field ?? null);
      if (f.conflict) {
        setPassword("");
        setStep(0);
        await reload();
      }
    } finally {
      setBusy(false);
    }
  }

  const policyOptions = supportedPolicies.length ? supportedPolicies : ["REJECT_NEW_DEVICE"];
  const hb = bounds.duration_hours;
  const qb = bounds.data_quota_bytes;
  const lb = bounds.device_limit;
  const hint = (k: "down_kbps" | "up_kbps") => `Allowed: ${fmtSpeed(bounds[k].min)} to ${fmtSpeed(bounds[k].max)}. Default ${fmtSpeed(bounds[k].default)}.`;

  return (
    <Sheet open={open} onOpenChange={(v) => !busy && onOpenChange(v)}>
      <SheetContent width="lg" aria-label="Edit After Check-out Access">
        <SheetHeader
          eyebrow="After Check-out Access"
          icon={<LogOut />}
          title="Edit settings"
          description={`Changes are saved as version ${state.config_version + 1} and apply to future check-outs only.`}
        >
          <Stepper steps={STEPS} current={step} onStep={(i) => !busy && setStep(i)} />
        </SheetHeader>

        {step === 0 && (
          <>
            <SheetBody>
              {error && <ErrorBanner err={error} />}
              <form id="aca-terms" onSubmit={toReview} className="space-y-6" aria-label="After Check-out Access settings" noValidate>
                <SheetSection title="Switch" description="Turn the feature off to give nobody access after check-out.">
                  <div className="flex items-center justify-between gap-4 rounded-lg border border-border px-3.5 py-3">
                    <label htmlFor="aca-enabled" className="text-sm font-medium">
                      Give access after check-out
                    </label>
                    <Switch id="aca-enabled" label="Give access after check-out" checked={draft.enabled} onCheckedChange={(v) => set("enabled", v)} />
                  </div>
                </SheetSection>

                <SheetSection title="Allowance" description="How long it lasts and what the guest's devices share.">
                  <div className="grid gap-4 sm:grid-cols-2">
                    <Field
                      label="Duration after verified check-out (hours)"
                      hint={`Starts when the check-out is verified. Allowed: ${hb.min} to ${hb.max} hours. Default ${hb.default}.`}
                      error={err("hours")}
                      className="sm:col-span-2"
                    >
                      <Input type="number" inputMode="numeric" min={hb.min} max={hb.max} step={1} value={draft.hours}
                        onChange={(e) => set("hours", e.target.value)} />
                    </Field>
                    <Field label="Download speed (Mbps)" hint={hint("down_kbps")} error={err("downMbps")}>
                      <Input type="number" inputMode="decimal" min={bounds.down_kbps.min / 1000} max={bounds.down_kbps.max / 1000} step="any"
                        value={draft.downMbps} onChange={(e) => set("downMbps", e.target.value)} />
                    </Field>
                    <Field label="Upload speed (Mbps)" hint={hint("up_kbps")} error={err("upMbps")}>
                      <Input type="number" inputMode="decimal" min={bounds.up_kbps.min / 1000} max={bounds.up_kbps.max / 1000} step="any"
                        value={draft.upMbps} onChange={(e) => set("upMbps", e.target.value)} />
                    </Field>
                    <Field
                      label="Shared data quota (MB)"
                      hint={`One quota shared by all of the stay's devices, not per device. Allowed: ${fmtData(qb.min)} to ${fmtData(qb.max)}. Default ${fmtData(qb.default)}. 1 GB = 1024 MB.`}
                      error={err("quotaMb")}
                      className="sm:col-span-2"
                    >
                      <Input type="number" inputMode="numeric" min={qb.min / MB} max={qb.max / MB} step="any" value={draft.quotaMb}
                        onChange={(e) => set("quotaMb", e.target.value)} />
                    </Field>
                  </div>
                </SheetSection>

                <SheetSection title="Devices" description="Which devices may reconnect.">
                  <div role="radiogroup" aria-label="New devices" className="grid gap-2">
                    {policyOptions.map((p) => (
                      <OptionCard
                        key={p}
                        name="aca-device-policy"
                        value={p}
                        checked={draft.devicePolicy === p}
                        onChange={(v) => set("devicePolicy", v)}
                        title={devicePolicyTitle(p)}
                        description={DEVICE_POLICY_TEXT[p]?.description}
                      />
                    ))}
                  </div>
                  <Field
                    label="Device allowance"
                    hint={`How many of the stay's authorised devices can be connected at once. Allowed: ${lb.min} to ${lb.max}. Default ${lb.default}.`}
                    error={err("deviceLimit")}
                  >
                    <Input type="number" inputMode="numeric" min={lb.min} max={lb.max} step={1} value={draft.deviceLimit}
                      onChange={(e) => set("deviceLimit", e.target.value)} />
                  </Field>
                </SheetSection>

                <section aria-label="Guest will receive" className="rounded-lg border border-primary/20 bg-primary-subtle/40 p-4">
                  <div className="text-xs font-medium text-muted-foreground">Guest will receive</div>
                  <p className="mt-1.5 text-sm leading-relaxed" data-testid="aca-preview">
                    {!draft.enabled
                      ? "Nothing: After Check-out Access is turned off."
                      : valid
                        ? guestReceivesSentence({ ...terms, duration_seconds: terms.duration_hours * 3600 })
                        : "Correct the highlighted fields to see what a guest will receive."}
                  </p>
                </section>
              </form>
            </SheetBody>
            <SheetFooter>
              <Button variant="ghost" onClick={() => onOpenChange(false)}>Cancel</Button>
              <Button type="submit" form="aca-terms" disabled={touched && !valid}>
                Review changes <ArrowRight />
              </Button>
            </SheetFooter>
          </>
        )}

        {step === 1 && (
          <>
            <SheetBody>
              {error && <ErrorBanner err={error} />}
              <SheetSection
                title="What changes"
                description={`Compared with what is saved now (version ${state.config_version}).`}
                actions={<Badge tone={changedCount ? "accent" : "neutral"}>{changedCount} change{changedCount === 1 ? "" : "s"}</Badge>}
              >
                <ul aria-label="Changes" className="divide-y divide-border rounded-lg border border-border">
                  {rows.map((c) => (
                    <li key={c.key} className={cn("flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 px-3.5 py-2.5 text-sm", c.changed && "bg-primary-subtle/30")}>
                      <span className={cn("text-muted-foreground", c.changed && "font-medium text-foreground")}>{c.label}</span>
                      <span className="flex flex-wrap items-baseline gap-2 tabular">
                        {c.changed ? (
                          <>
                            <span className="text-muted-foreground line-through">{c.from}</span>
                            <ArrowRight className="size-3.5 self-center text-muted-foreground" aria-label="becomes" />
                            <span className="font-medium">{c.to}</span>
                          </>
                        ) : (
                          <span>{c.to}</span>
                        )}
                      </span>
                    </li>
                  ))}
                </ul>
              </SheetSection>

              {nothingChanged ? (
                <Callout tone="info" title="Nothing to save">
                  These settings are identical to version {state.config_version}. Go back and change something, or cancel.
                </Callout>
              ) : (
                <p className="text-xs text-muted-foreground">
                  This applies to future check-outs only. A guest who has already checked out keeps the exact terms they were given.
                </p>
              )}

              <SheetSection title="Confirm">
                <Field label="Reason" hint="Recorded with this version in the history." error={fieldError === "reason" ? "Choose a reason." : undefined}>
                  <Select value={reasonChoice} onChange={(e) => setReasonChoice(e.target.value)}>
                    {REASON_CHOICES.filter((r) => state.configured || r.code !== "POLICY_CHANGE").map((r) => (
                      <option key={r.code} value={r.code}>{r.label}</option>
                    ))}
                    <option value={OTHER_REASON}>Other…</option>
                  </Select>
                </Field>
                {reasonChoice === OTHER_REASON && (
                  <Field
                    label="Describe the reason"
                    hint={reasonOk ? `Recorded as ${reasonCode}` : undefined}
                    error={!reasonOk && otherReason.trim() !== "" ? "Use letters, numbers and spaces, starting with a letter." : undefined}
                  >
                    <Input value={otherReason} maxLength={80} placeholder="e.g. late checkout season" onChange={(e) => setOtherReason(e.target.value)} />
                  </Field>
                )}
                <Field
                  label="Confirm your password"
                  hint="This changes what every departing guest receives."
                  error={fieldError === "password" ? "Password not accepted." : undefined}
                >
                  <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
                </Field>
              </SheetSection>
            </SheetBody>
            <SheetFooter>
              <Button variant="ghost" onClick={() => setStep(0)} disabled={busy}>Back</Button>
              <Button onClick={save} disabled={busy || !canWrite || !valid || !reasonOk || !password || nothingChanged}>
                {busy ? "Saving…" : "Save settings"}
              </Button>
            </SheetFooter>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}
