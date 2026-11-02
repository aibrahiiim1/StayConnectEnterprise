/** An IPv4 CIDR as its first address and size, or null when it is not one. */
export function parseCidr(cidr: string): { base: number; size: number } | null {
  const m = /^(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\/(\d{1,2})$/.exec(cidr.trim());
  if (!m) return null;
  const o = m.slice(1, 5).map(Number);
  const bits = Number(m[5]);
  if (o.some((x) => x > 255) || bits > 32) return null;
  const ip = ((o[0] * 256 + o[1]) * 256 + o[2]) * 256 + o[3];
  const size = 2 ** (32 - bits);
  return { base: ip - (ip % size), size };
}

/** Two subnets share an address (one inside the other counts): the same rule the appliance applies. */
export function cidrsOverlap(a: string, b: string): boolean {
  const x = parseCidr(a);
  const y = parseCidr(b);
  if (!x || !y) return a.trim() === b.trim();
  return x.base < y.base + y.size && y.base < x.base + x.size;
}

/** The first subnet in `used` that overlaps `cidr`, if any. */
export function overlapping(cidr: string, used: Iterable<string>): string | undefined {
  for (const u of used) if (cidrsOverlap(cidr, u)) return u;
  return undefined;
}

/**
 * 10.<n>.0.0/19 (every client network is a /19 by default) with the first n whose /19 overlaps no existing network or
 * other row. A legacy /24 or /20 inside 10.<n>.0.0/19 makes that /19 unavailable.
 */
export function suggestSubnet(vlan: number, used: Iterable<string>): string {
  const taken = [...used];
  const seeds = [vlan % 250 || 250];
  for (let n = 1; n <= 250; n++) seeds.push(n);
  for (const n of seeds) {
    const s = `10.${n}.0.0/19`;
    if (!overlapping(s, taken)) return s;
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
