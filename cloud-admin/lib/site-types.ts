// SITE TYPES. Descriptive only: what kind of place a site is. A site type never grants or gates anything — the
// license's modules do. Central accepts exactly these values for writes (control-plane site_types.go).

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

/** The label of a site type. A type this console does not know yet is shown as Central sent it. */
export function siteTypeLabel(v: string | null | undefined): string {
  if (!v) return "Not specified";
  return SITE_TYPES.find((t) => t.value === v)?.label ?? v;
}
