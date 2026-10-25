export type ArchivedNetwork = {
  id: string;
  name: string;
  archived_at: string;
  retired_by: string;
  has_history: boolean;
  has_pms_routing: boolean;
};

export type ReadinessItem = { id: string; label: string; state: "ok" | "attention" | "unresolved"; detail: string };

export type PmsCandidate = { pms_interface_id: string; label: string; routed_from: string[]; preview: string };

export type PinnedPackage = { code: string; revision: number; networks: string[] };

export type DefaultReadiness = {
  network_id: string;
  name: string;
  present: boolean;
  items: ReadinessItem[];
  pms_candidates: PmsCandidate[];
  packages_pinned_to_other_networks: PinnedPackage[];
};
