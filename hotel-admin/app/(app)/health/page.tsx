"use client";

// DIAGNOSTICS — is each service on this appliance running, and what can be done about one that is not.
//
// The page polls every 10 seconds. It dims rather than blanks while it refreshes, and a failed refresh keeps the
// last answer on screen (LiveStatus says so) instead of emptying the table under the operator.
//
// Actions follow the server's permissions exactly: reading logs is a read, so every role that can open this
// page can read them; Recheck and Restart are writes, so they are only offered to a role that may use them.
// Restart additionally requires a reason and password confirmation, which the server enforces.

import { useEffect, useState, useCallback } from "react";
import { api, ApiError, Whoami } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { cn } from "@/lib/utils";
import { PageShell, PageHeader, StatCard } from "@/components/ui/page";
import { Card, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { EmptyState } from "@/components/ui/empty-state";
import { Skeleton, SkeletonRows } from "@/components/ui/misc";
import { KeyValueGrid, Timeline } from "@/components/ui/data";
import { Sheet, SheetContent, SheetHeader, SheetBody, SheetSection } from "@/components/ui/sheet";
import { LiveStatus, ReadOnlyNotice, refreshingClass } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import {
  Stethoscope, RefreshCw, RotateCw, FileText, CheckCircle2, XCircle, Server, Hourglass,
} from "lucide-react";

type ServiceHealth = {
  service: string;
  state: string;
  process_state: string;
  health_ok: boolean | null;
  health_detail: string;
  consecutive_failures: number;
  restart_count: number;
  restarts_in_window: number;
  restart_window_secs: number;
  backoff_level: number;
  backoff_ms: number;
  next_retry_at: string | null;
  first_failure_at: string | null;
  last_failure_at: string | null;
  last_failure_reason: string;
  last_exit_code: number | null;
  last_exit_signal: string;
  last_healthy_at: string | null;
  last_recovery_at: string | null;
  time_since_healthy_s: number | null;
  degraded_dependency: string;
  critical: boolean;
  updated_at: string;
};
type Boot = { converged: boolean; alert_open: boolean; pending: string[]; boot_at?: string; converged_at?: string; deadline_at?: string };
type Summary = { overall: string; counts: Record<string, number>; services: ServiceHealth[]; boot: Boot | null; generated_at: string };
type RecoveryEvent = { id: number; service: string; event: string; cause: string; action: string; backoff_level: number; result: string; duration_ms: number; actor: string; created_at: string };

type Tone = "ok" | "warn" | "err" | "default" | "info";

function stateTone(s: string): Tone {
  switch (s) {
    case "healthy": return "ok";
    // "waiting" is a correct state, not a fault: the service is intentionally idle because its
    // configuration prerequisite does not exist yet (Kea before guest networking is configured).
    case "waiting": return "default";
    case "recovering": case "starting": return "info";
    case "degraded": return "warn";
    case "crash_loop": case "failed": return "err";
    default: return "default";
  }
}
function overallTone(s: string): Tone { return s === "healthy" ? "ok" : s === "recovering" ? "info" : "err"; }

const STATE_WORDS: Record<string, string> = {
  healthy: "Healthy", waiting: "Waiting", degraded: "Degraded", recovering: "Recovering",
  crash_loop: "Crash-loop", failed: "Failed", starting: "Starting",
};
const stateWord = (s: string) => STATE_WORDS[s] ?? (s ? s.replace(/_/g, " ") : "Unknown");

// The count tiles, in the order the handoff names them. Tone is only raised when the count is non-zero, so a
// row of zeros reads as quiet rather than as seven alarms.
const TILES: { key: string; label: string; tone: "ok" | "warn" | "err" | "info" | "default" }[] = [
  { key: "healthy", label: "Healthy", tone: "ok" },
  { key: "waiting", label: "Waiting", tone: "default" },
  { key: "degraded", label: "Degraded", tone: "warn" },
  { key: "recovering", label: "Recovering", tone: "info" },
  { key: "crash_loop", label: "Crash-loop", tone: "err" },
  { key: "failed", label: "Failed", tone: "err" },
  { key: "starting", label: "Starting", tone: "info" },
];

function ago(ts?: string | null): string {
  if (!ts) return "—";
  const s = Math.max(0, Math.floor((Date.now() - new Date(ts).getTime()) / 1000));
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}
function until(ts?: string | null): string {
  if (!ts) return "—";
  const s = Math.floor((new Date(ts).getTime() - Date.now()) / 1000);
  return s <= 0 ? "now" : `in ${s}s`;
}

const POLL_SECONDS = 10;

export default function HealthPage() {
  const toast = useToast();
  const [sum, setSum] = useState<Summary | null>(null);
  const [me, setMe] = useState<Whoami | null>(null);
  const [loadErr, setLoadErr] = useState<string | null>(null);
  const [actionErr, setActionErr] = useState<string | null>(null);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [sel, setSel] = useState<string | null>(null);
  const [detail, setDetail] = useState<{ service: ServiceHealth; recovery_events: RecoveryEvent[] } | null>(null);
  const [logs, setLogs] = useState<string[] | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const writable = me ? canWrite("diagnostics", me.roles) : false;

  const load = useCallback(async () => {
    setRefreshing(true);
    try {
      setSum(await api.get<Summary>("/diagnostics/services"));
      setLoadErr(null);
      setUpdatedAt(Date.now());
    } catch (e) {
      setLoadErr(e instanceof ApiError ? e.message : "failed to load health");
    } finally {
      setRefreshing(false);
    }
  }, []);

  useEffect(() => { api.get<Whoami>("/auth/whoami").then(setMe).catch(() => {}); }, []);
  useEffect(() => { load(); const t = setInterval(load, POLL_SECONDS * 1000); return () => clearInterval(t); }, [load]);

  // The service awaiting a restart confirmation.
  const [restarting, setRestarting] = useState<string | null>(null);
  const [restartErr, setRestartErr] = useState<string | null>(null);

  async function openDetail(name: string) {
    setSel(name); setLogs(null);
    try { setDetail(await api.get(`/diagnostics/services/${name}`)); } catch { setDetail(null); }
  }
  function closeDetail() { setSel(null); setDetail(null); setLogs(null); }

  async function recheck(name: string) {
    setBusy("recheck:" + name);
    setActionErr(null);
    try {
      await api.post(`/diagnostics/services/${name}/recheck`);
      toast.success(`${name} was checked again`);
      await load();
      if (sel === name) await openDetail(name);
    }
    catch (e) { setActionErr(e instanceof ApiError ? e.message : "recheck failed"); }
    finally { setBusy(null); }
  }
  async function viewLogs(name: string) {
    setBusy("logs:" + name);
    setActionErr(null);
    try { const r = await api.get<{ lines: string[] }>(`/diagnostics/services/${name}/logs`); setLogs(r.lines || []); }
    catch (e) { setActionErr(e instanceof ApiError ? e.message : "logs failed"); }
    finally { setBusy(null); }
  }
  // RESTARTING A SERVICE TOOK A PASSWORD THROUGH window.prompt().
  //
  // `prompt()` rendered a plain text input, so every character of an operator's own admin password was displayed
  // on a front-desk screen — and the first prompt asked for a reason with no indication of what restarting that
  // particular service would interrupt. The confirmation now names the impact, and the password is masked.
  async function restart({ reason, password }: { reason: string; password: string }) {
    if (!restarting) return;
    const name = restarting;
    setBusy("restart:" + name);
    setRestartErr(null);
    try {
      await api.post(`/diagnostics/services/${name}/restart`, { reason, password });
      setRestarting(null);
      toast.success(`Restart of ${name} requested`, "It is recorded in Activity against your account.");
      await load();
    } catch (e) {
      setRestartErr(e instanceof ApiError ? (e.body?.error === "reauth_required" ? "Password confirmation failed." : e.message) : "restart failed");
    } finally { setBusy(null); }
  }

  const c = sum?.counts || {};
  const services = sum?.services || [];
  const loading = sum === null && !loadErr;

  return (
    <PageShell width="wide">
      <PageHeader
        icon={<Stethoscope />}
        eyebrow="System"
        title="Diagnostics"
        description="Whether each service on this appliance is running. Recheck a service, read its recent logs, or restart it."
        actions={
          <>
            {sum && (
              <Badge tone={overallTone(sum.overall)} dot>
                Appliance: {stateWord(sum.overall)}
              </Badge>
            )}
            <LiveStatus
              updatedAt={updatedAt}
              refreshing={refreshing}
              intervalSeconds={POLL_SECONDS}
              error={!!loadErr && sum !== null}
              onRefresh={() => void load()}
            />
          </>
        }
      />

      {me && !writable && (
        <ReadOnlyNotice>Your role can view diagnostics and read logs, but not recheck or restart services.</ReadOnlyNotice>
      )}

      <ErrorBanner err={sum === null ? loadErr : null} className="mb-0" />
      <ErrorBanner err={actionErr} className="mb-0" />

      {sum?.boot && !sum.boot.converged && (
        <Callout
          tone={sum.boot.alert_open ? "danger" : "warning"}
          icon={<Hourglass />}
          title="The appliance is still starting after boot"
        >
          Waiting on: {sum.boot.pending?.join(", ") || "—"}.
          {sum.boot.alert_open && <> This has taken longer than expected — check the waiting services below.</>}
        </Callout>
      )}

      {/* Count tiles */}
      <div className={cn("grid grid-cols-2 gap-3 sm:grid-cols-4 xl:grid-cols-7", refreshing && sum && refreshingClass)}>
        {loading
          ? TILES.map((t) => <Skeleton key={t.key} className="h-[5.5rem] rounded-lg" />)
          : TILES.map((t) => {
              const n = c[t.key] ?? 0;
              return (
                <StatCard
                  key={t.key}
                  label={t.label}
                  value={n}
                  tone={n > 0 ? t.tone : "default"}
                />
              );
            })}
      </div>

      {/* Service table */}
      <Card>
        <CardHeader>
          <div className="space-y-0.5">
            <CardTitle>Services</CardTitle>
            <CardDescription>Select a service to see its details, recent logs and recovery history.</CardDescription>
          </div>
        </CardHeader>
        <div className={cn(refreshing && sum && refreshingClass)}>
          {loading ? (
            <SkeletonRows rows={6} cols={5} />
          ) : services.length === 0 ? (
            <EmptyState
              icon={<Server />}
              title={sum ? "No services reported" : "Services could not be read"}
              hint={sum ? "The appliance did not report any monitored services." : "The list appears here once the appliance answers."}
            />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Service</TH>
                  <TH>State</TH>
                  <TH className="hidden md:table-cell">Health check</TH>
                  <TH className="hidden lg:table-cell">Restarts</TH>
                  <TH className="hidden lg:table-cell">Backoff / next</TH>
                  <TH className="hidden md:table-cell">Last failure</TH>
                  <TH className="hidden sm:table-cell">Uptime</TH>
                  <TH className="text-end"><span className="sr-only">Actions</span></TH>
                </TR>
              </THead>
              <TBody>
                {services.map((s) => (
                  <TR key={s.service}>
                    <TD>
                      <button
                        type="button"
                        className="rounded-sm font-medium text-foreground hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        onClick={() => openDetail(s.service)}
                      >
                        {s.service}
                      </button>
                      <div className="text-caption text-muted-foreground">{s.process_state}</div>
                    </TD>
                    <TD><Badge tone={stateTone(s.state)}>{stateWord(s.state)}</Badge></TD>
                    <TD className="hidden max-w-[16rem] md:table-cell">
                      <div className="flex items-start gap-1.5 text-xs text-muted-foreground" title={s.health_detail}>
                        {s.health_ok === false ? (
                          <XCircle className="mt-0.5 size-3.5 shrink-0 text-destructive" aria-label="Check failing" />
                        ) : s.health_ok ? (
                          <CheckCircle2 className="mt-0.5 size-3.5 shrink-0 text-success" aria-label="Check passing" />
                        ) : null}
                        <span className="truncate">
                          {s.degraded_dependency ? <span className="text-warning-subtle-foreground">Depends on {s.degraded_dependency} — </span> : ""}
                          {s.health_detail || "—"}
                        </span>
                      </div>
                    </TD>
                    <TD className="hidden text-xs tabular lg:table-cell">
                      {s.restart_count}
                      <span className="text-muted-foreground"> ({s.restarts_in_window} in {s.restart_window_secs}s)</span>
                      {s.consecutive_failures > 0 && (
                        <div className="text-caption text-destructive">{s.consecutive_failures} failures in a row</div>
                      )}
                    </TD>
                    <TD className="hidden text-xs lg:table-cell">
                      {s.backoff_level > 0
                        ? <span className="text-warning-subtle-foreground">Level {s.backoff_level} · retry {until(s.next_retry_at)}</span>
                        : <span className="text-muted-foreground">—</span>}
                    </TD>
                    <TD className="hidden max-w-[14rem] text-xs text-muted-foreground md:table-cell" title={s.last_failure_reason}>
                      {s.last_failure_reason
                        ? <><div className="truncate">{s.last_failure_reason}</div><div className="text-caption">{ago(s.last_failure_at)}</div></>
                        : "—"}
                    </TD>
                    <TD className="hidden text-xs text-muted-foreground sm:table-cell">
                      {s.state === "healthy" ? ago(s.last_healthy_at).replace(" ago", "") : (s.last_recovery_at ? "recovered " + ago(s.last_recovery_at) : "—")}
                    </TD>
                    <TD className="text-end">
                      <div className="flex flex-wrap justify-end gap-1">
                        {writable && (
                          <Button
                            size="xs"
                            variant="ghost"
                            aria-label={`Recheck ${s.service}`}
                            disabled={busy === "recheck:" + s.service}
                            onClick={() => recheck(s.service)}
                          >
                            <RefreshCw /> <span className="hidden sm:inline">Recheck</span>
                          </Button>
                        )}
                        <Button
                          size="xs"
                          variant="ghost"
                          aria-label={`Logs for ${s.service}`}
                          disabled={busy === "logs:" + s.service}
                          onClick={() => { openDetail(s.service); viewLogs(s.service); }}
                        >
                          <FileText /> <span className="hidden sm:inline">Logs</span>
                        </Button>
                        {writable && (
                          <Button
                            size="xs"
                            variant="ghost"
                            aria-label={`Restart ${s.service}`}
                            disabled={busy === "restart:" + s.service}
                            onClick={() => { setRestartErr(null); setRestarting(s.service); }}
                          >
                            <RotateCw /> <span className="hidden sm:inline">Restart</span>
                          </Button>
                        )}
                      </div>
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          )}
        </div>
      </Card>

      {/* Detail sheet: the record, its recent logs and its recovery history. */}
      <Sheet open={sel !== null} onOpenChange={(v) => !v && closeDetail()}>
        <SheetContent width="lg">
          <SheetHeader
            icon={<Server />}
            eyebrow="Service"
            title={sel ?? "Service"}
            description="Details, recent logs and recovery history."
            badges={detail ? <Badge tone={stateTone(detail.service.state)}>{stateWord(detail.service.state)}</Badge> : undefined}
          />
          <SheetBody>
            {sel && !detail ? (
              <div className="space-y-3" aria-busy="true">
                <Skeleton className="h-4 w-1/2" />
                <Skeleton className="h-4 w-2/3" />
                <Skeleton className="h-4 w-1/3" />
              </div>
            ) : detail ? (
              <>
                <SheetSection title="Details">
                  <KeyValueGrid
                    columns={2}
                    items={[
                      { label: "Process", value: detail.service.process_state || "—" },
                      { label: "Restarts (lifetime)", value: String(detail.service.restart_count) },
                      { label: "Restarts in window", value: `${detail.service.restarts_in_window} in ${detail.service.restart_window_secs}s` },
                      { label: "Failures in a row", value: String(detail.service.consecutive_failures) },
                      { label: "Backoff", value: detail.service.backoff_level > 0 ? `Level ${detail.service.backoff_level} (about ${Math.round(detail.service.backoff_ms / 1000)}s)` : "None" },
                      { label: "Next retry", value: until(detail.service.next_retry_at) },
                      { label: "First failure", value: ago(detail.service.first_failure_at) },
                      { label: "Last failure", value: ago(detail.service.last_failure_at) },
                      { label: "Last recovery", value: ago(detail.service.last_recovery_at) },
                      { label: "Last healthy", value: ago(detail.service.last_healthy_at) },
                      { label: "Exit", value: detail.service.last_exit_signal || (detail.service.last_exit_code != null ? `code ${detail.service.last_exit_code}` : "—") },
                      { label: "Dependency", value: detail.service.degraded_dependency || "—" },
                    ]}
                  />
                  {detail.service.last_failure_reason && (
                    <Callout tone="warning" title="Last failure">{detail.service.last_failure_reason}</Callout>
                  )}
                </SheetSection>

                <SheetSection
                  title="Recent logs"
                  description="Sanitised: secrets and guest details are removed by the appliance."
                  actions={
                    <Button
                      size="xs"
                      variant="secondary"
                      disabled={busy === "logs:" + sel}
                      onClick={() => sel && viewLogs(sel)}
                    >
                      <FileText /> {logs ? "Reload" : "Load logs"}
                    </Button>
                  }
                >
                  {logs ? (
                    <pre className="max-h-72 overflow-auto rounded-md border border-border bg-surface p-3 font-mono text-[11px] leading-relaxed text-foreground">
                      {logs.join("\n") || "(no logs)"}
                    </pre>
                  ) : busy === "logs:" + sel ? (
                    <Skeleton className="h-24 w-full" />
                  ) : null}
                </SheetSection>

                <SheetSection title="Recovery history">
                  <Timeline
                    emptyLabel="No events recorded."
                    items={(detail.recovery_events || []).map((e) => ({
                      key: String(e.id),
                      title: e.event.replace(/_/g, " "),
                      when: ago(e.created_at),
                      tone: e.event.includes("recover") || e.event.includes("converged") ? "ok"
                        : e.event.includes("crash") || e.event.includes("not_converged") ? "err"
                        : e.event.includes("manual") ? "info" : "warn",
                      body: (
                        <>
                          {e.action || e.cause || e.result}
                          {e.actor && e.actor !== "system" ? ` · by ${e.actor.slice(0, 8)}` : ""}
                        </>
                      ),
                    }))}
                  />
                </SheetSection>
              </>
            ) : null}
          </SheetBody>
        </SheetContent>
      </Sheet>

      {/*
        The restart confirmation. It names the service and says what restarting it INTERRUPTS, because that is the
        only thing the operator is actually deciding — and the password is typed into a masked field.
      */}
      <ConfirmDialog
        open={restarting !== null}
        onOpenChange={(v) => { if (!v) { setRestarting(null); setRestartErr(null); } }}
        title={restarting ? `Restart ${restarting}?` : "Restart service"}
        description={restarting ? RESTART_IMPACT[restarting] ?? GENERIC_RESTART_IMPACT : undefined}
        confirmLabel="Restart now"
        confirmVariant="danger"
        busy={busy === "restart:" + restarting}
        error={restartErr}
        requireReason
        reasonLabel="Why are you restarting it?"
        reasonPlaceholder="Health check failing since 02:10"
        requirePassword
        onConfirm={restart}
      >
        <Callout tone="warning">
          This is recorded in the activity log against your account.
        </Callout>
      </ConfirmDialog>
    </PageShell>
  );
}

// WHAT A RESTART COSTS, per service. The operator is not choosing whether to restart "a process" — they are
// choosing whether to drop every guest, or to stop new devices getting an address for a few seconds. An
// unrecognised service falls back to the generic warning rather than claiming something specific.
const RESTART_IMPACT: Record<string, string> = {
  scd: "Every guest currently online is disconnected and has to reconnect. Enforcement of speed and data limits stops until it comes back.",
  edged: "This admin interface goes away for a few seconds and you may have to sign in again. Guests are not affected.",
  netd: "Network configuration changes cannot be applied while it is down. Guests already online stay online.",
  portald: "The guest sign-in page stops loading, so nobody new can sign in. Guests already online stay online.",
  acctd: "Usage measurement pauses, so data and time allowances stop being counted for a few seconds.",
  "hotel-admin": "This admin interface reloads. Guests are not affected.",
  caddy: "Both the admin interface and the guest portal are briefly unreachable. Guests already online stay online.",
  kea: "New devices cannot get an IP address until it returns, so new guests cannot connect. Existing devices keep their lease.",
  unbound: "Name lookups stop for guests, which looks to them like the internet is down, until it returns.",
  postgres: "Everything stops: guest sign-in, this admin and the PMS connection all depend on the database.",
};
const GENERIC_RESTART_IMPACT =
  "The service will be stopped and started again. Anything depending on it is interrupted until it returns.";
