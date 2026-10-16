"use client";

// EMAIL ACCOUNTS — THE TERMS (contract docs/architecture/ONEGATE_EMAIL_ACCOUNTS_SELF_SERVICE.md §6, migration 0117).
//
// How long a client who registered with their email address has to verify it, what happens if they do not, how
// often they may ask for the email again, the one-time extra hour, and how many new accounts one device may open
// a day. A persisted, per-site, audited setting (docs/OPERATIONAL_SETTINGS_POLICY.md): every range, unit and
// default on this card comes from the appliance (edged GET /self-service-settings), so the browser never offers a
// value the appliance would refuse. A save takes a reason and the operator's password, and is recorded in the
// change history shown below the form.
//
// Who may change it is who may change the sign-in methods (the auth-methods key); every role that may see the
// methods sees the terms.

import * as React from "react";
import { History, Mail, RefreshCw } from "lucide-react";
import {
  api,
  type SelfServiceBound,
  type SelfServiceChange,
  type SelfServiceSettings,
  type SelfServiceSettingsResp,
} from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { ErrorBanner } from "@/components/ui/error-banner";
import { Field, Input, Select } from "@/components/ui/input";
import { Timeline } from "@/components/ui/data";
import { Skeleton, Switch } from "@/components/ui/misc";
import { SettingField } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { formatRelative } from "@/lib/utils";

type NumericKey = Exclude<keyof SelfServiceSettings, "verification_required" | "suspension_policy">;

/** The words for each setting. The numbers (range, unit, default) are the server's. */
export const SELF_SERVICE_WORDS: Record<NumericKey, { label: string; explanation: string }> = {
  verification_deadline_hours: {
    label: "Time to verify",
    explanation: "How long a new account works before its email address must be verified, counted from registration.",
  },
  reminder_hours_before: {
    label: "Reminder before the deadline",
    explanation: "One reminder email this long before the deadline. 0 = no reminder. Must be less than the time to verify.",
  },
  resend_cooldown_seconds: {
    label: "Wait between verification emails",
    explanation: "How long a client waits before they can ask for the verification email again.",
  },
  resend_max_per_day: {
    label: "Verification emails per day",
    explanation: "The most verification emails one account can ask for in a day.",
  },
  grace_minutes: {
    label: "Extra time when suspended",
    explanation: "How long “Allow Internet for 1 More Hour” lifts the suspension. It asks for the password again and adds no package time.",
  },
  max_grace_extensions: {
    label: "Extra time allowed per account",
    explanation: "How many times one account may use it. 0 = never offered.",
  },
  registrations_per_device_per_day: {
    label: "New accounts per device per day",
    explanation: "Stops one device opening account after account to take free packages again.",
  },
};

const POLICY_WORDS: Record<string, string> = {
  SUSPEND: "Suspend Internet until the address is verified",
  WARN_ONLY: "Only remind; never suspend",
};

const ALL_KEYS: (keyof SelfServiceSettings)[] = [
  "verification_required", "verification_deadline_hours", "suspension_policy", "reminder_hours_before",
  "resend_cooldown_seconds", "resend_max_per_day", "grace_minutes", "max_grace_extensions",
  "registrations_per_device_per_day",
];

const LABELS: Record<keyof SelfServiceSettings, string> = {
  verification_required: "Verification required",
  suspension_policy: "When not verified",
  ...Object.fromEntries(Object.entries(SELF_SERVICE_WORDS).map(([k, w]) => [k, w.label])),
} as Record<keyof SelfServiceSettings, string>;

function isResp(v: unknown): v is SelfServiceSettingsResp {
  const r = v as SelfServiceSettingsResp | null;
  return !!r && !!r.settings && typeof r.settings.verification_deadline_hours === "number" &&
    !!r.limits && Array.isArray(r.limits.bounds) && r.limits.bounds.length > 0;
}

function valueWords(key: keyof SelfServiceSettings, v: unknown): string {
  if (key === "verification_required") return v ? "on" : "off";
  if (key === "suspension_policy") return v === "WARN_ONLY" ? "remind only" : "suspend";
  return String(v);
}

/** One change-log row in words: the values that changed, old → new, as far as the row carries them. */
export function changeSummary(c: SelfServiceChange): string {
  const parts: string[] = [];
  for (const k of ALL_KEYS) {
    const nv = c[`new_${k}`];
    if (nv === undefined) continue;
    const ov = c[`old_${k}`];
    if (ov === undefined || ov === null) continue;
    if (ov !== nv) parts.push(`${LABELS[k]}: ${valueWords(k, ov)} → ${valueWords(k, nv)}`);
  }
  if (parts.length) return parts.join("; ");
  const first = ALL_KEYS.every((k) => c[`old_${k}`] === undefined || c[`old_${k}`] === null);
  return first ? "First saved settings" : "Settings saved";
}

/** The same bound the server applies to a reason (4-500 characters). */
function reasonProblem(reason: string): string | null {
  const n = reason.trim().length;
  if (n < 4) return "Give a reason of at least 4 characters. It is recorded with your name.";
  if (n > 500) return "Keep the reason under 500 characters.";
  return null;
}

export type Draft = { required: boolean; policy: string; nums: Record<NumericKey, string> };

function draftOf(s: SelfServiceSettings): Draft {
  const nums = {} as Record<NumericKey, string>;
  for (const k of Object.keys(SELF_SERVICE_WORDS) as NumericKey[]) nums[k] = String(s[k]);
  return { required: s.verification_required, policy: s.suspension_policy, nums };
}

/** The first problem with a draft, checked against the server's bounds; null when it can be saved. */
export function draftProblem(d: Draft, bounds: SelfServiceBound[]): string | null {
  for (const b of bounds) {
    if (!(b.key in d.nums)) continue;
    const raw = d.nums[b.key as NumericKey];
    const n = Number(raw);
    if (raw.trim() === "" || !Number.isInteger(n) || n < b.min || n > b.max) {
      return `${SELF_SERVICE_WORDS[b.key as NumericKey]?.label ?? b.key} must be a whole number from ${b.min} to ${b.max} ${b.unit}.`;
    }
  }
  if (Number(d.nums.reminder_hours_before) >= Number(d.nums.verification_deadline_hours)) {
    return "The reminder must come before the deadline: make it shorter than the time to verify, or 0 for no reminder.";
  }
  return null;
}

export function EmailAccountsSettingsCard({ canEdit }: { canEdit: boolean }) {
  const toast = useToast();
  const [data, setData] = React.useState<SelfServiceSettingsResp | null>(null);
  const [unavailable, setUnavailable] = React.useState(false);
  const [loadErr, setLoadErr] = React.useState<unknown>(null);
  const [draft, setDraft] = React.useState<Draft | null>(null);
  const [reason, setReason] = React.useState("");
  const [password, setPassword] = React.useState("");
  const [busy, setBusy] = React.useState(false);
  const [saveErr, setSaveErr] = React.useState<unknown>(null);

  const load = React.useCallback(() => {
    setLoadErr(null);
    setUnavailable(false);
    api
      .get<SelfServiceSettingsResp>("/self-service-settings/")
      .then((r) => {
        if (!isResp(r)) { setUnavailable(true); return; }
        setData(r);
        setDraft(draftOf(r.settings));
      })
      .catch(setLoadErr);
  }, []);
  React.useEffect(load, [load]);

  const bounds = data?.limits.bounds ?? [];
  const boundOf = (k: NumericKey) => bounds.find((b) => b.key === k);
  const stored = data ? draftOf(data.settings) : null;
  const dirty = !!draft && !!stored && JSON.stringify(draft) !== JSON.stringify(stored);
  const problem = draft ? draftProblem(draft, bounds) : null;
  const reasonErr = reason === "" ? null : reasonProblem(reason);
  const editable = canEdit && !busy && !data?.awaiting_assignment;

  async function save() {
    if (!draft || problem) return;
    const bad = reasonProblem(reason);
    if (bad) { setSaveErr(new Error(bad)); return; }
    setBusy(true); setSaveErr(null);
    try {
      const body: Record<string, unknown> = {
        verification_required: draft.required,
        suspension_policy: draft.policy,
        reason: reason.trim(),
        password,
      };
      for (const k of Object.keys(draft.nums) as NumericKey[]) body[k] = Number(draft.nums[k]);
      const out = await api.put<{ notice?: string }>("/self-service-settings/", body);
      toast.success("Email accounts settings saved", out?.notice);
      setReason(""); setPassword("");
      load();
    } catch (e) {
      setSaveErr(e);
    } finally {
      setBusy(false);
      setPassword("");
    }
  }

  return (
    <Card>
      <CardHeader className="items-start">
        <div className="min-w-0 space-y-1">
          <CardTitle className="flex items-center gap-2 [&_svg]:size-4"><Mail aria-hidden /> Email accounts: verification and limits</CardTitle>
          <CardDescription>
            A client who registers with an email address and a password can use the Internet straight away. They then
            have a set time to confirm the address from the email they are sent. These settings decide that time, what
            happens if they do not, and how often they may ask for help.
          </CardDescription>
        </div>
      </CardHeader>
      <CardBody className="space-y-4">
        {loadErr ? (
          <div className="space-y-2">
            <ErrorBanner err={loadErr} />
            <Button size="sm" variant="secondary" onClick={load}><RefreshCw /> Try again</Button>
          </div>
        ) : unavailable ? (
          <p className="text-sm text-muted-foreground">This appliance does not report Email accounts settings.</p>
        ) : !data || !draft ? (
          <Skeleton className="h-40" />
        ) : (
          <form
            className="space-y-4"
            onSubmit={(e) => { e.preventDefault(); void save(); }}
            aria-label="Email accounts settings"
          >
            <div className="flex flex-wrap items-center justify-between gap-3 rounded-md border border-border px-3.5 py-2.5">
              <div className="min-w-0 space-y-0.5">
                <p className="text-sm font-medium">Require email verification</p>
                <p className="text-xs text-muted-foreground">
                  Off: accounts never need to be verified, and nobody is suspended.
                </p>
              </div>
              {canEdit ? (
                <Switch
                  checked={draft.required}
                  disabled={!editable}
                  onCheckedChange={(v) => setDraft({ ...draft, required: v })}
                  label="Require email verification"
                />
              ) : (
                <span className="text-sm">{draft.required ? "On" : "Off"}</span>
              )}
            </div>

            <div className="grid gap-4 md:grid-cols-2">
              <Field
                label="When the address is not verified in time"
                hint="Suspending ends the account's Internet sessions until the client verifies, or uses the extra time. Nothing is deleted and the package keeps its time and data."
              >
                <Select
                  aria-label="When the address is not verified in time"
                  value={draft.policy}
                  disabled={!editable || !draft.required}
                  onChange={(e) => setDraft({ ...draft, policy: e.target.value })}
                >
                  {(data.limits.suspension_policies?.length ? data.limits.suspension_policies : ["SUSPEND", "WARN_ONLY"]).map((p) => (
                    <option key={p} value={p}>{POLICY_WORDS[p] ?? p}</option>
                  ))}
                </Select>
              </Field>
              {(Object.keys(SELF_SERVICE_WORDS) as NumericKey[]).map((k) => {
                const b = boundOf(k);
                if (!b) return null;
                return (
                  <SettingField
                    key={k}
                    label={SELF_SERVICE_WORDS[k].label}
                    value={draft.nums[k]}
                    onChange={(v) => setDraft({ ...draft, nums: { ...draft.nums, [k]: v } })}
                    unit={b.unit}
                    min={b.min}
                    max={b.max}
                    defaultValue={b.default}
                    readOnly={!editable}
                    explanation={SELF_SERVICE_WORDS[k].explanation}
                  />
                );
              })}
            </div>

            <p className="text-xs text-muted-foreground" role="note">
              A changed time to verify applies to accounts registered after the change; accounts already registered keep
              the deadline they were given. Switching verification off, or choosing to only remind, applies to every
              account at once.
            </p>

            {problem && <p className="text-xs text-destructive" role="alert">{problem}</p>}

            {canEdit && !data.awaiting_assignment && (
              <div className="grid gap-4 md:grid-cols-2">
                <Field label="Reason for the change" error={reasonErr ?? undefined} hint="Recorded in the history below." required>
                  <Input value={reason} maxLength={500} disabled={busy} onChange={(e) => setReason(e.target.value)}
                    aria-label="Reason for the change" placeholder="Guests at the conference need longer" />
                </Field>
                <Field label="Confirm your password" required hint="Changing these settings asks for your password.">
                  <Input type="password" autoComplete="current-password" value={password} disabled={busy}
                    onChange={(e) => setPassword(e.target.value)} aria-label="Confirm your password" />
                </Field>
              </div>
            )}
            {saveErr ? <ErrorBanner err={saveErr} /> : null}
            {canEdit && !data.awaiting_assignment && (
              <div className="flex items-center gap-3">
                <Button type="submit" disabled={!dirty || !!problem || reason.trim().length < 4 || password === "" || busy}>
                  {busy ? "Saving…" : "Save Email accounts settings"}
                </Button>
                {dirty && !busy && <span className="text-xs text-muted-foreground">Not saved yet.</span>}
              </div>
            )}
            {!canEdit && <p className="text-xs text-muted-foreground">Your role can see the Email accounts settings but not change them.</p>}
            {data.awaiting_assignment && (
              <p className="text-xs text-muted-foreground">This appliance has no site yet, so these are the defaults and nothing can be saved.</p>
            )}

            <div className="space-y-2">
              <p className="inline-flex items-center gap-2 text-sm font-medium">
                <History className="size-4" aria-hidden /> Change history
              </p>
              <p className="text-xs text-muted-foreground">
                {data.config_version === 0 || !data.updated_at
                  ? "Not changed yet: the defaults are in use."
                  : `Last changed ${formatRelative(data.updated_at)}${data.updated_by ? ` by ${data.updated_by}` : ""}.`}
              </p>
              {(data.changes ?? []).length > 0 && (
                <Timeline
                  items={(data.changes ?? []).map((c, i) => ({
                    key: `${c.changed_at}-${i}`,
                    title: changeSummary(c),
                    when: formatRelative(c.changed_at),
                    body: [c.changed_by, c.change_reason && `“${c.change_reason}”`].filter(Boolean).join(" · "),
                    tone: "info" as const,
                  }))}
                />
              )}
            </div>
          </form>
        )}
      </CardBody>
    </Card>
  );
}
