"use client";

import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { ChevronDown, MailWarning } from "lucide-react";
import { api } from "@/lib/api";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Callout } from "@/components/ui/error-banner";
import { Switch } from "@/components/ui/misc";
import { SettingField } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { formatDate } from "@/lib/utils";
import { moduleLicensed, useCapabilities } from "@/lib/capabilities";
import {
  resetMailWords, resetSettingsError, resetSummary,
  type ResetBound, type ResetNumberKey, type ResetPolicy, type ResetPolicyAnswer,
} from "@/lib/password-reset";

const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";
const ENDPOINT = "/operators/password-reset";

type Draft = { enabled: boolean; allow_last_site_admin: boolean; numbers: Record<ResetNumberKey, string> };

function draftOf(p: ResetPolicy, bounds: ResetBound[]): Draft {
  return {
    enabled: p.enabled,
    allow_last_site_admin: p.allow_last_site_admin,
    numbers: Object.fromEntries(bounds.map((b) => [b.key, String(p[b.key])])) as Record<ResetNumberKey, string>,
  };
}

export function OperatorPasswordReset({ writable }: { writable: boolean }) {
  const toast = useToast();
  const emailLicensed = moduleLicensed(useCapabilities(), "email_otp");
  const [data, setData] = useState<ResetPolicyAnswer | null>(null);
  const [draft, setDraft] = useState<Draft | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const r = await api.get<ResetPolicyAnswer>(ENDPOINT);
      if (!r?.settings || !Array.isArray(r.bounds) || !r.mail) return;
      setData(r);
      setDraft(draftOf(r.settings, r.bounds));
    } catch {
      setData(null);
    }
  }, []);
  useEffect(() => { void load(); }, [load]);

  if (!data || !draft) return null;
  const { settings, bounds, mail } = data;
  const number = (k: ResetNumberKey) => {
    const raw = draft.numbers[k];
    return raw.trim() === "" ? settings[k] : Number(raw);
  };
  const dirty = draft.enabled !== settings.enabled || draft.allow_last_site_admin !== settings.allow_last_site_admin
    || bounds.some((b) => number(b.key) !== settings[b.key]);
  const outOfRange = bounds.some((b) => { const v = number(b.key); return !Number.isInteger(v) || v < b.min || v > b.max; });
  const mailWords = resetMailWords(mail);
  const provenance = settings.config_version === 0
    ? "Nothing has been saved yet, so the defaults below are in force."
    : settings.updated_at
      ? `Last changed ${formatDate(settings.updated_at)}${settings.updated_by ? ` by ${settings.updated_by}` : ""}.`
      : null;

  async function save({ password }: { reason: string; password: string }) {
    if (!draft) return;
    setSaving(true); setErr(null);
    try {
      const r = await api.put<{ notice?: string }>(ENDPOINT, {
        enabled: draft.enabled,
        allow_last_site_admin: draft.allow_last_site_admin,
        ...Object.fromEntries(bounds.map((b) => [b.key, number(b.key)])),
        expected_version: settings.config_version,
        password,
      });
      toast.success("Password reset settings saved", r?.notice);
      setConfirming(false);
      await load();
    } catch (e) {
      setErr(resetSettingsError(e));
    } finally {
      setSaving(false);
    }
  }

  return (
    <Card>
      <details className="group">
        <summary className="flex cursor-pointer select-none flex-wrap items-center gap-x-2 gap-y-1 rounded-lg px-5 py-3.5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden">
          <ChevronDown className="size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-180" aria-hidden />
          <span className="text-emphasis">Forgot password by email</span>
          {mailWords && <MailWarning className="size-4 shrink-0 text-warning" aria-label="Needs an SMTP mail server" />}
          <span className="ms-auto hidden text-caption text-muted-foreground sm:inline">{resetSummary(settings, mail)}</span>
        </summary>
        <div className="space-y-5 border-t border-border px-5 py-4">
          <p className="max-w-3xl text-sm text-muted-foreground">
            Lets staff who forgot their password get an 8-digit code by email and choose a new one from the sign-in
            page. The code goes only to the email address on their account and only through the site&rsquo;s own SMTP
            mail server. An account with an authenticator app still needs its code. After a reset every session of
            the account is signed out, a notice goes to the same address, and the reset is recorded in Activity.
            {provenance && <> {provenance}</>}
          </p>

          {mailWords && (
            <div className={FADE}>
              <Callout tone="warning" title="Forgot password is off at sign-in">
                <p>{mailWords}</p>
                <p className="mt-1">
                  {emailLicensed ? (
                    <>Set it up under <Link href="/notifications" className="font-medium underline underline-offset-2">Delivery</Link>, then send a test message. Until then, a site admin resets forgotten passwords below.</>
                  ) : (
                    <>Email delivery comes with the Email sign-in module, which this site&rsquo;s licence does not include. Ask your OneGate vendor to add it. Until then, a site admin resets forgotten passwords below.</>
                  )}
                </p>
              </Callout>
            </div>
          )}

          <div className="space-y-3">
            <div className="flex items-start gap-3 text-sm">
              <Switch
                id="reset-enabled"
                checked={draft.enabled}
                disabled={!writable}
                onCheckedChange={(v) => setDraft({ ...draft, enabled: v })}
                className="mt-0.5"
              />
              <label htmlFor="reset-enabled">
                <span className="block font-medium">Offer forgot password at sign-in</span>
                <span className="block text-caption text-muted-foreground">
                  Off by default. When on, it is offered only while an SMTP mail server is set up.
                </span>
              </label>
            </div>
            <div className="flex items-start gap-3 text-sm">
              <Switch
                id="reset-last-admin"
                checked={draft.allow_last_site_admin}
                disabled={!writable}
                onCheckedChange={(v) => setDraft({ ...draft, allow_last_site_admin: v })}
                className="mt-0.5"
              />
              <label htmlFor="reset-last-admin">
                <span className="block font-medium">Let the last site admin reset by email</span>
                <span className="block text-caption text-muted-foreground">
                  Off by default, so whoever controls the only site admin&rsquo;s mailbox cannot take over the whole
                  appliance. That admin uses the command on the appliance instead.
                </span>
              </label>
            </div>
          </div>

          <div className="grid gap-5 sm:grid-cols-2">
            {bounds.map((b) => (
              <SettingField
                key={b.key}
                label={b.label}
                unit={b.unit}
                min={b.min}
                max={b.max}
                defaultValue={b.default}
                explanation={b.explains}
                readOnly={!writable}
                value={draft.numbers[b.key]}
                onChange={(v) => setDraft({ ...draft, numbers: { ...draft.numbers, [b.key]: v } })}
              />
            ))}
          </div>

          {writable && dirty && (
            <div className="flex flex-wrap gap-2">
              <Button disabled={saving || outOfRange} onClick={() => { setErr(null); setConfirming(true); }}>
                Save password reset settings
              </Button>
              <Button variant="ghost" disabled={saving} onClick={() => { setDraft(draftOf(settings, bounds)); setErr(null); }}>
                Discard changes
              </Button>
            </div>
          )}
          <ConfirmDialog
            open={confirming}
            onOpenChange={setConfirming}
            title="Save password reset settings"
            description={draft.enabled && !mail.ready
              ? "They apply right away, but forgot password stays off at sign-in until an SMTP mail server is set up."
              : "They apply right away."}
            confirmLabel="Save"
            strictPassword
            busy={saving}
            error={err}
            onConfirm={save}
          />
        </div>
      </details>
    </Card>
  );
}
