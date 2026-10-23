import type { GuestAccount } from "@/lib/api";
import { formatDate } from "@/lib/utils";

export type VerificationStatus = { label: string; tone: "ok" | "warn" | "err" | "neutral" };

export function verificationStatus(a: GuestAccount, now = Date.now()): VerificationStatus | null {
  if (!a.self_registered) return null;
  if (a.email_verified) return { label: "Email verified", tone: "ok" };
  if (a.suspended) return { label: "Suspended: email not verified", tone: "err" };
  if (a.grace_until && new Date(a.grace_until).getTime() > now) {
    return { label: `Extra time until ${formatDate(a.grace_until)}`, tone: "warn" };
  }
  if (a.verification_deadline) {
    return new Date(a.verification_deadline).getTime() > now
      ? { label: `Verify by ${formatDate(a.verification_deadline)}`, tone: "warn" }
      // The site may only remind after the deadline; suspension comes from the server.
      : { label: "Email not verified", tone: "warn" };
  }
  return { label: "Email not verified", tone: "neutral" };
}

// Renaming removed usernames frees the address while retaining account history.
export function isRemovedAccount(a: GuestAccount): boolean {
  return !a.enabled && /\.removed-[0-9a-f]+\.invalid$/i.test(a.username);
}
