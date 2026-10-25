// Factory network, staged changes and Access / Trunk port mode, as the Admin Console API reports them.

export type FactoryState = "awaiting_assignment" | "lan_unknown" | "adopted" | "staged" | "activated" | "blocked" | "prepared";

export type FactoryInfo = {
  state: FactoryState;
  reason: string;
  template: {
    name: string;
    mode: string;
    subnet_cidr: string;
    gateway_ip: string;
    dhcp: string;
    pool: { start_ip: string; end_ip: string };
    services: string[];
    pms: string;
  };
  lan_interface?: string;
  network_id?: string;
  conflicts: string[];
  checklist: string[];
};

export type StagedChange = { id: string; network_id: string; network: string; kind: "ENABLE" | "DISABLE"; state: string };

export type StagedGroup = {
  group_id: string;
  group_kind: "ACTIVATE" | "FACTORY_ACTIVATE" | "PORT_MODE" | "RESTORE" | "FACTORY_RESET" | "FULL_RESET";
  state: "PENDING" | "APPLIED";
  created_at: string;
  changes: StagedChange[];
};

export const GROUP_LABEL: Record<StagedGroup["group_kind"], string> = {
  ACTIVATE: "Activation",
  FACTORY_ACTIVATE: "Factory network activation",
  PORT_MODE: "Port mode change",
  RESTORE: "Restore",
  FACTORY_RESET: "Restore factory network defaults",
  FULL_RESET: "Full client networking reset",
};

export type PortNetwork = { id: string; name: string; vlan_id?: number; subnet_cidr: string; enabled: boolean; active_clients: number };

export type PortInfo = {
  name: string;
  protected: boolean;
  mode: "access" | "trunk" | "unused";
  native?: PortNetwork;
  vlans: PortNetwork[];
  staged_changes: number;
};

export type PortModePlan = {
  mode: "access" | "trunk";
  port: string;
  create: { name: string; vlan_id: number; subnet_cidr: string; gateway_ip: string; pools: { start_ip: string; end_ip: string }[] }[];
  switch_off: PortNetwork[];
  keep_native?: PortNetwork;
  impact: string[];
  switch_requirements: string[];
  conflicts: string[];
};

export const PORT_MODE_LABEL: Record<PortInfo["mode"], string> = { access: "Access", trunk: "Trunk", unused: "Not in use" };
