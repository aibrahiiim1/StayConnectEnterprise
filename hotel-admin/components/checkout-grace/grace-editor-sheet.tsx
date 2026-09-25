"use client";

// The checkout grace editor: terms → review (old → new) → reason + password → publish.
//
// Publishing changes what every future departing guest receives, so it keeps a review step, a password step-up,
// a bounded reason code and the version the operator was looking at (optimistic concurrency). A concurrent
// publication is a 409: the page reloads what is now in force and the operator's draft is kept, so they can
// compare it against the new policy instead of silently overwriting it.

import * as React from "react";
import { ArrowRight, LogOut } from "lucide-react";
import { api } from "@/lib/api";
import {
  BOUNDS,
  DEVICE_POLICY_TEXT,
  type DraftField,
  type DurationUnit,
  type GraceDraft,
  type GraceTerms,
  OTHER_REASON,
  REASON_CHOICES,
  compareTerms,
  describePublishFailure,
  devicePolicyTitle,
  draftFromTerms,
  draftToTerms,
  fmtData,
  guestReceivesSentence,
  isValidReasonCode,
  normaliseReasonCode,
  validateDraft,
} from "@/lib/api/checkout-grace";
import { Sheet, SheetBody, SheetContent, SheetFooter, SheetHeader, SheetSection } from "@/components/ui/sheet";
import { Stepper, OptionCard } from "@/components/ui/data";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { cn } from "@/lib/utils";
import { HelpSection, HelpTip } from "@/components/help";

const STEPS = ["Terms", "Review and publish"];

/** A number plus its unit. Receives the id/aria props `Field` injects, so the label binds to the number input. */
function DurationInput({
  value,
  unit,
  onValue,
  onUnit,
  unitLabel,
  id,
  ...aria
}: {
  value: string;
  unit: DurationUnit;
  onValue: (v: string) => void;
  onUnit: (u: DurationUnit) => void;
  unitLabel: string;
  id?: string;
  "aria-describedby"?: string;
  "aria-invalid"?: boolean;
}) {
  return (
    <div className="flex gap-2">
      <Input
        id={id}
        type="number"
        inputMode="decimal"
        min={1}
        step="any"
        value={value}
        onChange={(e) => onValue(e.target.value)}
        className="min-w-0 flex-1"
        {...aria}
      />
      <Select aria-label={unitLabel} value={unit} onChange={(e) => onUnit(e.target.value as DurationUnit)} className="w-28">
        <option value="min">minutes</option>
        <option value="h">hours</option>
        <option value="d">days</option>
      </Select>
    </div>
  );
}

export function GraceEditorSheet({
  open,
  onOpenChange,
  base,
  baseLabel,
  published,
  version,
  supportedPolicies,
  canWrite,
  onPublished,
  reload,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** The terms in force now — the draft starts from them and the review compares against them. */
  base: GraceTerms;
  baseLabel: string;
  published: boolean;
  /** The config_version the operator is looking at; sent as expected_config_version. */
  version: number;
  supportedPolicies: string[];
  canWrite: boolean;
  onPublished: (newVersion: number) => void | Promise<void>;
  /** Re-read the page after a version conflict. */
  reload: () => Promise<void>;
}) {
  const [step, setStep] = React.useState(0);
  const [draft, setDraft] = React.useState<GraceDraft>(() => draftFromTerms(base, supportedPolicies));
  const [touched, setTouched] = React.useState(false);
  const [reasonChoice, setReasonChoice] = React.useState<string>(published ? "POLICY_CHANGE" : "INITIAL_SETUP");
  const [otherReason, setOtherReason] = React.useState("");
  const [password, setPassword] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [error, setError] = React.useState<string | null>(null);
  const [fieldError, setFieldError] = React.useState<"password" | "reason" | null>(null);

  // Seed on open, and only on open: a reload after a conflict changes `base` but must not wipe the draft.
  React.useEffect(() => {
    if (open) {
      setDraft(draftFromTerms(base, supportedPolicies));
      setStep(0);
      setTouched(false);
      setReasonChoice(published ? "POLICY_CHANGE" : "INITIAL_SETUP");
      setOtherReason("");
      setError(null);
      setFieldError(null);
    } else {
      setPassword("");
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const errors = validateDraft(draft, supportedPolicies);
  const valid = Object.keys(errors).length === 0;
  const terms = draftToTerms(draft);
  const changes = compareTerms(base, terms);
  const changedCount = changes.filter((c) => c.changed).length;
  // An identical re-publish is idempotent on the server (the version does not move), so it is not offered.
  const nothingChanged = published && changedCount === 0;

  const reasonCode = reasonChoice === OTHER_REASON ? normaliseReasonCode(otherReason) : reasonChoice;
  const reasonOk = isValidReasonCode(reasonCode);

  const set = (k: keyof GraceDraft, v: string) => setDraft((d) => ({ ...d, [k]: v }));
  const err = (k: DraftField) => (touched || draft[k] === "" ? errors[k] : undefined);

  function toReview(e?: React.FormEvent) {
    e?.preventDefault();
    setTouched(true);
    if (!valid) return;
    setError(null);
    setStep(1);
  }

  async function publish() {
    if (!valid || !reasonOk || !password || nothingChanged || !canWrite) return;
    setBusy(true);
    setError(null);
    setFieldError(null);
    try {
      // No package is sent: the server derives the one that expresses these terms exactly.
      const r = await api.put<{ config_version: number }>("/checkout-grace", {
        ...terms,
        config_version: version,
        expected_config_version: version,
        password,
        reason_code: reasonCode,
      });
      setPassword("");
      await onPublished(r.config_version);
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

  return (
    <Sheet open={open} onOpenChange={(v) => !busy && onOpenChange(v)}>
      <SheetContent width="lg" aria-label="Edit checkout grace policy">
        <SheetHeader
          eyebrow="Checkout grace"
          icon={<LogOut />}
          title={published ? "Edit policy" : "Create hotel policy"}
          description={
            published
              ? `Changes publish as version ${version + 1} and apply to future checkouts only.`
              : "Replace the emergency fallback with terms chosen for this hotel. Applies to future checkouts only."
          }
        >
          <Stepper steps={STEPS} current={step} onStep={(i) => !busy && setStep(i)} />
        </SheetHeader>

        {step === 0 && (
          <>
            <SheetBody>
              {error && <ErrorBanner err={error} />}
              <form id="grace-terms" onSubmit={toReview} className="space-y-6" aria-label="Checkout grace terms" noValidate>
                <SheetSection title="Allowance" description="How long grace lasts and what it includes.">
                  <div className="grid gap-4 sm:grid-cols-2">
                    <Field
                      label="Grace time"
                      hint="Counted on the clock from checkout, whether or not the guest is online."
                      error={err("durationValue")}
                      className="sm:col-span-2"
                    >
                      <DurationInput
                        value={draft.durationValue}
                        unit={draft.durationUnit}
                        onValue={(v) => set("durationValue", v)}
                        onUnit={(u) => set("durationUnit", u)}
                        unitLabel="Grace time unit"
                      />
                    </Field>
                    <Field label="Download speed (Mbps)" error={err("downMbps")}>
                      <Input
                        type="number"
                        inputMode="decimal"
                        min={0.001}
                        step="any"
                        value={draft.downMbps}
                        onChange={(e) => set("downMbps", e.target.value)}
                      />
                    </Field>
                    <Field label="Upload speed (Mbps)" error={err("upMbps")}>
                      <Input
                        type="number"
                        inputMode="decimal"
                        min={0.001}
                        step="any"
                        value={draft.upMbps}
                        onChange={(e) => set("upMbps", e.target.value)}
                      />
                    </Field>
                    <Field
                      label="Data allowance (MB)"
                      hint={
                        errors.allowanceMb
                          ? "1 GB = 1024 MB."
                          : `${fmtData(terms.grace_data_quota_bytes)} in total for the whole grace. 1 GB = 1024 MB.`
                      }
                      error={err("allowanceMb")}
                      className="sm:col-span-2"
                    >
                      <Input
                        type="number"
                        inputMode="numeric"
                        min={1}
                        max={BOUNDS.quotaBytes.max / (1024 * 1024)}
                        step="any"
                        value={draft.allowanceMb}
                        onChange={(e) => set("allowanceMb", e.target.value)}
                      />
                    </Field>
                  </div>
                </SheetSection>

                <SheetSection title="Devices" description="Which of the guest's devices stay online during grace.">
                  <div role="radiogroup" aria-label="Device handling" className="grid gap-2">
                    {policyOptions.map((p) => (
                      <OptionCard
                        key={p}
                        name="grace-device-policy"
                        value={p}
                        checked={draft.devicePolicy === p}
                        onChange={(v) => set("devicePolicy", v)}
                        title={devicePolicyTitle(p)}
                        description={DEVICE_POLICY_TEXT[p]?.description}
                      />
                    ))}
                  </div>
                  <Field
                    label="Device limit"
                    hint="Recorded with each guest's grace for reporting. It never disconnects a device that was online at checkout, and it never lets a new device join."
                    error={err("deviceLimit")}
                  >
                    <Input
                      type="number"
                      inputMode="numeric"
                      min={BOUNDS.deviceLimit.min}
                      max={BOUNDS.deviceLimit.max}
                      step={1}
                      value={draft.deviceLimit}
                      onChange={(e) => set("deviceLimit", e.target.value)}
                    />
                  </Field>
                </SheetSection>

                <SheetSection
                  title="Eligibility"
                  actions={
                    <HelpTip title="Eligibility">
                      <HelpSection>
                        <p>
                          Every guest who still has active internet access when they check out qualifies — free, paid
                          or included with the room. A guest with no active access at checkout gets no grace. Each stay
                          receives grace once.
                        </p>
                        <p>
                          &ldquo;Stay rules after checkout&rdquo; is how long after checkout the stay still counts for
                          stay-based package rules.
                        </p>
                      </HelpSection>
                    </HelpTip>
                  }
                >
                  <Field
                    label="Stay rules after checkout"
                    hint="It never removes grace from a guest who qualifies."
                    error={err("eligibilityValue")}
                  >
                    <DurationInput
                      value={draft.eligibilityValue}
                      unit={draft.eligibilityUnit}
                      onValue={(v) => set("eligibilityValue", v)}
                      onUnit={(u) => set("eligibilityUnit", u)}
                      unitLabel="Stay rules after checkout unit"
                    />
                  </Field>
                </SheetSection>

                <section aria-label="Guest will receive" className="rounded-lg border border-primary/20 bg-primary-subtle/40 p-4">
                  <div className="text-2xs font-semibold uppercase tracking-widest text-muted-foreground">
                    Guest will receive
                  </div>
                  <p className="mt-1.5 text-sm leading-relaxed" data-testid="grace-preview">
                    {valid ? guestReceivesSentence(terms) : "Correct the highlighted fields to see what a guest will receive."}
                  </p>
                </section>
              </form>
            </SheetBody>
            <SheetFooter>
              <Button variant="ghost" onClick={() => onOpenChange(false)}>
                Cancel
              </Button>
              <Button type="submit" form="grace-terms" disabled={touched && !valid}>
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
                description={`Compared with what is in force now (${baseLabel}).`}
                actions={
                  <Badge tone={changedCount ? "accent" : "neutral"}>
                    {changedCount} change{changedCount === 1 ? "" : "s"}
                  </Badge>
                }
              >
                <ul aria-label="Policy changes" className="divide-y divide-border rounded-lg border border-border">
                  {changes.map((c) => (
                    <li
                      key={c.key}
                      className={cn(
                        "flex flex-wrap items-baseline justify-between gap-x-4 gap-y-1 px-3.5 py-2.5 text-sm",
                        c.changed && "bg-primary-subtle/30",
                      )}
                    >
                      <span className={cn("text-muted-foreground", c.changed && "font-medium text-foreground")}>
                        {c.label}
                      </span>
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

              <section aria-label="Guest will receive" className="rounded-lg border border-primary/20 bg-primary-subtle/40 p-4">
                <div className="text-2xs font-semibold uppercase tracking-widest text-muted-foreground">
                  Guest will receive
                </div>
                <p className="mt-1.5 text-sm leading-relaxed">{guestReceivesSentence(terms)}</p>
              </section>

              {nothingChanged ? (
                <Callout tone="info" title="Nothing to publish">
                  These terms are identical to version {version}. Go back and change something, or cancel.
                </Callout>
              ) : (
                <p className="text-xs text-muted-foreground">
                  This becomes version {version + 1}. It applies to future checkouts only — a guest already in grace
                  keeps the exact terms they were given at checkout.
                </p>
              )}

              <SheetSection title="Confirm">
                <Field label="Reason" hint="Recorded with this version in the policy history." error={fieldError === "reason" ? "Choose a reason." : undefined}>
                  <Select value={reasonChoice} onChange={(e) => setReasonChoice(e.target.value)}>
                    {REASON_CHOICES.filter((r) => published || r.code !== "POLICY_CHANGE").map((r) => (
                      <option key={r.code} value={r.code}>
                        {r.label}
                      </option>
                    ))}
                    <option value={OTHER_REASON}>Other…</option>
                  </Select>
                </Field>
                {reasonChoice === OTHER_REASON && (
                  <Field
                    label="Describe the reason"
                    hint={
                      reasonOk
                        ? `Recorded as ${reasonCode}`
                        : undefined
                    }
                    error={
                      !reasonOk && otherReason.trim() !== ""
                        ? "Use letters, numbers and spaces, starting with a letter."
                        : undefined
                    }
                  >
                    <Input
                      value={otherReason}
                      maxLength={80}
                      placeholder="e.g. late checkout season"
                      onChange={(e) => setOtherReason(e.target.value)}
                    />
                  </Field>
                )}
                <Field
                  label="Confirm your password"
                  hint="Publishing changes what every departing guest receives."
                  error={fieldError === "password" ? "Password not accepted." : undefined}
                >
                  <Input
                    type="password"
                    autoComplete="current-password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                  />
                </Field>
              </SheetSection>
            </SheetBody>
            <SheetFooter>
              <Button variant="ghost" onClick={() => setStep(0)} disabled={busy}>
                Back
              </Button>
              <Button
                onClick={publish}
                disabled={busy || !canWrite || !valid || !reasonOk || !password || nothingChanged}
              >
                {busy ? "Publishing…" : "Publish policy"}
              </Button>
            </SheetFooter>
          </>
        )}
      </SheetContent>
    </Sheet>
  );
}
