// Plain-language health states with a tone. Kept here so the dashboard, health pill, Diagnostics and Appliance &
// licence cannot contradict each other.

export type Tone = "ok" | "warn" | "err" | "default";

export type Explained = {
  headline: string;
  summary: string;
  tone: Tone;
};

const n = (v?: number | null) => (typeof v === "number" && Number.isFinite(v) ? v : 0);
const num = (v: number) => v.toLocaleString();

export function describeDatabase(ok: boolean): Explained {
  return ok
    ? {
        headline: "Reachable",
        summary: "The appliance's own database is answering. Every screen in this admin reads from it.",
        tone: "ok",
      }
    : {
        headline: "Unreachable",
        summary:
          "The appliance cannot reach its own database. Client sign-in and this admin both depend on it, so " +
          "treat this as the first thing to fix.",
        tone: "err",
      };
}

export function describeSessionController(ok: boolean): Explained {
  return ok
    ? {
        headline: "Running",
        summary:
          "The service that puts client devices online and enforces speed and data limits is answering.",
        tone: "ok",
      }
    : {
        headline: "Not answering",
        summary:
          "The service that puts client devices online is not answering. New clients cannot be connected and " +
          "limits are not being enforced until it recovers.",
        tone: "err",
      };
}

/**
 * Licence state in terms of what it stops. States are the appliance's section 8 vocabulary
 * (docs/CENTRAL_CONTROL_PLANE.md), keyed lowercase.
 */
export function describeLicense(state: string | null | undefined): Explained {
  switch (state) {
    case "none":
    case null:
    case undefined:
      return {
        headline: "Not activated",
        summary:
          "No licence is installed yet, so clients cannot sign in. Activate this appliance on Appliance & licence.",
        tone: "warn",
      };
    case "active":
      return { headline: "Active", summary: "This appliance is licensed and fully enabled.", tone: "ok" };
    case "expiring":
      return {
        headline: "Expires soon",
        summary: "The licence ends within 30 days. Ask your OneGate vendor to renew it; clients are not affected yet.",
        tone: "warn",
      };
    case "grace":
      // Grace begins when the licence's own end date passes; losing Central changes nothing.
      return {
        headline: "Grace period",
        summary:
          "The licence end date has passed and the appliance is running on its renewal grace period. " +
          "Clients keep signing in exactly as before. When the grace period ends, new sign-ins stop; " +
          "sessions already in progress are not cut off. Appliance & licence shows the exact end date.",
        tone: "warn",
      };
    case "suspended":
      return {
        headline: "Suspended",
        summary:
          "Your OneGate vendor has suspended the licence. New client sign-ins are refused; clients already online " +
          "are not disconnected.",
        tone: "err",
      };
    case "expired":
      return {
        headline: "Expired",
        summary:
          "The licence end date and its grace period have both passed. New client sign-ins are now refused; " +
          "clients already online are not disconnected. Renew to restore service.",
        tone: "err",
      };
    case "revoked":
      return {
        headline: "Revoked",
        summary:
          "Your OneGate vendor has revoked this licence. New client sign-ins are refused; clients already online " +
          "are not disconnected.",
        tone: "err",
      };
    case "wrong_hardware":
      return {
        headline: "Wrong appliance",
        summary:
          "The installed licence was issued for a different appliance, so new client sign-ins are refused. Ask your " +
          "OneGate vendor for a licence for this appliance's serial number.",
        tone: "err",
      };
    default:
      return { headline: state, summary: "The licence state could not be interpreted.", tone: "default" };
  }
}

/** PMS transport/sync as one sentence: can a guest type their room number and get online now. */
export function describePmsReadiness(args: {
  transport?: string;
  sync?: string;
  roomAuthReady?: boolean;
  /** Which roster answers while ready: the live feed, or the last complete guest list. */
  source?: string;
  inHouse?: number;
}): Explained {
  const { transport, sync, roomAuthReady, source, inHouse } = args;
  if (roomAuthReady && source === "LAST_GOOD_ROSTER") {
    const offline = transport !== "CONNECTED";
    return {
      headline: offline ? "Room sign-in working without the PMS" : "Room sign-in working while the guest list refreshes",
      summary:
        (offline ? "The PMS is not connected. " : "The PMS is sending a fresh guest list. ") +
        "Clients sign in against the last complete guest list" +
        (typeof inHouse === "number" ? ` (${num(inHouse)} in house)` : "") +
        (offline ? " until the connection returns or the list grows too old." : " until the new one is in place."),
      tone: "warn",
    };
  }
  if (roomAuthReady) {
    return {
      headline: "Room sign-in working",
      summary:
        `The PMS is connected and its guest list is loaded` +
        (typeof inHouse === "number" ? ` (${num(inHouse)} in house)` : "") +
        ". Clients can sign in with their room number and name.",
      tone: "ok",
    };
  }
  if (transport === "DISCONNECTED" || transport === "UNKNOWN") {
    return {
      headline: "PMS not connected",
      summary:
        "The appliance is not connected to the property management system, so nobody can sign in with a room " +
        "number. Vouchers and client accounts still work.",
      tone: "err",
    };
  }
  if (sync === "RESYNC_IN_PROGRESS" || sync === "RESYNCING" || sync === "RESYNC_REQUIRED") {
    return {
      headline: "Loading the guest list",
      summary:
        "The PMS is connected and the guest list is being refreshed. Room sign-in resumes automatically when " +
        "it finishes.",
      tone: "warn",
    };
  }
  return {
    headline: "Room sign-in unavailable",
    summary:
      "The PMS connection is up but not ready to verify clients yet. Open PMS connection to see which of the " +
      "checks is failing.",
    tone: "warn",
  };
}

export const badgeTone = (t: Tone): "ok" | "warn" | "err" | "default" => t;
