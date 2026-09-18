"use client";

// BACKUPS — what the appliance actually has, and what an operator can actually do about it.
//
// This screen used to list public.backup_records and nothing else. Nothing has ever written a row to that
// table on this appliance, so it said "No backups yet" forever — while the appliance was retaining hundreds
// of megabytes of deploy and rollback artefacts and running a daily retention sweep that already knew its
// disk pressure, its protected paths and what it had reclaimed. All of that was real and none of it was
// reachable from here.
//
// Nothing below is invented. Retention figures come from the sweep's own status document, the schedule from
// systemd, the artefact list from the directory the sweep manages, and the database backups from dumps this
// screen can now take. Where a fact cannot be read, it says so rather than showing a comfortable default.

import { useCallback, useEffect, useState } from "react";
import {
  api, ListResp, BackupHealth, BackupArtifact, BackupVerifyResult, BackupSettings, Whoami,
} from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { canWrite } from "@/lib/roles";
import { formatBytes, formatDate, errMsg } from "@/lib/utils";
import { Archive, Database, HardDrive, ShieldCheck, Download, Clock } from "lucide-react";

function statusTone(s: string): "ok" | "err" | "info" | "default" {
  switch (s) {
    case "ok": return "ok";
    case "failed": return "err";
    case "running": return "info";
    default: return "default";
  }
}

/** systemd reports times as microseconds since the epoch, or an empty/zero marker when there is none. */
function fromSystemd(v?: string): string | null {
  if (!v) return null;
  const n = Number(v);
  if (!Number.isFinite(n) || n <= 0) return null;
  return new Date(n / 1000).toLocaleString();
}

export default function BackupsPage() {
  const [health, setHealth] = useState<BackupHealth | null>(null);
  const [artifacts, setArtifacts] = useState<BackupArtifact[]>([]);
  const [roles, setRoles] = useState<string[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [password, setPassword] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [verifying, setVerifying] = useState<string | null>(null);
  const [verdict, setVerdict] = useState<Record<string, BackupVerifyResult>>({});
  const [settings, setSettings] = useState<BackupSettings | null>(null);
  const [edits, setEdits] = useState<Record<string, number>>({});
  const [schedule, setSchedule] = useState("");
  const [settingsPw, setSettingsPw] = useState("");
  const [savingSettings, setSavingSettings] = useState(false);

  const writable = canWrite("backups", roles);

  const load = useCallback(async () => {
    try {
      const [h, arts, st] = await Promise.all([
        api.get<BackupHealth>("/backups/health"),
        api.get<ListResp<BackupArtifact>>("/backups/artifacts"),
        api.get<BackupSettings>("/backups/settings"),
      ]);
      setHealth(h);
      setSettings(st);
      setEdits(Object.fromEntries(st.retention.map((x) => [x.key, x.value])));
      setSchedule(st.schedule ?? "");
      setArtifacts(arts.data ?? []);
    } catch (e) {
      setErr(errMsg(e));
    }
  }, []);

  useEffect(() => {
    load();
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => {});
  }, [load]);

  async function takeBackup() {
    setBusy(true); setErr(null); setMsg(null);
    try {
      const r = await api.post<{ name: string; size_bytes: number }>("/backups/run", { password });
      setMsg(`Backup taken: ${r.name} (${formatBytes(r.size_bytes)}).`);
      setPassword(""); setConfirming(false);
      await load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(false); }
  }

  async function verify(name: string) {
    setVerifying(name); setErr(null);
    try {
      const v = await api.post<BackupVerifyResult>(`/backups/artifacts/${encodeURIComponent(name)}/verify`, {});
      setVerdict((m) => ({ ...m, [name]: v }));
    } catch (e) { setErr(errMsg(e)); }
    finally { setVerifying(null); }
  }

  async function saveSettings() {
    setSavingSettings(true); setErr(null); setMsg(null);
    try {
      const r = await api.put<BackupSettings>("/backups/settings", {
        retention: edits, schedule, password: settingsPw,
      });
      setSettings(r);
      setSchedule(r.schedule ?? "");
      setSettingsPw("");
      setMsg("Retention policy saved. The nightly sweep uses it from its next run.");
      await load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setSavingSettings(false); }
  }

  const settingsDirty =
    !!settings &&
    (settings.retention.some((x) => edits[x.key] !== x.value) || schedule !== (settings.schedule ?? ""));

  const ret = health?.retention ?? {};
  const diskPct = typeof ret.disk_pct === "number" ? ret.disk_pct : null;
  const diskTone = diskPct == null ? "default" : diskPct >= (ret.disk_crit ?? 90) ? "err" : diskPct >= (ret.disk_warn ?? 80) ? "warn" : "ok";

  return (
    <div className="mx-auto w-full max-w-7xl space-y-5">
      <div className="mb-4">
        <div className="text-2xs font-semibold uppercase tracking-widest text-muted-foreground">System</div>
        <h1 className="flex items-center gap-2 text-xl font-semibold tracking-tight sm:text-2xl">
          <Archive className="h-5 w-5" /> Backups
        </h1>
      </div>

      {err && <div role="alert" className="text-sm text-destructive">{err}</div>}
      {msg && <div role="status" className="text-sm text-success-subtle-foreground">{msg}</div>}

      {/* ---- HEALTH: is this appliance actually protected right now? ---- */}
      <div className="grid gap-4 md:grid-cols-3">
        <Card>
          <CardHeader><CardTitle className="flex items-center gap-2"><Database className="h-4 w-4" /> Database backups</CardTitle></CardHeader>
          <CardBody>
            {health == null ? (
              <p className="text-sm text-muted">Checking…</p>
            ) : health.database_backups === 0 ? (
              <>
                {/* Stated, not implied. This appliance has never had a database backup, and an operator
                    should learn that here rather than discover it when they need one. */}
                <Badge tone="warn">None yet</Badge>
                <p className="mt-2 text-sm text-muted">
                  No database backup has ever been taken on this appliance. Deployment rollback artefacts are
                  retained, but they do not contain guest, stay or configuration data.
                </p>
              </>
            ) : (
              <>
                <Badge tone="ok">{health.database_backups} available</Badge>
                <p className="mt-2 text-sm text-muted">
                  Newest {health.newest_database_backup ? formatDate(health.newest_database_backup) : "—"}
                </p>
              </>
            )}
          </CardBody>
        </Card>

        <Card>
          <CardHeader><CardTitle className="flex items-center gap-2"><HardDrive className="h-4 w-4" /> Disk</CardTitle></CardHeader>
          <CardBody>
            {health?.retention_readable ? (
              <>
                <Badge tone={diskTone as any}>{diskPct}% used</Badge>
                <p className="mt-2 text-sm text-muted">
                  Warns at {ret.disk_warn ?? "—"}%, critical at {ret.disk_crit ?? "—"}%. Last sweep reclaimed{" "}
                  {formatBytes(Number(ret.reclaimed_kb ?? 0) * 1024)}.
                </p>
              </>
            ) : (
              <p className="text-sm text-muted">{health?.retention_error ?? "Checking…"}</p>
            )}
          </CardBody>
        </Card>

        <Card>
          <CardHeader><CardTitle className="flex items-center gap-2"><Clock className="h-4 w-4" /> Retention sweep</CardTitle></CardHeader>
          <CardBody>
            {health == null ? (
              <p className="text-sm text-muted">Checking…</p>
            ) : (
              <>
                <Badge tone={health.timer_active ? "ok" : "warn"}>
                  {health.timer_readable ? (health.timer_active ? "Scheduled" : "Not scheduled") : "Unknown"}
                </Badge>
                <p className="mt-2 text-sm text-muted">
                  {fromSystemd(health.timer_last_run) ? <>Last run {fromSystemd(health.timer_last_run)}. </> : null}
                  {fromSystemd(health.timer_next_run) ? <>Next {fromSystemd(health.timer_next_run)}.</> : null}
                </p>
                {health.retention_readable && Number(ret.failures ?? 0) > 0 && (
                  <p role="alert" className="mt-2 text-sm text-destructive">
                    {ret.failures} failure(s) on the last sweep: {String(ret.failure_detail || "no detail")}
                  </p>
                )}
              </>
            )}
          </CardBody>
        </Card>
      </div>

      {/* ---- RETENTION POLICY: shown as applied, and editable (§0C) ---- */}
      {settings && (
        <Card>
          <CardHeader><CardTitle>Retention and schedule</CardTitle></CardHeader>
          <CardBody className="space-y-4">
            <p className="text-sm text-muted">
              What the nightly sweep keeps, and when it runs.{" "}
              {settings.config_present
                ? "These values are stored on the appliance."
                : "No policy has been saved yet, so the built-in defaults below are in force."}
            </p>

            <div className="grid gap-4 sm:grid-cols-2">
              {settings.retention.map((sIt) => (
                <label key={sIt.key} className="block text-sm">
                  {sIt.label} <span className="text-muted">({sIt.unit})</span>
                  <Input
                    type="number"
                    min={sIt.min}
                    max={sIt.max}
                    value={edits[sIt.key] ?? sIt.value}
                    onChange={(e) => setEdits((m) => ({ ...m, [sIt.key]: Number(e.target.value) }))}
                  />
                  <span className="mt-1 block text-xs text-muted">
                    {sIt.explains} Allowed {sIt.min}–{sIt.max}; default {sIt.default}.
                  </span>
                </label>
              ))}
              <label className="block text-sm">
                Nightly sweep runs at
                <Input
                  type="time"
                  value={schedule}
                  onChange={(e) => setSchedule(e.target.value)}
                />
                <span className="mt-1 block text-xs text-muted">
                  Appliance local time.{" "}
                  {settings.schedule_known
                    ? `Currently scheduled for ${settings.schedule}.`
                    : "The current schedule could not be read from the appliance."}
                </span>
              </label>
            </div>

            {/* The sweep is what stands between this appliance and a full disk, and retention decides how far
                back it can recover. Changing either is attributed. */}
            {settingsDirty && (
              <div className="space-y-3 border-t pt-3">
                <label className="block max-w-sm text-sm">
                  Confirm your password
                  <Input type="password" autoComplete="current-password"
                    value={settingsPw} onChange={(e) => setSettingsPw(e.target.value)} />
                </label>
                <div className="flex gap-2">
                  <Button disabled={!writable || savingSettings} onClick={saveSettings}>
                    {savingSettings ? "Saving…" : "Save retention policy"}
                  </Button>
                  <Button variant="secondary" disabled={savingSettings} onClick={() => {
                    setEdits(Object.fromEntries(settings.retention.map((x) => [x.key, x.value])));
                    setSchedule(settings.schedule ?? "");
                    setSettingsPw("");
                  }}>
                    Discard changes
                  </Button>
                </div>
              </div>
            )}

            <p className="text-xs text-muted">
              {String(ret.retained ?? 0)} artefacts retained, {String(ret.protected ?? 0)} protected and never
              deleted (identity, certificates and trust material), {String(ret.pinned ?? 0)} pinned by an
              operator. The sweep never removes the current or previous release, the newest database backup,
              or anything pinned — whatever these numbers say.
            </p>
          </CardBody>
        </Card>
      )}

      {/* ---- TAKE ONE ---- */}
      <Card>
        <CardHeader><CardTitle>Take a database backup</CardTitle></CardHeader>
        <CardBody className="space-y-3">
          <p className="text-sm text-muted">
            Produces a compressed dump of the site database — guests, stays, entitlements, configuration and
            audit history. It is written alongside the appliance&apos;s other artefacts and falls under the
            retention policy above.
          </p>
          {!confirming ? (
            <Button disabled={!writable || busy} onClick={() => { setConfirming(true); setMsg(null); }}>
              Take backup now
            </Button>
          ) : (
            <div className="space-y-3">
              {/* STEP-UP, even though a backup destroys nothing: the artefact is a complete copy of the
                  site's data and the download below will hand it to whoever asks next. */}
              <p className="text-sm">
                A backup contains all guest and stay data. Confirm your password to continue.
              </p>
              <label className="block max-w-sm text-sm">
                Confirm your password
                <Input
                  type="password"
                  autoComplete="current-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                />
              </label>
              <div className="flex gap-2">
                <Button disabled={busy} onClick={takeBackup}>{busy ? "Backing up…" : "Confirm and back up"}</Button>
                <Button variant="secondary" disabled={busy} onClick={() => { setConfirming(false); setPassword(""); }}>
                  Cancel
                </Button>
              </div>
            </div>
          )}
          {!writable && <p className="text-sm text-muted">Your role can view backups but not create them.</p>}
        </CardBody>
      </Card>

      {/* ---- ARTEFACTS ---- */}
      <Card>
        <CardHeader><CardTitle>Stored artefacts</CardTitle></CardHeader>
        <CardBody className="p-0">
          {artifacts.length === 0 ? (
            <EmptyState title="Nothing stored yet" hint="Backups and deployment rollback sets appear here." />
          ) : (
            <Table>
              <THead>
                <TR><TH>Name</TH><TH>Kind</TH><TH>Created</TH><TH className="text-right">Size</TH><TH></TH></TR>
              </THead>
              <tbody>
                {artifacts.map((a) => {
                  const v = verdict[a.name];
                  return (
                    <TR key={a.name}>
                      <TD className="font-mono text-xs">{a.name}</TD>
                      <TD>{a.kind}</TD>
                      <TD className="text-muted">{formatDate(a.modified_at)}</TD>
                      <TD className="text-right">{formatBytes(a.size_bytes)}</TD>
                      <TD className="space-x-2 whitespace-nowrap text-right">
                        {a.kind === "database" && (
                          <Button size="sm" variant="secondary" disabled={verifying === a.name}
                            onClick={() => verify(a.name)}>
                            {verifying === a.name ? "Verifying…" : "Verify"}
                          </Button>
                        )}
                        {a.downloadable && (
                          <a
                            className="inline-flex items-center gap-1 text-sm text-brand hover:underline"
                            href={`/edge/v1/backups/artifacts/${encodeURIComponent(a.name)}/download`}
                          >
                            <Download className="h-3.5 w-3.5" /> Download
                          </a>
                        )}
                        {v && (
                          <span className={"ml-2 text-xs " + (v.ok ? "text-success-subtle-foreground" : "text-destructive")}>
                            {v.ok ? `restores cleanly · ${v.tables} tables · ${v.duration}` : v.detail}
                          </span>
                        )}
                      </TD>
                    </TR>
                  );
                })}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      {/* ---- RESTORE ---- */}
      <Card>
        <CardHeader><CardTitle className="flex items-center gap-2"><ShieldCheck className="h-4 w-4" /> Restoring</CardTitle></CardHeader>
        <CardBody className="space-y-2 text-sm">
          {/* WHY THERE IS NO RESTORE BUTTON. A live restore replaces the running site's data and cannot be
              undone by clicking again; it is not something to offer beside a download link. Verification
              answers the question an operator actually has -- "would this work?" -- and answers it safely. */}
          <p>
            <strong>Verify</strong> above loads a backup into a temporary database and reports whether it
            restores cleanly. Nothing about the running site is read, written or locked.
          </p>
          <p className="text-muted">
            A real restore replaces live guest, stay and configuration data and cannot be undone. It is
            performed on the appliance, with the site out of service, against a backup that has been verified
            first — deliberately not a button on this page.
          </p>
        </CardBody>
      </Card>

      {/* THE LEDGER CARD THAT WAS HERE IS GONE, and that is the fix rather than a regression.
          public.backup_records exists and this screen used to list it, but neither service role can write to
          it -- svc_edged holds SELECT only and svc_scd holds nothing -- so nothing has ever inserted a row and
          the card read "No backups yet" forever. Granting INSERT is a migration this mission does not
          authorise. The artefacts above are the real history, and who took each one is in the audit log. */}
    </div>
  );
}
