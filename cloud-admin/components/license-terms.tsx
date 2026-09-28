"use client";

import { Field, Input } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Segmented } from "@/components/ui/tabs";
import type { LicenseTerms } from "@/lib/api";
import { presetFor, requiresLabel, toggleModule, useModuleCatalog, type ModuleCatalog } from "@/lib/modules";
import { siteTypeLabel } from "@/lib/site-types";

/** The license terms as typed. Strings, so a half-typed number is not coerced under the operator's cursor. */
export type TermsDraft = {
  maxGuests: string;
  validMode: "days" | "date";
  validDays: string;
  validUntil: string; // yyyy-mm-dd
  graceDays: string;
  /** Module ids the license authorises. [] = core only. */
  modules: string[];
  /** The operator changed the modules by hand, so a site-type suggestion no longer replaces them. */
  modulesTouched: boolean;
};

/** Central's defaults for a new license: 500 concurrent guests, one year, 30 days' grace, core only. */
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

/** The first thing wrong with the draft, in words, or null. */
export function termsProblem(d: TermsDraft, today: Date = new Date()): string | null {
  if (!whole(d.maxGuests) || Number(d.maxGuests) < 1) return "Enter how many clients may be online at once (1 or more).";
  if (d.validMode === "days") {
    if (!whole(d.validDays) || Number(d.validDays) < 1) return "Enter how many days the license is valid (1 or more).";
  } else {
    if (!/^\d{4}-\d{2}-\d{2}$/.test(d.validUntil)) return "Choose the date the license ends.";
    if (new Date(`${d.validUntil}T23:59:59Z`).getTime() <= today.getTime()) return "The end date must be in the future.";
  }
  if (!whole(d.graceDays)) return "Enter the grace period in whole days (0 or more).";
  return null;
}

/**
 * The §6 license body. A chosen end date is sent as the END of that day in UTC, so "valid until 31 Dec" includes
 * 31 December everywhere.
 */
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

/**
 * The site type's suggested modules, applied to a draft the operator has not edited by hand. A suggestion only:
 * once the operator ticks or unticks a module it is theirs.
 */
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
}: {
  value: TermsDraft;
  onChange: (next: TermsDraft) => void;
  /** The type of the site the license is for, when known: offers its suggested modules. */
  siteType?: string | null;
}) {
  const set = (patch: Partial<TermsDraft>) => onChange({ ...value, ...patch });
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Field label="Clients online at once" required hint="The most client devices that may be online at the same time.">
        <Input
          inputMode="numeric"
          value={value.maxGuests}
          onChange={(e) => set({ maxGuests: e.target.value })}
        />
      </Field>
      <Field label="Grace period (days)" required hint="How long it keeps working after the end date.">
        <Input inputMode="numeric" value={value.graceDays} onChange={(e) => set({ graceDays: e.target.value })} />
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
          <Field label="Days from today" required>
            <Input inputMode="numeric" value={value.validDays} onChange={(e) => set({ validDays: e.target.value })} />
          </Field>
        ) : (
          <Field label="Ends on" required>
            <Input type="date" value={value.validUntil} onChange={(e) => set({ validUntil: e.target.value })} />
          </Field>
        )}
      </div>
      <ModuleFields
        value={value.modules}
        siteType={siteType}
        onChange={(modules) => set({ modules, modulesTouched: true })}
      />
    </div>
  );
}

/** The license's modules: one checkbox per registered module, dependencies ticked and unticked with them. */
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
              <label
                key={m.id}
                className="flex cursor-pointer items-start gap-2.5 rounded-md border border-border px-3 py-2 text-sm hover:bg-accent"
              >
                <input
                  type="checkbox"
                  className="mt-0.5 h-4 w-4 shrink-0 accent-primary"
                  checked={checked}
                  onChange={(e) => onChange(toggleModule(catalog, value, m.id, e.target.checked))}
                />
                <span>
                  <span className="font-medium">{m.label}</span>
                  {needs && <span className="block text-caption text-muted-foreground">Needs {needs}</span>}
                </span>
              </label>
            );
          })}
        </div>
      )}
    </div>
  );
}
