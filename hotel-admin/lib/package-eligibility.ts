// A package's eligibility, in words an administrator can scan in the package list. Only the conditions that tie a package to a place are spelled out (which client
// networks, which PMS connections); every other condition is counted. Package eligibility decides what a signed-in client may get; it never hides a sign-in method.

type RawRule = { type?: string; Type?: string; value?: unknown; Value?: unknown };

export type EligibilityNames = { networks: Map<string, string>; connections: Map<string, string> };

const idsOf = (v: unknown, key: string): string[] => {
  const value = (v ?? {}) as Record<string, unknown>;
  const list = value[key];
  return Array.isArray(list) ? list.filter((x): x is string => typeof x === "string") : [];
};

export function eligibilityWords(rules: RawRule[] | undefined | null, names: EligibilityNames): string[] {
  const out: string[] = [];
  let other = 0;
  for (const r of rules ?? []) {
    const type = r.type ?? r.Type;
    const value = r.value ?? r.Value;
    if (type === "SITE_NETWORK") {
      const ns = idsOf(value, "guest_network_ids").map((id) => names.networks.get(id) ?? "a removed network");
      out.push(`Only on ${ns.length ? ns.join(", ") : "no network"}`);
    } else if (type === "PMS_INTERFACE") {
      const cs = idsOf(value, "pms_interface_ids").map((id) => names.connections.get(id) ?? "a removed connection");
      out.push(`Only guests of ${cs.length ? cs.join(", ") : "no connection"}`);
    } else if (type) {
      other += 1;
    }
  }
  if (other > 0) out.push(`${other} other condition${other === 1 ? "" : "s"}`);
  return out;
}
