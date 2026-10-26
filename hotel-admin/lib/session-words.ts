// How a session is described to a person; one place so the list, detail panel and dashboard agree.

import type { Session, SubjectKind } from "./api";

export type SessionIdentity = {
  title: string;
  subtitle?: string;
  kind: SubjectKind;
  kindLabel: string;
  anonymous: boolean;
};

const KIND_LABELS: Record<string, string> = {
  room: "Room",
  account: "Account",
  voucher: "Voucher",
  guest: "Client sign-in",
};

/**
 * The single place that decides what a session is called: room number first (how the front desk refers to a guest),
 * then username.
 * A voucher is unnamed (the admin service cannot read its code). With nothing else known it falls back to the MAC
 * address, marked `anonymous`.
 */
export function identifySession(s: Session, multiPms = false): SessionIdentity {
  const kind = (s.subject_kind ?? "") as SubjectKind;
  const kindLabel = KIND_LABELS[kind] ?? "Device";

  if (kind === "room") {
    const room = s.room ?? s.subject_label;
    const name = s.subject_name ?? undefined;
    return {
      title: room ? `Room ${room}` : "Room stay",
      subtitle: [multiPms ? s.pms_interface_label : undefined, name ?? (s.external_reservation_id ? `Reservation ${s.external_reservation_id}` : undefined)]
        .filter(Boolean).join(" · ") || undefined,
      kind, kindLabel, anonymous: false,
    };
  }
  if (kind === "account") {
    return {
      title: s.subject_label ?? "Client account",
      subtitle: s.subject_name ?? undefined,
      kind, kindLabel, anonymous: false,
    };
  }
  if (kind === "voucher") {
    return {
      title: "Voucher",
      // Stated rather than blank: no code appears HERE by design (a session list left open must not reveal one); the
      // audited reveal is elsewhere.
      subtitle: "Code not shown here — use Show code on the Vouchers screen, which records who looked",
      kind, kindLabel, anonymous: false,
    };
  }
  if (kind === "guest") {
    return {
      title: "Client sign-in",
      subtitle: s.credential_method ? methodLabel(s.credential_method) : "Email, phone or social login",
      kind, kindLabel, anonymous: false,
    };
  }
  return { title: s.mac || s.ip || "Unknown device", kind: "", kindLabel: "Device", anonymous: true };
}

export function methodLabel(method?: string | null): string {
  if (!method) return "—";
  const map: Record<string, string> = {
    ROOM: "Room number and name",
    ROOM_NAME: "Room number and name",
    PMS_ROOM: "Room number and name",
    PMS: "Room sign-in",
    VOUCHER: "Voucher code",
    ACCOUNT: "Username and password",
    USERNAME_PASSWORD: "Username and password",
    // Credential labels scd records on the session (iamv2/credential.go); the email one-time code is replaced by Email
    // accounts but old sessions stay readable.
    EMAIL_ACCOUNT: "Email and password",
    EMAIL_OTP: "Email code (former)",
    SMS_OTP: "Texted code",
    WHATSAPP_OTP: "WhatsApp code",
    EMAIL_OTP_ASSISTED: "Email code, shown by staff (former)",
    SMS_OTP_ASSISTED: "Texted code, shown by staff",
    WHATSAPP_OTP_ASSISTED: "WhatsApp code, shown by staff",
    SOCIAL: "Social login",
    SOCIAL_GOOGLE: "Google",
    SOCIAL_APPLE: "Apple",
    SOCIAL_MICROSOFT: "Microsoft",
    SOCIAL_FACEBOOK: "Facebook",
    PRINCIPAL: "Email, phone or social login",
    OPEN: "No sign-in (chose a package)",
    POST_STAY: "Post-stay access",
    GRACE: "Grace Period",
    ADMIN: "Granted by an operator",
  };
  if (map[method]) return map[method];
  if (method.startsWith("SOCIAL_")) return method.slice(7).charAt(0) + method.slice(8).toLowerCase();
  return method.replace(/_/g, " ").toLowerCase();
}

export function acquisitionOrdinal(n?: number | null): string | undefined {
  if (typeof n !== "number" || n < 2) return undefined;
  const mod100 = n % 100;
  const suffix = mod100 >= 11 && mod100 <= 13 ? "th" : n % 10 === 1 ? "st" : n % 10 === 2 ? "nd" : n % 10 === 3 ? "rd" : "th";
  return `${n}${suffix} time`;
}

export function stateWords(state: string): { label: string; tone: "ok" | "warn" | "err" | "default" | "info" } {
  switch (state) {
    case "active":
      return { label: "Online", tone: "ok" };
    case "PENDING_ENFORCEMENT":
      // The session exists in the database but the kernel has not been told yet; "connecting" is what a duty manager
      // watches.
      return { label: "Connecting", tone: "warn" };
    case "ended":
    case "closed":
      return { label: "Ended", tone: "default" };
    default:
      return { label: state.replace(/_/g, " ").toLowerCase(), tone: "default" };
  }
}

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
    LOGOUT: "Client signed out",
  };
  return map[reason] ?? reason.replace(/_/g, " ").toLowerCase();
}

export function speedPair(down?: number | null, up?: number | null): string | undefined {
  const mb = (k?: number | null) =>
    typeof k === "number" && k > 0 ? (k >= 1000 ? `${(k / 1000).toFixed(k % 1000 === 0 ? 0 : 1)} Mbps` : `${k} kbps`) : null;
  const d = mb(down);
  const u = mb(up);
  if (!d && !u) return undefined;
  return `${d ?? "—"} down · ${u ?? "—"} up`;
}
