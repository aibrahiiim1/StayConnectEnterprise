"use client";

// Session bounds come from the appliance; a change affects open sessions on their next request.

import { useCallback, useEffect, useState } from "react";
import { ChevronRight, Timer } from "lucide-react";
import { api, SessionPolicy, SessionPolicyBound } from "@/lib/api";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { ErrorBanner } from "@/components/ui/error-banner";
import { SettingField } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { errMsg, formatDate } from "@/lib/utils";

type Resp = { settings: SessionPolicy; bounds: SessionPolicyBound[] };

export function OperatorSessionLimits({ writable }: { writable: boolean }) {
  const toast = useToast();
  const [data, setData] = useState<Resp | null>(null);
  const [edits, setEdits] = useState<Record<string, string>>({});
  const [reason, setReason] = useState("");
  const [password, setPassword] = useState("");
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const r = await api.get<Resp>("/operators/session-policy");
      if (!r?.settings || !Array.isArray(r.bounds)) return;
      setData(r);
      setEdits(Object.fromEntries(r.bounds.map((b) => [b.key, String(r.settings[b.key])])));
    } catch { /* Keep the Operators page usable if this setting cannot load. */ }
  }, []);
  useEffect(() => { load(); }, [load]);

  if (!data) return null;
  const { settings, bounds } = data;
  const valueOf = (b: SessionPolicyBound) => {
    const raw = edits[b.key];
    return raw === undefined || raw.trim() === "" ? settings[b.key] : Number(raw);
  };
  const dirty = bounds.some((b) => valueOf(b) !== settings[b.key]);
  const outOfRange = bounds.some((b) => { const v = valueOf(b); return !Number.isInteger(v) || v < b.min || v > b.max; });
  const reasonOk = reason.trim().length >= 4 && reason.trim().length <= 500;

  async function save() {
    setSaving(true); setErr(null);
    try {
      await api.put("/operators/session-policy", {
        ...Object.fromEntries(bounds.map((b) => [b.key, valueOf(b)])),
        reason: reason.trim(),
        password,
      });
      toast.success("Session limits saved", "They apply to every open session from its next request.");
      setReason(""); setPassword("");
      await load();
    } catch (e) { setErr(errMsg(e)); } finally { setSaving(false); }
  }

  return (
    <Card>
      <details className="group">
        <summary className="flex cursor-pointer select-none items-center gap-2 rounded-lg px-5 py-4 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
          <ChevronRight className="size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-90 rtl:-scale-x-100" aria-hidden />
          <Timer className="size-4 shrink-0 text-muted-foreground" aria-hidden />
          <span className="text-emphasis">Session limits</span>
          <span className="ms-auto hidden text-caption text-muted-foreground sm:inline">
            Signed out after {settings.idle_timeout_minutes} min idle, {settings.max_session_hours} h at most
          </span>
        </summary>
        <div className="space-y-5 border-t border-border px-5 py-5">
          <p className="text-sm text-muted-foreground">
            How long a signed-in session lasts. A session with no activity for the first limit ends, and so does any
            session once it reaches the second, however active. An open tab that is only being looked at counts as
            inactive. Changes apply to every open session, yours included, from its next request.
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
                  {saving ? "Saving…" : "Save session limits"}
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
