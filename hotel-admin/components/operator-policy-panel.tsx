"use client";

import { useCallback, useEffect, useState } from "react";
import { ChevronDown } from "lucide-react";
import { api } from "@/lib/api";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/dialog";
import { SettingField } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { formatDate } from "@/lib/utils";
import { operatorError } from "@/lib/operator-errors";
import { FADE, LoadFailure } from "@/app/(app)/backups/load-failure";

export type PolicySettings = { config_version: number; updated_at?: string | null; updated_by?: string | null };
export type PolicyBound<S> = {
  key: keyof S & string;
  label: string; unit: string; min: number; max: number; default: number; explains: string;
  allow_never?: boolean;
};

export function OperatorPolicyPanel<S extends PolicySettings>({
  endpoint,
  title,
  summary,
  description,
  saveLabel,
  saved,
  writable,
}: {
  endpoint: string;
  title: string;
  summary: (settings: S) => React.ReactNode;
  description: React.ReactNode;
  saveLabel: string;
  saved: { title: string; body: string };
  writable: boolean;
}) {
  const toast = useToast();
  const [data, setData] = useState<{ settings: S; bounds: PolicyBound<S>[] } | null>(null);
  const [edits, setEdits] = useState<Record<string, string>>({});
  const [confirming, setConfirming] = useState(false);
  const [saving, setSaving] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [failed, setFailed] = useState(false);
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const r = await api.get<{ settings: S; bounds: PolicyBound<S>[] }>(endpoint);
      if (!r?.settings || !Array.isArray(r.bounds)) { setFailed(true); return; }
      setData(r);
      setFailed(false);
      setEdits(Object.fromEntries(r.bounds.map((b) => [b.key, String(r.settings[b.key])])));
    } catch {
      setFailed(true);
    } finally {
      setLoading(false);
    }
  }, [endpoint]);
  useEffect(() => { void load(); }, [load]);

  if (!data) {
    if (!failed) return null;
    return (
      <Card className={FADE}>
        <div className="px-5 py-3.5 text-emphasis">{title}</div>
        <div className="border-t border-border">
          <LoadFailure what={`The ${title.toLowerCase()} settings`} busy={loading} onRetry={() => void load()} />
        </div>
      </Card>
    );
  }
  const { settings, bounds } = data;
  const current = (b: PolicyBound<S>) => settings[b.key] as unknown as number;
  const valueOf = (b: PolicyBound<S>) => {
    const raw = edits[b.key];
    return raw === undefined || raw.trim() === "" ? current(b) : Number(raw);
  };
  const dirty = bounds.some((b) => valueOf(b) !== current(b));
  const outOfRange = bounds.some((b) => { const v = valueOf(b); return !Number.isInteger(v) || (b.allow_never && v === 0 ? false : v < b.min) || v > b.max; });

  async function save() {
    setSaving(true); setErr(null);
    try {
      await api.put(endpoint, {
        ...Object.fromEntries(bounds.map((b) => [b.key, valueOf(b)])),
      });
      toast.success(saved.title, saved.body);
      setConfirming(false);
      await load();
    } catch (e) {
      setErr(operatorError(e, "The settings could not be saved. Try again in a moment. Nothing was changed."));
    } finally { setSaving(false); }
  }

  const provenance = settings.config_version === 0
    ? "No value has been saved yet, so the defaults below are in force."
    : settings.updated_at
      ? `Last changed ${formatDate(settings.updated_at)}${settings.updated_by ? ` by ${settings.updated_by}` : ""}.`
      : null;

  return (
    <Card>
      <details className="group">
        <summary className="flex cursor-pointer select-none flex-wrap items-center gap-x-2 gap-y-1 rounded-lg px-5 py-3.5 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden">
          <ChevronDown className="size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-180" aria-hidden />
          <span className="text-emphasis">{title}</span>
          <span className="ms-auto hidden text-caption text-muted-foreground sm:inline">{summary(settings)}</span>
        </summary>
        <div className={`space-y-5 border-t border-border px-5 py-4 ${FADE}`}>
          <p className="max-w-3xl text-sm text-muted-foreground">
            {description}
            {provenance && <> {provenance}</>}
          </p>
          <div className="grid gap-5 sm:grid-cols-2">
            {bounds.map((b) => (
              <SettingField
                key={b.key}
                label={b.label}
                unit={b.unit}
                min={b.allow_never ? 0 : b.min}
                max={b.max}
                defaultValue={b.default}
                explanation={b.explains}
                readOnly={!writable}
                value={edits[b.key] ?? String(current(b))}
                onChange={(v) => setEdits((m) => ({ ...m, [b.key]: v }))}
                extra={b.allow_never && (
                  <label className="mt-1.5 flex items-center gap-2 text-sm">
                    <input
                      type="checkbox"
                      disabled={!writable}
                      checked={valueOf(b) === 0}
                      onChange={(e) => setEdits((m) => ({ ...m, [b.key]: e.target.checked ? "0" : String(b.default) }))}
                    />
                    Never (no limit)
                  </label>
                )}
              />
            ))}
          </div>
          {writable && dirty && (
            <div className="flex flex-wrap gap-2">
              <Button disabled={saving || outOfRange} onClick={() => { setErr(null); setConfirming(true); }}>{saveLabel}</Button>
              <Button variant="ghost" disabled={saving} onClick={() => {
                setEdits(Object.fromEntries(bounds.map((b) => [b.key, String(current(b))])));
                setErr(null);
              }}>Discard changes</Button>
            </div>
          )}
          <ConfirmDialog
            open={confirming}
            onOpenChange={(v) => { setConfirming(v); if (!v) setErr(null); }}
            title={saveLabel}
            description={saved.body}
            confirmLabel="Save"
            busy={saving}
            error={err}
            onConfirm={save}
          />
        </div>
      </details>
    </Card>
  );
}
