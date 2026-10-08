// The one-line rule summary of a client group, for the table. Lives beside the page rather than in it:
// a Next page file may export only page members, and this is shared with the tests.
import type { ClientGroupRule } from "@/lib/api";

/** "company.com, company.ae · Microsoft tenant" — the group's rules in one line for the table. */
export function ruleSummary(rules: ClientGroupRule[] | null | undefined): string {
  if (!rules || rules.length === 0) return "No rules — matches nobody";
  return rules.map((r) => {
    switch (r.type) {
      case "EMAIL_DOMAIN": {
        const d = (r.value.domains ?? []).join(", ");
        return r.value.include_subdomains ? `${d} (and subdomains)` : d;
      }
      case "IDP_TENANT": return (r.value.tenant_ids ?? []).length > 1 ? `${r.value.tenant_ids!.length} Microsoft tenants` : "Microsoft tenant";
      case "IDP_HOSTED_DOMAIN": return `Google Workspace ${(r.value.domains ?? []).join(", ")}`;
      default: return String((r as { type: string }).type);
    }
  }).join(" · ");
}

