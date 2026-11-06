export type AuditSeverity = "notice" | "change" | "security";

export type AuditWords = {
  title: string;
  category: AuditCategory;
  severity: AuditSeverity;
  note?: string;
};

export type AuditCategory =
  | "Sign-in & access"
  | "Staff & roles"
  | "Client accounts"
  | "Client data"
  | "Client Portal"
  | "Internet offering"
  | "Vouchers & codes"
  | "Hotel"
  | "Payments"
  | "Networks"
  | "Licence & certificates"
  | "Backups"
  | "Diagnostics"
  | "Other";

export const AUDIT_CATEGORIES: AuditCategory[] = [
  "Sign-in & access", "Staff & roles", "Client accounts", "Client data", "Client Portal", "Internet offering",
  "Vouchers & codes", "Hotel", "Payments", "Networks", "Licence & certificates", "Backups", "Diagnostics", "Other",
];

export const MODULE_CATEGORIES: Partial<Record<AuditCategory, string>> = { Hotel: "hospitality" };

export const OCCASIONAL_CATEGORIES: AuditCategory[] = ["Payments", "Other"];

export function auditCategoriesFor(hasHistory: (module: string) => boolean): AuditCategory[] {
  return AUDIT_CATEGORIES.filter((c) => {
    const m = MODULE_CATEGORIES[c];
    return !m || hasHistory(m);
  });
}

type Entry = [title: string, severity: AuditSeverity, note?: string];

function group(category: AuditCategory, entries: Record<string, Entry>): Record<string, AuditWords> {
  const out: Record<string, AuditWords> = {};
  for (const [action, [title, severity, note]] of Object.entries(entries)) {
    out[action] = note ? { title, category, severity, note } : { title, category, severity };
  }
  return out;
}

const WORDS: Record<string, AuditWords> = {
  ...group("Sign-in & access", {
    "operator.login": ["Signed in to Admin Console", "notice"],
    "operator.logout": ["Signed out of Admin Console", "notice"],
    "operator.login_failed": ["A sign-in to Admin Console failed", "security",
      "The reason and the address it came from are recorded; the password typed never is."],
    "operator.login_locked": ["Admin Console sign-in locked after repeated failures", "security",
      "An account or an address reached the limit set under Operators, Sign-in protection."],
    "operator.reauth_failed": ["A password confirmation failed", "security",
      "Someone signed in typed the wrong password when an action asked them to confirm it."],
    "operator.reauth_locked": ["Password confirmations locked after repeated failures", "security"],
    "operator.password_changed": ["Changed their own password", "security"],
    "operator.mfa_setup_started": ["Started setting up two-step sign-in", "notice"],
    "operator.mfa_enrolled": ["Turned on two-step sign-in", "change"],
    "operator.mfa_recovery_code_used": ["Signed in with a recovery code", "security",
      "A one-time recovery code was used instead of the authenticator app. That code cannot be used again."],
    "operator.mfa_recovery_codes_replaced": ["Replaced their recovery codes", "security",
      "The earlier recovery codes no longer work."],
    "session.disconnected": ["Disconnected a client device", "change",
      "A member of staff ended a client's internet session from Active sessions."],
    "client_device.disconnected": ["Disconnected a client device", "change",
      "A member of staff ended a device's internet session from Client devices."],
    "client_device.place_released": ["Released a device's place on its package", "change",
      "The package can be used on another device in its place."],
    "client_device.signin_required": ["Asked a device to sign in again", "change",
      "The device's remembered sign-in was forgotten, so it sees the Client Portal on its next connection."],
    "client_device.prepared_for_test": ["Prepared a device for a sign-in test", "change",
      "Its places were released and its remembered sign-in forgotten, so it signs in from the start."],
    "device_settings.changed": ["Changed the client device settings", "change"],
    "guest_signin_attempt.credentials_viewed": ["Viewed what a client typed at sign-in", "security",
      "The details a client entered are only revealed on request, and every reveal is recorded here."],
    "guest_signin_restriction.release": ["Released a device from sign-in protection", "change",
      "A device that had been temporarily blocked after repeated failures was allowed to try again."],
    "guest_signin_protection.update": ["Changed client sign-in protection", "security"],
    "guest_signin_network_policy.update": ["Changed when room sign-in pauses on a network", "change"],
    "guest_signin_network_pause.end": ["Ended a room sign-in pause", "change",
      "Room sign-in on that client network was allowed again before the pause ran out."],
    "guest_signin_network_pause.confirm_device": ["Let one device sign in during a pause", "change"],
    "signin.room_network_paused": ["Room sign-in was paused on a client network", "security",
      "Too many wrong room or name answers arrived on that network, so room sign-in there stopped for a while."],
    "auth_methods.updated": ["Changed how clients sign in", "change"],
    "network.signin_changed": ["Changed how clients sign in on a network", "change"],
    "signin_capacity.changed": ["Changed how many sign-ins are handled at once", "change"],
    "social_oauth_provider.created": ["Added a social sign-in provider", "change"],
    "social_oauth_provider.updated": ["Changed a social sign-in provider", "change"],
    "social_oauth_provider.deleted": ["Removed a social sign-in provider", "change"],
    "notification_provider.created": ["Added an email or text message provider", "change"],
    "notification_provider.updated": ["Changed an email or text message provider", "change"],
    "notification_provider.update_failed": ["A provider change was not saved", "notice"],
    "notification_provider.deleted": ["Removed an email or text message provider", "change"],
    "notification_provider.tested": ["Sent a test message through a provider", "notice"],
    "enforcement_policy.update": ["Changed how internet access is enforced", "security"],
    "enforcement_policy.update_failed": ["An enforcement change was not saved", "notice"],
    "post_stay.signin_refused": ["Refused a sign-in after check-out", "notice",
      "A device asked for After Check-out Access and was turned away. The reason is in the record."],
    "post_stay.signin_refusals_suppressed": ["Stopped recording repeated after check-out refusals", "notice",
      "Many refusals arrived together, so only their number was kept."],
    "post_stay.entitlement_joined": ["A device rejoined After Check-out Access", "notice",
      "A device already allowed on the stay came back while the access was still running."],
  }),

  ...group("Staff & roles", {
    "operator.created": ["Added a staff account", "security"],
    "operator.disabled": ["Disabled a staff account", "security"],
    "operator.role_added": ["Gave a staff account a role", "security"],
    "operator.role_removed": ["Took a role away from a staff account", "security"],
    "operator.password_reset": ["Reset an operator's password", "security", "The operator was signed out everywhere."],
    "operator.password_change_required": ["Required a new password at sign-in", "security",
      "The password used was shorter than the policy, so it must be replaced before the console can be used."],
    "operator.mfa_disabled": ["Turned off two-step sign-in", "security"],
    "operator.mfa_reset": ["Reset an operator's two-step sign-in", "security",
      "Their authenticator was removed, so they set up two-step sign-in again at their next sign-in."],
    "operator.session_policy_changed": ["Changed staff sign-in rules", "security"],
    "operator.account_retention_changed": ["Changed how long disabled staff accounts are kept", "security",
      "After this period a disabled account's email, name, password and authenticator are removed."],
    "operator.signin_protection_changed": ["Changed Admin Console sign-in protection", "security"],
    "operator.password_reset_policy_changed": ["Changed forgot password by email", "security"],
    "operator.password_reset_requested": ["Asked for a password reset code by email", "security"],
    "operator.password_reset_refused": ["A password reset code was asked for but not sent", "security",
      "No code was sent: the address has no account here, the account is not active, it is the last site admin, or the hourly limit was reached."],
    "operator.password_reset_failed": ["A password reset code was refused", "security"],
    "operator.password_reset_by_email": ["Reset their own password with a code sent by email", "security", "The operator was signed out everywhere."],
    "role.created": ["Created a role", "security"],
    "role.updated": ["Changed a role", "security"],
    "role.deleted": ["Deleted a role", "security"],
  }),

  ...group("Client accounts", {
    "guest_account.created": ["Created a client account", "change"],
    "guest_account.updated": ["Changed a client account", "change"],
    "guest_account.enabled": ["Enabled a client account", "change"],
    "guest_account.disabled": ["Disabled a client account", "change"],
    "guest_account.deleted": ["Deleted a client account", "change"],
    "guest_account.removed": ["Removed a client account", "change"],
    "guest_account.password_set": ["Set a client account's password", "security"],
    "guest_account.password_format_changed": ["Changed the password rules for client accounts", "change"],
    "guest_account.verification_resent": ["Resent a verification email", "notice"],
    "guest_account.verification_restarted": ["Started a new verification period", "change"],
    "guest_account.email_marked_verified": ["Marked a client's email as verified", "change"],
    "guest_account.sessions_disconnected": ["Disconnected a client account's devices", "change"],
    "client_group.created": ["Created a client group", "change"],
    "client_group.updated": ["Changed a client group", "change"],
    "client_group.deleted": ["Deleted a client group", "change"],
    "self_service_settings.changed": ["Changed the client self-service settings", "change"],
  }),

  ...group("Client data", {
    "guest_data.viewed": ["Opened a client's record", "notice",
      "Opening one client's account, device, session, stay, sign-in attempt or usage is recorded, so the site can show who looked at whose data. Opening the same record again within 10 minutes is not recorded twice."],
    "guest_data.searched": ["Searched client data", "notice",
      "Every search of a client list is recorded with what was searched for. A search typed letter by letter is one entry."],
    "guest_data.listed": ["Viewed a list of client data", "notice",
      "Opening a page of a client, device, session, stay or sign-in list is recorded with the filter that was applied, never with anything typed into a search. Opening the same list again within 10 minutes is not recorded twice."],
    "guest_data.exported": ["Exported a client's data", "security",
      "Everything held about one client, downloaded to hand over on their request."],
    "guest_data.export_refused": ["An export was refused", "notice"],
    "guest_data.export_failed": ["An export failed", "notice", "Nothing was handed over."],
    "guest_data.erased": ["Erased a client's data", "security"],
    "guest_data.erase_refused": ["An erasure was refused", "notice",
      "The client was still in-house, signed in or online, so nothing was erased."],
    "guest_data.erase_failed": ["An erasure failed", "security", "The client's data may not have been fully erased. Try again from Guest data requests."],
    "guest_data.request_recorded": ["Recorded a guest data request", "change"],
    "guest_data.request_extended": ["Extended the deadline of a guest data request", "change"],
    "guest_data.request_answered": ["Answered a guest data request", "change"],
    "guest_data.assisted_opened": ["Opened a guest data request with staff help", "change"],
    "guest_data.assisted_reviewed": ["Reviewed a guest data request", "notice"],
    "guest_data.assisted_export_started": ["Started exporting a client's data for a request", "security"],
    "guest_data.assisted_export_refused": ["An export for a request was refused", "notice"],
    "guest_data.assisted_erased": ["Erased a client's data for a request", "security"],
    "guest_data.assisted_erase_refused": ["An erasure for a request was refused", "notice"],
    "guest_data.assisted_completed": ["Completed a guest data request", "change"],
    "guest_data.assisted_cancelled": ["Cancelled a guest data request", "change"],
    "data_retention.changed": ["Changed how long client data is kept", "security"],
  }),

  ...group("Client Portal", {
    "branding.published": ["Saved the Client Portal settings", "change"],
    "branding.draft_saved": ["Edited the Client Portal settings", "notice",
      "Work in progress, kept so a closed tab does not lose it. Clients see nothing until it is saved."],
    "branding.rolled_back": ["Restored an earlier Client Portal design", "change"],
    "portal_asset.uploaded": ["Uploaded a portal image", "change"],
    "portal_asset.deleted": ["Deleted a portal image", "change"],
    "portal_profile.created": ["Created a portal design", "change"],
    "portal_profile.renamed": ["Renamed a portal design", "change"],
    "portal_profile.assigned": ["Chose the portal design for a network", "change"],
    "portal_profile.deleted": ["Deleted a portal design", "change"],
    "walled_garden.created": ["Allowed a website before sign-in", "change"],
    "walled_garden.updated": ["Changed a website allowed before sign-in", "change"],
    "walled_garden.deleted": ["Removed a website allowed before sign-in", "change"],
    "walled_garden.policy_changed": ["Changed which websites work before sign-in", "change"],
  }),

  ...group("Internet offering", {
    "commercial_package.published": ["Published an internet package", "change"],
    "commercial_package.activated": ["Made an internet package available", "change"],
    "commercial_package.deactivated": ["Withdrew an internet package", "change"],
    "commercial_grace.published": ["Changed the extra time after a package ends", "change"],
    "service_plan.published": ["Published a service plan", "change"],
    "package.ended": ["Ended a client's package", "change"],
    "package.reset": ["Reset a client's package usage", "change"],
  }),

  ...group("Vouchers & codes", {
    "voucher.issued": ["Issued vouchers", "change"],
    "voucher.revoked": ["Cancelled a voucher", "change"],
    "voucher_batch.revoked": ["Cancelled a batch of vouchers", "change"],
    "voucher.code_revealed": ["Showed a voucher code", "security",
      "Voucher codes are only shown on request, and every time is recorded here."],
    "voucher.codes_exported": ["Exported voucher codes", "security"],
    "voucher.code_format_changed": ["Changed the voucher code format", "change"],
    "voucher.code_key_rotated": ["Replaced the key that makes voucher codes", "security",
      "Codes already issued keep working."],
    "otp_code.revealed": ["Showed a client's sign-in code", "security",
      "A code sent by email or text message was read out to a client who did not receive it."],
    "otp_code.reveal_failed": ["A sign-in code could not be shown", "notice"],
  }),

  ...group("Hotel", {
    "after_checkout_access.updated": ["Changed After Check-out Access", "change",
      "How long a departing guest's devices keep internet after a verified check-out, or turning that on or off."],
    "checkout_grace.published": ["Changed After Check-out Access (earlier Grace Period policy)", "change",
      "How long a departing guest keeps internet after checking out."],
    "poststay.revoke": ["Ended a guest's After Check-out Access", "change"],
    "operational_alert.acknowledged": ["Acknowledged an alert", "change"],
    "operational_alert.resolved": ["Resolved an alert", "change"],
    "stay.cross_pms_transfer": ["Moved a stay to another PMS", "change"],
    "pms_provider.created": ["Added a PMS provider", "change"],
    "pms_interface.created": ["Added a PMS connection", "change"],
    "pms_interface.lifecycle": ["Changed a PMS connection's state", "change"],
    "pms_interface_revision.authored": ["Drafted PMS connection settings", "notice",
      "Drafted only. Nothing reached the PMS until the revision was published."],
    "pms_interface.revision_published": ["Published PMS connection settings", "change"],
    "pms_connection_settings.update": ["Changed PMS connection settings", "change"],
    "pms_interface.secret_rotated": ["Replaced a PMS connection's password", "security"],
    "pms_interface.connection_tested": ["Tested a PMS connection", "notice"],
    "pms_interface.full_resync_requested": ["Asked the PMS to resend every stay", "change"],
    "pms_interface.answer_confirmation": ["Changed how room sign-in answers are checked", "change"],
    "pms_routing.set": ["Pointed a client network at a PMS", "change",
      "Which PMS guest list room sign-ins on that network are checked against."],
    "pms_routing.policy_set": ["Pointed a client network at a PMS", "change",
      "Which PMS guest list room sign-ins on that network are checked against."],
    "pms_routing.previewed": ["Previewed which PMS a network would use", "notice"],
    "pms_routing.proposal_refused": ["A PMS choice for a network was refused", "notice"],
    "pms_routing.cleared": ["Stopped checking a network against a PMS", "change"],
    "pms_event.acknowledge": ["Acknowledged a PMS event", "change"],
    "pms_event.link": ["Linked a PMS event to a stay", "change"],
    "pms_event.expiry_setting_changed": ["Changed how long PMS events wait for review", "change"],
    "pms_roster_reconciliation.dry_run": ["Compared the guest list with the PMS", "notice"],
    "pms_roster_reconciliation.settings_changed": ["Changed the guest list comparison settings", "change"],
  }),

  ...group("Payments", {
    "payment_account.save": ["Changed a payment provider account", "security"],
    "payment_account.test": ["Tested a payment provider account", "notice"],
    "payment_account.conversion": ["Changed currency conversion for payments", "change"],
    "payment_domains.update": ["Changed the payment websites allowed before sign-in", "change"],
    "card_payment_settings.update": ["Changed card payment settings", "change"],
    "card_recheck_settings.update": ["Changed when card payments are checked again", "change"],
    "card_case.resolved": ["Closed a card refund review", "change"],
    "financial_review.decided": ["Decided a room charge under review", "change"],
    "financial_recovery.hold_resolved": ["Cleared a hold on room charges", "change"],
    "financial_recovery.released": ["Let room charges post again", "change"],
    "financial_recovery.retry_authorized": ["Allowed a room charge to be sent again", "change"],
    "posting_lane.parked_charge_released": ["Released a held room charge", "change"],
    "posting_lane.setting_changed": ["Changed how room charges are sent", "change"],
    "pms_interface.financial_onboard": ["Turned on room charges for a PMS connection", "change"],
    "pms_interface.financial_mirror_max_age": ["Changed how old the guest list may be for room charges", "change"],
    "stay.room_charge_admin_block": ["Stopped room charges for a stay", "change"],
    "accounting_retention.changed": ["Changed how long payment records are kept", "security"],
  }),

  ...group("Networks", {
    "network.guest.created": ["Created a client network", "change"],
    "network.guest.updated": ["Changed a client network", "change"],
    "network.guest.enabled": ["Switched on a client network", "change"],
    "network.guest.disabled": ["Switched off a client network", "change"],
    "network.guest.deleted": ["Deleted a client network", "change"],
    "network.unarchived": ["Brought back an archived client network", "change"],
    "network.guest.replacement_staged": ["Prepared a replacement for a client network", "change"],
    "network.guest.replacement_confirmed": ["Confirmed a client network replacement", "change"],
    "network.guest.replacement_reverted": ["Undid a client network replacement", "change"],
    "network.guest.replacement_cancelled": ["Cancelled a client network replacement", "change"],
    "network.guest.replacement_settle_withheld": ["Held back finishing a client network replacement", "notice",
      "Clients were still connected to the old network, so it was kept for now."],
    "network.dhcp.reservation.created": ["Added an address reservation", "change"],
    "network.dhcp.reservation.updated": ["Changed an address reservation", "change"],
    "network.dhcp.reservation.deleted": ["Removed an address reservation", "change"],
    "network.interface.role": ["Changed what a network port is used for", "change"],
    "network.port.mode_staged": ["Prepared a change to a network port", "change"],
    "network.factory.activation_staged": ["Prepared the first client network", "change"],
    "network.recovery.staged": ["Prepared a network recovery", "change"],
    "network.recovery.review_acknowledged": ["Acknowledged a network recovery item", "change"],
    "network.change.cancelled": ["Cancelled pending network changes", "change"],
    "network.apply": ["Applied network changes", "change", "Applied to the running appliance, pending confirmation."],
    "network.revision.confirmed": ["Confirmed network changes", "change"],
    "network.revision.confirm_failed": ["Network changes could not be confirmed", "notice"],
    "network.revision.rolledback": ["Rolled back network changes", "change",
      "The appliance returned to the previous working configuration."],
    "network.revision.rollback_failed": ["A network roll back failed", "security",
      "The appliance could not return to the previous configuration. Check Network recovery."],
    "network.system.apply": ["Applied appliance network settings", "change", "Applied pending confirmation."],
    "network.system.confirm": ["Confirmed appliance network settings", "change"],
    "network.system.rollback": ["Rolled back appliance network settings", "change"],
    "portal_https.address_changed": ["Changed the Client Portal address", "change"],
    "portal_https.check_requested": ["Checked the Client Portal address", "notice"],
    "portal_https.renew_requested": ["Asked to renew the Client Portal certificate", "change"],
  }),

  ...group("Licence & certificates", {
    "license.permissive_attempt_blocked": ["Blocked an attempt to run without a licence", "security",
      "This appliance refused to run in unlicensed mode and kept enforcement on. Recorded every time it starts while the misconfiguration is present."],
    "license.installed": ["Installed a licence", "change"],
    "module.update": ["Switched a module on or off", "change"],
    "license.clock_reset": ["Reset the licence clock", "security"],
    "license.clock_reset_refused": ["A licence clock reset was refused", "security"],
    "license.clock_reset_failed": ["A licence clock reset failed", "security"],
    "cloud.refresh_license": ["Refreshed the licence from OneGate", "notice"],
    "cloud.test_connection": ["Tested the OneGate connection", "notice"],
    "central.check_now": ["Checked in with OneGate", "notice"],
    "central.offline_request_downloaded": ["Downloaded an offline licence request", "notice"],
    "central.offline_package_applied": ["Applied an offline licence file", "change"],
    "central.offline_package_rejected": ["An offline licence file was rejected", "security"],
    "central.offline_package_failed": ["An offline licence file could not be applied", "notice"],
    "appliance.tenant_transition_purge": ["Cleared the previous hotel's data", "security",
      "The appliance was moved to another hotel, so the earlier hotel's records were removed. Only counts are kept."],
    "renewal_started": ["Certificate renewal started", "notice"],
    "renewal_succeeded": ["Certificate renewed", "notice"],
    "hotel_admin_cert.check": ["Checked the Admin Console certificate", "notice"],
    "hotel_admin_cert.rotate_requested": ["Asked for a new Admin Console certificate", "change"],
  }),

  ...group("Backups", {
    "backup.created": ["Created a backup", "change"],
    "backup.refused": ["A backup was refused", "notice"],
    "backup.verified": ["Verified a backup", "notice"],
    "backup.failed": ["A backup failed", "security", "Recovery readiness was reduced until a later backup succeeded."],
    "backup.download_authorized": ["Confirmed a password to download a backup", "security"],
    "backup.download_refused": ["A backup download was refused", "security"],
    "backup.downloaded": ["Downloaded a backup", "security",
      "A backup is a complete copy of the site's data. Every download is recorded."],
    "backup.settings_changed": ["Changed backup retention or schedule", "change"],
    "backup.restore_started": ["Started restoring the database", "security",
      "A restore replaces the site's data. The appliance takes a safety backup first and stops serving while it runs."],
    "backup.restore_refused": ["A restore was refused", "security"],
    "backup.restore_succeeded": ["Restored the database", "security"],
    "backup.restore_failed": ["A database restore failed", "security",
      "The appliance attempted to return to the state captured in its safety backup."],
    "backup.restore_rolled_back": ["Rolled back a failed restore", "security",
      "The safety backup taken before the restore was put back."],
    "backup.mirror_run": ["Copied the payment records to their second store", "notice"],
    "backup.earlier_removed": ["Removed an earlier payment records copy", "change"],
    "backup.ledger_recovered": ["Recovered the payment records", "security"],
    "backup.ledger_storage_changed": ["Changed where payment records are copied", "change"],
    "backup.ledger_storage_refused": ["A payment records storage change was refused", "notice"],
    "backup.ledger_storage_removed": ["Removed a payment records store", "security"],
    "backup.ledger_storage_remove_refused": ["Removing a payment records store was refused", "notice"],
    "backup_key.added": ["Added a backup key holder", "security"],
    "backup_key.confirmed": ["A backup key holder confirmed their key", "change"],
    "backup_key.removed": ["Removed a backup key holder", "security"],
    "backup_key.restore_tested": ["Tested restoring with a backup key", "notice"],
    "backup_key.restore_test_failed": ["A backup key restore test failed", "security"],
  }),

  ...group("Diagnostics", {
    "health.recheck": ["Re-ran the health checks", "notice"],
    "health.logs_viewed": ["Viewed a service log", "notice"],
    "health.restart_requested": ["Restarted a service", "change"],
    "scd.poke_failed": ["Session control did not take a change straight away", "notice",
      "The change was saved and is picked up on the next regular check."],
  }),
};

export function auditWords(action: string): AuditWords {
  const known = WORDS[action];
  if (known) return known;
  return { title: action, category: categoryFromPrefix(action), severity: "notice" };
}

function categoryFromPrefix(action: string): AuditCategory {
  const p = action.split(".")[0];
  switch (p) {
    case "operator": case "session": case "client_device": case "device_settings": case "guest_signin_attempt":
    case "guest_signin_restriction": case "guest_signin_protection": case "guest_signin_network_policy":
    case "guest_signin_network_pause": case "signin": case "auth_methods": case "signin_capacity":
    case "social_oauth_provider": case "notification_provider": case "enforcement_policy": case "post_stay":
      return "Sign-in & access";
    case "role": return "Staff & roles";
    case "guest_account": case "client_group": case "self_service_settings": return "Client accounts";
    case "guest_data": case "data_retention": return "Client data";
    case "branding": case "portal_asset": case "portal_profile": case "walled_garden": return "Client Portal";
    case "commercial_package": case "commercial_grace": case "service_plan": case "package": return "Internet offering";
    case "voucher": case "voucher_batch": case "otp_code": return "Vouchers & codes";
    case "checkout_grace": case "after_checkout_access": case "poststay": case "operational_alert": case "stay":
    case "pms_interface": case "pms_provider": case "pms_routing": case "pms_interface_revision": case "pms_event":
    case "pms_connection_settings": case "pms_roster_reconciliation":
      return "Hotel";
    case "payment_account": case "payment_domains": case "card_payment_settings": case "card_recheck_settings":
    case "card_case": case "financial_review": case "financial_recovery": case "posting_lane": case "accounting_retention":
      return "Payments";
    case "network": case "portal_https": return "Networks";
    case "license": case "module": case "cloud": case "central": case "appliance": case "renewal_started": case "renewal_succeeded":
    case "hotel_admin_cert":
      return "Licence & certificates";
    case "backup": case "backup_key": return "Backups";
    case "health": case "scd": return "Diagnostics";
    default: return "Other";
  }
}

export function auditActor(
  actorType?: string | null,
  actorID?: string | null,
  names?: Map<string, string> | null,
  target?: { type?: string | null; id?: string | null },
): string {
  if (actorType === "system" || !actorType) return "The appliance";
  if (actorType === "guest") return "A client device";
  const id = actorID || (target?.type === "operator" ? target.id : null);
  if (!id) return "An operator";
  const known = names?.get(id);
  if (known) return known;
  if (!names) return "An operator";
  if (/^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(id)) {
    return "A removed operator";
  }
  return id;
}

const TARGET_WORDS: Record<string, string> = {
  operator: "Staff account", custom_role: "Role", guest_account: "Client account", client_group: "Client group",
  device: "Client device", guest_device: "Client device", session: "Session", stay: "Stay", stay_events: "PMS event",
  voucher: "Voucher", voucher_batch: "Voucher batch", otp_challenge: "Sign-in code", guest_network: "Client network",
  guest_network_change: "Network change", system_network: "Appliance network", interface: "Network port", port: "Network port",
  dhcp_reservation: "Address reservation", network: "Network", network_revision: "Network change",
  pms_interface: "PMS connection", pms_interfaces: "PMS connection", pms_provider: "PMS provider",
  pms_posting: "Room charge", posting_outbox: "Room charge", recovery_hold: "Room charge hold",
  financial_recovery: "Room charges", card_reversal_review: "Card refund review", payment_provider_account: "Payment account",
  backup: "Backup", backup_key_holder: "Backup key holder", ledger: "Payment records", license: "Licence",
  appliance: "Appliance", tenant: "Hotel", site: "Site", module: "Module", portal_asset: "Portal image",
  portal_profile: "Portal design", post_stay_profile: "After Check-out Access", post_stay_signin: "After Check-out Access",
  checkout_grace_audit: "Alert", commercial_package: "Internet package", commercial_grace: "Internet package",
  service_plan: "Service plan", notification_provider: "Message provider", social_oauth_provider: "Social sign-in provider",
  walled_garden_rule: "Allowed website", walled_garden_policy: "Allowed websites", assisted_guest_request: "Guest data request",
  guest_data_request: "Guest data request", guest_data_erasure: "Erasure", sign_in_attempt: "Sign-in attempt",
  hotel_admin_cert: "Admin Console certificate", scd: "Session control", service: "Service",
};

export function auditTarget(type?: string | null): string | null {
  if (!type) return null;
  return TARGET_WORDS[type] ?? null;
}

export const SEVERITY_TONE: Record<AuditSeverity, "default" | "neutral" | "warn"> = {
  notice: "default",
  change: "neutral",
  security: "warn",
};
