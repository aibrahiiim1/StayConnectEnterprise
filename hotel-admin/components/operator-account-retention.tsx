"use client";

// DISABLED ACCOUNTS — how long a disabled staff account is kept before it is anonymised (edged
// /operators/account-retention, migration 0138, GDPR audit B4; docs/OPERATIONAL_SETTINGS_POLICY.md).
//
// Every role that can open Operators reads it; only a role that can change operators (site admin) edits it,
// with a reason and the password, and every change is recorded. The bound comes from the appliance, so this
// form cannot drift from what edged and the database accept.

import { useCallback, useEffect, useState } from "react";
import { ChevronRight, UserMinus } from "lucide-react";
import { api, AccountRetention, AccountRetentionBound } from "@/lib/api";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { ErrorBanner } from "@/components/ui/error-banner";
import { SettingField } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { errMsg, formatDate } from "@/lib/utils";

type Resp = { settings: AccountRetention; bounds: AccountRetentionBound[] };

export function OperatorAccountRetention({ writable }: { writable: boolean }) {
  const toast = useToast();
  const [data, setData] = useState<Resp | null>(null);
  const [edits, setEdits] = useState<Record<string, string>>({});
  const [reason, setReason] = useState("");
  const [password, setPassword] = useState("");
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const r = await api.get<Resp>("/operators/account-retention");
      if (!r?.settings || !Array.isArray(r.bounds)) return;
      setData(r);
      setEdits(Object.fromEntries(r.bounds.map((b) => [b.key, String(r.settings[b.key])])));
    } catch { /* the section stays absent rather than breaking the Operators page */ }
  }, []);
  useEffect(() => { load(); }, [load]);

  if (!data) return null;
  const { settings, bounds } = data;
  const valueOf = (b: AccountRetentionBound) => {
    const raw = edits[b.key];
    return raw === undefined || raw.trim() === "" ? settings[b.key] : Number(raw);
  };
  const dirty = bounds.some((b) => valueOf(b) !== settings[b.key]);
  const outOfRange = bounds.some((b) => { const v = valueOf(b); return !Number.isInteger(v) || v < b.min || v > b.max; });
  const reasonOk = reason.trim().length >= 4 && reason.trim().length <= 500;

  async function save() {
    setSaving(true); setErr(null);
    try {
      await api.put("/operators/account-retention", {
        ...Object.fromEntries(bounds.map((b) => [b.key, valueOf(b)])),
        reason: reason.trim(),
        password,
      });
      toast.success("Saved", "Disabled accounts are anonymised after the new period from the next hourly run.");
      setReason(""); setPassword("");
      await load();
    } catch (e) { setErr(errMsg(e)); } finally { setSaving(false); }
  }

  return (
    <Card>
      <details className="group">
        <summary className="flex cursor-pointer select-none items-center gap-2 rounded-lg px-5 py-4 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
          <ChevronRight className="size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-90 rtl:-scale-x-100" aria-hidden />
          <UserMinus className="size-4 shrink-0 text-muted-foreground" aria-hidden />
          <span className="text-emphasis">Disabled accounts</span>
          <span className="ms-auto hidden text-caption text-muted-foreground sm:inline">
            Anonymised {settings.disabled_account_days} days after being disabled
          </span>
        </summary>
        <div className="space-y-5 border-t border-border px-5 py-5">
          <p className="text-sm text-muted-foreground">
            Disabling an operator keeps their account so the activity log can still say who did what. After this
            period the account is anonymised: its email, name, password and authenticator are removed, and the
            activity log shows it as a removed account. An anonymised account cannot be enabled again.
            {settings.config_version === 0
              ? " No value has been saved yet, so the defaults below are in force."
              : settings.updated_at ? ` Last changed ${formatDate(settings.updated_at)}${settings.updated_by ? ` by ${settings.updated_by}` : ""}.` : ""}
          </p>
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
                value={edits[b.key] ?? String(settings[b.key])}
                onChange={(v) => setEdits((m) => ({ ...m, [b.key]: v }))}
              />
            ))}
          </div>
          {writable && dirty && (
            <div className="space-y-3 rounded-md border border-border bg-surface p-4">
              <ErrorBanner err={err} className="mb-0" />
              <Field label="Reason" required hint="Recorded with the change.">
                <Input value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} />
              </Field>
              <Field label="Confirm your password" className="max-w-sm">
                <Input type="password" autoComplete="current-password" value={password}
                  onChange={(e) => setPassword(e.target.value)} />
              </Field>
              <div className="flex flex-wrap gap-2">
                <Button disabled={saving || !password || !reasonOk || outOfRange} onClick={save}>
                  {saving ? "Saving…" : "Save disabled-account period"}
                </Button>
                <Button variant="ghost" disabled={saving} onClick={() => {
                  setEdits(Object.fromEntries(bounds.map((b) => [b.key, String(settings[b.key])])));
                  setReason(""); setPassword(""); setErr(null);
                }}>Discard changes</Button>
              </div>
            </div>
          )}
        </div>
      </details>
    </Card>
  );
}
