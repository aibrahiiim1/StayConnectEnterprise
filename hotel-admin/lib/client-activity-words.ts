// How a client-activity event reads; codes come from edged's /sessions/activity.

import { auditWords } from "@/lib/audit-words";
import { resultLabel } from "@/lib/api/dashboard";
import { endReasonWords, methodLabel } from "@/lib/session-words";

export type ActivityEvent = {
  id: string;
  at: string;
  category: string;
  kind: string;
  severity: "ok" | "info" | "warning" | "error";
  detail?: string;
  client_kind?: string;
  client_label?: string;
  package?: string;
  mac?: string;
  ip?: string;
  network?: string;
  session_id?: string;
  entitlement_id?: string;
  stay_id?: string;
  actor?: string;
};

const TITLES: Record<string, string> = {
  CONNECTED: "Connected",
  DISCONNECTED: "Disconnected",
  PACKAGE_STARTED: "Package started",
  PACKAGE_ENDED: "Package ended",
  DEVICE_REPLACE: "Device replaced another",
  DEVICE_RELEASE: "Device place released",
  ACCOUNT_CREATED: "Account created",
  ACCOUNT_VERIFIED: "Email address verified",
  ROOM_SIGN_IN: "Room sign-in",
  SIGN_IN_PAUSED: "Sign-in paused on this device",
  SIGN_IN_RESUMED: "Sign-in allowed again",
  STAY_RECEIVED: "Stay received from the PMS",
  CHECKED_OUT: "Checked out",
};

const DISCONNECT: Record<string, string> = {
  ADDRESS_NO_LONGER_OWNED: "Left the Wi-Fi: its address lease ended",
  ADDRESS_CHANGED: "Came back with a new address",
  SUPERSEDED_ON_ADDRESS: "Another device took this address",
  ENTITLEMENT_ENDED: "The package ended",
  DEVICE_REPLACED: "Replaced by another of the client's devices",
  DEVICE_RELEASED: "Its place was released",
  DEVICE_LIMIT_OLDEST_DISCONNECTED: "Made room for a newer device",
  ENFORCEMENT_TORN_DOWN: "Access removed by the appliance",
};

const PACKAGE_SOURCES: Record<string, string> = {
  GUEST_SELECTION: "Chosen on the portal",
  VOUCHER_REDEMPTION: "Voucher",
  ACCOUNT_AUTO_GRANT: "Client account",
  OTP_SOCIAL_DEFAULT: "Email, phone or social sign-in",
  CHECKOUT_GRACE: "After check-out access",
  EMERGENCY_GRACE: "After check-out access (built-in terms)",
  CROSS_PMS_TRANSFER: "Moved between property systems",
  ADMIN_GRANT: "Granted by staff",
  RENEWAL: "Renewal",
};

const PACKAGE_ENDS: Record<string, string> = {
  TIME: "Time allowance used up",
  DATA: "Data allowance used up",
  ADMIN: "Ended by staff",
  SUPERSEDED: "Replaced by a newer package",
  CONVERTED: "Converted to another package",
  CHECKOUT: "Guest checked out",
  HARD_EXPIRY: "Validity period ended",
};

const humanize = (s: string) => s.charAt(0) + s.slice(1).toLowerCase().replace(/_/g, " ");

export function activityWords(e: ActivityEvent): { title: string; detail?: string } {
  const d = e.detail ?? "";
  if (e.category === "staff") {
    return { title: auditWords(e.kind).title, detail: d ? `Reason: ${d}` : undefined };
  }
  const title = TITLES[e.kind] ?? humanize(e.kind);
  switch (e.kind) {
    case "CONNECTED":
      return { title, detail: d ? methodLabel(d) : undefined };
    case "DISCONNECTED":
      return { title, detail: d ? DISCONNECT[d] ?? endReasonWords(d) : undefined };
    case "PACKAGE_STARTED":
      return { title, detail: d ? PACKAGE_SOURCES[d] ?? humanize(d) : undefined };
    case "PACKAGE_ENDED":
      return { title, detail: d ? PACKAGE_ENDS[d] ?? humanize(d) : undefined };
    case "DEVICE_REPLACE":
    case "DEVICE_RELEASE":
      return { title, detail: d && d !== "OK" ? `Refused: ${humanize(d.replace(/^REFUSED_/, ""))}` : undefined };
    case "ACCOUNT_CREATED":
      return { title, detail: d === "SELF" ? "Registered on the portal" : "Created by staff" };
    case "ROOM_SIGN_IN":
      return { title, detail: d ? resultLabel(d) : undefined };
    case "SIGN_IN_PAUSED":
      return { title, detail: d === "FAILED_CREDENTIAL_THRESHOLD" || d === "FAILED_ATTEMPTS" ? "Too many failed attempts" : humanize(d) };
    case "SIGN_IN_RESUMED":
      return { title, detail: d ? `Reason: ${d}` : undefined };
    default:
      return { title, detail: d ? humanize(d) : undefined };
  }
}

export function clientWords(e: ActivityEvent): string {
  const label = e.client_label ?? "";
  switch (e.client_kind) {
    case "room":
      return label ? `Room ${label}` : "Room stay";
    case "account":
      return label || "Client account";
    case "voucher":
      return "Voucher";
    case "guest":
      return "Client sign-in";
    case "open":
      return "Free access";
    default:
      return e.mac || "—";
  }
}
