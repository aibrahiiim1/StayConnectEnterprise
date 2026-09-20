"use client";

// BACKUPS — one job, in the order it is done.
//
// WHAT THIS REPLACES. A page of equal-weight cards: retention knobs, a disk gauge, a schedule, a health
// summary and, somewhere among them, the backups themselves. Everything was present and nothing was first.
// An operator arriving to answer "could we recover from this?" had to assemble the answer from four panels.
//
// The journey is BACKUP -> VERIFY -> DOWNLOAD -> RESTORE, so that is the shape of the page: recovery
// readiness at the top in one sentence, the backups under it with their actions, and the settings that
// govern them below, available and quiet.
//
// RESTORE IS DELIBERATELY HARD TO DO BY ACCIDENT and easy to understand. It only offers backups that have
// passed Verify, it says in plain words that the property's current data will be replaced, it requires the
// backup's own name typed out and a password, and it tells the operator what the appliance will do before it
// does it -- take a safety copy, stop serving, swap, check, and put everything back if the check fails.

import { useCallback, useEffect, useState } from "react";
import { api, ApiError, ListResp, BackupHealth, BackupSettings } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { SkeletonRows } from "@/components/ui/misc";
import { canWrite } from "@/lib/roles";
import { errMsg, formatDate } from "@/lib/utils";
import { formatBytes } from "@/lib/bytes";
import { Whoami } from "@/lib/api";
import {
  Archive, ShieldCheck, Download, RotateCcw, AlertTriangle, CheckCircle2, Loader2,
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

export default function BackupsPage() {
  const [roles, setRoles] = useState<string[]>([]);
  const [artifacts, setArtifacts] = useState<Artifact[] | null>(null);
  const [maint, setMaint] = useState<Maintenance | null>(null);
  const [lastRestore, setLastRestore] = useState<RestoreResult | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [verifying, setVerifying] = useState<string | null>(null);
  const [restoring, setRestoring] = useState<Artifact | null>(null);

  const writable = canWrite("backups", roles);

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
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => {});
  }, [load]);

  const newest = artifacts?.[0] ?? null;
  const verifiedNewest = !!newest?.verified_at;

  async function backupNow() {
    setBusy("backup"); setErr(null); setMsg(null);
    try { await api.post("/backups/run"); setMsg("Backup created."); await load(); }
    catch (e) { setErr(errMsg(e)); } finally { setBusy(null); }
  }

  async function verify(name: string) {
    setVerifying(name); setErr(null); setMsg(null);
    try {
      const r = await api.post<{ ok: boolean; tables: number; duration: string; detail?: string }>(
        `/backups/artifacts/${encodeURIComponent(name)}/verify`);
      setMsg(r.ok
        ? `Verified: the backup loads and contains ${r.tables} tables (${r.duration}).`
        : `This backup did NOT verify: ${r.detail ?? "it could not be loaded"}.`);
      await load();
    } catch (e) { setErr(errMsg(e)); } finally { setVerifying(null); }
  }

  return (
    <div className="mx-auto w-full max-w-5xl space-y-5">
      <header>
        <div className="text-2xs font-semibold uppercase tracking-widest text-muted-foreground">System</div>
        <h1 className="flex items-center gap-2 text-xl font-semibold tracking-tight sm:text-2xl">
          <Archive className="h-5 w-5" /> Backups
        </h1>
        <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
          A complete copy of this property&rsquo;s data, taken nightly and on demand.
        </p>
      </header>

      {/* MAINTENANCE IS THE MOST IMPORTANT THING ON THE PAGE WHEN IT IS TRUE. */}
      {maint?.active && (
        <div role="alert" className="flex items-start gap-3 rounded-lg border border-warning/30 bg-warning-subtle p-4">
          <Loader2 className="mt-0.5 h-5 w-5 shrink-0 animate-spin text-warning-subtle-foreground" />
          <div className="text-sm text-warning-subtle-foreground">
            <p className="font-medium">This appliance is not serving guests right now</p>
            <p>{maint.reason}{maint.since ? ` · started ${formatDate(maint.since)}` : ""}</p>
          </div>
        </div>
      )}

      {err && <Banner tone="err">{err}</Banner>}
      {msg && <Banner tone="ok">{msg}</Banner>}

      {/* ---- 1. CAN WE RECOVER? One sentence, at the top. -------------------------------------------- */}
      <Card>
        <CardBody className="space-y-4">
          {artifacts === null ? <SkeletonRows rows={2} cols={2} /> : !newest ? (
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="flex items-start gap-3">
                <AlertTriangle className="mt-0.5 h-5 w-5 text-warning-subtle-foreground" />
                <div>
                  <p className="font-medium">There is no backup of this property yet</p>
                  <p className="text-sm text-muted-foreground">Nothing could be recovered if the appliance failed today.</p>
                </div>
              </div>
              <Button onClick={backupNow} disabled={!writable || busy === "backup"}>
                {busy === "backup" ? "Backing up…" : "Back up now"}
              </Button>
            </div>
          ) : (
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="flex items-start gap-3">
                {verifiedNewest
                  ? <CheckCircle2 className="mt-0.5 h-5 w-5 text-success-subtle-foreground" />
                  : <AlertTriangle className="mt-0.5 h-5 w-5 text-warning-subtle-foreground" />}
                <div>
                  <p className="font-medium">
                    {verifiedNewest
                      ? "This property can be recovered from its latest backup"
                      : "The latest backup has not been checked yet"}
                  </p>
                  <p className="text-sm text-muted-foreground">
                    Taken {newest.modified_at ? formatDate(newest.modified_at) : "recently"} · {formatBytes(newest.size_bytes)}
                    {verifiedNewest
                      ? ` · verified ${formatDate(newest.verified_at!)}`
                      : " · verifying proves it can actually be loaded back"}
                  </p>
                </div>
              </div>
              <div className="flex gap-2">
                {!verifiedNewest && (
                  <Button variant="secondary" onClick={() => verify(newest.name)}
                    disabled={!writable || verifying === newest.name}>
                    {verifying === newest.name ? "Checking…" : "Verify it"}
                  </Button>
                )}
                <Button onClick={backupNow} disabled={!writable || busy === "backup"}>
                  {busy === "backup" ? "Backing up…" : "Back up now"}
                </Button>
              </div>
            </div>
          )}
        </CardBody>
      </Card>

      {/* ---- the outcome of the last restore, if there was one --------------------------------------- */}
      {lastRestore && <LastRestoreCard result={lastRestore} />}

      {/* ---- 2. THE BACKUPS, with their actions ------------------------------------------------------ */}
      <Card>
        <CardHeader>
          <div>
            <CardTitle>Available backups</CardTitle>
            <p className="mt-0.5 text-xs text-muted-foreground">
              Verify proves a backup can be loaded back. Only a verified backup can be restored.
            </p>
          </div>
        </CardHeader>
        <CardBody>
          {artifacts === null ? <SkeletonRows rows={3} cols={4} /> : artifacts.length === 0 ? (
            <EmptyState icon={<Archive />} title="No backups yet"
              hint="The nightly job creates one, or use Back up now." />
          ) : (
            <ul className="divide-y">
              {artifacts.map((a) => (
                <li key={a.name} className="flex flex-wrap items-center justify-between gap-3 py-3">
                  <div className="min-w-0">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-medium">{a.modified_at ? formatDate(a.modified_at) : a.name}</span>
                      {a.verified_at
                        ? <Badge tone="ok">Verified</Badge>
                        : <Badge tone="default">Not checked</Badge>}
                    </div>
                    <div className="text-xs text-muted-foreground">
                      {formatBytes(a.size_bytes)}
                      {a.verified_at ? ` · checked ${formatDate(a.verified_at)}${a.verified_tables ? `, ${a.verified_tables} tables` : ""}` : ""}
                      <span className="ml-1 font-mono opacity-60">{a.name}</span>
                    </div>
                  </div>
                  <div className="flex flex-wrap gap-2">
                    <Button size="sm" variant="secondary" disabled={!writable || verifying === a.name}
                      onClick={() => verify(a.name)}>
                      <ShieldCheck className="mr-1 h-3.5 w-3.5" />
                      {verifying === a.name ? "Checking…" : a.verified_at ? "Re-verify" : "Verify"}
                    </Button>
                    <a href={`/api/edge/v1/backups/artifacts/${encodeURIComponent(a.name)}/download`}
                       className="inline-flex h-8 items-center rounded-md border px-3 text-sm">
                      <Download className="mr-1 h-3.5 w-3.5" /> Download
                    </a>
                    <Button size="sm" variant="secondary" disabled={!writable || !a.verified_at}
                      title={a.verified_at ? undefined : "Verify this backup before it can be restored"}
                      onClick={() => setRestoring(a)}>
                      <RotateCcw className="mr-1 h-3.5 w-3.5" /> Restore
                    </Button>
                  </div>
                </li>
              ))}
            </ul>
          )}
        </CardBody>
      </Card>

      {/* ---- 3. THE SETTINGS THAT GOVERN THEM: present, and not in the way ------------------------- */}
      <StorageAndRetention writable={writable} setErr={setErr} setMsg={setMsg} />

      {restoring && (
        <RestoreDialog
          backup={restoring}
          onClose={() => setRestoring(null)}
          onDone={async (r) => { setRestoring(null); setLastRestore(r); await load(); }}
        />
      )}
    </div>
  );
}

function Banner({ tone, children }: { tone: "ok" | "err"; children: React.ReactNode }) {
  const cls = tone === "ok"
    ? "border-success/25 bg-success-subtle text-success-subtle-foreground"
    : "border-destructive/25 bg-destructive-subtle text-destructive-subtle-foreground";
  return <div role={tone === "err" ? "alert" : "status"} className={`rounded-lg border p-3 text-sm ${cls}`}>{children}</div>;
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
          {running ? <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />
            : result.ok ? <CheckCircle2 className="h-4 w-4 text-success-subtle-foreground" />
                        : <AlertTriangle className="h-4 w-4 text-destructive-subtle-foreground" />}
          {running ? "Restore in progress" : "Last restore"}
        </CardTitle>
      </CardHeader>
      <CardBody className="space-y-2 text-sm">
        <p>
          {running
            ? "The appliance is replacing the database. It stops serving guests until this finishes. Do not start another restore."
            : result.summary}
        </p>
        <p className="text-xs text-muted-foreground">
          {result.backup}
          {running
            ? (result.started ? ` · started ${formatDate(result.started)}` : "")
            : <>{result.finished ? ` · ${formatDate(result.finished)}` : ""}{result.duration ? ` · took ${result.duration}` : ""}</>}
        </p>
        {/* EVERY STEP, INCLUDING THE ONES THAT DID NOT RUN. A restore that stopped early should show where. */}
        {result.steps && result.steps.length > 0 && (
          <ol className="space-y-1">
            {result.steps.map((s, i) => (
              <li key={i} className="flex items-start gap-2 text-xs">
                {s.ok ? <CheckCircle2 className="mt-0.5 h-3.5 w-3.5 shrink-0 text-success-subtle-foreground" />
                      : <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0 text-destructive-subtle-foreground" />}
                <span><strong>{s.step.replace(/_/g, " ")}</strong>{s.detail ? ` — ${s.detail}` : ""}</span>
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
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");
  const [busy, setBusy] = useState(false);
  const [started, setStarted] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  // START IT, THEN WATCH IT. The request returns as soon as the appliance has accepted the restore; the
  // outcome arrives from the durable record. This is not a nicety: the restore restarts edged, so the
  // connection this request was made over is one of the things being taken away. A screen that waited for a
  // reply would eventually show a timeout as a FAILURE while the restore was still running -- which is how an
  // operator ends up starting a second destructive operation on top of the first.
  //
  // Nothing here retries anything. It asks what happened until the appliance says.
  async function go() {
    setBusy(true); setErr(null);
    try {
      await api.post(`/backups/artifacts/${encodeURIComponent(backup.name)}/restore`, { password, confirm });
    } catch (e) {
      setErr(e instanceof ApiError ? e.message : errMsg(e));
      setBusy(false);
      return;
    }
    setStarted(true);
    for (;;) {
      await new Promise((r) => setTimeout(r, 4000));
      let r: RestoreResult | null = null;
      // A failed poll means edged is restarting, which is expected mid-restore. Keep asking.
      try { r = await api.get<RestoreResult>("/backups/last-restore"); } catch { continue; }
      if (r?.present && r.running === false) { onDone(r); return; }
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-foreground/45 p-4" role="dialog" aria-modal="true">
      <Card className="max-h-[90vh] w-full max-w-lg overflow-y-auto">
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <AlertTriangle className="h-5 w-5 text-warning-subtle-foreground" /> Restore this backup?
          </CardTitle>
        </CardHeader>
        <CardBody className="space-y-4 text-sm">
          <div className="rounded-lg border border-warning/30 bg-warning-subtle p-3 text-warning-subtle-foreground">
            <p className="font-medium">
              This property&rsquo;s current data will be replaced by the contents of this backup.
            </p>
            <p className="mt-1">
              Everything recorded since {backup.modified_at ? formatDate(backup.modified_at) : "it was taken"} —
              stays, sessions, guest accounts, settings and the activity trail — will be replaced by what the
              backup contains.
            </p>
          </div>

          <div>
            <div className="text-xs text-muted-foreground">Restoring</div>
            <div className="font-medium">{backup.modified_at ? formatDate(backup.modified_at) : backup.name}</div>
            <div className="font-mono text-2xs text-muted-foreground">{backup.name}</div>
            <div className="mt-1 text-xs text-muted-foreground">
              {formatBytes(backup.size_bytes)}
              {backup.verified_at ? ` · verified ${formatDate(backup.verified_at)}` : ""}
            </div>
          </div>

          {/* SAYING WHAT WILL HAPPEN IS PART OF THE CONSENT. An operator agreeing to a destructive step
              should know the appliance protects them, and how, rather than hoping it does. */}
          <div>
            <div className="mb-1 font-medium">What the appliance will do</div>
            <ol className="list-decimal space-y-1 pl-5 text-xs text-muted-foreground">
              <li>Take a fresh safety copy of the current data first.</li>
              <li>Stop serving guests and staff while it works.</li>
              <li>Load the backup into a separate database — if it will not load, nothing is replaced.</li>
              <li>Swap the two, keeping the current database rather than deleting it.</li>
              <li>Check the restored database, and put the original back if the check fails.</li>
              <li>Start serving again.</li>
            </ol>
          </div>

          {err && <Banner tone="err">{err}</Banner>}

          <label className="block">
            <span className="text-xs text-muted-foreground">
              Type <span className="font-mono">{backup.name}</span> to confirm
            </span>
            <Input value={confirm} onChange={(e) => setConfirm(e.target.value)} className="mt-1 font-mono"
              aria-label="Type the backup name to confirm" autoComplete="off" />
          </label>
          <label className="block">
            <span className="text-xs text-muted-foreground">Confirm your password</span>
            <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)}
              className="mt-1" aria-label="Confirm your password" autoComplete="current-password" />
          </label>

          <div className="flex flex-wrap justify-end gap-2 pt-2">
            <Button variant="secondary" onClick={onClose} disabled={busy}>Cancel</Button>
            <Button onClick={go} disabled={busy || confirm !== backup.name || !password}>
              {busy ? (started ? "Restoring…" : "Starting…") : "Restore this backup"}
            </Button>
          </div>
          {started && (
            <p className="text-xs text-muted-foreground">
              The appliance has accepted the restore and is working through it. This can take several minutes,
              and the appliance stops serving guests while the database is replaced. The outcome is recorded on
              the appliance, so if this page disconnects — it will, briefly, because the services are
              restarted — you will still see what happened when it comes back. Do not start another restore.
            </p>
          )}
        </CardBody>
      </Card>
    </div>
  );
}

/** RETENTION, SCHEDULE, DISK AND THE SWEEP.
 *
 *  Kept in full, moved below the journey. The previous page gave these equal billing with the backups
 *  themselves, so an operator arriving to answer "could we recover from this?" had to assemble the answer
 *  from four panels of roughly equal weight. They are not unimportant -- the sweep is what stands between
 *  this appliance and a full disk, and retention decides how far back it can recover -- they are just not
 *  the question anyone opens this page with.
 *
 *  Collapsed by default and opened in one click, because the numbers matter on the day somebody is looking
 *  for them and are noise on every other day. */
function StorageAndRetention({ writable, setErr, setMsg }: {
  writable: boolean;
  setErr: (s: string | null) => void;
  setMsg: (s: string | null) => void;
}) {
  const [health, setHealth] = useState<BackupHealth | null>(null);
  const [settings, setSettings] = useState<BackupSettings | null>(null);
  const [edits, setEdits] = useState<Record<string, number>>({});
  const [schedule, setSchedule] = useState("");
  const [settingsPw, setSettingsPw] = useState("");
  const [saving, setSaving] = useState(false);

  const load = useCallback(async () => {
    try {
      const [h, s] = await Promise.all([
        api.get<BackupHealth>("/backups/health"),
        api.get<BackupSettings>("/backups/settings"),
      ]);
      setHealth(h);
      setSettings(s);
      setEdits(Object.fromEntries(s.retention.map((x) => [x.key, x.value])));
      setSchedule(s.schedule ?? "");
    } catch { /* the section reports its own absence below rather than breaking the page */ }
  }, []);
  useEffect(() => { load(); }, [load]);

  const dirty = !!settings && (
    settings.retention.some((x) => (edits[x.key] ?? x.value) !== x.value) ||
    (schedule !== (settings.schedule ?? ""))
  );

  async function save() {
    if (!settings) return;
    setSaving(true); setErr(null); setMsg(null);
    try {
      await api.put("/backups/settings", {
        retention: Object.fromEntries(settings.retention.map((x) => [x.key, edits[x.key] ?? x.value])),
        schedule,
        password: settingsPw,
      });
      setMsg("Retention policy saved.");
      setSettingsPw("");
      await load();
    } catch (e) { setErr(errMsg(e)); } finally { setSaving(false); }
  }

  const ret = (health?.retention ?? {}) as Record<string, unknown>;
  const diskPct = Number(ret.disk_pct ?? 0);

  return (
    <details className="rounded-lg border">
      <summary className="cursor-pointer px-4 py-3 text-sm font-medium">
        Storage, retention and the nightly sweep
      </summary>
      <div className="space-y-4 border-t p-4">
        <div className="grid gap-4 sm:grid-cols-3">
          <Figure label="Disk used"
            value={health?.retention_readable ? `${diskPct}%` : "—"}
            hint={health?.retention_readable
              ? `Warns at ${ret.disk_warn ?? "—"}%, critical at ${ret.disk_crit ?? "—"}%`
              : (health?.retention_error ?? "Checking…")} />
          <Figure label="Backups kept"
            value={health ? String(health.database_backups ?? 0) : "—"}
            hint={health?.newest_database_backup ? `Newest ${formatDate(health.newest_database_backup)}` : undefined} />
          <Figure label="Nightly sweep"
            value={health ? (health.timer_readable ? (health.timer_active ? "Scheduled" : "Not scheduled") : "Unknown") : "—"}
            hint={settings?.schedule_known ? `Runs at ${settings.schedule}` : undefined} />
        </div>

        {health?.retention_readable && Number(ret.failures ?? 0) > 0 && (
          <p role="alert" className="text-sm text-destructive-subtle-foreground">
            {String(ret.failures)} failure(s) on the last sweep: {String(ret.failure_detail || "no detail")}
          </p>
        )}

        {settings && (
          <>
            <p className="text-sm text-muted-foreground">
              What the nightly sweep keeps, and when it runs.{" "}
              {settings.config_present
                ? "These values are stored on the appliance."
                : "No policy has been saved yet, so the built-in defaults below are in force."}
            </p>
            <div className="grid gap-4 sm:grid-cols-2">
              {settings.retention.map((sIt) => (
                <label key={sIt.key} className="block text-sm">
                  {sIt.label} <span className="text-muted-foreground">({sIt.unit})</span>
                  <Input type="number" min={sIt.min} max={sIt.max} disabled={!writable}
                    value={edits[sIt.key] ?? sIt.value}
                    onChange={(e) => setEdits((m) => ({ ...m, [sIt.key]: Number(e.target.value) }))} />
                  <span className="mt-1 block text-xs text-muted-foreground">
                    {sIt.explains} Allowed {sIt.min}–{sIt.max}; default {sIt.default}.
                  </span>
                </label>
              ))}
              <label className="block text-sm">
                Nightly sweep runs at
                <Input type="time" value={schedule} disabled={!writable}
                  onChange={(e) => setSchedule(e.target.value)} />
                <span className="mt-1 block text-xs text-muted-foreground">
                  Appliance local time.{" "}
                  {settings.schedule_known
                    ? `Currently scheduled for ${settings.schedule}.`
                    : "The current schedule could not be read from the appliance."}
                </span>
              </label>
            </div>

            {/* The sweep is what stands between this appliance and a full disk, and retention decides how
                far back it can recover. Changing either is attributed. */}
            {dirty && (
              <div className="space-y-3 border-t pt-3">
                <label className="block max-w-sm text-sm">
                  Confirm your password
                  <Input type="password" autoComplete="current-password" value={settingsPw}
                    onChange={(e) => setSettingsPw(e.target.value)} />
                </label>
                <div className="flex gap-2">
                  <Button disabled={!writable || saving || !settingsPw} onClick={save}>
                    {saving ? "Saving…" : "Save retention policy"}
                  </Button>
                  <Button variant="secondary" disabled={saving} onClick={() => {
                    setEdits(Object.fromEntries(settings.retention.map((x) => [x.key, x.value])));
                    setSchedule(settings.schedule ?? "");
                    setSettingsPw("");
                  }}>Discard changes</Button>
                </div>
              </div>
            )}

            <p className="text-xs text-muted-foreground">
              {String(ret.retained ?? 0)} artefacts retained, {String(ret.protected ?? 0)} protected and never
              deleted (identity, certificates and trust material), {String(ret.pinned ?? 0)} pinned by an
              operator. The sweep never removes the current or previous release, the newest database backup,
              or anything pinned — whatever these numbers say.
            </p>
          </>
        )}
      </div>
    </details>
  );
}

function Figure({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div>
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="text-lg font-semibold tabular-nums">{value}</div>
      {hint && <div className="text-xs text-muted-foreground">{hint}</div>}
    </div>
  );
}
