"use client";

// Sign-in protection is always on; sites without saved policy use approved defaults.

import { useCallback, useEffect, useState } from "react";
import { api, ApiError, GuestSignInProtection } from "@/lib/api";
import { ConfirmDialog } from "@/components/ui/dialog";
import { REASON_MAX_CHARS, REASON_MIN_CHARS } from "@/lib/reason";
import { Card, CardBody, CardFooter, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { HelpSection, HelpTip } from "@/components/help";
import { Skeleton } from "@/components/ui/misc";
import { ReadOnlyNotice, SettingField } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { formatDate } from "@/lib/utils";

type Draft = { max: string; window: string; restriction: string };

// Read allowed ranges from the server so the form matches enforcement.
const STANDARD = { max: 5, window: 60, restriction: 60 } as const;

function draftOf(p: GuestSignInProtection): Draft {
  return {
    max: String(p.max_failed_attempts),
    window: String(p.observation_window_seconds),
    restriction: String(p.restriction_seconds),
  };
}

function invalid(value: string, min: number, max: number): string | undefined {
  const n = Number(value);
  const bad = value.trim() === "" || !Number.isInteger(n) || n < min || n > max;
  return bad ? `Enter a whole number between ${min} and ${max}.` : undefined;
}

export function GuestSignInProtectionCard({ canWrite }: { canWrite: boolean }) {
  const toast = useToast();
  const [policy, setPolicy] = useState<GuestSignInProtection | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [saving, setSaving] = useState(false);
  const [note, setNote] = useState("");
  // Site-wide changes require reason and operator password.
  const [confirming, setConfirming] = useState(false);
  const [saveErr, setSaveErr] = useState<unknown>(null);

  const load = useCallback(async () => {
    try {
      const p = await api.get<GuestSignInProtection>("/guest-signin-protection");
      setPolicy(p);
      setDraft(draftOf(p));
      setErr(null);
    } catch (e) {
      setErr(e);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  if (err && !policy) return <ErrorBanner err={err} />;
  if (!policy || !draft) return <Skeleton className="h-64 w-full" />;

  const lim = policy.limits;
  const dirty =
    draft.max !== String(policy.max_failed_attempts) ||
    draft.window !== String(policy.observation_window_seconds) ||
    draft.restriction !== String(policy.restriction_seconds);
  const errors = {
    max: invalid(draft.max, lim.min_failed_attempts, lim.max_failed_attempts),
    window: invalid(draft.window, lim.min_observation_window_seconds, lim.max_observation_window_seconds),
    restriction: invalid(draft.restriction, lim.min_restriction_seconds, lim.max_restriction_seconds),
  };
  const anyInvalid = !!(errors.max || errors.window || errors.restriction);

  async function save({ reason, password }: { reason: string; password: string }) {
    setSaving(true);
    setNote("");
    setSaveErr(null);
    try {
      const next = await api.put<GuestSignInProtection>("/guest-signin-protection", {
        max_failed_attempts: Number(draft!.max),
        observation_window_seconds: Number(draft!.window),
        restriction_seconds: Number(draft!.restriction),
        reason: reason.trim(),
        password,
      });
      setPolicy(next);
      setDraft(draftOf(next));
      setErr(null);
      setConfirming(false);
      setNote("Saved. The next sign-in attempt is judged by these numbers — nothing needs restarting.");
      toast.success("Protection settings saved", "The next sign-in attempt is judged by these numbers.");
    } catch (e) {
      setSaveErr(e instanceof ApiError && e.code === "reauth_required" ? new Error("Your password was not accepted.") : e);
    } finally {
      setSaving(false);
    }
  }

  const readOnly = !canWrite || saving;
  const lc = policy.last_change;

  return (
    <Card>
      <CardHeader>
        <div className="min-w-0">
          <CardTitle className="flex flex-wrap items-center gap-2">
            Client sign-in protection
            <HelpTip title="Client sign-in protection">
              <HelpSection>
                <p>
                  After too many incorrect <strong>Room sign-in</strong> details (a room number and the detail
                  from the booking) from the <strong>same device</strong>, that device is asked to wait before it can
                  try again. It protects clients from someone working through room numbers, and it is always on —
                  these settings decide how strict it is, not whether it runs. Other sign-in methods are not counted
                  here.
                </p>
              </HelpSection>
            </HelpTip>
            {policy.is_default && <Badge tone="default">Using the standard settings</Badge>}
          </CardTitle>
          {/* Make clear that default settings still protect sign-in. */}
          <CardDescription>
            It is always on — these settings decide how strict it is, not whether it runs.
          </CardDescription>
        </div>
      </CardHeader>
      <CardBody className="space-y-5">
        {!canWrite && (
          // Reception may release a device; only IT may change thresholds.
          <ReadOnlyNotice>Your role can see these settings but not change them.</ReadOnlyNotice>
        )}
        {err ? <ErrorBanner err={err} className="mb-0" /> : null}
        {note && !dirty && <Callout tone="success">{note}</Callout>}

        <div className="grid gap-5 sm:grid-cols-3">
          <SettingField
            id="gsp-maximum-failed-attempts"
            label="Maximum failed attempts"
            unit="attempts"
            value={draft.max}
            min={lim.min_failed_attempts}
            max={lim.max_failed_attempts}
            defaultValue={STANDARD.max}
            readOnly={readOnly}
            error={canWrite ? errors.max : undefined}
            onChange={(v) => setDraft({ ...draft, max: v })}
            explanation="Incorrect sign-ins from one device before it has to wait."
          />
          <SettingField
            id="gsp-observation-window"
            label="Observation window"
            unit="seconds"
            value={draft.window}
            min={lim.min_observation_window_seconds}
            max={lim.max_observation_window_seconds}
            defaultValue={STANDARD.window}
            readOnly={readOnly}
            error={canWrite ? errors.window : undefined}
            onChange={(v) => setDraft({ ...draft, window: v })}
            explanation="Attempts older than this stop counting. The window moves continuously, so it cannot be sidestepped by waiting for a clock boundary."
          />
          <SettingField
            id="gsp-wait-after-too-many-attempts"
            label="Wait after too many attempts"
            unit="seconds"
            value={draft.restriction}
            min={lim.min_restriction_seconds}
            max={lim.max_restriction_seconds}
            defaultValue={STANDARD.restriction}
            readOnly={readOnly}
            error={canWrite ? errors.restriction : undefined}
            onChange={(v) => setDraft({ ...draft, restriction: v })}
            explanation="How long the device is asked to wait. Further attempts during the wait do not extend it."
          />
        </div>

        {/* New wait settings do not shorten a restriction already running. */}
        <div className="space-y-1 rounded-md border border-border bg-surface px-3.5 py-2.5 text-xs text-muted-foreground">
          <p>
            New settings apply to <strong>what happens next</strong>. A device already waiting keeps the time it
            was given — shortening the wait here does not end a wait already running.
          </p>
          <p>
            To let one client try again now, use <strong>Release</strong> on the Active restrictions tab of Guest
            sign-in attempts, under Hotel. Releasing allows another attempt; it does not sign anyone in.
          </p>
        </div>

        {lc && (
          <p className="text-xs text-muted-foreground">
            Last changed {formatDate(lc.changed_at)} by {lc.changed_by}
            {lc.old_max_failed_attempts != null && (
              <>
                {" "}— from {lc.old_max_failed_attempts} attempts / {lc.old_observation_window_seconds}s /{" "}
                {lc.old_restriction_seconds}s
              </>
            )}
            {" "}to {lc.new_max_failed_attempts} attempts / {lc.new_observation_window_seconds}s /{" "}
            {lc.new_restriction_seconds}s
            {lc.reason ? ` — ${lc.reason}` : ""}
          </p>
        )}
      </CardBody>
      {canWrite && (
        <CardFooter className="justify-end">
          {dirty && !saving && (
            <Button variant="ghost" onClick={() => setDraft(draftOf(policy))}>Discard</Button>
          )}
          <Button onClick={() => { setSaveErr(null); setConfirming(true); }} disabled={!dirty || saving || anyInvalid}>
            {saving ? "Saving…" : "Save protection settings"}
          </Button>
        </CardFooter>
      )}
      <ConfirmDialog
        open={confirming}
        onOpenChange={(v) => !v && !saving && setConfirming(false)}
        title="Change client sign-in protection?"
        description={`From the next attempt: ${draft.max} attempts within ${draft.window} seconds, then a ${draft.restriction}-second wait.`}
        consequences={["Applies to every client on this site until it is changed again. Waits already running are not shortened."]}
        confirmLabel="Save protection settings"
        requireReason
        reasonMinLength={REASON_MIN_CHARS}
        reasonMaxLength={REASON_MAX_CHARS}
        reasonPlaceholder="For example: conference floor, guests kept mistyping the surname"
        requirePassword
        busy={saving}
        error={saveErr}
        onConfirm={save}
      />
    </Card>
  );
}
