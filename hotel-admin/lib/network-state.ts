// One reading of where a client network stands, shared by every screen that lists or chooses networks.
//
//   serving   enabled and not archived: a place clients can be served right now (whether it is healthy is the network overview's question)
//   off       switched off but still part of the current set-up; it can be switched on again
//   archived  history kept by a reset or a replacement; it can be restored from the archive
//
// Only serving networks are offered as places to do something (assign a design, route room sign-in, pick for a package). Off and archived networks are shown, clearly marked,
// where their history or their settings still matter.

export type ServiceState = "serving" | "off" | "archived";

export type NetworkLike = {
  enabled?: boolean;
  archived?: boolean;
  service_state?: string;
  network_type?: string;
  vlan_id?: number | null;
  parent_interface?: string;
};

export function serviceStateOf(n: Pick<NetworkLike, "enabled" | "archived" | "service_state">): ServiceState {
  if (n.service_state === "serving" || n.service_state === "off" || n.service_state === "archived") return n.service_state;
  if (n.archived) return "archived";
  return n.enabled === false ? "off" : "serving";
}

export const SERVICE_STATE_WORDS: Record<ServiceState, { label: string; tone: "ok" | "neutral" | "warn"; meaning: string }> = {
  serving: { label: "Serving clients", tone: "ok", meaning: "Switched on. Clients can join and see this network's sign-in page." },
  off: { label: "Switched off", tone: "neutral", meaning: "Not serving clients. Its settings are kept and it can be switched on again." },
  archived: { label: "Archived", tone: "warn", meaning: "History from a reset or a replacement. It serves no one and can be restored from the archive." },
};

export const isServing = (n: Pick<NetworkLike, "enabled" | "archived" | "service_state">) => serviceStateOf(n) === "serving";

export function splitByState<T extends Pick<NetworkLike, "enabled" | "archived" | "service_state">>(list: T[]): Record<ServiceState, T[]> {
  const out: Record<ServiceState, T[]> = { serving: [], off: [], archived: [] };
  for (const n of list) out[serviceStateOf(n)].push(n);
  return out;
}

/** How clients reach the network. The port a network is on does not decide its purpose: an untagged (native) network and tagged VLANs can share one trunk port. */
export function topologyWords(n: Pick<NetworkLike, "network_type" | "vlan_id" | "parent_interface">): { label: string; detail: string } {
  const port = n.parent_interface ? ` on ${n.parent_interface}` : "";
  if (n.network_type === "vlan") {
    return { label: `VLAN ${n.vlan_id ?? "?"}`, detail: `Tagged (802.1Q)${port}. The switch must carry VLAN ${n.vlan_id ?? "?"} to the appliance.` };
  }
  return { label: "Untagged (native)", detail: `Arrives untagged${port}. On a trunk port this is the native VLAN.` };
}
