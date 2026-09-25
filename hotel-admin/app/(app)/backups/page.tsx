"use client";

// BACKUPS — one job, in the order it is done.
//
// The journey is BACKUP -> VERIFY -> DOWNLOAD -> RESTORE, so that is the shape of the page: recovery
// readiness at the top in one sentence, the backups under it with their actions, and the settings that
// govern them below, collapsed and quiet.
//
// RESTORE IS DELIBERATELY HARD TO DO BY ACCIDENT and easy to understand. It is only offered for backups that
// have passed Verify, it says in plain words what will be replaced, it requires the backup's own name typed
// out and a password, and it tells the operator what the appliance will do before it does it -- take a safety
// copy, stop serving, swap, check, and put everything back if the check fails. It then stays open, showing
// progress, until the appliance reports the outcome.

import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, ListResp, BackupHealth, BackupSettings, Whoami } from "@/lib/api";
import { PageShell, PageHeader, StatCard } from "@/components/ui/page";
import { Card, CardBody, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button, buttonVariants } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { ConfirmDialog } from "@/components/ui/dialog";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Skeleton, SkeletonRows } from "@/components/ui/misc";
import { ConsequenceList, ReadOnlyNotice, SettingField } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { canWrite } from "@/lib/roles";
import { cn, errMsg, formatDate } from "@/lib/utils";
import { formatBytes } from "@/lib/bytes";
import {
  Archive, ShieldCheck, Download, RotateCcw, AlertTriangle, CheckCircle2, Loader2, HardDrive, ChevronRight,
} from "lucide-react";

type Artifact = {
  name: string; kind?: string; size_bytes: number; modified_at?: string;
  verified_at?: string | null; verified_tables?: number | null;
};
type Maintenance = { active: boolean; reason?: string; since?: string; backup?: string; unknown?: boolean };
type RestoreStep = { step: string; ok: boolean; detail?: string };
type RestoreResult = {
  present?: boolean; ok?: boolean; running?: boolean; backup?: string; summary?: string;
  started?: string; finished?: string; duration?: string; steps?: RestoreStep[];
};
type VerifyOutcome = { name: string; ok: boolean; text: string };

export default function BackupsPage() {
  const toast = useToast();
  const [roles, setRoles] = useState<string[] | null>(null);
  const [artifacts, setArtifacts] = useState<Artifact[] | null>(null);
  const [maint, setMaint] = useState<Maintenance | null>(null);
  const [lastRestore, setLastRestore] = useState<RestoreResult | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [verifyOutcome, setVerifyOutcome] = useState<VerifyOutcome | null>(null);
  const [verifying, setVerifying] = useState<string | null>(null);
  const [restoring, setRestoring] = useState<Artifact | null>(null);

  const writable = roles ? canWrite("backups", roles) : false;

  const load = useCallback(async () => {
    try {
      const [a, m, lr] = await Promise.all([
        api.get<ListResp<Artifact>>("/backups/artifacts"),
        api.get<Maintenance>("/backups/maintenance").catch(() => ({ active: false })),
        api.get<RestoreResult>("/backups/last-restore").catch(() => ({ present: false })),
      ]);
      setArtifacts((a.data ?? []).filter((x) => x.name.startsWith("db-")));
      setMaint(m);
      setLastRestore(lr?.present ? lr : null);
    } catch (e) { setErr(errMsg(e)); }
  }, []);

  useEffect(() => {
    load();
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => setRoles([]));
  }, [load]);

  const newest = artifacts?.[0] ?? null;
  const verifiedNewest = !!newest?.verified_at;

  // TAKING A BACKUP NEEDS THE OPERATOR'S PASSWORD. The endpoint has always required password confirmation -- a
  // backup writes a complete copy of the property's data to disk -- and the screen once posted no body at all,
  // so every click returned "malformed request body". The confirmation dialog collects it, masked.
  const [backupOpen, setBackupOpen] = useState(false);
  const [backupBusy, setBackupBusy] = useState(false);
  const [backupErr, setBackupErr] = useState<string | null>(null);

  async function backupNow({ password }: { password: string }) {
    setBackupBusy(true); setBackupErr(null); setErr(null);
    try {
      await api.post("/backups/run", { password });
      setBackupOpen(false);
      toast.success("Backup created", "Verify it to prove it can be loaded back.");
      await load();
    } catch (e) { setBackupErr(errMsg(e)); } finally { setBackupBusy(false); }
  }

  async function verify(name: string) {
    setVerifying(name); setErr(null); setVerifyOutcome(null);
    try {
      const r = await api.post<{ ok: boolean; tables: number; duration: string; detail?: string }>(
        `/backups/artifacts/${encodeURIComponent(name)}/verify`);
      if (r.ok) {
        toast.success("Backup verified", `It loads and contains ${r.tables} tables (${r.duration}).`);
        setVerifyOutcome({ name, ok: true, text: `Verified: the backup loads and contains ${r.tables} tables (${r.duration}).` });
      } else {
        setVerifyOutcome({ name, ok: false, text: `This backup did NOT verify: ${r.detail ?? "it could not be loaded"}.` });
      }
      await load();
    } catch (e) { setErr(errMsg(e)); } finally { setVerifying(null); }
  }

  const openBackup = () => { setBackupErr(null); setBackupOpen(true); };

  return (
    <PageShell>
      <PageHeader
        icon={<Archive />}
        eyebrow="System"
        title="Backups"
        description="A complete copy of this property’s data, taken nightly and on demand. Verify a backup to prove it can be restored."
        actions={writable && <Button onClick={openBackup}><Archive /> Back up now</Button>}
      />

      {roles !== null && !writable && (
        <ReadOnlyNotice>Your role can see and download backups, but not take, verify or restore them.</ReadOnlyNotice>
      )}

      {/* MAINTENANCE IS THE MOST IMPORTANT THING ON THE PAGE WHEN IT IS TRUE. */}
      {maint?.active && (
        <Callout tone="warning" icon={<Loader2 className="animate-spin motion-reduce:animate-none" />}
          title="This appliance is not serving guests right now">
          {maint.reason}{maint.since ? ` · started ${formatDate(maint.since)}` : ""}
        </Callout>
      )}

      <ErrorBanner err={err} className="mb-0" />
      {verifyOutcome && !verifyOutcome.ok && (
        <Callout tone="danger" title="Verification failed">{verifyOutcome.text}</Callout>
      )}

      {/* ---- 1. CAN WE RECOVER? One sentence, at the top. -------------------------------------------- */}
      <Card>
        <CardBody>
          {artifacts === null ? (
            err ? (
              <p className="text-sm text-muted-foreground">Recovery readiness appears once the backups can be read.</p>
            ) : (
              <div className="flex items-start gap-3" aria-busy="true">
                <Skeleton className="size-9 rounded-full" />
                <div className="flex-1 space-y-2"><Skeleton className="h-4 w-2/3" /><Skeleton className="h-3 w-1/2" /></div>
              </div>
            )
          ) : (
            <div className="flex flex-wrap items-center justify-between gap-4">
              <div className="flex min-w-0 items-start gap-3">
                <span
                  className={cn(
                    "inline-flex size-9 shrink-0 items-center justify-center rounded-full",
                    newest && verifiedNewest ? "bg-success-subtle text-success" : "bg-warning-subtle text-warning-subtle-foreground",
                  )}
                  aria-hidden
                >
                  {newest && verifiedNewest ? <CheckCircle2 className="size-5" /> : <AlertTriangle className="size-5" />}
                </span>
                <div className="min-w-0">
                  <p className="text-emphasis">
                    {!newest
                      ? "There is no backup of this property yet"
                      : verifiedNewest
                        ? "This property can be recovered from its latest backup"
                        : "The latest backup has not been checked yet"}
                  </p>
                  <p className="text-sm text-muted-foreground">
                    {!newest
                      ? "Nothing could be recovered if the appliance failed today."
                      : <>
                          Taken {newest.modified_at ? formatDate(newest.modified_at) : "recently"} · {formatBytes(newest.size_bytes)}
                          {verifiedNewest
                            ? ` · verified ${formatDate(newest.verified_at!)}`
                            : " · verifying proves it can actually be loaded back"}
                        </>}
                  </p>
                </div>
              </div>
              {writable && newest && !verifiedNewest && (
                <Button variant="secondary" onClick={() => verify(newest.name)} disabled={verifying === newest.name}>
                  <ShieldCheck /> {verifying === newest.name ? "Checking…" : "Verify it"}
                </Button>
              )}
              {writable && !newest && (
                <Button variant="secondary" onClick={openBackup}>Back up now</Button>
              )}
            </div>
          )}
        </CardBody>
      </Card>

      {/* ---- the outcome of the last restore, if there was one --------------------------------------- */}
      {lastRestore && <LastRestoreCard result={lastRestore} />}

      {/* ---- 2. THE BACKUPS, with their actions ------------------------------------------------------ */}
      <Card className="overflow-hidden">
        <CardHeader>
          <div className="space-y-0.5">
            <CardTitle>Available backups</CardTitle>
            <CardDescription>
              Verify proves a backup can be loaded back. Only a verified backup can be restored.
            </CardDescription>
          </div>
        </CardHeader>
        {artifacts === null ? (
          err ? null : <SkeletonRows rows={3} cols={4} />
        ) : artifacts.length === 0 ? (
          <EmptyState icon={<Archive />} title="No backups yet"
            hint="The nightly job creates one, or use Back up now."
            action={writable ? <Button variant="secondary" size="sm" onClick={openBackup}>Back up now</Button> : undefined} />
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>Backup</TH>
                <TH>Status</TH>
                <TH className="hidden sm:table-cell">Size</TH>
                <TH className="hidden md:table-cell">Checked</TH>
                <TH className="text-end"><span className="sr-only">Actions</span></TH>
              </TR>
            </THead>
            <TBody>
              {artifacts.map((a) => (
                <TR key={a.name}>
                  <TD>
                    <div className="font-medium">{a.modified_at ? formatDate(a.modified_at) : a.name}</div>
                    <div className="break-all font-mono text-2xs text-muted-foreground">{a.name}</div>
                  </TD>
                  <TD>
                    {a.verified_at
                      ? <Badge tone="ok" dot>Verified</Badge>
                      : <Badge tone="default">Not checked</Badge>}
                  </TD>
                  <TD className="hidden tabular sm:table-cell">{formatBytes(a.size_bytes)}</TD>
                  <TD className="hidden text-sm text-muted-foreground md:table-cell">
                    {a.verified_at
                      ? `${formatDate(a.verified_at)}${a.verified_tables ? ` · ${a.verified_tables} tables` : ""}`
                      : "—"}
                  </TD>
                  <TD className="text-end">
                    <div className="flex flex-wrap justify-end gap-2">
                      {writable && (
                        <Button size="sm" variant="secondary" disabled={verifying === a.name}
                          aria-label={`${a.verified_at ? "Re-verify" : "Verify"} ${a.name}`}
                          onClick={() => verify(a.name)}>
                          <ShieldCheck />
                          {verifying === a.name ? "Checking…" : a.verified_at ? "Re-verify" : "Verify"}
                        </Button>
                      )}
                      <a href={`/api/edge/v1/backups/artifacts/${encodeURIComponent(a.name)}/download`}
                        aria-label={`Download ${a.name}`}
                        className={buttonVariants({ variant: "outline", size: "sm" })}>
                        <Download /> Download
                      </a>
                      {writable && a.verified_at && (
                        <Button size="sm" variant="danger" aria-label={`Restore ${a.name}`} onClick={() => setRestoring(a)}>
                          <RotateCcw /> Restore
                        </Button>
                      )}
                    </div>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      </Card>

      {/* ---- 3. THE SETTINGS THAT GOVERN THEM: present, and not in the way ------------------------- */}
      <StorageAndRetention writable={writable} />

      {/* ---- Back up now: password confirmation ------------------------------------------------------ */}
      <ConfirmDialog
        open={backupOpen}
        onOpenChange={(v) => { if (!v) { setBackupOpen(false); setBackupErr(null); } }}
        title="Back up this property now?"
        description="A backup writes a complete copy of this property’s data to the appliance. It takes a moment and guests are not affected."
        confirmLabel="Back up now"
        busy={backupBusy}
        error={backupErr}
        requirePassword
        onConfirm={backupNow}
      />

      {restoring && (
        <RestoreDialog
          backup={restoring}
          onClose={() => setRestoring(null)}
          onDone={async (r) => {
            setRestoring(null); setLastRestore(r);
            if (r.ok) toast.success("Restore finished", r.summary);
            else toast.error("Restore did not complete", r.summary);
            await load();
          }}
        />
      )}
    </PageShell>
  );
}

function LastRestoreCard({ result }: { result: RestoreResult }) {
  // A RESTORE IN PROGRESS IS NOT A FAILED ONE. The durable record carries ok:false until it finishes, which
  // is correct -- nothing has succeeded yet -- but rendering it with a warning icon and a red step list would
  // tell an operator their restore had gone wrong while it was quietly working.
  const running = result.running === true;
  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2">
          {running ? <Loader2 className="size-4 animate-spin text-muted-foreground motion-reduce:animate-none" aria-hidden />
            : result.ok ? <CheckCircle2 className="size-4 text-success" aria-hidden />
                        : <AlertTriangle className="size-4 text-destructive" aria-hidden />}
          {running ? "Restore in progress" : "Last restore"}
        </CardTitle>
        {!running && (
          <Badge tone={result.ok ? "ok" : "err"} dot>{result.ok ? "Succeeded" : "Did not complete"}</Badge>
        )}
      </CardHeader>
      <CardBody className="space-y-3 text-sm">
        <p>
          {running
            ? "The appliance is replacing the database. It stops serving guests until this finishes. Do not start another restore."
            : result.summary}
        </p>
        <p className="text-caption text-muted-foreground">
          <span className="font-mono">{result.backup}</span>
          {running
            ? (result.started ? ` · started ${formatDate(result.started)}` : "")
            : <>{result.finished ? ` · ${formatDate(result.finished)}` : ""}{result.duration ? ` · took ${result.duration}` : ""}</>}
        </p>
        {/* EVERY STEP, INCLUDING THE ONES THAT DID NOT RUN. A restore that stopped early should show where. */}
        {result.steps && result.steps.length > 0 && (
          <ol className="space-y-1.5">
            {result.steps.map((s, i) => (
              <li key={i} className="flex items-start gap-2 text-xs">
                {s.ok ? <CheckCircle2 className="mt-0.5 size-3.5 shrink-0 text-success" aria-hidden />
                      : <AlertTriangle className="mt-0.5 size-3.5 shrink-0 text-destructive" aria-hidden />}
                <span>
                  <span className="sr-only">{s.ok ? "Done: " : "Not done: "}</span>
                  <strong className="font-semibold">{s.step.replace(/_/g, " ")}</strong>{s.detail ? ` — ${s.detail}` : ""}
                </span>
              </li>
            ))}
          </ol>
        )}
      </CardBody>
    </Card>
  );
}

/** THE CONFIRMATION. Two independent proofs, and a plain account of what is about to happen. */
function RestoreDialog({ backup, onClose, onDone }: {
  backup: Artifact;
  onClose: () => void;
  onDone: (r: RestoreResult) => void;
}) {
  const [busy, setBusy] = useState(false);
  const [started, setStarted] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const alive = useRef(true);
  useEffect(() => () => { alive.current = false; }, []);

  // START IT, THEN WATCH IT. The request returns as soon as the appliance has accepted the restore; the
  // outcome arrives from the durable record. This is not a nicety: the restore restarts edged, so the
  // connection this request was made over is one of the things being taken away. A screen that waited for a
  // reply would eventually show a timeout as a FAILURE while the restore was still running -- which is how an
  // operator ends up starting a second destructive operation on top of the first.
  //
  // Nothing here retries anything. It asks what happened until the appliance says. The dialog cannot be
  // closed while it is busy.
  async function go({ password, confirm }: { password: string; confirm: string }) {
    setBusy(true); setErr(null);
    try {
      await api.post(`/backups/artifacts/${encodeURIComponent(backup.name)}/restore`, { password, confirm });
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : errMsg(e));
      setBusy(false);
      return;
    }
    setStarted(true);
    while (alive.current) {
      await new Promise((r) => setTimeout(r, 4000));
      let r: RestoreResult | null = null;
      // A failed poll means edged is restarting, which is expected mid-restore. Keep asking.
      try { r = await api.get<RestoreResult>("/backups/last-restore"); } catch { continue; }
      if (r?.present && r.running === false) { onDone(r); return; }
    }
  }

  const taken = backup.modified_at ? formatDate(backup.modified_at) : "it was taken";

  return (
    <ConfirmDialog
      open
      onOpenChange={(v) => { if (!v && !busy) onClose(); }}
      title="Restore this backup?"
      confirmLabel={started ? "Restoring" : "Restore this backup"}
      confirmVariant="danger"
      busy={busy}
      error={err}
      confirmText={backup.name}
      requirePassword
      onConfirm={({ password }) => go({ password, confirm: backup.name })}
    >
      <ConsequenceList
        title="This property’s current data will be replaced"
        items={[
          <>Everything recorded since {taken} — stays, sessions, guest accounts, settings and the activity trail — is replaced by what the backup contains.</>,
          <>The appliance stops serving guests and staff while it works, usually for several minutes.</>,
        ]}
      />

      <div className="rounded-md border border-border bg-surface p-3">
        <div className="text-caption text-muted-foreground">Restoring</div>
        <div className="font-medium">{backup.modified_at ? formatDate(backup.modified_at) : backup.name}</div>
        <div className="break-all font-mono text-2xs text-muted-foreground">{backup.name}</div>
        <div className="mt-1 text-caption text-muted-foreground">
          {formatBytes(backup.size_bytes)}
          {backup.verified_at ? ` · verified ${formatDate(backup.verified_at)}` : ""}
        </div>
      </div>

      {/* SAYING WHAT WILL HAPPEN IS PART OF THE CONSENT. An operator agreeing to a destructive step
          should know the appliance protects them, and how, rather than hoping it does. */}
      <div>
        <div className="mb-1.5 text-label">What the appliance will do</div>
        <ol className="list-decimal space-y-1 ps-5 text-sm text-muted-foreground">
          <li>Take a fresh safety copy of the current data first.</li>
          <li>Stop serving guests and staff while it works.</li>
          <li>Load the backup into a separate database — if it will not load, nothing is replaced.</li>
          <li>Swap the two, keeping the current database rather than deleting it.</li>
          <li>Check the restored database, and put the original back if the check fails.</li>
          <li>Start serving again.</li>
        </ol>
      </div>

      {started && (
        <div role="status" aria-live="polite" className="flex items-start gap-2.5 rounded-md border border-info/25 bg-info-subtle px-3.5 py-3 text-sm text-info-subtle-foreground">
          <Loader2 className="mt-0.5 size-4 shrink-0 animate-spin motion-reduce:animate-none" aria-hidden />
          <p>
            <strong className="font-semibold">Restoring — this window stays open until it finishes.</strong>{" "}
            The appliance has accepted the restore and is working through it. This can take several minutes. The
            outcome is recorded on the appliance, so if this page disconnects — it will, briefly, because the
            services are restarted — you will still see what happened when it comes back. Do not start another
            restore.
          </p>
        </div>
      )}
    </ConfirmDialog>
  );
}

/** RETENTION, SCHEDULE, DISK AND THE SWEEP.
 *
 *  Kept in full, below the journey, collapsed by default: the numbers matter on the day somebody is looking
 *  for them and are noise on every other day. Retention values are hotel-editable settings, each with its unit,
 *  default and allowed range; saving them requires password confirmation, as the appliance enforces. */
function StorageAndRetention({ writable }: { writable: boolean }) {
  const toast = useToast();
  const [health, setHealth] = useState<BackupHealth | null>(null);
  const [settings, setSettings] = useState<BackupSettings | null>(null);
  const [edits, setEdits] = useState<Record<string, string>>({});
  const [schedule, setSchedule] = useState("");
  const [settingsPw, setSettingsPw] = useState("");
  const [saving, setSaving] = useState(false);
  const [saveErr, setSaveErr] = useState<string | null>(null);

  const load = useCallback(async () => {
    try {
      const [h, s] = await Promise.all([
        api.get<BackupHealth>("/backups/health"),
        api.get<BackupSettings>("/backups/settings"),
      ]);
      setHealth(h);
      setSettings(s);
      setEdits(Object.fromEntries(s.retention.map((x) => [x.key, String(x.value)])));
      setSchedule(s.schedule ?? "");
    } catch { /* the section reports its own absence below rather than breaking the page */ }
  }, []);
  useEffect(() => { load(); }, [load]);

  const valueOf = (key: string, fallback: number) => {
    const raw = edits[key];
    return raw === undefined || raw.trim() === "" ? fallback : Number(raw);
  };
  const dirty = !!settings && (
    settings.retention.some((x) => valueOf(x.key, x.value) !== x.value) ||
    (schedule !== (settings.schedule ?? ""))
  );
  const outOfRange = !!settings && settings.retention.some((x) => {
    const v = valueOf(x.key, x.value);
    return !Number.isFinite(v) || v < x.min || v > x.max;
  });

  async function save() {
    if (!settings) return;
    setSaving(true); setSaveErr(null);
    try {
      await api.put("/backups/settings", {
        retention: Object.fromEntries(settings.retention.map((x) => [x.key, valueOf(x.key, x.value)])),
        schedule,
        password: settingsPw,
      });
      toast.success("Retention policy saved");
      setSettingsPw("");
      await load();
    } catch (e) { setSaveErr(errMsg(e)); } finally { setSaving(false); }
  }

  const ret = (health?.retention ?? {}) as Record<string, unknown>;
  const diskPct = Number(ret.disk_pct ?? 0);

  return (
    <Card>
      <details className="group">
        <summary className="flex cursor-pointer select-none items-center gap-2 rounded-lg px-5 py-4 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
          <ChevronRight className="size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-90 rtl:-scale-x-100" aria-hidden />
          <HardDrive className="size-4 shrink-0 text-muted-foreground" aria-hidden />
          <span className="text-emphasis">Storage &amp; retention</span>
          <span className="ms-auto hidden text-caption text-muted-foreground sm:inline">
            Disk used, backups kept, the nightly sweep and how long things are kept
          </span>
        </summary>
        <div className="space-y-5 border-t border-border px-5 py-5">
          <div className="grid gap-3 sm:grid-cols-3">
            <StatCard
              label="Disk used"
              value={health?.retention_readable ? `${diskPct}%` : "—"}
              tone={health?.retention_readable
                ? (diskPct >= Number(ret.disk_crit ?? 101) ? "err" : diskPct >= Number(ret.disk_warn ?? 101) ? "warn" : "default")
                : "default"}
              hint={health?.retention_readable
                ? `Warns at ${ret.disk_warn ?? "—"}%, critical at ${ret.disk_crit ?? "—"}%`
                : (health?.retention_error ?? "Checking…")}
            />
            <StatCard
              label="Backups kept"
              value={health ? String(health.database_backups ?? 0) : "—"}
              hint={health?.newest_database_backup ? `Newest ${formatDate(health.newest_database_backup)}` : undefined}
            />
            <StatCard
              label="Nightly sweep"
              value={<span className="text-subtitle">{health ? (health.timer_readable ? (health.timer_active ? "Scheduled" : "Not scheduled") : "Unknown") : "—"}</span>}
              hint={settings?.schedule_known ? `Runs at ${settings.schedule}` : undefined}
            />
          </div>

          {health?.retention_readable && Number(ret.failures ?? 0) > 0 && (
            <Callout tone="danger" title={`${String(ret.failures)} failure(s) on the last sweep`}>
              {String(ret.failure_detail || "no detail")}
            </Callout>
          )}

          {settings && (
            <>
              <p className="text-sm text-muted-foreground">
                What the nightly sweep keeps, and when it runs.{" "}
                {settings.config_present
                  ? "These values are stored on the appliance."
                  : "No policy has been saved yet, so the built-in defaults below are in force."}
              </p>
              <div className="grid gap-5 sm:grid-cols-2">
                {settings.retention.map((sIt) => (
                  <SettingField
                    key={sIt.key}
                    label={sIt.label}
                    unit={sIt.unit}
                    min={sIt.min}
                    max={sIt.max}
                    defaultValue={sIt.default}
                    explanation={sIt.explains}
                    readOnly={!writable}
                    value={edits[sIt.key] ?? String(sIt.value)}
                    onChange={(v) => setEdits((m) => ({ ...m, [sIt.key]: v }))}
                  />
                ))}
                <Field
                  label="Nightly sweep runs at"
                  hint={<>Appliance local time.{" "}
                    {settings.schedule_known
                      ? `Currently scheduled for ${settings.schedule}.`
                      : "The current schedule could not be read from the appliance."}</>}
                >
                  <Input type="time" value={schedule} disabled={!writable} className="w-40"
                    onChange={(e) => setSchedule(e.target.value)} />
                </Field>
              </div>

              {/* The sweep is what stands between this appliance and a full disk, and retention decides how
                  far back it can recover. Changing either is attributed and needs password confirmation. */}
              {writable && dirty && (
                <div className="space-y-3 rounded-md border border-border bg-surface p-4">
                  <ErrorBanner err={saveErr} className="mb-0" />
                  <Field label="Confirm your password" className="max-w-sm"
                    hint="Changing what is kept, and when, is recorded against your account.">
                    <Input type="password" autoComplete="current-password" value={settingsPw}
                      onChange={(e) => setSettingsPw(e.target.value)} />
                  </Field>
                  <div className="flex flex-wrap gap-2">
                    <Button disabled={saving || !settingsPw || outOfRange} onClick={save}>
                      {saving ? "Saving…" : "Save retention policy"}
                    </Button>
                    <Button variant="ghost" disabled={saving} onClick={() => {
                      setEdits(Object.fromEntries(settings.retention.map((x) => [x.key, String(x.value)])));
                      setSchedule(settings.schedule ?? "");
                      setSettingsPw("");
                      setSaveErr(null);
                    }}>Discard changes</Button>
                  </div>
                </div>
              )}

              <p className="text-caption text-muted-foreground">
                {String(ret.retained ?? 0)} artefacts retained, {String(ret.protected ?? 0)} protected and never
                deleted (identity, certificates and trust material), {String(ret.pinned ?? 0)} pinned by an
                operator. The sweep never removes the current or previous release, the newest database backup,
                or anything pinned — whatever these numbers say.
              </p>
            </>
          )}
        </div>
      </details>
    </Card>
  );
}
