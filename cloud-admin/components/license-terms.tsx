"use client";

import { Field, Input } from "@/components/ui/input";
import { Segmented } from "@/components/ui/tabs";
import type { LicenseTerms } from "@/lib/api";

/** The license terms as typed. Strings, so a half-typed number is not coerced under the operator's cursor. */
export type TermsDraft = {
  maxGuests: string;
  validMode: "days" | "date";
  validDays: string;
  validUntil: string; // yyyy-mm-dd
  graceDays: string;
};

/** Central's defaults for a new license: 500 concurrent guests, one year, 30 days' grace. */
export const DEFAULT_TERMS: TermsDraft = {
  maxGuests: "500",
  validMode: "days",
  validDays: "365",
  validUntil: "",
  graceDays: "30",
};

const whole = (s: string) => /^\d+$/.test(s.trim());

/** The first thing wrong with the draft, in words, or null. */
export function termsProblem(d: TermsDraft, today: Date = new Date()): string | null {
  if (!whole(d.maxGuests) || Number(d.maxGuests) < 1) return "Enter how many guests may be online at once (1 or more).";
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
  };
  return d.validMode === "days"
    ? { ...base, valid_days: Number(d.validDays) }
    : { ...base, valid_until: `${d.validUntil}T23:59:59Z` };
}

export function LicenseTermsFields({
  value,
  onChange,
}: {
  value: TermsDraft;
  onChange: (next: TermsDraft) => void;
}) {
  const set = (patch: Partial<TermsDraft>) => onChange({ ...value, ...patch });
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Field label="Guests online at once" required hint="The most guest devices that may be online at the same time.">
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
    </div>
  );
}
