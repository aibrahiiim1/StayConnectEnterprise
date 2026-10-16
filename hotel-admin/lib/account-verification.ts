// A self-service email account's verification state (self-service contract §3), in the words the desk says to
// the client. edged computes `suspended` from the site's Email accounts settings; this only words it.

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
      // Past the deadline but not suspended: the site only reminds (or the flag could not be computed).
      : { label: "Email not verified", tone: "warn" };
  }
  return { label: "Email not verified", tone: "neutral" };
}

// A REMOVED account (edged guest_accounts_manage.go) is disabled and its username moved to
// "<name>.removed-<id>.invalid", which frees the address while its history stays. It is listed only under Removed.
export function isRemovedAccount(a: GuestAccount): boolean {
  return !a.enabled && /\.removed-[0-9a-f]+\.invalid$/i.test(a.username);
}
