export type RecoveryOp = "restore_last_working" | "factory_defaults" | "full_reset";

export type PlanNetwork = { id: string; name: string; action: "switch_on" | "switch_off" | "restore_settings"; changes?: string[]; active_clients: number };

export type ReviewLine = { network_id: string; network: string; layer: string; state: "UNCHANGED" | "CHANGED" | "MISSING" | "NO_RECORD"; detail: string };

export type RecoveryPlan = {
  op: RecoveryOp;
  summary: string;
  networks: PlanNetwork[];
  review: ReviewLine[];
  cannot_restore: string[];
  conflicts: string[];
  protected: string[];
  switch_checklist: string[];
  confirm_site_name?: string;
};

export type RecoveryStatus = {
  last_working: { revision: number | null; confirmed_at: string | null; available: boolean };
  factory: { available: boolean; subnet: string };
  review_open: number;
};

export type ReviewItem = { id: string; network_id: string; network: string; layer: string; detail: string; room_sign_in_paused: boolean; created_at: string };

export const OP_TITLE: Record<RecoveryOp, string> = {
  restore_last_working: "Restore last working",
  factory_defaults: "Restore factory defaults",
  full_reset: "Full client networking reset",
};

export const ACTION_LABEL: Record<PlanNetwork["action"], string> = {
  switch_on: "Switched on",
  switch_off: "Switched off",
  restore_settings: "Settings put back",
};

export const LAYER_LABEL: Record<string, string> = {
  portal_profile: "Portal design",
  signin: "Sign-in options",
  pms_routing: "PMS routing",
  all: "Portal design, sign-in options and PMS routing",
};

export const STATE_LABEL: Record<string, string> = {
  UNCHANGED: "Unchanged",
  CHANGED: "Restored – review needed",
  MISSING: "No longer exists",
  NO_RECORD: "No record",
};
