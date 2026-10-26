
import type { AssessmentNetwork, RoomSignInAssessment } from "@/lib/api/pms-routing";

export type AffectedRoute = {
  guestNetwork: string;
  pmsInterface: string;
  reason: string;
  networkId?: string;
  undecided?: boolean;
};

export type RoomSignInReadiness =
  | { state: "unknown" }
  | { state: "none" }
  | { state: "ready"; unchecked: string[] }
  | { state: "down"; reason: string; affected: AffectedRoute[]; unchecked: string[] }
  | { state: "partial"; affected: AffectedRoute[]; unchecked: string[] };

const FAULTY = new Set(["BLOCKED", "MISCONFIGURED", "UNDECIDED"]);

const lc = (t: string) => (t ? t.charAt(0).toLowerCase() + t.slice(1) : t);

function affected(n: AssessmentNetwork): AffectedRoute {
  const names = n.interfaces.map((i) => i.label).filter(Boolean);
  const reason = n.reasons.length > 0 ? n.reasons.map((r) => r.text).join("; ") : n.summary;
  return {
    guestNetwork: n.name || n.network_id,
    pmsInterface: names.length === 0 ? "no PMS chosen" : names.join(", "),
    reason: lc(reason).replace(/\.$/, ""),
    networkId: n.network_id,
    undecided: n.state === "UNDECIDED",
  };
}

export function onlyUndecided(r: RoomSignInReadiness): boolean {
  return (r.state === "down" || r.state === "partial") && r.affected.length > 0 && r.affected.every((a) => a.undecided);
}

export function readinessFromAssessment(a: RoomSignInAssessment | null | undefined): RoomSignInReadiness {
  if (!a || !Array.isArray(a.networks)) return { state: "unknown" };
  const faulty = a.networks.filter((n) => FAULTY.has(n.state)).map(affected);
  const unchecked = a.networks.filter((n) => n.state === "UNKNOWN").map((n) => n.name || n.network_id);
  switch (a.overall) {
    case "READY":
      return { state: "ready", unchecked };
    case "NOT_OFFERED":
      return { state: "none" };
    case "BLOCKED": {
      const reasons = new Set(faulty.map((f) => f.reason));
      return { state: "down", reason: reasons.size === 1 ? [...reasons][0] : "room sign-in is unavailable on every network that offers it", affected: faulty, unchecked };
    }
    case "PARTIAL":
      return { state: "partial", affected: faulty, unchecked };
    default:
      return { state: "unknown" };
  }
}
