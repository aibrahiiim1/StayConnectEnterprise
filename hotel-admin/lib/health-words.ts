// PLAIN LANGUAGE FOR THE FIGURES THE ADMIN REPORTS.
//
// Everything below turns one of the appliance's internal states into a sentence an operator can act on, plus a
// tone so the UI can colour it consistently. The rules are kept here rather than in a component because the same
// figures appear on the dashboard, in the top-bar health pill, on Diagnostics and on Appliance & licence, and
// independent descriptions of one number is how a product ends up contradicting itself.

export type Tone = "ok" | "warn" | "err" | "default";

export type Explained = {
  /** One line, safe to show as the value itself. */
  headline: string;
  /** A sentence or two of what it means and what to do. */
  summary: string;
  tone: Tone;
};

const n = (v?: number | null) => (typeof v === "number" && Number.isFinite(v) ? v : 0);
const num = (v: number) => v.toLocaleString();

/** The site database. Shown as its own row because every screen in the admin reads from it. */
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

/**
 * The session controller (scd). Named for what it does rather than by its process name: it is what actually
 * lets a device onto the internet and what enforces speed and quota.
 */
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
 * Licence state, in terms of what it stops rather than what it is called.
 *
 * The state is the appliance's own section 8 vocabulary (docs/CENTRAL_CONTROL_PLANE.md): none, active,
 * expiring, grace, expired, suspended, revoked, wrong_hardware -- computed once by the appliance, so the
 * dashboard and Appliance & licence cannot disagree. (This used to key on "Unlicensed" while the appliance sent
 * "unlicensed", and it told operators an unlicensed appliance "runs in a permissive mode". It does not: a
 * production appliance with no licence signs in no guests at all.)
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
      // Grace is entered because the licence's own end date passed. Losing the connection to Central does not
      // change the licence state at all.
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

/**
 * PMS transport/sync, as ONE sentence about whether room sign-in works right now.
 *
 * The PMS screens already break this into four axes, which is right there. Everywhere else — the dashboard, the
 * health pill — what is needed is the consequence: can a guest type their room number and get online.
 */
export function describePmsReadiness(args: {
  transport?: string;
  sync?: string;
  roomAuthReady?: boolean;
  inHouse?: number;
}): Explained {
  const { transport, sync, roomAuthReady, inHouse } = args;
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

/** Maps a tone onto the Badge component's tone vocabulary. */
export const badgeTone = (t: Tone): "ok" | "warn" | "err" | "default" => t;
