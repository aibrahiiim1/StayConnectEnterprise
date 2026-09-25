"use client";

// GUEST SIGN-IN PROTECTION — the three numbers a hotel administrator may change, on the screen where they
// already decide how guests sign in.
//
// WHY THESE ARE SETTINGS AT ALL. A threshold, a waiting period and an observation window are exactly the
// values a property discovers it has wrong at 22:00 on a full night — a conference floor of guests mistyping
// a surname, a resort with elderly guests who need longer to read the message. Compiled in, correcting them
// means a code change, a build and a deployment to fix a number. So they are persisted, site-scoped,
// permission-checked, validated and audited, and they take effect on the next submission.
//
// WHAT IS DELIBERATELY NOT HERE. There is no "enable protection" switch. A security control that must be
// switched on is switched off wherever nobody remembered, and the property that most needs it is the one that
// never opened this screen. Absence of a saved policy means the approved defaults, not "off".

import { useCallback, useEffect, useState } from "react";
import { ShieldAlert } from "lucide-react";
import { api, GuestSignInProtection } from "@/lib/api";
import { Card, CardBody, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { HelpSection, HelpTip } from "@/components/help";
import { Skeleton } from "@/components/ui/misc";
import { ReadOnlyNotice, SettingField } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { formatDate } from "@/lib/utils";

type Draft = { max: string; window: string; restriction: string };

// The approved defaults a site runs on until it saves a policy of its own. The allowed range for each comes
// from the SERVER's response, so the form and the thing that enforces it cannot drift apart.
const STANDARD = { max: 5, window: 60, restriction: 60 } as const;

function draftOf(p: GuestSignInProtection): Draft {
  return {
    max: String(p.max_failed_attempts),
    window: String(p.observation_window_seconds),
    restriction: String(p.restriction_seconds),
  };
}

/** A whole number inside the server's bounds, or the sentence that says it is not. */
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

  async function save() {
    setSaving(true);
    setNote("");
    try {
      const next = await api.put<GuestSignInProtection>("/guest-signin-protection", {
        max_failed_attempts: Number(draft!.max),
        observation_window_seconds: Number(draft!.window),
        restriction_seconds: Number(draft!.restriction),
      });
      setPolicy(next);
      setDraft(draftOf(next));
      setErr(null);
      setNote("Saved. The next sign-in attempt is judged by these numbers — nothing needs restarting.");
      toast.success("Protection settings saved", "The next sign-in attempt is judged by these numbers.");
    } catch (e) {
      setErr(e);
    } finally {
      setSaving(false);
    }
  }

  const readOnly = !canWrite || saving;
  const lc = policy.last_change;

  return (
    <Card>
      <CardHeader className="items-start">
        <div className="min-w-0 space-y-1">
          <CardTitle className="flex flex-wrap items-center gap-2">
            <ShieldAlert className="size-4 text-muted-foreground" aria-hidden /> Guest sign-in protection
            <HelpTip title="Guest sign-in protection">
              <HelpSection>
                <p>
                  After too many incorrect sign-in details from the <strong>same device</strong>, that device is
                  asked to wait before it can try again. It protects guests from someone working through room
                  numbers, and it is always on — these settings decide how strict it is, not whether it runs.
                </p>
              </HelpSection>
            </HelpTip>
            {policy.is_default && <Badge tone="default">Using the standard settings</Badge>}
          </CardTitle>
        </div>
      </CardHeader>
      <CardBody className="space-y-5">
        {!canWrite && (
          // The desk cannot retune protection: reception may release one device, only IT changes the thresholds.
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

        {/* SAID PLAINLY, BECAUSE IT IS THE ONE THING AN OPERATOR WILL EXPECT TO WORK THE OTHER WAY. Somebody
            shortening the wait to help a guest who is waiting right now would otherwise watch nothing happen
            and assume the setting is broken. */}
        <div className="space-y-1 rounded-md border border-border bg-surface px-3.5 py-3 text-sm text-muted-foreground">
          <p>
            New settings apply to <strong>what happens next</strong>. A device already waiting keeps the time it
            was given — shortening the wait here does not end a wait already running.
          </p>
          <p>
            To let one guest try again now, use <strong>Release</strong> on the Active restrictions tab of Guest
            sign-in attempts. Releasing allows another attempt; it does not sign anyone in.
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
          <Button onClick={() => void save()} disabled={!dirty || saving || anyInvalid}>
            {saving ? "Saving…" : "Save protection settings"}
          </Button>
        </CardFooter>
      )}
    </Card>
  );
}
