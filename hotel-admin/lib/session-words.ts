// HOW A SESSION IS DESCRIBED TO A PERSON.
//
// The API sends a subject kind and whatever labels it is permitted to send. Turning that into "Room 412 ·
// Anderson" or "Voucher" is presentation, and it belongs in one place because the sessions list, the session
// detail panel and the dashboard all have to say the same thing about the same session.

import type { Session, SubjectKind } from "./api";

export type SessionIdentity = {
  /** The primary line: what the operator reads first. Never empty. */
  title: string;
  /** The secondary line, when there is one worth showing. */
  subtitle?: string;
  kind: SubjectKind;
  kindLabel: string;
  /** True when nothing better than the hardware address is known. */
  anonymous: boolean;
};

const KIND_LABELS: Record<string, string> = {
  room: "Room",
  account: "Account",
  voucher: "Voucher",
  guest: "Guest sign-in",
};

/**
 * identifySession — the single place that decides what a session is called.
 *
 * The ordering is deliberate. A room-authenticated session leads with the room number, because that is how the
 * front desk refers to a guest and it is the thing a complaint arrives attached to ("the internet in 318 is
 * slow"). An account leads with its username. A voucher cannot be named at all — the admin service holds no
 * privilege on the voucher table, so its code is genuinely unavailable rather than merely unfetched — and a
 * social/email sign-in is likewise unnamed here.
 *
 * Falling back to the MAC address is correct when nothing else is known, but it is marked `anonymous` so the UI
 * can style it as the absence of an answer rather than as an answer.
 */
export function identifySession(s: Session): SessionIdentity {
  const kind = (s.subject_kind ?? "") as SubjectKind;
  const kindLabel = KIND_LABELS[kind] ?? "Device";

  if (kind === "room") {
    const room = s.room ?? s.subject_label;
    const name = s.subject_name ?? undefined;
    return {
      title: room ? `Room ${room}` : "Room stay",
      subtitle: name ?? (s.external_reservation_id ? `Reservation ${s.external_reservation_id}` : undefined),
      kind, kindLabel, anonymous: false,
    };
  }
  if (kind === "account") {
    return {
      title: s.subject_label ?? "Guest account",
      subtitle: s.subject_name ?? undefined,
      kind, kindLabel, anonymous: false,
    };
  }
  if (kind === "voucher") {
    return {
      title: "Voucher",
      // Stated rather than left blank: an operator who sees no code needs to know that is by design, not a bug
      // they should report.
      subtitle: "Code not shown — the admin service cannot read voucher codes",
      kind, kindLabel, anonymous: false,
    };
  }
  if (kind === "guest") {
    return {
      title: "Guest sign-in",
      subtitle: s.credential_method ? methodLabel(s.credential_method) : "Email, phone or social login",
      kind, kindLabel, anonymous: false,
    };
  }
  return { title: s.mac || s.ip || "Unknown device", kind: "", kindLabel: "Device", anonymous: true };
}

/** The credential a guest used, in product words rather than the enum's. */
export function methodLabel(method?: string | null): string {
  if (!method) return "—";
  const map: Record<string, string> = {
    ROOM: "Room number and name",
    ROOM_NAME: "Room number and name",
    PMS_ROOM: "Room number and name",
    VOUCHER: "Voucher code",
    ACCOUNT: "Username and password",
    USERNAME_PASSWORD: "Username and password",
    EMAIL_OTP: "Emailed code",
    SMS_OTP: "Texted code",
    SOCIAL: "Social login",
    POST_STAY: "Post-stay access",
    GRACE: "Checkout grace",
    ADMIN: "Granted by an operator",
  };
  return map[method] ?? method.replace(/_/g, " ").toLowerCase();
}

/** Session state, in words, with the tone it should carry. */
export function stateWords(state: string): { label: string; tone: "ok" | "warn" | "err" | "default" | "info" } {
  switch (state) {
    case "active":
      return { label: "Online", tone: "ok" };
    case "PENDING_ENFORCEMENT":
      // A real and briefly-visible state: the session exists in the database but the kernel has not been told
      // yet. "Pending enforcement" means nothing to a duty manager; "connecting" is what they are watching.
      return { label: "Connecting", tone: "warn" };
    case "ended":
    case "closed":
      return { label: "Ended", tone: "default" };
    default:
      return { label: state.replace(/_/g, " ").toLowerCase(), tone: "default" };
  }
}

/** Why a session ended, in words. */
export function endReasonWords(reason?: string | null): string | undefined {
  if (!reason) return undefined;
  const map: Record<string, string> = {
    admin: "Disconnected by an operator",
    ADMIN: "Disconnected by an operator",
    TIME: "Time allowance used up",
    DATA: "Data allowance used up",
    HARD_EXPIRY: "Validity period ended",
    CHECKOUT: "Guest checked out",
    IDLE: "Idle too long",
    REVOKED: "Access revoked",
    SUPERSEDED: "Replaced by a newer session",
    DEVICE_LIMIT: "Made room for another device",
    LOGOUT: "Guest signed out",
  };
  return map[reason] ?? reason.replace(/_/g, " ").toLowerCase();
}

/** A speed pair as an operator reads it. */
export function speedPair(down?: number | null, up?: number | null): string | undefined {
  const mb = (k?: number | null) =>
    typeof k === "number" && k > 0 ? (k >= 1000 ? `${(k / 1000).toFixed(k % 1000 === 0 ? 0 : 1)} Mbps` : `${k} kbps`) : null;
  const d = mb(down);
  const u = mb(up);
  if (!d && !u) return undefined;
  return `${d ?? "—"} down · ${u ?? "—"} up`;
}
