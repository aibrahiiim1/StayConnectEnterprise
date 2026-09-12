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
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { ErrorBanner } from "@/components/ui/error-banner";
import { formatDate } from "@/lib/utils";

type Draft = { max: string; window: string; restriction: string };

function draftOf(p: GuestSignInProtection): Draft {
  return {
    max: String(p.max_failed_attempts),
    window: String(p.observation_window_seconds),
    restriction: String(p.restriction_seconds),
  };
}

export function GuestSignInProtectionCard({ canWrite }: { canWrite: boolean }) {
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
  if (!policy || !draft) return null;

  const lim = policy.limits;
  const dirty =
    draft.max !== String(policy.max_failed_attempts) ||
    draft.window !== String(policy.observation_window_seconds) ||
    draft.restriction !== String(policy.restriction_seconds);

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
    } catch (e) {
      setErr(e);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          <ShieldAlert size={16} /> Guest sign-in protection
          {policy.is_default && <Badge tone="default">Using the standard settings</Badge>}
        </CardTitle>
      </CardHeader>
      <CardBody className="space-y-4">
        <p className="text-sm text-muted-foreground max-w-2xl">
          After too many incorrect sign-in details from the <strong>same device</strong>, that device is asked
          to wait before it can try again. It protects guests from someone working through room numbers, and it
          is always on — these settings decide how strict it is, not whether it runs.
        </p>

        {err ? <ErrorBanner err={err} /> : null}
        {note && <p className="text-sm text-success-subtle-foreground" role="status">{note}</p>}

        <div className="grid gap-4 sm:grid-cols-3">
          <Field
            label="Maximum failed attempts"
            unit="attempts"
            value={draft.max}
            min={lim.min_failed_attempts}
            max={lim.max_failed_attempts}
            disabled={!canWrite || saving}
            onChange={(v) => setDraft({ ...draft, max: v })}
            help={`How many incorrect sign-ins from one device are allowed before it has to wait. Standard: 5. Allowed: ${lim.min_failed_attempts}–${lim.max_failed_attempts}.`}
          />
          <Field
            label="Observation window"
            unit="seconds"
            value={draft.window}
            min={lim.min_observation_window_seconds}
            max={lim.max_observation_window_seconds}
            disabled={!canWrite || saving}
            onChange={(v) => setDraft({ ...draft, window: v })}
            help={`Attempts older than this stop counting. The window moves continuously, so it cannot be sidestepped by waiting for a clock boundary. Standard: 60. Allowed: ${lim.min_observation_window_seconds}–${lim.max_observation_window_seconds}.`}
          />
          <Field
            label="Wait after too many attempts"
            unit="seconds"
            value={draft.restriction}
            min={lim.min_restriction_seconds}
            max={lim.max_restriction_seconds}
            disabled={!canWrite || saving}
            onChange={(v) => setDraft({ ...draft, restriction: v })}
            help={`How long the device is asked to wait. Further attempts during the wait do not extend it. Standard: 60. Allowed: ${lim.min_restriction_seconds}–${lim.max_restriction_seconds}.`}
          />
        </div>

        {/* SAID PLAINLY, BECAUSE IT IS THE ONE THING AN OPERATOR WILL EXPECT TO WORK THE OTHER WAY. Somebody
            shortening the wait to help a guest who is waiting right now would otherwise watch nothing happen
            and assume the setting is broken. */}
        <div className="rounded-md border border-border bg-muted/30 p-3 text-sm text-muted-foreground">
          <p>
            New settings apply to <strong>what happens next</strong>. A device already waiting keeps the time it
            was given — shortening the wait here does not end a wait already running.
          </p>
          <p className="mt-1">
            To let one guest try again now, use <strong>Release</strong> on the Active restrictions tab of Guest
            sign-in attempts. Releasing allows another attempt; it does not sign anyone in.
          </p>
        </div>

        {policy.last_change && (
          <p className="text-xs text-muted-foreground">
            Last changed {formatDate(policy.last_change.changed_at)} by {policy.last_change.changed_by}
            {policy.last_change.old_max_failed_attempts != null && (
              <>
                {" "}— from {policy.last_change.old_max_failed_attempts} attempts /{" "}
                {policy.last_change.old_observation_window_seconds}s /{" "}
                {policy.last_change.old_restriction_seconds}s
              </>
            )}
            {" "}to {policy.last_change.new_max_failed_attempts} attempts /{" "}
            {policy.last_change.new_observation_window_seconds}s /{" "}
            {policy.last_change.new_restriction_seconds}s
            {policy.last_change.reason ? ` — ${policy.last_change.reason}` : ""}
          </p>
        )}

        {canWrite ? (
          <div className="flex items-center gap-2">
            <Button onClick={() => void save()} disabled={!dirty || saving}>
              {saving ? "Saving…" : "Save protection settings"}
            </Button>
            {dirty && !saving && (
              <Button variant="secondary" onClick={() => setDraft(draftOf(policy))}>Discard</Button>
            )}
          </div>
        ) : (
          // The server refuses the write regardless; this only avoids offering a button that 403s.
          <p className="text-sm text-muted-foreground">
            Your role can see these settings but not change them.
          </p>
        )}
      </CardBody>
    </Card>
  );
}

// Field is a bounded number input. The bounds come from the SERVER's response rather than being written here,
// so the form and the thing that enforces them cannot drift apart — and the unit is in the label because
// "60" alone has been read as minutes more than once.
function Field({
  label, unit, value, min, max, disabled, onChange, help,
}: {
  label: string; unit: string; value: string; min: number; max: number;
  disabled: boolean; onChange: (v: string) => void; help: string;
}) {
  const n = Number(value);
  const bad = value.trim() === "" || !Number.isInteger(n) || n < min || n > max;
  // The label is ASSOCIATED with the input, not merely printed above it. A number field whose label is
  // decorative is a field a screen reader announces as "edit text, blank" — and on this screen the label is
  // the only thing distinguishing three identical boxes of seconds and attempts.
  const id = "gsp-" + label.toLowerCase().replace(/[^a-z0-9]+/g, "-");
  return (
    <div className="space-y-1">
      <label htmlFor={id} className="block text-sm font-medium">
        {label} <span className="font-normal text-muted-foreground">({unit})</span>
      </label>
      <Input
        id={id}
        type="number"
        inputMode="numeric"
        value={value}
        min={min}
        max={max}
        step={1}
        disabled={disabled}
        aria-invalid={bad}
        aria-describedby={`${id}-help`}
        onChange={(e) => onChange(e.target.value)}
        className={bad ? "border-danger" : ""}
      />
      <p id={`${id}-help`} className="text-xs text-muted-foreground">{help}</p>
      {bad && (
        <p className="text-xs text-danger" role="alert">
          Enter a whole number between {min} and {max}.
        </p>
      )}
    </div>
  );
}
