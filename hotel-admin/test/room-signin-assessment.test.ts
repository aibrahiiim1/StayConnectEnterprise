import { describe, it, expect } from "vitest";
import { readinessFromAssessment } from "@/lib/pms-availability";
import type { AssessmentNetwork, RoomSignInAssessment } from "@/lib/api/pms-routing";

const net = (id: string, state: AssessmentNetwork["state"], over: Partial<AssessmentNetwork> = {}): AssessmentNetwork => ({
  network_id: id, name: `Net ${id}`, enabled: true, is_default: false, policy: "DEDICATED", state, summary: `summary ${id}`,
  interfaces: [{ pms_interface_id: "p1", label: "Protel", lifecycle_state: "ACTIVE", published: true, health_known: true, room_auth_ready: state === "READY" }],
  reasons: [], ...over,
});
const a = (overall: RoomSignInAssessment["overall"], networks: AssessmentNetwork[]): RoomSignInAssessment =>
  ({ generated_at: "x", overall, networks, interfaces: [], issues: [] });

describe("the view of the shared room sign-in assessment", () => {
  it("is unknown, never an outage, when the assessment could not be read", () => {
    expect(readinessFromAssessment(null).state).toBe("unknown");
    expect(readinessFromAssessment(undefined).state).toBe("unknown");
    expect(readinessFromAssessment({} as RoomSignInAssessment).state).toBe("unknown");
    expect(readinessFromAssessment(a("UNKNOWN", [net("1", "UNKNOWN")])).state).toBe("unknown");
  });

  it("a PMS-free site is 'none', which is not a fault", () => {
    expect(readinessFromAssessment(a("NOT_OFFERED", [net("1", "OFF", { policy: "NONE" })])).state).toBe("none");
  });

  it("lists exactly the networks that cannot serve, with the interface and the reason", () => {
    const r = readinessFromAssessment(a("PARTIAL", [
      net("1", "READY"),
      net("2", "BLOCKED", { reasons: [{ code: "FEED_SILENT", text: "Protel: the PMS has stopped responding", pms_interface_id: "p1" }] }),
      net("3", "OFF", { policy: "NONE" }),
    ]));
    expect(r.state).toBe("partial");
    if (r.state !== "partial") return;
    expect(r.affected).toHaveLength(1);
    expect(r.affected[0]).toMatchObject({ guestNetwork: "Net 2", pmsInterface: "Protel", networkId: "2" });
    expect(r.affected[0].reason).toContain("the PMS has stopped responding");
  });

  it("an undecided network is reported as needing a decision, not hidden", () => {
    const r = readinessFromAssessment(a("BLOCKED", [net("1", "UNDECIDED", { policy: "", interfaces: [], summary: "No one has decided how room sign-in works on this network." })]));
    expect(r.state).toBe("down");
    if (r.state === "down") expect(r.affected[0].pmsInterface).toBe("no PMS chosen");
  });

  it("names unchecked networks without calling them failed", () => {
    const r = readinessFromAssessment(a("READY", [net("1", "READY"), net("2", "UNKNOWN")]));
    expect(r.state).toBe("ready");
    if (r.state === "ready") expect(r.unchecked).toEqual(["Net 2"]);
  });
});
