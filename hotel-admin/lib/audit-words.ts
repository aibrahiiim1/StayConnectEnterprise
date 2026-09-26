// WHAT HAPPENED, IN WORDS A HOTEL USES.
//
// The audit trail records action codes: `branding.draft_saved`, `license.permissive_attempt_blocked`,
// `network.revision.rolledback`. Those are exactly right for support and forensics and exactly wrong as the
// only thing on screen — an operator scanning for "who changed the wifi settings on Tuesday" cannot scan
// forty dotted identifiers.
//
// So every code gets a sentence, a category and a severity. THE CODE IS NEVER REPLACED: it stays in the row's
// technical details along with the actor id, target id, IP and payload, because the day this screen matters
// most is the day somebody needs the exact record.
//
// EVERY ENTRY BELOW IS A CODE THIS APPLIANCE HAS ACTUALLY WRITTEN. The list was built from the live trail
// rather than from the source, so it describes what happens rather than what was once intended to. A code
// with no entry here is shown as-is — an unknown action is information, and inventing a friendly name for one
// would hide that the product has learned something new.

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
  | "Guest portal"
  | "Internet offering"
  | "Property management system"
  | "Networks"
  | "Licence & cloud"
  | "Backups"
  | "Diagnostics";

export const AUDIT_CATEGORIES: AuditCategory[] = [
  "Sign-in & access", "Guest portal", "Internet offering", "Property management system",
  "Networks", "Licence & cloud", "Backups", "Diagnostics",
];

const WORDS: Record<string, AuditWords> = {
  // ---- sign-in & access ---------------------------------------------------------------------------------
  "operator.login": { title: "Signed in to Hotel Admin", category: "Sign-in & access", severity: "notice" },
  "session.disconnected": {
    title: "Disconnected a guest device", category: "Sign-in & access", severity: "change",
    note: "A member of staff ended a guest's internet session from Active sessions.",
  },
  "guest_signin_attempt.credentials_viewed": {
    title: "Viewed what a guest typed at sign-in", category: "Sign-in & access", severity: "security",
    note: "The details a guest entered are only revealed on request, and every reveal is recorded here.",
  },
  "guest_signin_restriction.release": {
    title: "Released a device from sign-in protection", category: "Sign-in & access", severity: "change",
    note: "A device that had been temporarily blocked after repeated failures was allowed to try again.",
  },
  "auth_methods.updated": { title: "Changed how guests sign in", category: "Sign-in & access", severity: "change" },

  // ---- guest portal -------------------------------------------------------------------------------------
  "branding.published": { title: "Saved the guest portal settings", category: "Guest portal", severity: "change" },
  "branding.draft_saved": {
    title: "Edited the guest portal settings", category: "Guest portal", severity: "notice",
    note: "Work in progress, kept so a closed tab does not lose it. Guests see nothing until it is saved.",
  },
  "branding.rolled_back": { title: "Restored an earlier guest portal design", category: "Guest portal", severity: "change" },
  "portal_asset.uploaded": { title: "Uploaded a portal image", category: "Guest portal", severity: "change" },
  "portal_asset.deleted": { title: "Deleted a portal image", category: "Guest portal", severity: "change" },

  // ---- internet offering --------------------------------------------------------------------------------
  "commercial_package.published": { title: "Published an internet package", category: "Internet offering", severity: "change" },
  "commercial_package.activated": { title: "Made an internet package available", category: "Internet offering", severity: "change" },
  "commercial_package.deactivated": { title: "Withdrew an internet package", category: "Internet offering", severity: "change" },
  "service_plan.published": { title: "Published a service plan", category: "Internet offering", severity: "change" },
  "checkout_grace.published": {
    title: "Changed the checkout grace policy", category: "Internet offering", severity: "change",
    note: "How long a departing guest keeps internet after checking out.",
  },

  // ---- PMS ---------------------------------------------------------------------------------------------
  "pms_provider.created": { title: "Added a PMS provider", category: "Property management system", severity: "change" },
  "pms_interface.created": { title: "Added a PMS connection", category: "Property management system", severity: "change" },
  "pms_interface.lifecycle": { title: "Changed a PMS connection's state", category: "Property management system", severity: "change" },
  "pms_interface_revision.authored": {
    title: "Drafted PMS connection settings", category: "Property management system", severity: "notice",
    note: "Drafted only. Nothing reached the PMS until the revision was published.",
  },
  "pms_interface.revision_published": {
    title: "Published PMS connection settings", category: "Property management system", severity: "change",
  },
  "pms_routing.set": {
    title: "Pointed a guest network at a PMS", category: "Property management system", severity: "change",
    note: "Which property's guest list room sign-ins on that network are checked against.",
  },

  // ---- networks ----------------------------------------------------------------------------------------
  "network.guest.created": { title: "Created a guest network", category: "Networks", severity: "change" },
  "network.guest.updated": { title: "Changed a guest network", category: "Networks", severity: "change" },
  "network.guest.disabled": { title: "Disabled a guest network", category: "Networks", severity: "change" },
  "network.guest.deleted": { title: "Deleted a guest network", category: "Networks", severity: "change" },
  "network.apply": {
    title: "Applied network changes", category: "Networks", severity: "change",
    note: "Applied to the running appliance, pending confirmation.",
  },
  "network.revision.confirmed": { title: "Confirmed network changes", category: "Networks", severity: "change" },
  "network.revision.rolledback": {
    title: "Rolled back network changes", category: "Networks", severity: "change",
    note: "The appliance returned to the previous working configuration.",
  },

  // ---- licence & cloud ----------------------------------------------------------------------------------
  "license.permissive_attempt_blocked": {
    title: "Blocked an attempt to run without a licence", category: "Licence & cloud", severity: "security",
    note: "This appliance refused to run in unlicensed mode and kept enforcement on. Recorded every time it starts while the misconfiguration is present.",
  },
  "cloud.refresh_license": { title: "Refreshed the licence from OneGate", category: "Licence & cloud", severity: "notice" },
  "cloud.test_connection": { title: "Tested the OneGate connection", category: "Licence & cloud", severity: "notice" },
  "renewal_started": { title: "Certificate renewal started", category: "Licence & cloud", severity: "notice" },
  "renewal_succeeded": { title: "Certificate renewed", category: "Licence & cloud", severity: "notice" },
  "hotel_admin_cert.check": { title: "Checked the Hotel Admin certificate", category: "Licence & cloud", severity: "notice" },

  // ---- backups -----------------------------------------------------------------------------------------
  "backup.created": { title: "Created a backup", category: "Backups", severity: "change" },
  "backup.verified": { title: "Verified a backup", category: "Backups", severity: "notice" },
  "backup.failed": {
    title: "A backup failed", category: "Backups", severity: "security",
    note: "Recovery readiness was reduced until a later backup succeeded.",
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

  // ---- diagnostics -------------------------------------------------------------------------------------
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
    case "branding": case "portal_asset": return "Guest portal";
    case "commercial_package": case "service_plan": case "checkout_grace": return "Internet offering";
    case "pms_interface": case "pms_provider": case "pms_routing": case "pms_interface_revision":
      return "Property management system";
    case "network": return "Networks";
    case "license": case "cloud": case "renewal_started": case "renewal_succeeded": case "hotel_admin_cert":
      return "Licence & cloud";
    case "backup": return "Backups";
    case "health": return "Diagnostics";
    default: return "Sign-in & access";
  }
}

/** Who did it, as something readable. A system action says so rather than showing an empty column. */
/** Who did it, in words.
 *
 *  Found on the live appliance: half the rows said "An operator" and half showed a raw uuid, depending only
 *  on whether the action happened to record an actor id. The uuid is the more precise fact and the less
 *  useful one -- an operator scanning the trail for "who changed the retention policy" cannot read it, and
 *  the screen has the answer, because the operator directory is one request away.
 *
 *  `names` maps operator id to a human name. An id that is not in it is an operator who has since been
 *  removed, which is worth saying plainly rather than printing their uuid as if it were a name. The exact id
 *  is always in the expandable record; this is the summary line. */
export function auditActor(
  actorType?: string | null,
  actorID?: string | null,
  names?: Map<string, string>,
): string {
  if (actorType === "system" || !actorType) return "The appliance";
  if (!actorID) return "An operator";
  const known = names?.get(actorID);
  if (known) return known;
  // Anything that is not a uuid is already readable -- a username, a service name, a device.
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
