// Site types are descriptive; only license modules grant capabilities.

export const SITE_TYPES = [
  { value: "HOTEL", label: "Hotel" },
  { value: "CAFE", label: "Cafe" },
  { value: "OFFICE", label: "Office" },
  { value: "CLINIC", label: "Clinic" },
  { value: "CAMPUS", label: "Campus" },
  { value: "VENUE", label: "Venue" },
  { value: "COMPOUND", label: "Compound" },
  { value: "BEACH_CLUB", label: "Beach / Beach club" },
  { value: "OTHER", label: "Other" },
  { value: "UNSPECIFIED", label: "Not specified" },
] as const;

export type SiteType = (typeof SITE_TYPES)[number]["value"];

export const DEFAULT_SITE_TYPE: SiteType = "UNSPECIFIED";

/** Show unknown site types as Central sends them. */
export function siteTypeLabel(v: string | null | undefined): string {
  if (!v) return "Not specified";
  return SITE_TYPES.find((t) => t.value === v)?.label ?? v;
}
