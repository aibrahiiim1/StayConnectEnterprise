export type RoutingMode = "NONE" | "DEDICATED" | "SHARED_STRICT";
/** "" = nobody has decided how room sign-in works on this network. */
export type RoutingPolicy = RoutingMode | "";

export type RoutingInterface = {
  pms_interface_id: string;
  label: string;
  lifecycle_state: string;
  published: boolean;
};

export type RoutingState = {
  network_id: string;
  name: string;
  enabled: boolean;
  archived: boolean;
  is_default: boolean;
  mode: RoutingPolicy;
  interfaces: RoutingInterface[];
  configured_at?: string;
  configured_by?: string;
  /** Sent back with a change so only the previewed state can be replaced. */
  fingerprint: string;
};

export type RoutingNote = { code: string; text: string; pms_interface_id?: string };

export type RoutingImpactInterface = RoutingInterface & {
  in_house_stays: number;
  room_auth_ready: boolean;
  health_known: boolean;
  room_auth_reason?: string;
  also_used_by: string[];
  live_sessions_on_this_network: number;
};

export type RoutingPlan = {
  network: RoutingState;
  proposed: { mode: string; interfaces: RoutingImpactInterface[] };
  added: RoutingInterface[];
  removed: RoutingInterface[];
  blockers: RoutingNote[];
  warnings: RoutingNote[];
  impact: {
    active_sessions: number;
    live_room_entitlements: number;
    room_numbers_in_more_than_one_selected_pms: number;
    sign_in_methods_switched_off: string[];
    network_serves_clients: boolean;
  };
  guarantees: string[];
};

export type AssessmentState = "OFF" | "READY" | "BLOCKED" | "UNKNOWN" | "UNDECIDED" | "NOT_SET" | "MISCONFIGURED" | "INACTIVE";

export type AssessmentInterface = {
  pms_interface_id: string;
  label: string;
  lifecycle_state: string;
  published: boolean;
  health_known: boolean;
  room_auth_ready: boolean;
  room_auth_reason?: string;
  room_auth_source?: string;
  transport_status?: string;
};

export type AssessmentNetwork = {
  network_id: string;
  name: string;
  enabled: boolean;
  is_default: boolean;
  policy: RoutingPolicy;
  legacy_unclassified?: boolean;
  state: AssessmentState;
  interfaces: AssessmentInterface[];
  reasons: { code: string; text: string; pms_interface_id?: string; pms_interface_label?: string }[];
  summary: string;
};

export type AssessmentIssue = {
  id: string;
  severity: "warn" | "err";
  title: string;
  detail: string;
  href: string;
  action: string;
  network_id?: string;
};

export type AssessmentInterfaceSummary = AssessmentInterface & {
  serving_networks: string[];
  archived_or_off_routes: number;
  state: "SERVING" | "IMPAIRED" | "NOT_USED" | "UNKNOWN" | "NOT_IN_SERVICE";
};

export type RoomSignInAssessment = {
  generated_at: string;
  overall: "READY" | "PARTIAL" | "BLOCKED" | "NOT_OFFERED" | "UNKNOWN";
  networks: AssessmentNetwork[];
  interfaces: AssessmentInterfaceSummary[];
  issues: AssessmentIssue[];
};

export type RoomSignInBrief = { policy: RoutingPolicy; state: AssessmentState; summary: string; interfaces: string[] };

export type RoomSignInStep = {
  offered: boolean;
  network_id?: string;
  network_name?: string;
  policy: RoutingPolicy;
  state?: AssessmentState;
  summary?: string;
};

export type NetworkConsequence = {
  network_id: string;
  name: string;
  enabled: boolean;
  active_clients: number;
  active_sessions: number;
  live_room_entitlements: number;
  room_sign_in?: RoomSignInBrief;
  sign_in_methods_switched_off: string[];
  packages_limited_to_this_network: string[];
  notes: string[];
};

export const POLICY_WORDS: Record<string, string> = {
  NONE: "No room sign-in",
  DEDICATED: "One dedicated PMS",
  SHARED_STRICT: "Shared strict set",
  "": "Not decided",
};

export const STATE_WORDS: Record<AssessmentState, { label: string; tone: "ok" | "warn" | "err" | "neutral" }> = {
  READY: { label: "Working", tone: "ok" },
  OFF: { label: "Not offered", tone: "neutral" },
  BLOCKED: { label: "Not working", tone: "err" },
  UNKNOWN: { label: "Not known", tone: "neutral" },
  UNDECIDED: { label: "Needs a decision", tone: "warn" },
  NOT_SET: { label: "Not set up", tone: "neutral" },
  MISCONFIGURED: { label: "Needs correcting", tone: "err" },
  INACTIVE: { label: "Network off", tone: "neutral" },
};
