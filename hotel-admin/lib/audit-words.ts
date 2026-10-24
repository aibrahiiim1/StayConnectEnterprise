// Audit action codes as sentences, categories and severities. The code itself is never replaced (it stays in the row's
// technical details);
// an unknown code is shown as-is rather than given an invented friendly name.

export type AuditSeverity = "notice" | "change" | "security";

export type AuditWords = {
  /** What happened, as a sentence fragment an operator can read in a list. */
  title: string;
  category: AuditCategory;
  severity: AuditSeverity;
  /** Why it matters, shown when the row is opened. Only where it is not self-evident. */
  note?: string;
};

export type AuditCategory =
  | "Sign-in & access"
  | "Client data"
  | "Client Portal"
  | "Internet offering"
  | "Hotel"
  | "Networks"
  | "Licence & certificates"
  | "Backups"
  | "Diagnostics";

export const AUDIT_CATEGORIES: AuditCategory[] = [
  "Sign-in & access", "Client data", "Client Portal", "Internet offering", "Hotel",
  "Networks", "Licence & certificates", "Backups", "Diagnostics",
];

/** Keep Hotel history filter available after its module license expires. */
export const MODULE_CATEGORIES: Partial<Record<AuditCategory, string>> = { Hotel: "hospitality" };

/** The categories offered as filters, given which modules have history here. */
export function auditCategoriesFor(hasHistory: (module: string) => boolean): AuditCategory[] {
  return AUDIT_CATEGORIES.filter((c) => {
    const m = MODULE_CATEGORIES[c];
    return !m || hasHistory(m);
  });
}

const WORDS: Record<string, AuditWords> = {
  "operator.login": { title: "Signed in to Admin Console", category: "Sign-in & access", severity: "notice" },
  "operator.logout": { title: "Signed out of Admin Console", category: "Sign-in & access", severity: "notice" },
  "operator.login_failed": {
    title: "A sign-in to Admin Console failed", category: "Sign-in & access", severity: "security",
    note: "The reason and the address it came from are recorded; the password typed never is.",
  },
  "operator.login_locked": {
    title: "Admin Console sign-in locked after repeated failures", category: "Sign-in & access", severity: "security",
    note: "An account or an address reached the limit set under Operators → Sign-in protection.",
  },
  "operator.reauth_failed": {
    title: "A password confirmation failed", category: "Sign-in & access", severity: "security",
    note: "Someone signed in typed the wrong password when an action asked them to confirm it.",
  },
  "operator.reauth_locked": {
    title: "Password confirmations locked after repeated failures", category: "Sign-in & access", severity: "security",
  },
  "operator.password_changed": { title: "Changed their own password", category: "Sign-in & access", severity: "security" },
  "operator.password_reset": {
    title: "Reset an operator's password", category: "Sign-in & access", severity: "security",
    note: "The operator was signed out everywhere.",
  },
  "operator.password_change_required": {
    title: "Required a new password at sign-in", category: "Sign-in & access", severity: "security",
    note: "The password used was shorter than the policy, so it must be replaced before the console can be used.",
  },
  "operator.account_retention_changed": {
    title: "Changed how long disabled staff accounts are kept", category: "Sign-in & access", severity: "security",
    note: "After this period a disabled account's email, name, password and authenticator are removed.",
  },
  "operator.signin_protection_changed": { title: "Changed Admin Console sign-in protection", category: "Sign-in & access", severity: "security" },
  "session.disconnected": {
    title: "Disconnected a client device", category: "Sign-in & access", severity: "change",
    note: "A member of staff ended a client's internet session from Active sessions.",
  },
  "guest_signin_attempt.credentials_viewed": {
    title: "Viewed what a client typed at sign-in", category: "Sign-in & access", severity: "security",
    note: "The details a client entered are only revealed on request, and every reveal is recorded here.",
  },
  "guest_data.viewed": {
    title: "Opened a client's record", category: "Client data", severity: "notice",
    note: "Opening one client's account, device, session, stay, sign-in attempt or usage is recorded, so the site can show who looked at whose data. Opening the same record again within 10 minutes is not recorded twice.",
  },
  "guest_data.searched": {
    title: "Searched client data", category: "Client data", severity: "notice",
    note: "Every search of a client list is recorded with what was searched for. A search typed letter by letter is one entry.",
  },
  "guest_data.exported": {
    title: "Exported a client's data", category: "Client data", severity: "security",
    note: "Everything held about one client, downloaded to hand over on their request.",
  },
  "guest_data.erased": { title: "Erased a client's data", category: "Client data", severity: "security" },
  "guest_data.erase_refused": {
    title: "An erasure was refused", category: "Client data", severity: "notice",
    note: "The client was still in-house, signed in or online, so nothing was erased.",
  },
  "guest_signin_restriction.release": {
    title: "Released a device from sign-in protection", category: "Sign-in & access", severity: "change",
    note: "A device that had been temporarily blocked after repeated failures was allowed to try again.",
  },
  "auth_methods.updated": { title: "Changed how clients sign in", category: "Sign-in & access", severity: "change" },

  "branding.published": { title: "Saved the Client Portal settings", category: "Client Portal", severity: "change" },
  "branding.draft_saved": {
    title: "Edited the Client Portal settings", category: "Client Portal", severity: "notice",
    note: "Work in progress, kept so a closed tab does not lose it. Clients see nothing until it is saved.",
  },
  "branding.rolled_back": { title: "Restored an earlier Client Portal design", category: "Client Portal", severity: "change" },
  "portal_asset.uploaded": { title: "Uploaded a portal image", category: "Client Portal", severity: "change" },
  "portal_asset.deleted": { title: "Deleted a portal image", category: "Client Portal", severity: "change" },

  "commercial_package.published": { title: "Published an internet package", category: "Internet offering", severity: "change" },
  "commercial_package.activated": { title: "Made an internet package available", category: "Internet offering", severity: "change" },
  "commercial_package.deactivated": { title: "Withdrew an internet package", category: "Internet offering", severity: "change" },
  "service_plan.published": { title: "Published a service plan", category: "Internet offering", severity: "change" },
  "checkout_grace.published": {
    title: "Changed the Grace Period policy", category: "Hotel", severity: "change",
    note: "How long a departing guest keeps internet after checking out.",
  },

  "pms_provider.created": { title: "Added a PMS provider", category: "Hotel", severity: "change" },
  "pms_interface.created": { title: "Added a PMS connection", category: "Hotel", severity: "change" },
  "pms_interface.lifecycle": { title: "Changed a PMS connection's state", category: "Hotel", severity: "change" },
  "pms_interface_revision.authored": {
    title: "Drafted PMS connection settings", category: "Hotel", severity: "notice",
    note: "Drafted only. Nothing reached the PMS until the revision was published.",
  },
  "pms_interface.revision_published": {
    title: "Published PMS connection settings", category: "Hotel", severity: "change",
  },
  "pms_routing.set": {
    title: "Pointed a client network at a PMS", category: "Hotel", severity: "change",
    note: "Which PMS guest list room sign-ins on that network are checked against.",
  },

  "network.guest.created": { title: "Created a client network", category: "Networks", severity: "change" },
  "network.guest.updated": { title: "Changed a client network", category: "Networks", severity: "change" },
  "network.guest.disabled": { title: "Disabled a client network", category: "Networks", severity: "change" },
  "network.guest.deleted": { title: "Deleted a client network", category: "Networks", severity: "change" },
  "network.apply": {
    title: "Applied network changes", category: "Networks", severity: "change",
    note: "Applied to the running appliance, pending confirmation.",
  },
  "network.revision.confirmed": { title: "Confirmed network changes", category: "Networks", severity: "change" },
  "network.revision.rolledback": {
    title: "Rolled back network changes", category: "Networks", severity: "change",
    note: "The appliance returned to the previous working configuration.",
  },

  "license.permissive_attempt_blocked": {
    title: "Blocked an attempt to run without a licence", category: "Licence & certificates", severity: "security",
    note: "This appliance refused to run in unlicensed mode and kept enforcement on. Recorded every time it starts while the misconfiguration is present.",
  },
  "cloud.refresh_license": { title: "Refreshed the licence from OneGate", category: "Licence & certificates", severity: "notice" },
  "cloud.test_connection": { title: "Tested the OneGate connection", category: "Licence & certificates", severity: "notice" },
  "renewal_started": { title: "Certificate renewal started", category: "Licence & certificates", severity: "notice" },
  "renewal_succeeded": { title: "Certificate renewed", category: "Licence & certificates", severity: "notice" },
  "hotel_admin_cert.check": { title: "Checked the Admin Console certificate", category: "Licence & certificates", severity: "notice" },

  "backup.created": { title: "Created a backup", category: "Backups", severity: "change" },
  "backup.verified": { title: "Verified a backup", category: "Backups", severity: "notice" },
  "backup.failed": {
    title: "A backup failed", category: "Backups", severity: "security",
    note: "Recovery readiness was reduced until a later backup succeeded.",
  },
  "backup.download_authorized": {
    title: "Confirmed a password to download a backup", category: "Backups", severity: "security",
  },
  "backup.downloaded": {
    title: "Downloaded a backup", category: "Backups", severity: "security",
    note: "A backup is a complete copy of the site's data. Every download is recorded.",
  },
  "backup.settings_changed": { title: "Changed backup retention or schedule", category: "Backups", severity: "change" },
  "backup.restore_started": {
    title: "Started restoring the database", category: "Backups", severity: "security",
    note: "A restore replaces the site's data. The appliance takes a safety backup first and stops serving while it runs.",
  },
  "backup.restore_succeeded": { title: "Restored the database", category: "Backups", severity: "security" },
  "backup.restore_failed": {
    title: "A database restore failed", category: "Backups", severity: "security",
    note: "The appliance attempted to return to the state captured in its safety backup.",
  },
  "backup.restore_rolled_back": {
    title: "Rolled back a failed restore", category: "Backups", severity: "security",
    note: "The safety backup taken before the restore was put back.",
  },

  "health.recheck": { title: "Re-ran the health checks", category: "Diagnostics", severity: "notice" },
};

/** Unknown codes keep their identifier and are grouped honestly rather than guessed at. */
export function auditWords(action: string): AuditWords {
  const known = WORDS[action];
  if (known) return known;
  return { title: action, category: categoryFromPrefix(action), severity: "notice" };
}

function categoryFromPrefix(action: string): AuditCategory {
  const p = action.split(".")[0];
  switch (p) {
    case "branding": case "portal_asset": return "Client Portal";
    case "commercial_package": case "service_plan": return "Internet offering";
    case "checkout_grace": case "pms_interface": case "pms_provider": case "pms_routing": case "pms_interface_revision":
      return "Hotel";
    case "network": return "Networks";
    case "license": case "cloud": case "renewal_started": case "renewal_succeeded": case "hotel_admin_cert":
      return "Licence & certificates";
    case "backup": return "Backups";
    case "guest_data": return "Client data";
    case "health": return "Diagnostics";
    default: return "Sign-in & access";
  }
}

/**
 * Who did it, in words. `names` maps operator id to name; an id not in it is a removed operator, said plainly. The
 * exact id stays in the expandable record.
 */
export function auditActor(
  actorType?: string | null,
  actorID?: string | null,
  names?: Map<string, string>,
): string {
  if (actorType === "system" || !actorType) return "The appliance";
  if (!actorID) return "An operator";
  const known = names?.get(actorID);
  if (known) return known;
  if (/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(actorID)) {
    return "A removed operator";
  }
  return actorID;
}

export const SEVERITY_TONE: Record<AuditSeverity, "default" | "neutral" | "warn"> = {
  notice: "default",
  change: "neutral",
  security: "warn",
};
