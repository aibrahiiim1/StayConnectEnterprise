// Shapes edged returns for /edge/v1/network/enforcement-policy, and the allow-list rules the server applies
// (resources_enforcement_policy.go). The server stays the authority; this only explains a mistake before sending.

export type EnforcementPolicy = {
  private_deny_enabled: boolean;
  private_allow_cidrs: string[];
  allow_upstream_infrastructure: boolean;
  mac_binding_enabled: boolean;
  cutoff_seconds: number;
  config_version: number;
  updated_at: string;
  updated_by?: string;
};

export type EnforcementSetting = {
  field: string;
  label: string;
  default: unknown;
  unit?: string;
  explanation: string;
  min?: number;
  max?: number;
};

export type EnforcementChange = {
  changed_at: string;
  changed_by: string;
  reason: string;
  field: string;
  old_value: string | null;
  new_value: string | null;
  config_version: number;
};

export type EnforcementDoc = {
  policy: EnforcementPolicy;
  settings: EnforcementSetting[];
  limits: { max_cidrs: number; min_prefix: number; max_prefix: number; cutoff_min: number; cutoff_max: number };
  history: EnforcementChange[];
};

export const FIELD_LABEL: Record<string, string> = {
  private_deny_enabled: "Private-network denial",
  private_allow_cidrs: "Allowed private networks",
  allow_upstream_infrastructure: "Upstream gateway and DNS",
  mac_binding_enabled: "Device-address (MAC) binding",
  cutoff_seconds: "Cut-off delay",
  all: "All settings",
};

/** The words one history value is shown with. */
export function valueWords(field: string, v: string | null): string {
  if (v === null || v === undefined) return "—";
  if (field === "cutoff_seconds") return `${v} s`;
  if (field === "private_allow_cidrs") return v === "" ? "none" : v.split(",").join(", ");
  if (v === "true") return "on";
  if (v === "false") return "off";
  return v;
}

/** One entry per line or comma-separated; blank entries dropped. */
export function splitCidrs(text: string): string[] {
  return text.split(/[\s,;]+/).map((s) => s.trim()).filter(Boolean);
}

function ipv4Octets(s: string): number[] | null {
  const parts = s.split(".");
  if (parts.length !== 4) return null;
  const out: number[] = [];
  for (const p of parts) {
    if (!/^\d{1,3}$/.test(p)) return null;
    const n = Number(p);
    if (n > 255) return null;
    out.push(n);
  }
  return out;
}

/** Same rules as the server: IPv4, /8../32, no host bits. Returns an operator-readable problem, or null. */
export function cidrProblem(raw: string, minPrefix = 8, maxPrefix = 32): string | null {
  const m = /^([^/]+)\/(\d{1,2})$/.exec(raw);
  const o = m ? ipv4Octets(m[1]) : null;
  if (!m || !o) return `"${raw}" is not an IPv4 network in CIDR form (for example 192.168.50.0/24).`;
  const len = Number(m[2]);
  if (len < minPrefix || len > maxPrefix) return `"${raw}" must be between /${minPrefix} and /${maxPrefix}.`;
  const addr = ((o[0] << 24) | (o[1] << 16) | (o[2] << 8) | o[3]) >>> 0;
  const mask = len === 0 ? 0 : (0xffffffff << (32 - len)) >>> 0;
  if ((addr & mask) >>> 0 !== addr) {
    const net = (addr & mask) >>> 0;
    return `"${raw}" has host bits set; write the network address, ${[net >>> 24, (net >>> 16) & 255, (net >>> 8) & 255, net & 255].join(".")}/${len}.`;
  }
  return null;
}

/** First problem in the whole list (including the count), or null. */
export function allowListProblem(entries: string[], max = 64, minPrefix = 8, maxPrefix = 32): string | null {
  for (const e of entries) {
    const p = cidrProblem(e, minPrefix, maxPrefix);
    if (p) return p;
  }
  if (new Set(entries).size > max) return `At most ${max} networks may be allowed.`;
  return null;
}

export function sameList(a: string[], b: string[]): boolean {
  const x = [...new Set(a)].sort();
  const y = [...new Set(b)].sort();
  return x.length === y.length && x.every((v, i) => v === y[i]);
}

/** The exact text the operator must type before private-network denial can be turned off. */
export const TURN_OFF_PHRASE = "TURN OFF";
