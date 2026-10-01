// Addressing suggestions for laying out several VLANs on one trunk (app/(app)/network/trunk).

/** 10.<n>.0.0/24 with the first n not used by an existing network or another row. */
export function suggestSubnet(vlan: number, used: Set<string>): string {
  const seeds = [vlan % 250 || 250];
  for (let n = 1; n <= 250; n++) seeds.push(n);
  for (const n of seeds) {
    const s = `10.${n}.0.0/24`;
    if (!used.has(s)) return s;
  }
  return "";
}

/** Gateway .1 and pool .100–.250 of a /24 written as a.b.c.0/24. */
export function addressingFor(subnet: string): { gateway: string; poolStart: string; poolEnd: string } {
  const m = /^(\d+\.\d+\.\d+)\.0\/24$/.exec(subnet.trim());
  if (!m) return { gateway: "", poolStart: "", poolEnd: "" };
  return { gateway: `${m[1]}.1`, poolStart: `${m[1]}.100`, poolEnd: `${m[1]}.250` };
}
