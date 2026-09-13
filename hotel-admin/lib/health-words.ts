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
  /** What this appliance may say to Central. LICENSING_ONLY is a decision, not a fault. */
  mode?: "LICENSING_ONLY" | "FULL";
  reason?: string;
  pending?: number;
  dead?: number;
  oldest_pending?: string | null;
  /** Delivered, and the whole-table accounting that makes "recovered" checkable rather than asserted. */
  delivered?: number;
  total?: number;
  bytes?: number;
  oldest_exhausted?: string | null;
  balanced?: boolean;
  retention_days?: number;
  /**
   * What the LAST delivery attempt learned — evidence, not a size threshold. The four failure states are
   * genuinely different problems: no connection at all, a connection with nothing listening at the far end,
   * a far end that answered and refused, or a queue that is simply large and draining.
   */
  delivery?: {
    state?:
      | "UNKNOWN"
      | "TRANSPORT_UNAVAILABLE"
      | "RECEIVER_UNAVAILABLE"
      | "RECEIVER_REJECTED"
      | "DRAINING"
      | "IDLE";
    at?: string;
    sent?: number;
    detail?: string;
  };
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
  // LICENSING ONLY IS A DECISION, AND MUST NOT READ AS A FAULT.
  //
  // This is the state the property is actually in: the hotel decided its operations stay on its own
  // appliance, and Central serves it for licensing alone. An operator seeing "not draining" or a nudge to
  // check the cloud connection would be reading a correct configuration as a problem, and the obvious
  // "repair" is the one thing that must not happen — turning the reporting back on.
  //
  // So there is no tone above neutral here, no call to action, and no mention of a backlog. What there IS:
  // a plain statement of what still works, because the question behind every cloud message on this screen
  // is whether guests are affected.
  if (o && o.mode === "LICENSING_ONLY") {
    return {
      headline: "Licensing only",
      summary:
        "This appliance uses the StayConnect cloud for its licence only. Operational reporting is " +
        "intentionally switched off, so nothing is being sent and nothing needs reconnecting. Guest " +
        "internet, sign-in, the PMS connection, sessions and accounting all run locally on this appliance " +
        "and are unaffected.",
      tone: "default",
    };
  }
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
  const state = o.delivery?.state ?? "UNKNOWN";

  // WHY THE QUEUE IS NOT DRAINING IS A FACT, NOT AN INFERENCE FROM ITS SIZE.
  //
  // This used to read the size and conclude: ten thousand waiting meant "the queue is not draining — check
  // Cloud connection." That sentence sent an operator to look at a network that was working perfectly. It
  // cannot distinguish a severed link from a connected appliance whose telemetry receiver is not running,
  // from a receiver that does not recognise this appliance, from a large queue draining normally at speed.
  // Those are four different problems with four different owners, and only one of them is the hotel's.
  //
  // The appliance now records what its last delivery attempt actually learned, and these sentences report
  // that. Size still decides how LOUD the message is; it no longer decides what the message says.
  const explain: Record<string, { line: string; tone: Tone; headline?: string }> = {
    TRANSPORT_UNAVAILABLE: {
      line:
        "The appliance currently has no connection to the StayConnect cloud, so nothing can be sent. " +
        "Records are kept safely and go out when the connection returns.",
      tone: "warn",
      headline: "No connection to the cloud",
    },
    RECEIVER_UNAVAILABLE: {
      line:
        "The appliance can reach the StayConnect cloud, but nothing there is listening for this " +
        "appliance's reports. This is a cloud-side problem — the hotel network is not the cause.",
      tone: "err",
      headline: "The cloud is not listening",
    },
    RECEIVER_REJECTED: {
      line:
        "The StayConnect cloud answered and refused the records. Retrying will not change that; it needs " +
        "someone to look at how this appliance is registered.",
      tone: "err",
      headline: "The cloud refused the records",
    },
    DRAINING: { line: "The queue is being sent now, oldest first.", tone: "ok", headline: "Sending" },
    IDLE: { line: "", tone: "ok" },
    UNKNOWN: { line: "The appliance has not tried to send since it last started.", tone: "default" },
  };
  const reason = explain[state] ?? explain.UNKNOWN;

  // Size sets the floor on severity. A backlog draining normally is still worth noticing at ten thousand,
  // because "normal" at that depth still means somebody should know it happened.
  const sizeTone: Tone = pending >= 10_000 ? "warn" : "ok";
  const worse = (a: Tone, b: Tone): Tone =>
    a === "err" || b === "err" ? "err" : a === "warn" || b === "warn" ? "warn" : a === "ok" || b === "ok" ? "ok" : "default";
  // Records the appliance gave up on are never ordinary: they are outside the retry machinery entirely and
  // will not move again until somebody recovers them.
  const tone: Tone = dead > 0 ? worse("warn", worse(reason.tone, sizeTone)) : worse(reason.tone, sizeTone);

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
          `${dead === 1 ? "it" : "them"}. ${dead === 1 ? "It" : "They"} will not be sent again until ` +
          `${dead === 1 ? "it is" : "they are"} recovered — Cloud connection has the button.`,
      );
    }
    if (reason.line) parts.push(reason.line);
  }
  // The reassurance goes LAST and is always present: it is the answer to the question the numbers provoke.
  parts.push(
    "Guest internet, sign-in and the PMS connection work locally and are not affected by this queue.",
  );

  const headline =
    pending === 0 && dead === 0
      ? "Up to date"
      : reason.headline
        ? reason.headline
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
