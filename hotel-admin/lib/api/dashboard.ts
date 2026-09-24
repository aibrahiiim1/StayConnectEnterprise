// THE OVERVIEW CONTRACT (GET /edge/v1/reports/overview?range=24h|7d|30d, GET /reports/appliance-resources).
//
// Mirrors data-plane/cmd/edged/resources_overview.go. Every block carries its own `available`/`reason`; a block
// that is unavailable is rendered as the reason, never as zeros. `normalizeOverview` fills a block the payload
// did not carry (an older edged, a proxy error page) as unavailable, so a partial response degrades to an honest
// surface instead of a client-side exception.

import { api } from "@/lib/api";

export type OverviewRange = "24h" | "7d" | "30d";
export const OVERVIEW_RANGES: { value: OverviewRange; label: string; long: string }[] = [
  { value: "24h", label: "24h", long: "the last 24 hours" },
  { value: "7d", label: "7 days", long: "the last 7 days" },
  { value: "30d", label: "30 days", long: "the last 30 days" },
];

export type Block = { available: boolean; reason?: string };

export type OvMethod = { method: string; total: number; series: number[] };

export type OvGuests = Block & {
  devices_online: number;
  guests_online: number;
  sign_ins: number;
  unique_devices: number;
  sign_ins_series: number[];
  sign_ins_by_method: OvMethod[];
  concurrency: Block & { definition: string; peak: number[]; average: number[]; peak_in_range: number };
  heatmap: { rows: string[]; columns: string[]; values: number[][] };
};

export type OvTrafficRow = { key: string; name: string; bytes_down: number; bytes_up: number };

export type OvTraffic = Block & {
  bytes_down: number;
  bytes_up: number;
  series_down: number[];
  series_up: number[];
  top_networks: OvTrafficRow[];
  top_packages: OvTrafficRow[];
  sessions_with_traffic: number;
  computed_at?: string;
  source?: string;
};

export type OvSignIn = Block & {
  total: number;
  verified: number;
  by_result: { result: string; count: number }[];
  series_verified: number[];
  series_failed: number[];
  latency: { samples: number; p50_ms: number | null; p95_ms: number | null };
  covered: string;
  not_recorded: string[];
};

export type OvPackageRow = { package_id: string; name: string; code?: string; count: number };

export type OvPackages = Block & {
  active_now: OvPackageRow[];
  grants: OvPackageRow[];
  terminations: { reason: string; count: number }[];
};

export type OvPmsInterface = {
  pms_interface_id: string;
  display_label: string;
  lifecycle_state: string;
  published: boolean;
  transport_status?: string;
  continuity_status?: string;
  sync_status?: string;
  sync_stage?: string;
  room_auth_ready: boolean;
  room_auth_reason?: string;
  in_house_stays: number;
  pending_events: number;
  review_events: number;
  last_stay_event_at?: string | null;
  last_heartbeat_at?: string | null;
  last_complete_sync_at?: string | null;
  materialization_ready?: boolean;
};

export type OvPms = Block & {
  interfaces: OvPmsInterface[];
  events_today: number;
  events_applied_today: number;
  events_needing_review: number;
  historical_exceptions?: number;
  active_interfaces: number;
  ready_interfaces: number;
  occupancy: Block & {
    in_house: number;
    with_internet: number;
    arrivals_today: number;
    departures_today: number;
    posting_allowed: number;
  };
  postings: Block & {
    posted_today: number;
    failed_today: number;
    pending: number;
    review_open: number;
    unknown_open: number;
  };
};

export type OvNetwork = {
  id: string;
  name: string;
  enabled: boolean;
  vlan_id?: number | null;
  subnet_cidr: string;
  bridge_name: string;
  dhcp_mode: string;
  dns_mode: string;
  dns_servers: string[];
  captive_portal_enabled: boolean;
  internet_access_enabled: boolean;
  pool_size: number;
  invalid_pools?: number;
  devices_online: number;
  leases_known: boolean;
  active_leases?: number;
  leases_in_pool?: number;
  expiring_soon?: number;
  utilisation_pct?: number;
  traffic_known: boolean;
  bytes_down: number;
  bytes_up: number;
};

export type OvNetworks = Block & { rows: OvNetwork[]; leases_reason?: string };

export type OvDhcp = Block & {
  server_configured?: boolean;
  server_healthy?: boolean;
  server_detail?: string;
  leases_available: boolean;
  leases_reason?: string;
  active_leases: number;
  unmatched_leases: number;
  expiring_soon: number;
  expiring_within_seconds: number;
  pools: { network: string; start: string; end: string; size: number; active: number; utilisation_pct?: number }[];
  captive_option_networks: string[];
  local_dhcp_networks: number;
};

export type OvDns = Block & {
  resolver_state: string;
  resolver_healthy?: boolean | null;
  resolver_detail?: string;
  last_healthy_at?: string | null;
  networks: { network: string; mode: string; servers: string[] }[];
  statistics_collected: boolean;
};

export type OvService = {
  name: string;
  label: string;
  state: string;
  health_ok: boolean | null;
  critical: boolean;
  detail?: string;
  restart_count: number;
  last_healthy_at?: string | null;
  failures_in_range: number;
  manual_restarts_in_range: number;
};

export type OvServices = Block & { overall?: string; counts?: Record<string, number>; services: OvService[] };

export type ApplianceResources = Block & {
  collected_at?: string;
  load?: { one: number; five: number; fifteen: number; cpus: number; one_per_cpu: number };
  memory?: { total_bytes: number; available_bytes: number; used_bytes: number; used_pct: number };
  uptime_seconds?: number;
  disks: {
    path: string;
    total_bytes: number;
    used_bytes: number;
    available_bytes: number;
    used_pct: number;
    same_filesystem_as?: string;
  }[];
  errors?: string[];
};

export type OvRevision = {
  seq: number;
  state: string;
  applied_at?: string | null;
  confirmed_at?: string | null;
  confirm_deadline?: string | null;
};

export type OvAppliance = {
  version: string;
  license: Block & { state?: string; installed: boolean; valid_until?: string; grace_until?: string };
  network: Block & {
    wan_interface?: string;
    wan_mode?: string;
    wan_address?: string;
    wan_gateway?: string;
    wan_link_up?: boolean;
    gateway_reachable?: boolean;
    internet_reachable?: boolean;
    lan_bridge?: string;
    lan_address?: string;
    lan_link_up?: boolean;
    system_change_pending?: boolean;
    checked_at?: string;
  };
  revisions: Block & { latest_active?: OvRevision; pending?: OvRevision };
  resources: ApplianceResources;
};

export type AttentionItem = {
  id: string;
  severity: "warn" | "err";
  title: string;
  detail?: string;
  href: string;
  action: string;
};

export type OverviewSnapshot = {
  generated_at: string;
  range: OverviewRange;
  window_start: string;
  window_end: string;
  bucket_seconds: number;
  buckets: string[];
  timezone?: string;
  guests: OvGuests;
  traffic: OvTraffic;
  sign_in_outcomes: OvSignIn;
  packages: OvPackages;
  pms: OvPms;
  networks: OvNetworks;
  dhcp: OvDhcp;
  dns: OvDns;
  services: OvServices;
  appliance: OvAppliance;
  attention: AttentionItem[];
};

export function fetchOverview(range: OverviewRange): Promise<OverviewSnapshot> {
  return api.get<OverviewSnapshot>(`/reports/overview?range=${encodeURIComponent(range)}`);
}

// ---------------------------------------------------------------------------------------------------------
// normalisation
// ---------------------------------------------------------------------------------------------------------

const ABSENT: Block = { available: false, reason: "section_absent_from_response" };
const arr = <T,>(v: T[] | undefined | null): T[] => (Array.isArray(v) ? v : []);

export function normalizeOverview(d: Partial<OverviewSnapshot> | null | undefined): OverviewSnapshot {
  const x = (d ?? {}) as Partial<OverviewSnapshot>;
  const n = arr(x.buckets).length;
  const zeros = () => Array.from({ length: n }, () => 0);
  const g = x.guests;
  const t = x.traffic;
  const s = x.sign_in_outcomes;
  const p = x.packages;
  const pms = x.pms;
  const a = x.appliance;
  return {
    generated_at: x.generated_at ?? new Date().toISOString(),
    range: (x.range as OverviewRange) ?? "24h",
    window_start: x.window_start ?? "",
    window_end: x.window_end ?? "",
    bucket_seconds: x.bucket_seconds ?? 3600,
    buckets: arr(x.buckets),
    timezone: x.timezone,
    guests: g
      ? {
          ...g,
          sign_ins_series: arr(g.sign_ins_series).length ? g.sign_ins_series : zeros(),
          sign_ins_by_method: arr(g.sign_ins_by_method),
          concurrency: g.concurrency
            ? { ...g.concurrency, peak: arr(g.concurrency.peak), average: arr(g.concurrency.average) }
            : { ...ABSENT, definition: "", peak: [], average: [], peak_in_range: 0 },
          heatmap: g.heatmap ?? { rows: [], columns: [], values: [] },
        }
      : {
          ...ABSENT, devices_online: 0, guests_online: 0, sign_ins: 0, unique_devices: 0, sign_ins_series: [],
          sign_ins_by_method: [], concurrency: { ...ABSENT, definition: "", peak: [], average: [], peak_in_range: 0 },
          heatmap: { rows: [], columns: [], values: [] },
        },
    traffic: t
      ? { ...t, series_down: arr(t.series_down), series_up: arr(t.series_up), top_networks: arr(t.top_networks), top_packages: arr(t.top_packages) }
      : { ...ABSENT, bytes_down: 0, bytes_up: 0, series_down: [], series_up: [], top_networks: [], top_packages: [], sessions_with_traffic: 0 },
    sign_in_outcomes: s
      ? {
          ...s, by_result: arr(s.by_result), series_verified: arr(s.series_verified), series_failed: arr(s.series_failed),
          latency: s.latency ?? { samples: 0, p50_ms: null, p95_ms: null }, not_recorded: arr(s.not_recorded),
        }
      : {
          ...ABSENT, total: 0, verified: 0, by_result: [], series_verified: [], series_failed: [],
          latency: { samples: 0, p50_ms: null, p95_ms: null }, covered: "",
          not_recorded: ["VOUCHER failures", "ACCOUNT failures", "OTP failures"],
        },
    packages: p
      ? { ...p, active_now: arr(p.active_now), grants: arr(p.grants), terminations: arr(p.terminations) }
      : { ...ABSENT, active_now: [], grants: [], terminations: [] },
    pms: pms
      ? {
          ...pms,
          interfaces: arr(pms.interfaces),
          occupancy: pms.occupancy ?? { ...ABSENT, in_house: 0, with_internet: 0, arrivals_today: 0, departures_today: 0, posting_allowed: 0 },
          postings: pms.postings ?? { ...ABSENT, posted_today: 0, failed_today: 0, pending: 0, review_open: 0, unknown_open: 0 },
        }
      : {
          ...ABSENT, interfaces: [], events_today: 0, events_applied_today: 0, events_needing_review: 0,
          active_interfaces: 0, ready_interfaces: 0,
          occupancy: { ...ABSENT, in_house: 0, with_internet: 0, arrivals_today: 0, departures_today: 0, posting_allowed: 0 },
          postings: { ...ABSENT, posted_today: 0, failed_today: 0, pending: 0, review_open: 0, unknown_open: 0 },
        },
    networks: x.networks ? { ...x.networks, rows: arr(x.networks.rows) } : { ...ABSENT, rows: [] },
    dhcp: x.dhcp
      ? { ...x.dhcp, pools: arr(x.dhcp.pools), captive_option_networks: arr(x.dhcp.captive_option_networks) }
      : {
          ...ABSENT, leases_available: false, active_leases: 0, unmatched_leases: 0, expiring_soon: 0,
          expiring_within_seconds: 600, pools: [], captive_option_networks: [], local_dhcp_networks: 0,
        },
    dns: x.dns
      ? { ...x.dns, networks: arr(x.dns.networks) }
      : { ...ABSENT, resolver_state: "unknown", networks: [], statistics_collected: false },
    services: x.services ? { ...x.services, services: arr(x.services.services) } : { ...ABSENT, services: [] },
    appliance: {
      version: a?.version ?? "",
      license: a?.license ?? { ...ABSENT, installed: false },
      network: a?.network ?? { ...ABSENT },
      revisions: a?.revisions ?? { ...ABSENT },
      resources: a?.resources ? { ...a.resources, disks: arr(a.resources.disks) } : { ...ABSENT, disks: [] },
    },
    attention: arr(x.attention),
  };
}

// ---------------------------------------------------------------------------------------------------------
// words
// ---------------------------------------------------------------------------------------------------------

const REASONS: Record<string, string> = {
  not_permitted_for_role: "Your role does not include this information.",
  section_absent_from_response: "This appliance's software does not report this yet.",
  sessions_unreadable: "Session records could not be read.",
  too_many_sessions_in_range:
    "There are too many sessions in this range to compute it exactly within the time allowed. Choose a shorter range.",
  range_too_large_for_time_budget:
    "The traffic history for this range is too large to compute in the time allowed. Choose a shorter range.",
  samples_unreadable: "Traffic records could not be read.",
  sign_in_attempts_unreadable: "The room sign-in attempt log could not be read.",
  entitlements_unreadable: "Package records could not be read.",
  guest_networks_unreadable: "Guest network configuration could not be read.",
  network_controller_unreachable: "The network controller did not answer.",
  dhcp_leases_unreadable: "The DHCP server's leases could not be read.",
  no_health_records_yet: "The health monitor has not recorded any service yet.",
  session_controller_unreachable: "The session controller did not answer.",
  license_unreadable: "The licence status could not be read.",
  network_state_unreadable: "The WAN/LAN state could not be read.",
  revisions_unreadable: "Network change history could not be read.",
  unavailable_on_this_platform: "Resource figures are only available on the appliance itself.",
  resources_unreadable: "The appliance's resource counters could not be read.",
  pms_not_enabled: "No PMS connection is in use on this appliance.",
  no_pms_stay_data: "No PMS guest list is available on this appliance.",
  charges_not_enabled: "Room charging is not in use on this appliance.",
};

export function reasonText(reason?: string): string {
  if (!reason) return "Not available.";
  return REASONS[reason] ?? "Not available on this appliance.";
}

const METHODS: Record<string, string> = {
  PMS: "Room number",
  VOUCHER: "Voucher",
  ACCOUNT: "Guest account",
  PRINCIPAL: "Returning guest",
  UNKNOWN: "Not recorded",
};
export const methodLabel = (m: string) => METHODS[m] ?? m.charAt(0) + m.slice(1).toLowerCase().replace(/_/g, " ");

const RESULTS: Record<string, string> = {
  VERIFIED: "Verified",
  VERIFIED_NO_ELIGIBLE_PACKAGE: "Verified, no package applies",
  CREDENTIAL_MISMATCH: "Name or reservation did not match",
  ROOM_NOT_IN_MIRROR: "Room not in the guest list",
  STAY_NOT_ELIGIBLE: "Stay not eligible",
  AMBIGUOUS_ROOM_CANDIDATES: "More than one stay matched",
  MIRROR_STALE_OR_MISSING_CHANGE: "Guest list out of date",
  RATE_LIMITED: "Too many attempts",
  ROUTING_OR_INTERFACE_FAILURE: "PMS connection problem",
  SERVICE_UNAVAILABLE: "Service unavailable",
  SPENT_REQUEST_ID: "Repeated submission",
  MALFORMED_SUBMISSION: "Incomplete form",
};
export const resultLabel = (r: string) => RESULTS[r] ?? r.replace(/_/g, " ").toLowerCase();

const NOT_RECORDED: Record<string, string> = {
  "VOUCHER failures": "failed voucher sign-ins",
  "ACCOUNT failures": "failed guest-account sign-ins",
  "OTP failures": "failed one-time-code sign-ins",
};
export const notRecordedLabel = (s: string) => NOT_RECORDED[s] ?? s.toLowerCase();

export const terminationLabel = (r: string) =>
  r === "UNSPECIFIED" ? "No reason recorded" : r.charAt(0) + r.slice(1).toLowerCase().replace(/_/g, " ");

const SERVICE_STATES: Record<string, { label: string; tone: "ok" | "warn" | "err" | "default" }> = {
  healthy: { label: "Healthy", tone: "ok" },
  waiting: { label: "Not needed yet", tone: "default" },
  starting: { label: "Starting", tone: "warn" },
  recovering: { label: "Recovering", tone: "warn" },
  degraded: { label: "Degraded", tone: "err" },
  crash_loop: { label: "Restarting repeatedly", tone: "err" },
  failed: { label: "Down", tone: "err" },
  unknown: { label: "Unknown", tone: "default" },
};
export const serviceState = (s: string) => SERVICE_STATES[s] ?? { label: s, tone: "default" as const };

/** x-axis label for a bucket start, in the viewer's locale. */
export function bucketLabel(iso: string, range: OverviewRange): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "";
  if (range === "24h") return d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
  if (range === "7d") return `${d.toLocaleDateString(undefined, { weekday: "short" })} ${d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" })}`;
  return d.toLocaleDateString(undefined, { day: "numeric", month: "short" });
}

export function formatDuration(seconds?: number): string {
  if (typeof seconds !== "number" || !Number.isFinite(seconds) || seconds < 0) return "—";
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  if (d > 0) return `${d}d ${h}h`;
  if (h > 0) return `${h}h ${m}m`;
  return `${m}m`;
}

export const fmtInt = (n?: number | null) => (typeof n === "number" && Number.isFinite(n) ? n.toLocaleString() : "—");
