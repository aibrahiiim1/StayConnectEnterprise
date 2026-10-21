// Words for the erasure ledger card on the Backups screen (GDPR final mission). Kept apart from the page so a test can hold
// them, and so what an administrator reads about a failed copy is written once.

export type LedgerStatus = {
  erasure_available: boolean;
  state: "ok" | "not_configured" | "attention" | "recovery_needed" | "blocked" | "unavailable" | string;
  message: string;
  problems?: string[];
  checked: string;
  database: { entries: number; intact: boolean; unconfirmed: number };
  primary: { present: boolean; intact: boolean; entries: number; path: string; error?: string };
  secondary: {
    configured: boolean; kind?: string; describe?: string; reachable: boolean; in_sync: boolean; entries: number;
    encrypted: boolean; last_ok?: string; error?: string;
  };
};

export type BackupKind = "scheduled" | "manual" | "pre-change" | "pre-restore";

export type BackupStatus = {
  state: "ok" | "off" | "waiting" | "attention" | "needs_key_holder" | string;
  message: string;
  key_holders: boolean;
  running?: boolean;
  last_error?: string;
  last_attempt?: string;
  schedule: { enabled: boolean; hour: number; every_days: number; keep: number; safety_keep: number; next: string | null };
  backups: { name: string; kind: BackupKind; reason?: string; by?: string; size_bytes: number; taken_at: string; tables: number; restorable: boolean; ledger_seq: number; etc_files: number }[];
  problems: { name: string; detail: string }[];
  earlier: {
    count: number; size_bytes: number; unencrypted: number; oldest: string | null;
    sets: { name: string; kind: string; taken: string; size_bytes: number; encrypted: boolean; restorable_here: boolean }[];
  };
};

/** What each kind of backup is, in the words of the person reading the list. */
export const BACKUP_KIND_LABELS: Record<string, string> = {
  scheduled: "Scheduled",
  manual: "Manual",
  "pre-change": "Before a change",
  "pre-restore": "Before a restore",
  earlier: "Earlier backup",
};
export const backupKindLabel = (k?: string) => BACKUP_KIND_LABELS[k ?? ""] ?? "Backup";

/** The settings Hotel Admin shows on the Scheduled backups card (the rest of the retention file belongs to the sweep). */
export const SCHEDULE_SETTING_KEYS = ["BACKUP_ENABLED", "BACKUP_HOUR", "BACKUP_EVERY_DAYS", "BACKUP_KEEP", "SAFETY_BACKUP_KEEP"] as const;

export const STORAGE_KINDS: { value: "dir" | "sftp" | "s3"; label: string; hint: string }[] = [
  { value: "dir", label: "External disk or network share", hint: "A folder on a USB or SATA disk, or on a network share (NAS) that is mounted on the appliance. It must not be on the appliance’s own disk." },
  { value: "sftp", label: "SFTP server", hint: "A NAS or server that offers SFTP. The server’s identity is confirmed once and then checked on every connection." },
  { value: "s3", label: "Cloud storage (S3-compatible)", hint: "A bucket the hotel owns: Amazon S3, Backblaze B2, Wasabi, MinIO or a NAS that speaks S3. Turn on versioning for it if you can." },
];

export function kindLabel(kind?: string): string {
  return STORAGE_KINDS.find((k) => k.value === kind)?.label ?? "Not set up";
}

export type Headline = { tone: "ok" | "warn" | "err"; title: string; detail: string };

/** One sentence for the top of the card: is erasure available, and if not, why and what to do. Never about guest internet. */
export function ledgerHeadline(s: LedgerStatus | null): Headline {
  if (!s) return { tone: "warn", title: "Checking…", detail: "Asking the appliance about the two copies of the erasure ledger." };
  switch (s.state) {
    case "ok":
      return { tone: "ok", title: "Both copies are in step", detail: "Every erasure is written to the appliance and to the secondary storage before it is reported as done." };
    case "not_configured":
      return { tone: "err", title: "Set up the secondary storage", detail: "It is required. Until it is set up, erasure requests are refused, because an erasure must be recorded in two independent places." };
    case "recovery_needed":
      return { tone: "err", title: "Recovery needed", detail: s.message || "The secondary storage holds erasures this appliance does not have. Recover them before any new erasure." };
    case "blocked":
      return { tone: "err", title: "The ledger needs support", detail: (s.message || "The copies disagree.") + " Erasure requests are paused. Guest internet access is not affected." };
    default:
      return { tone: "warn", title: "Needs attention", detail: (s.message || "The copies are not in step.") + " Erasure requests are paused until both copies are in step. Guest internet access is not affected." };
  }
}

export function backupHeadline(d: BackupStatus | null): Headline {
  if (!d) return { tone: "warn", title: "Checking…", detail: "" };
  const at = `${String(d.schedule.hour).padStart(2, "0")}:00`;
  const every = d.schedule.every_days === 1 ? "every day" : `every ${d.schedule.every_days} days`;
  switch (d.state) {
    case "ok":
      return { tone: "ok", title: "Scheduled backups are running", detail: `A backup is taken ${every} at or after ${at}. The newest ${d.schedule.keep} are kept, each encrypted and proven restorable when it is taken.` };
    case "off":
      return { tone: "warn", title: "Scheduled backups are off", detail: "Switch them on below to have a backup taken on a schedule. Manual backups work now, and the backups a restore, a migration or a deployment take first are always taken, whatever this says." };
    case "waiting":
      return { tone: "warn", title: "Waiting for the first scheduled backup", detail: `It is taken ${every} at or after ${at}.` };
    case "needs_key_holder":
      return { tone: "err", title: "A backup key holder is required", detail: "Backups are always encrypted. Add a backup key holder above; until then no backup can be taken, scheduled or manual." };
    default:
      return { tone: "warn", title: "Needs attention", detail: d.message || (d.problems.length ? "A stored backup no longer matches its checksum." : "") };
  }
}

export function agoWords(iso: string | undefined, now = Date.now()): string {
  if (!iso) return "never";
  const ms = now - new Date(iso).getTime();
  if (!Number.isFinite(ms)) return "never";
  const m = Math.max(0, Math.round(ms / 60000));
  if (m < 2) return "just now";
  if (m < 120) return `${m} minutes ago`;
  const h = Math.round(m / 60);
  if (h < 48) return `${h} hours ago`;
  return `${Math.round(h / 24)} days ago`;
}
