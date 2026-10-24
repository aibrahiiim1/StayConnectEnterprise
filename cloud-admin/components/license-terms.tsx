"use client";

import { Checkbox, Field, FieldGrid, Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Segmented } from "@/components/ui/tabs";
import type { LicenseTerms } from "@/lib/api";
import { presetFor, requiresLabel, toggleModule, useModuleCatalog, type ModuleCatalog } from "@/lib/modules";
import { siteTypeLabel } from "@/lib/site-types";

/** Draft numbers stay strings so partial input is not coerced while typing. */
export type TermsDraft = {
  maxGuests: string;
  validMode: "days" | "date";
  validDays: string;
  validUntil: string;
  graceDays: string;
  /** Empty means core only. */
  modules: string[];
  /** Manual choices override site-type suggestions. */
  modulesTouched: boolean;
};

export const DEFAULT_TERMS: TermsDraft = {
  maxGuests: "500",
  validMode: "days",
  validDays: "365",
  validUntil: "",
  graceDays: "30",
  modules: [],
  modulesTouched: false,
};

const whole = (s: string) => /^\d+$/.test(s.trim());

export type TermsField = "maxGuests" | "validDays" | "validUntil" | "graceDays";

export function termsIssue(d: TermsDraft, today: Date = new Date()): { field: TermsField; message: string } | null {
  if (!whole(d.maxGuests) || Number(d.maxGuests) < 1) return { field: "maxGuests", message: "Enter how many clients may be online at once (1 or more)." };
  if (d.validMode === "days") {
    if (!whole(d.validDays) || Number(d.validDays) < 1) return { field: "validDays", message: "Enter how many days the license is valid (1 or more)." };
  } else {
    if (!/^\d{4}-\d{2}-\d{2}$/.test(d.validUntil)) return { field: "validUntil", message: "Choose the date the license ends." };
    if (new Date(`${d.validUntil}T23:59:59Z`).getTime() <= today.getTime()) return { field: "validUntil", message: "The end date must be in the future." };
  }
  if (!whole(d.graceDays)) return { field: "graceDays", message: "Enter the grace period in whole days (0 or more)." };
  return null;
}

export function termsProblem(d: TermsDraft, today: Date = new Date()): string | null {
  return termsIssue(d, today)?.message ?? null;
}

/** Send chosen end dates at 23:59:59 UTC so the selected calendar day remains valid. */
export function termsBody(d: TermsDraft): LicenseTerms {
  const base = {
    max_concurrent_online_guests: Number(d.maxGuests),
    grace_period_days: Number(d.graceDays),
    modules: [...d.modules],
  };
  return d.validMode === "days"
    ? { ...base, valid_days: Number(d.validDays) }
    : { ...base, valid_until: `${d.validUntil}T23:59:59Z` };
}

/** Apply site-type suggestions only until the operator edits modules. */
export function withSuggestedModules(d: TermsDraft, catalog: ModuleCatalog | null, siteType: string | null | undefined): TermsDraft {
  if (!catalog || d.modulesTouched) return d;
  const modules = presetFor(catalog, siteType);
  return sameSet(modules, d.modules) ? d : { ...d, modules };
}

const sameSet = (a: string[], b: string[]) => a.length === b.length && a.every((x) => b.includes(x));

export function LicenseTermsFields({
  value,
  onChange,
  siteType,
  errorFor,
  idFor,
}: {
  value: TermsDraft;
  onChange: (next: TermsDraft) => void;
  siteType?: string | null;
  errorFor?: (field: TermsField) => string | undefined;
  idFor?: (field: TermsField) => string;
}) {
  const set = (patch: Partial<TermsDraft>) => onChange({ ...value, ...patch });
  return (
    <FieldGrid cols={2}>
      <Field label="Clients online at once" required hint="The most client devices that may be online at the same time." error={errorFor?.("maxGuests")}>
        <Input
          id={idFor?.("maxGuests")}
          inputMode="numeric"
          value={value.maxGuests}
          onChange={(e) => set({ maxGuests: e.target.value })}
        />
      </Field>
      <Field
        label="Grace period (days)"
        required
        hint="Days it keeps admitting clients after the end date. 0 stops at the end date."
        error={errorFor?.("graceDays")}
      >
        <Input id={idFor?.("graceDays")} inputMode="numeric" value={value.graceDays} onChange={(e) => set({ graceDays: e.target.value })} />
      </Field>
      <div className="space-y-2 sm:col-span-2">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span className="text-label" id="valid-for-label">Valid for</span>
          <Segmented
            size="sm"
            label="How to set the end"
            value={value.validMode}
            onChange={(v) => set({ validMode: v })}
            options={[
              { value: "days", label: "Number of days" },
              { value: "date", label: "Until a date" },
            ]}
          />
        </div>
        {value.validMode === "days" ? (
          <Field label="Days from today" required error={errorFor?.("validDays")}>
            <Input id={idFor?.("validDays")} inputMode="numeric" value={value.validDays} onChange={(e) => set({ validDays: e.target.value })} />
          </Field>
        ) : (
          <Field label="Ends on" required error={errorFor?.("validUntil")}>
            <Input id={idFor?.("validUntil")} type="date" value={value.validUntil} onChange={(e) => set({ validUntil: e.target.value })} />
          </Field>
        )}
      </div>
      <ModuleFields
        value={value.modules}
        siteType={siteType}
        onChange={(modules) => set({ modules, modulesTouched: true })}
      />
    </FieldGrid>
  );
}

export function ModuleFields({
  value,
  onChange,
  siteType,
}: {
  value: string[];
  onChange: (next: string[]) => void;
  siteType?: string | null;
}) {
  const { catalog, error } = useModuleCatalog();
  const preset = catalog && siteType ? presetFor(catalog, siteType) : [];
  const canSuggest = preset.length > 0 && !sameSet(preset, value);
  return (
    <div role="group" aria-labelledby="license-modules-label" className="space-y-2 sm:col-span-2">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <span className="text-label" id="license-modules-label">Modules</span>
        {canSuggest && (
          <Button type="button" size="sm" variant="ghost" onClick={() => onChange(preset)}>
            Use the {siteTypeLabel(siteType)} suggestion
          </Button>
        )}
      </div>
      <p className="text-xs text-muted-foreground">
        Wi-Fi access is always included. A module only allows a feature; the site still turns it on.
      </p>
      {error ? (
        <p className="text-sm text-destructive">
          The module list could not be loaded. The license keeps {value.length ? value.join(", ") : "core access only"}.
        </p>
      ) : !catalog ? (
        <p className="text-sm text-muted-foreground">Loading modules…</p>
      ) : (
        <div className="grid gap-2 sm:grid-cols-2">
          {catalog.modules.map((m) => {
            const needs = requiresLabel(catalog, m.id);
            const checked = value.includes(m.id);
            return (
              <Checkbox
                key={m.id}
                className="rounded-md border border-border px-3 py-2 transition-colors hover:bg-accent/50"
                label={<span className="font-medium">{m.label}</span>}
                description={needs ? `Needs ${needs}` : undefined}
                checked={checked}
                onChange={(e) => onChange(toggleModule(catalog, value, m.id, e.target.checked))}
              />
            );
          })}
        </div>
      )}
    </div>
  );
}
