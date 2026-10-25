// Client network status and diagnostics, as the Admin Console API reports them. Status is derived on the appliance from what was confirmed and what it
// observes; this module only names the values and words them.

export type NetworkStatusValue =
  | "not_activated"
  | "activating"
  | "active"
  | "active_degraded"
  | "disabled"
  | "change_pending"
  | "failed";

export type NetworkChange = { field: string; running: string; saved: string };

export type OverviewNetwork = {
  id: string;
  name: string;
  network_type: string;
  vlan_id?: number;
  parent_interface: string;
  bridge_name: string;
  subnet_cidr: string;
  gateway_ip: string;
  dhcp_mode: string;
  saved_enabled: boolean;
  running: boolean;
  ever_activated: boolean;
  status: NetworkStatusValue;
  reason: string;
  changes: NetworkChange[];
  active_clients: number;
  pms_interfaces: string[];
  health_known: boolean;
  attention: string[];
};

export type RevisionInfo = {
  id: string;
  seq: number;
  state?: string;
  confirmed_at?: string;
  confirm_deadline?: string;
  reason?: string;
};

export type NetworkOverview = {
  active_revision: RevisionInfo | null;
  in_flight_revision: RevisionInfo | null;
  changes_blocked: boolean;
  blocked_detail?: string;
  diagnostics_available: boolean;
  networks: OverviewNetwork[];
};

export const STATUS_LABEL: Record<NetworkStatusValue, string> = {
  not_activated: "Not activated",
  activating: "Activating",
  active: "Active",
  active_degraded: "Active – needs attention",
  disabled: "Disabled",
  change_pending: "Change not applied",
  failed: "Last change failed",
};

export type StatusTone = "ok" | "warn" | "err" | "info" | "neutral" | "accent";

export const STATUS_TONE: Record<NetworkStatusValue, StatusTone> = {
  not_activated: "neutral",
  activating: "info",
  active: "ok",
  active_degraded: "warn",
  disabled: "neutral",
  change_pending: "accent",
  failed: "err",
};

const FIELD_LABEL: Record<string, string> = {
  enabled: "Switched on",
  name: "Name",
  gateway_ip: "Gateway address",
  subnet_cidr: "Subnet",
  dhcp_mode: "DHCP mode",
  dns_mode: "DNS mode",
  dns_servers: "DNS servers",
  domain_name: "Domain",
  lease_default_seconds: "Default lease",
  lease_min_seconds: "Shortest lease",
  lease_max_seconds: "Longest lease",
  captive_portal_enabled: "Sign-in page",
  internet_access_enabled: "Internet access",
  nat_enabled: "NAT",
  client_isolation_enabled: "Client isolation",
  pools: "Address pools",
};

export function fieldLabel(field: string): string {
  return FIELD_LABEL[field] ?? field;
}

/** One line naming what differs between what is running and what is saved. */
export function changeSummary(changes: NetworkChange[]): string {
  return changes.map((c) => fieldLabel(c.field)).join(", ");
}

export type DiagCheckState = "ok" | "fail" | "unknown" | "info";

export type DiagCheck = { id: string; label: string; state: DiagCheckState; detail?: string; where: string };

export type NetworkDiagnostics = {
  network: { network_id: string; name: string; checks: DiagCheck[]; findings: string[]; outside: string[] };
  server: DiagCheck[];
  revision?: string;
  not_running?: boolean;
  sample_ms?: number;
};

export const CHECK_STATE_LABEL: Record<DiagCheckState, string> = {
  ok: "OK",
  fail: "Problem",
  unknown: "Could not be read",
  info: "Observed",
};
