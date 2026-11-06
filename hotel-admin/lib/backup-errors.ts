export const NOTHING_CHANGED = "Nothing was changed.";

type Failure = { status?: number; code?: string; text: string };

function failure(e: unknown): Failure {
  const o = (e ?? {}) as { status?: unknown; code?: unknown; message?: unknown };
  return {
    status: typeof o.status === "number" ? o.status : undefined,
    code: typeof o.code === "string" ? o.code : undefined,
    text: typeof o.message === "string" ? o.message.trim() : "",
  };
}

export function isSentence(text: string | null | undefined): boolean {
  const t = (text ?? "").trim();
  return /^[A-Z]/.test(t) && /[.!?]$/.test(t);
}

function withNothingChanged(text: string): string {
  return /Nothing (was|has been) changed\.$/.test(text) ? text : `${text} ${NOTHING_CHANGED}`;
}

const CODE_WORDS: Record<string, string> = {
  reauth_required: `Your password was not accepted. ${NOTHING_CHANGED}`,
  reauth_unavailable: `Your password could not be checked just now. Try again in a moment. ${NOTHING_CHANGED}`,
  no_key_holder: "No backup was taken. Backups are always encrypted, and no backup key holder is set up yet: add one under Backup encryption on the Backups tab first.",
  backup_failed: "The backup did not complete. The earlier backups are kept and nothing was replaced.",
  scd_unreachable: `The appliance's backup service could not be reached. Try again in a moment. ${NOTHING_CHANGED}`,
  confirm_mismatch: `The confirmation text did not match. ${NOTHING_CHANGED}`,
  backup_key: `That key did not open this backup. Check that it is a current key holder's key. ${NOTHING_CHANGED}`,
  verify_failed: `The backup could not be checked just now. Try again in a moment. ${NOTHING_CHANGED}`,
  restore_failed: "The appliance did not start the restore. The current data is unchanged.",
  test_failed: `The key could not be tested just now. Try again in a moment. ${NOTHING_CHANGED}`,
  duplicate: `That key is already a key holder. ${NOTHING_CHANGED}`,
  invalid_label: `Name the key holder in up to 80 characters. ${NOTHING_CHANGED}`,
  acknowledgement_required: `Tick that you understand what happens if every key is lost. ${NOTHING_CHANGED}`,
  reason_required: `Give a reason of at least four characters. ${NOTHING_CHANGED}`,
  invalid_setting: `One of the values is outside its allowed range. ${NOTHING_CHANGED}`,
  awaiting_assignment: `This appliance is not activated for a site yet, so this cannot be done. ${NOTHING_CHANGED}`,
  not_available: `This is not available on this appliance just now. ${NOTHING_CHANGED}`,
  forbidden: `Your role cannot do this. ${NOTHING_CHANGED}`,
};

export function backupError(e: unknown, fallback: string): string {
  const f = failure(e);
  if (f.code && f.code !== "last_holder" && CODE_WORDS[f.code]) return CODE_WORDS[f.code];
  if (f.status === 429) return `Too many attempts. Wait a minute, then try again. ${NOTHING_CHANGED}`;
  if (isSentence(f.text)) return withNothingChanged(f.text);
  return fallback;
}

export const RESTORE_STEP_WORDS: Record<string, string> = {
  maintenance: "Stopped serving clients and staff",
  quiesce: "Paused the services",
  safety_backup: "Took a safety copy of the current data",
  key_holders: "Kept the backup key holders",
  ledger: "Read the erasure ledger",
  ledger_prepare: "Prepared the erasure ledger",
  ledger_storage: "Checked the erasure ledger's secondary storage",
  ledger_synced: "Brought the erasure ledger up to date",
  staging: "Prepared a separate database",
  load: "Loaded the backup into it",
  schema: "Checked the database structure",
  hypertables: "Checked the usage history tables",
  privileges: "Restored the service permissions",
  integrity: "Checked the restored data",
  financial_marker: "Marked the financial records",
  financial_recovery: "Checked the financial records",
  retention: "Applied the retention periods",
  retention_reapplied: "Applied the retention periods again",
  swap: "Swapped in the restored database",
  housekeeping: "Tidied up",
  services: "Started serving again",
};

export function restoreStepWords(step: string): string {
  return RESTORE_STEP_WORDS[step] ?? "Another step";
}
