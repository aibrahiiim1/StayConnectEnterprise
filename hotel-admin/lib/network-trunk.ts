/** 10.<n>.0.0/19 (every client network is a /19 by default) with the first n not used by an existing network or another row. */
export function suggestSubnet(vlan: number, used: Set<string>): string {
  const seeds = [vlan % 250 || 250];
  for (let n = 1; n <= 250; n++) seeds.push(n);
  for (const n of seeds) {
    const s = `10.${n}.0.0/19`;
    if (!used.has(s)) return s;
  }
  return "";
}

/**
 * Gateway and DHCP pool for a subnet written a.b.c.0/N: a /19 gets gateway .0.1 and the pool .0.10 to .31.250; a /24 keeps
 * gateway .1 and the pool .100 to .250. Any other size is left for the operator to fill in.
 */
export function addressingFor(subnet: string): { gateway: string; poolStart: string; poolEnd: string } {
  const m = /^(\d+)\.(\d+)\.(\d+)\.0\/(19|24)$/.exec(subnet.trim());
  if (!m) return { gateway: "", poolStart: "", poolEnd: "" };
  const [a, b, c] = [m[1], m[2], Number(m[3])];
  if (m[4] === "24") return { gateway: `${a}.${b}.${c}.1`, poolStart: `${a}.${b}.${c}.100`, poolEnd: `${a}.${b}.${c}.250` };
  if (c % 32 !== 0) return { gateway: "", poolStart: "", poolEnd: "" };
  return { gateway: `${a}.${b}.${c}.1`, poolStart: `${a}.${b}.${c}.10`, poolEnd: `${a}.${b}.${c + 31}.250` };
}
