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

export type DailyStatus = {
  state: "ok" | "waiting" | "attention" | "needs_key_holder" | string;
  message: string;
  keep: number;
  hour: number;
  key_holders: boolean;
  running?: boolean;
  last_error?: string;
  backups: { name: string; size_bytes: number; taken_at: string; tables: number; restorable: boolean; ledger_seq: number; etc_files: number }[];
  problems: { name: string; detail: string }[];
};

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

export function dailyHeadline(d: DailyStatus | null): Headline {
  if (!d) return { tone: "warn", title: "Checking…", detail: "" };
  switch (d.state) {
    case "ok":
      return { tone: "ok", title: "Daily backups are running", detail: `The newest ${d.keep} are kept, each encrypted and proven restorable when it is taken.` };
    case "waiting":
      return { tone: "warn", title: "Waiting for the first backup", detail: `It is taken after ${String(d.hour).padStart(2, "0")}:00 each night.` };
    case "needs_key_holder":
      return { tone: "err", title: "A backup key holder is required", detail: "Daily backups are always encrypted. Add a backup key holder above and the first backup runs that night." };
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
