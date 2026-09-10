// PLAIN LANGUAGE FOR THE FIGURES THE ADMIN REPORTS.
//
// This file exists because of a specific complaint about a specific line on the dashboard:
//
//     Cloud sync outbox    69572 pending    8424 dead
//
// Three words and two integers, and nothing on the screen said what an outbox is, what "pending" counts, what
// "dead" means, whether 8,424 is a catastrophe, or — the only question an operator actually has — whether any
// guest is affected by it. The numbers were true and useless.
//
// Everything below turns one of those internal counters into a sentence an operator can act on, plus a tone so
// the UI can colour it consistently. The rules are kept here rather than in a component because the same
// figures appear on the dashboard, in the top-bar health pill, on Diagnostics and on Cloud connection, and four
// independent descriptions of one number is how a product ends up contradicting itself.

export type Tone = "ok" | "warn" | "err" | "default";

export type OutboxFigures = {
  enabled: boolean;
  pending?: number;
  dead?: number;
  oldest_pending?: string | null;
};

export type Explained = {
  /** One line, safe to show as the value itself. */
  headline: string;
  /** A sentence or two of what it means and what to do. */
  summary: string;
  tone: Tone;
};

const n = (v?: number | null) => (typeof v === "number" && Number.isFinite(v) ? v : 0);
const num = (v: number) => v.toLocaleString();

/**
 * THE CLOUD SYNC OUTBOX.
 *
 * What it is: the appliance keeps working when the StayConnect cloud is unreachable, so everything it wants to
 * report upward — usage totals, session records, alerts — is written to a local queue first and sent later.
 * "Pending" is that queue. "Dead" is what it gave up on after retrying.
 *
 * The fact that matters and was never stated: NONE of this affects guests. Sign-in, speed and the PMS all run
 * locally. A large backlog is a reporting problem, not an outage.
 */
export function describeOutbox(o?: OutboxFigures | null): Explained {
  if (!o || !o.enabled) {
    return {
      headline: "Not in use",
      summary:
        "This appliance is not reporting to the StayConnect cloud, so nothing is queued. Guest internet, " +
        "sign-in and the PMS connection do not depend on it.",
      tone: "default",
    };
  }

  const pending = n(o.pending);
  const dead = n(o.dead);

  // The thresholds are about SHAPE, not size. A few hundred items mid-flight is an ordinary busy period; tens
  // of thousands means nothing has drained for a long time, which is a connection problem rather than a busy
  // one. "Dead" is never ordinary — it is work the appliance has abandoned.
  const backlogTone: Tone = pending >= 10_000 ? "err" : pending >= 1_000 ? "warn" : "ok";
  const tone: Tone = dead > 0 ? (backlogTone === "err" ? "err" : "warn") : backlogTone;

  const parts: string[] = [];
  if (pending === 0 && dead === 0) {
    parts.push("Everything this appliance has reported to the StayConnect cloud has been delivered.");
  } else {
    if (pending > 0) {
      parts.push(
        `${num(pending)} ${pending === 1 ? "record is" : "records are"} waiting to be sent to the StayConnect cloud.`,
      );
    }
    if (dead > 0) {
      parts.push(
        `${num(dead)} ${dead === 1 ? "record was" : "records were"} retried until the appliance gave up on ` +
          `${dead === 1 ? "it" : "them"}.`,
      );
    }
    if (backlogTone !== "ok") {
      parts.push("A backlog this size means the queue is not draining — check Cloud connection.");
    }
  }
  // The reassurance goes LAST and is always present: it is the answer to the question the numbers provoke.
  parts.push(
    "Guest internet, sign-in and the PMS connection work locally and are not affected by this queue.",
  );

  const headline =
    pending === 0 && dead === 0
      ? "Up to date"
      : dead > 0
        ? `${num(pending)} waiting · ${num(dead)} given up`
        : `${num(pending)} waiting`;

  return { headline, summary: parts.join(" "), tone };
}

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
          "The appliance cannot reach its own database. Guest sign-in and this admin both depend on it, so " +
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
          "The service that puts guest devices online and enforces speed and data limits is answering.",
        tone: "ok",
      }
    : {
        headline: "Not answering",
        summary:
          "The service that puts guest devices online is not answering. New guests cannot be connected and " +
          "limits are not being enforced until it recovers.",
        tone: "err",
      };
}

/** Licence state, in terms of what it stops rather than what it is called. */
export function describeLicense(state: string | null | undefined, installed: boolean | undefined): Explained {
  if (!installed) {
    return {
      headline: "Pending activation",
      summary:
        "No signed licence is installed. The appliance runs in a permissive mode for commissioning; it is " +
        "not activated, and it should be activated before the property opens.",
      tone: "warn",
    };
  }
  switch (state) {
    case "Active":
      return { headline: "Active", summary: "This appliance is licensed and fully enabled.", tone: "ok" };
    case "GracePeriod":
      return {
        headline: "Grace period",
        summary:
          "The licence has not been confirmed with the cloud recently, so the appliance is running on its " +
          "offline grace allowance. It keeps working; it will restrict itself if the grace runs out.",
        tone: "warn",
      };
    case "Suspended":
    case "Restricted":
      return {
        headline: state === "Suspended" ? "Suspended" : "Restricted",
        summary:
          "The licence is no longer in good standing, so some capabilities are withheld. Contact StayConnect.",
        tone: "warn",
      };
    case "Expired":
      return {
        headline: "Expired",
        summary: "The licence end date has passed. Renew it to restore full operation.",
        tone: "err",
      };
    case "Revoked":
      return {
        headline: "Revoked",
        summary: "This licence was revoked centrally. The appliance will not operate normally.",
        tone: "err",
      };
    case "Unlicensed":
      return {
        headline: "Unlicensed",
        summary: "No valid licence is in force on this appliance.",
        tone: "err",
      };
    default:
      return { headline: state ?? "Unknown", summary: "The licence state could not be interpreted.", tone: "default" };
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
        ". Guests can sign in with their room number and name.",
      tone: "ok",
    };
  }
  if (transport === "DISCONNECTED" || transport === "UNKNOWN") {
    return {
      headline: "PMS not connected",
      summary:
        "The appliance is not connected to the property management system, so nobody can sign in with a room " +
        "number. Vouchers and guest accounts still work.",
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
      "The PMS connection is up but not ready to verify guests yet. Open PMS connection to see which of the " +
      "checks is failing.",
    tone: "warn",
  };
}

/** Maps a tone onto the Badge component's tone vocabulary. */
export const badgeTone = (t: Tone): "ok" | "warn" | "err" | "default" => t;
