"use client";

// PROVIDER FIELDS — one renderer for every provider's configuration, driven by the provider's own schema.
//
// Each field becomes the input its type calls for (a web address, a host and port, a number with its unit, a
// choice, a switch), starts from its default, carries its help, and is checked for shape as it is typed. The
// server remains the authority: this only catches what is certainly wrong before an operator presses Create.

import { useState } from "react";
import { Field, Input, Select } from "@/components/ui/input";
import { Switch } from "@/components/ui/misc";
import { KeyValueGrid } from "@/components/ui/data";
import type { FormValues, PmsProvider, PmsProviderField } from "@/lib/api/pms-connections";
import { PROTEL_KIND, fieldsFor } from "@/lib/api/pms-connections";

export function ProviderFieldsForm({
  fields,
  values,
  errors,
  onChange,
  disabled,
}: {
  fields: PmsProviderField[];
  values: FormValues;
  errors: Record<string, string>;
  onChange: (key: string, v: string | boolean) => void;
  disabled?: boolean;
}) {
  const [advanced, setAdvanced] = useState(false);
  const main = fields.filter((f) => !f.advanced);
  const folded = fields.filter((f) => f.advanced);
  // An error inside the folded group must not be hidden behind a closed toggle.
  const foldedHasError = folded.some((f) => errors[f.key]);

  return (
    <div className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-2">
        {main.map((f) => (
          <FieldInput key={f.key} f={f} value={values[f.key]} error={errors[f.key]} onChange={onChange} disabled={disabled} />
        ))}
      </div>
      {folded.length > 0 && (
        <div>
          <button
            type="button"
            aria-expanded={advanced || foldedHasError}
            onClick={() => setAdvanced((v) => !v)}
            className="text-sm font-medium text-muted-foreground transition-colors hover:text-foreground"
          >
            {advanced || foldedHasError ? "Hide" : "Show"} timing settings
          </button>
          {(advanced || foldedHasError) && (
            <div className="mt-3 grid gap-4 rounded-md border border-border bg-surface/40 p-4 sm:grid-cols-2">
              {folded.map((f) => (
                <FieldInput key={f.key} f={f} value={values[f.key]} error={errors[f.key]} onChange={onChange} disabled={disabled} />
              ))}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

function FieldInput({
  f, value, error, onChange, disabled,
}: {
  f: PmsProviderField;
  value: string | boolean | undefined;
  error?: string;
  onChange: (key: string, v: string | boolean) => void;
  disabled?: boolean;
}) {
  const label = f.unit ? `${f.label} (${f.unit})` : f.label;
  if (f.type === "bool") {
    return (
      <Field label={label} hint={f.help} error={error}>
        <Switch checked={value === true} onCheckedChange={(v: boolean) => onChange(f.key, v)} disabled={disabled} />
      </Field>
    );
  }
  if (f.type === "enum") {
    return (
      <Field label={label} hint={f.help} error={error} required={f.required}>
        <Select value={typeof value === "string" ? value : ""} disabled={disabled}
          onChange={(e) => onChange(f.key, e.target.value)}>
          {!f.required && <option value="">Not set</option>}
          {f.required && (typeof value !== "string" || value === "") && <option value="">Choose…</option>}
          {(f.options ?? []).map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
        </Select>
      </Field>
    );
  }
  return (
    <Field label={label} hint={f.help} error={error} required={f.required}>
      <Input
        type={f.type === "int" ? "number" : f.type === "url" ? "url" : "text"}
        inputMode={f.type === "int" ? "numeric" : undefined}
        min={f.type === "int" ? f.min : undefined}
        max={f.type === "int" ? f.max : undefined}
        placeholder={f.placeholder ?? (f.type === "url" ? "https://" : undefined)}
        value={typeof value === "string" ? value : ""}
        disabled={disabled}
        autoComplete="off"
        spellCheck={false}
        onChange={(e) => onChange(f.key, e.target.value)}
      />
    </Field>
  );
}

// ---------------------------------------------------------------- reading a saved configuration

const ms = (v: unknown): string | null => {
  const n = typeof v === "number" ? v : Number(v);
  if (!Number.isFinite(n) || n <= 0) return null;
  if (n % 1000 !== 0) return `${n.toLocaleString()} ms`;
  const secs = n / 1000;
  if (secs % 3600 === 0) return `${secs / 3600} h`;
  if (secs % 60 === 0) return `${secs / 60} min`;
  return `${secs} s`;
};

const shown = (v: unknown): string => {
  if (v === undefined || v === null || v === "") return "—";
  if (v === "[redacted]") return "Hidden";
  if (typeof v === "boolean") return v ? "Yes" : "No";
  if (typeof v === "object") return "Set";
  return String(v);
};

/** The settings a saved version carries, as rows of label → value. Nothing is invented; absent is "—". */
export function configRows(
  provider: PmsProvider,
  rev: { config?: Record<string, unknown>; source_timezone?: string },
): { label: string; value: string }[] {
  const cfg = (rev.config ?? {}) as Record<string, unknown>;
  if (provider.kind === PROTEL_KIND) {
    const auth = (cfg.auth ?? {}) as Record<string, unknown>;
    const rows: { label: string; value: string }[] = [
      { label: "Connects to", value: shown(cfg.endpoint) },
      { label: "PMS time zone", value: rev.source_timezone || "—" },
      {
        label: "Keep-alive",
        value: [ms(cfg.heartbeat_interval_ms) && `every ${ms(cfg.heartbeat_interval_ms)}`,
          ms(cfg.heartbeat_timeout_ms) && `give up after ${ms(cfg.heartbeat_timeout_ms)}`]
          .filter(Boolean).join(", ") || "—",
      },
      { label: "Guest data treated as stale after", value: ms(cfg.feed_freshness_ms) ?? "—" },
      { label: "Full guest-list refresh at least every", value: ms(cfg.complete_sync_ms) ?? "—" },
      {
        label: "Timeouts",
        value: [ms(cfg.dial_timeout_ms) && `connect ${ms(cfg.dial_timeout_ms)}`,
          ms(cfg.read_timeout_ms) && `read ${ms(cfg.read_timeout_ms)}`,
          ms(cfg.write_timeout_ms) && `write ${ms(cfg.write_timeout_ms)}`].filter(Boolean).join(", ") || "—",
      },
      { label: "Direction", value: auth.read_only === false ? "Read and write" : "Read-only — StayConnect never writes to the PMS" },
      { label: "Credential", value: auth.credential_mode === "NONE" || auth.credential_mode === undefined ? "None needed for this link" : "Required" },
    ];
    if (cfg.resync_supported != null) {
      rows.push({ label: "Full refresh", value: cfg.resync_supported ? "Supported" : "Not supported" });
    }
    return rows;
  }
  const nested = (cfg.provider_config && typeof cfg.provider_config === "object"
    ? cfg.provider_config : {}) as Record<string, unknown>;
  const rows = [{ label: "PMS time zone", value: rev.source_timezone || "—" }];
  for (const f of fieldsFor(provider)) {
    const raw = nested[f.key] ?? cfg[f.key];
    const opt = f.type === "enum" ? f.options?.find((o) => o.value === raw)?.label : undefined;
    rows.push({ label: f.label, value: opt ?? (f.unit && raw !== undefined && raw !== "" ? `${shown(raw)} ${f.unit}` : shown(raw)) });
  }
  return rows;
}

export function ConfigSummary({
  provider, rev,
}: {
  provider: PmsProvider;
  rev: { config?: Record<string, unknown>; source_timezone?: string };
}) {
  return (
    <KeyValueGrid
      columns={2}
      items={configRows(provider, rev).map((r) => ({ label: r.label, value: r.value }))}
    />
  );
}
