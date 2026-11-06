"use client";

import Link from "next/link";
import { useEffect, useState, useCallback } from "react";
import { api, ApiError } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { useOperatorRoles } from "@/lib/whoami-context";
import { cn } from "@/lib/utils";
import { PageShell, PageHeader, StatCard, StatGrid } from "@/components/ui/page";
import { SectionCard } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { StatusBadge } from "@/components/ui/badge";
import { RowActions } from "@/components/ui/row-actions";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Callout } from "@/components/ui/error-banner";
import { EmptyState } from "@/components/ui/empty-state";
import { Skeleton, SkeletonRows } from "@/components/ui/misc";
import { DescriptionList, Timeline } from "@/components/ui/data";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { LiveStatus, ReadOnlyNotice, refreshingClass } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { HelpList, HelpSection } from "@/components/help";
import { moduleLicensed, useCapabilities } from "@/lib/capabilities";
import { AlertTriangle, FileText, CheckCircle2, XCircle, Server, Hourglass, RefreshCw } from "lucide-react";
import { usePoll } from "@/lib/use-poll";

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
type Check = { name: string; level: "ok" | "warn" | "critical" | string; reason?: string; message: string };
type Summary = { overall: string; counts: Record<string, number>; services: ServiceHealth[]; boot: Boot | null; checks?: Check[]; generated_at: string };
type RecoveryEvent = { id: number; service: string; event: string; cause: string; action: string; backoff_level: number; result: string; duration_ms: number; actor: string; created_at: string };
type Detail = { service: ServiceHealth; recovery_events: RecoveryEvent[] };

type Tone = "ok" | "warn" | "err" | "default" | "info";

const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";
const LINK = "font-medium underline underline-offset-2";
const SPIN = "animate-spin motion-reduce:animate-none";
const POLL_SECONDS = 10;

function stateTone(s: string): Tone {
  switch (s) {
    case "healthy": return "ok";
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
const stateWord = (s: string) => STATE_WORDS[s] ?? "Not recognised";

const SERVICE_NAMES: Record<string, string> = {
  scd: "Session control",
  edged: "Admin service",
  netd: "Network control",
  portald: "Client Portal",
  acctd: "Usage measurement",
  "hotel-admin": "Admin Console",
  caddy: "Web front end",
  kea: "DHCP server",
  unbound: "DNS resolver",
  postgres: "Database",
  appliance: "Appliance",
};
const serviceName = (s: string) => (SERVICE_NAMES[s] ? `${SERVICE_NAMES[s]} (${s})` : s);

const CHECK_TITLES: Record<string, string> = { portal_https: "Client Portal HTTPS" };

const EVENT_WORDS: Record<string, { title: string; tone: "ok" | "warn" | "err" | "info" }> = {
  failure_detected: { title: "Failure detected", tone: "warn" },
  recovering: { title: "Restarting automatically", tone: "warn" },
  recovered: { title: "Recovered", tone: "ok" },
  crash_loop: { title: "Crash-loop: kept stopping", tone: "err" },
  waiting: { title: "Waiting for a prerequisite", tone: "info" },
  manual_recheck: { title: "Checked again by an operator", tone: "info" },
  manual_restart: { title: "Restarted by an operator", tone: "info" },
  boot_converged: { title: "Boot finished", tone: "ok" },
  boot_not_converged: { title: "Boot did not finish in time", tone: "err" },
};
const eventWords = (e: string) => EVENT_WORDS[e] ?? { title: "An event this console does not recognise", tone: "warn" as const };

const SELF_RESTART = new Set(["edged", "hotel-admin", "caddy"]);

const TILES: { keys: string[]; label: string; tone: "ok" | "warn" | "err" | "default" }[] = [
  { keys: ["healthy"], label: "Healthy", tone: "ok" },
  { keys: ["waiting"], label: "Waiting", tone: "default" },
  { keys: ["starting", "recovering"], label: "Starting or recovering", tone: "default" },
  { keys: ["degraded"], label: "Degraded", tone: "warn" },
  { keys: ["crash_loop", "failed"], label: "Crash-loop or failed", tone: "err" },
];

function ago(ts?: string | null, none = "Never"): string {
  if (!ts) return none;
  const s = Math.max(0, Math.floor((Date.now() - new Date(ts).getTime()) / 1000));
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}
function until(ts?: string | null): string {
  if (!ts) return "No retry planned";
  const s = Math.floor((new Date(ts).getTime() - Date.now()) / 1000);
  if (s <= 0) return "now";
  if (s < 60) return `in ${s}s`;
  return `in ${Math.round(s / 60)} min`;
}
function windowWords(secs: number): string {
  if (secs >= 3600 && secs % 3600 === 0) return secs === 3600 ? "the last hour" : `the last ${secs / 3600} hours`;
  if (secs >= 60 && secs % 60 === 0) return secs === 60 ? "the last minute" : `the last ${secs / 60} minutes`;
  return `the last ${secs.toLocaleString()} seconds`;
}

function actionWords(e: unknown, what: string): string {
  if (e instanceof ApiError && e.code === "not_found") return "This service is not monitored on this appliance.";
  return `The ${what} could not be completed. Check the appliance is reachable, then try again.`;
}

function restartWords(e: unknown, name: string): string {
  const dropped = !(e instanceof ApiError) || [502, 503, 504].includes(e.status);
  if (SELF_RESTART.has(name) && dropped) {
    return `The connection dropped, which is expected while ${serviceName(name)} restarts. The restart was most likely carried out: wait a few seconds, then refresh this page.`;
  }
  if (e instanceof ApiError) {
    if (e.code === "reauth_required") return "Your password was not accepted. Nothing was restarted.";
    if (e.code === "reason_required") return "Enter why you are restarting it. Nothing was restarted.";
    if (e.code === "not_found") return "This service is not monitored on this appliance. Nothing was restarted.";
    if (e.code === "restart_failed") {
      const detail = typeof e.message === "string" && e.message !== e.code ? e.message.trim() : "";
      return `The service could not be restarted.${detail ? ` The system reported: ${detail}` : ""}`;
    }
  }
  return "The restart could not be confirmed. Check the service's state below before trying again.";
}

export default function HealthPage() {
  const toast = useToast();
  const hospitality = moduleLicensed(useCapabilities(), "hospitality");
  const roles = useOperatorRoles();
  const [sum, setSum] = useState<Summary | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [actionErr, setActionErr] = useState<string | null>(null);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [sel, setSel] = useState<string | null>(null);
  const [detail, setDetail] = useState<Detail | null>(null);
  const [detailFailed, setDetailFailed] = useState(false);
  const [logs, setLogs] = useState<string[] | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const writable = roles ? canWrite("diagnostics", roles) : false;

  const load = useCallback(async () => {
    setRefreshing(true);
    try {
      setSum(await api.get<Summary>("/diagnostics/services"));
      setLoadFailed(false);
      setUpdatedAt(Date.now());
    } catch {
      setLoadFailed(true);
    } finally {
      setRefreshing(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);
  usePoll(() => load(), POLL_SECONDS * 1000);

  const [restarting, setRestarting] = useState<string | null>(null);
  const [restartErr, setRestartErr] = useState<string | null>(null);

  async function fetchDetail(name: string) {
    setDetailFailed(false);
    try { setDetail(await api.get<Detail>(`/diagnostics/services/${name}`)); }
    catch { setDetail(null); setDetailFailed(true); }
  }
  async function openDetail(name: string) {
    setSel(name); setLogs(null); setDetail(null);
    await fetchDetail(name);
  }
  function closeDetail() { setSel(null); setDetail(null); setLogs(null); setDetailFailed(false); }

  async function recheck(name: string) {
    setBusy("recheck:" + name);
    setActionErr(null);
    try {
      await api.post(`/diagnostics/services/${name}/recheck`);
      toast.success(`${serviceName(name)} was checked again`);
      await load();
      if (sel === name) await fetchDetail(name);
    }
    catch (e) { setActionErr(actionWords(e, "recheck")); }
    finally { setBusy(null); }
  }
  async function viewLogs(name: string) {
    setBusy("logs:" + name);
    setActionErr(null);
    try { const r = await api.get<{ lines: string[] }>(`/diagnostics/services/${name}/logs`); setLogs(r.lines || []); }
    catch (e) { setActionErr(actionWords(e, "log request")); }
    finally { setBusy(null); }
  }
  async function restart({ reason, password }: { reason: string; password: string }) {
    if (!restarting) return;
    const name = restarting;
    setBusy("restart:" + name);
    setRestartErr(null);
    try {
      await api.post(`/diagnostics/services/${name}/restart`, { reason, password });
      setRestarting(null);
      toast.success(`Restart of ${serviceName(name)} requested`, "It is recorded in Activity against your account.");
      await load();
    } catch (e) {
      setRestartErr(restartWords(e, name));
    } finally { setBusy(null); }
  }

  const c = sum?.counts || {};
  const services = sum?.services || [];
  const loading = sum === null && !loadFailed;
  const failedFirst = sum === null && loadFailed;

  return (
    <PageShell width="wide">
      <PageHeader
        title="Diagnostics"
        description="Whether each service on this appliance is running."
        help={
          <>
            <HelpSection title="What this page shows">
              <p>
                Every monitored service on the appliance, refreshed every {POLL_SECONDS} seconds. If a refresh fails, the
                last answer stays on screen and the status beside the title says so.
              </p>
            </HelpSection>
            <HelpSection title="Service states">
              <HelpList
                items={[
                  <><strong>Healthy</strong> — running and passing its health check.</>,
                  <><strong>Waiting</strong> — intentionally idle until its prerequisite exists (for example DHCP before any client network is configured). Not a fault.</>,
                  <><strong>Starting / Recovering</strong> — coming up, or being restarted automatically.</>,
                  <><strong>Degraded</strong> — running, but a check or a dependency is failing.</>,
                  <><strong>Crash-loop / Failed</strong> — keeps stopping, or could not be recovered automatically.</>,
                ]}
              />
            </HelpSection>
            <HelpSection title="Actions">
              <HelpList
                items={[
                  <><strong>Logs</strong> — recent log lines, sanitised by the appliance (secrets and client details removed). They need a role that can change diagnostics, because log lines can still name a guest&rsquo;s room.</>,
                  <><strong>Recheck</strong> — runs the service&rsquo;s health check again now.</>,
                  <><strong>Restart</strong> — stops and starts the service. It asks for a reason and your password, says what it will interrupt, and is recorded against your account.</>,
                ]}
              />
              <p>Select a service for its details, recent logs and recovery history.</p>
            </HelpSection>
          </>
        }
        meta={sum && (
          <StatusBadge tone={overallTone(sum.overall)}>
            Appliance: {stateWord(sum.overall)}
          </StatusBadge>
        )}
        actions={
          sum ? (
            <LiveStatus
              updatedAt={updatedAt}
              refreshing={refreshing}
              intervalSeconds={POLL_SECONDS}
              error={loadFailed}
              onRefresh={() => void load()}
            />
          ) : undefined
        }
      />

      {roles !== null && !writable && (
        <ReadOnlyNotice>Your role can view diagnostics, but not read service logs, recheck or restart services.</ReadOnlyNotice>
      )}

      {actionErr && (
        <div className={FADE}>
          <Callout tone="danger">{actionErr}</Callout>
        </div>
      )}

      {sum?.boot && !sum.boot.converged && (
        <div className={FADE}>
          <Callout
            tone={sum.boot.alert_open ? "danger" : "warning"}
            icon={<Hourglass />}
            title="The appliance is still starting after boot"
          >
            Waiting on: {sum.boot.pending?.length ? sum.boot.pending.map(serviceName).join(", ") : "nothing reported"}.
            {sum.boot.alert_open && <> This has taken longer than expected. Check the waiting services below.</>}
          </Callout>
        </div>
      )}

      {(sum?.checks ?? []).filter((x) => x.level !== "ok").map((x) => (
        <div key={x.name} className={FADE}>
          <Callout
            tone={x.level === "critical" ? "danger" : "warning"}
            title={CHECK_TITLES[x.name] ?? "A check this console does not recognise"}
          >
            {x.message}{" "}
            {x.name === "portal_https" && <Link className={LINK} href="/portal-https">Open Portal HTTPS</Link>}
          </Callout>
        </div>
      ))}

      {!failedFirst && (
        <StatGrid cols={5} className={cn(refreshing && sum && refreshingClass)}>
          {loading
            ? TILES.map((t) => <Skeleton key={t.label} className="h-[4.75rem] rounded-lg" />)
            : TILES.map((t) => {
                const n = t.keys.reduce((a, k) => a + (c[k] ?? 0), 0);
                return <StatCard key={t.label} label={t.label} value={n.toLocaleString()} tone={n > 0 ? t.tone : "default"} />;
              })}
        </StatGrid>
      )}

      <SectionCard flush title="Services" description="Select a service for its details, logs and recovery history.">
        <div className={cn(refreshing && sum && refreshingClass)}>
          {failedFirst ? (
            <div role="alert" className={FADE}>
              <EmptyState
                size="sm"
                icon={<AlertTriangle />}
                title="The services could not be loaded"
                hint="Check the appliance is reachable, then try again. Nothing has been changed."
                action={
                  <Button size="sm" variant="secondary" onClick={() => void load()} disabled={refreshing}>
                    <RefreshCw className={cn(refreshing && SPIN)} /> Try again
                  </Button>
                }
              />
            </div>
          ) : loading ? (
            <SkeletonRows rows={6} cols={5} />
          ) : services.length === 0 ? (
            <EmptyState
              size="sm"
              icon={<Server />}
              title="No services reported"
              hint="The appliance did not report any monitored services."
            />
          ) : (
            <Table label="Services" aria-label="Services">
              <THead>
                <TR>
                  <TH>Service</TH>
                  <TH>State</TH>
                  <TH className="hidden md:table-cell">Health check</TH>
                  <TH numeric className="hidden lg:table-cell">Restarts</TH>
                  <TH className="hidden 2xl:table-cell">Backoff</TH>
                  <TH className="hidden 2xl:table-cell">Last failure</TH>
                  <TH className="hidden sm:table-cell">Last healthy</TH>
                  {writable && <TH><span className="sr-only">Actions</span></TH>}
                </TR>
              </THead>
              <TBody>
                {services.map((s) => (
                  <TR key={s.service} onClick={() => openDetail(s.service)} selected={sel === s.service}>
                    <TD nowrap className="max-w-[14rem]">
                      <div className="truncate font-medium text-foreground" title={serviceName(s.service)}>{serviceName(s.service)}</div>
                      <div className="truncate text-caption text-muted-foreground" title={s.process_state}>{s.process_state || "Not reported"}</div>
                    </TD>
                    <TD nowrap>
                      <StatusBadge tone={stateTone(s.state)}>{stateWord(s.state)}</StatusBadge>
                      {s.backoff_level > 0 && (
                        <div className="mt-1 text-caption text-warning-subtle-foreground 2xl:hidden">Retry {until(s.next_retry_at)}</div>
                      )}
                    </TD>
                    <TD className="hidden max-w-[14rem] md:table-cell">
                      <div className="flex items-start gap-1.5 text-xs text-muted-foreground" title={s.health_detail}>
                        {s.health_ok === false ? (
                          <XCircle className="mt-0.5 size-3.5 shrink-0 text-destructive" aria-label="Check failing" />
                        ) : s.health_ok ? (
                          <CheckCircle2 className="mt-0.5 size-3.5 shrink-0 text-success" aria-label="Check passing" />
                        ) : null}
                        <span className="truncate">
                          {s.degraded_dependency ? <span className="text-warning-subtle-foreground">Depends on {serviceName(s.degraded_dependency)}. </span> : ""}
                          {s.health_detail || "Not reported"}
                        </span>
                      </div>
                    </TD>
                    <TD numeric className="hidden text-xs lg:table-cell">
                      {s.restart_count.toLocaleString()}
                      <div className="text-caption text-muted-foreground">{s.restarts_in_window.toLocaleString()} in {windowWords(s.restart_window_secs)}</div>
                      {s.consecutive_failures > 0 && (
                        <div className="text-caption text-destructive">{s.consecutive_failures.toLocaleString()} failures in a row</div>
                      )}
                    </TD>
                    <TD nowrap className="hidden text-xs 2xl:table-cell">
                      {s.backoff_level > 0
                        ? <span className="text-warning-subtle-foreground">Level {s.backoff_level} · retry {until(s.next_retry_at)}</span>
                        : <span className="text-muted-foreground">None</span>}
                    </TD>
                    <TD className="hidden max-w-[14rem] text-xs text-muted-foreground 2xl:table-cell" title={s.last_failure_reason}>
                      {s.last_failure_reason
                        ? <><div className="truncate">{s.last_failure_reason}</div><div className="text-caption">{ago(s.last_failure_at)}</div></>
                        : "None"}
                    </TD>
                    <TD nowrap muted className="hidden text-xs sm:table-cell">
                      {s.state === "healthy" ? ago(s.last_healthy_at) : (s.last_recovery_at ? `Recovered ${ago(s.last_recovery_at)}` : "Never")}
                    </TD>
                    {writable && (
                      <TD className="w-0">
                        <RowActions
                          subject={serviceName(s.service)}
                          primary={{ label: "Recheck", disabled: busy === "recheck:" + s.service, onSelect: () => void recheck(s.service) }}
                          items={[
                            {
                              label: "Logs",
                              icon: <FileText />,
                              disabled: busy === "logs:" + s.service,
                              onSelect: () => { void openDetail(s.service); void viewLogs(s.service); },
                            },
                            {
                              label: "Restart…",
                              danger: true,
                              disabled: busy === "restart:" + s.service,
                              onSelect: () => { setRestartErr(null); setRestarting(s.service); },
                            },
                          ]}
                        />
                      </TD>
                    )}
                  </TR>
                ))}
              </TBody>
            </Table>
          )}
        </div>
      </SectionCard>

      <DetailsDrawer
        open={sel !== null}
        onOpenChange={(v) => !v && closeDetail()}
        width="lg"
        eyebrow="Service"
        title={sel ? serviceName(sel) : "Service"}
        badges={detail ? <StatusBadge tone={stateTone(detail.service.state)}>{stateWord(detail.service.state)}</StatusBadge> : undefined}
        loading={sel !== null && !detail && !detailFailed}
      >
        {detailFailed && sel && (
          <div role="alert" className={FADE}>
            <EmptyState
              size="sm"
              icon={<AlertTriangle />}
              title="The details could not be loaded"
              hint="Check the appliance is reachable, then try again."
              action={
                <Button size="sm" variant="secondary" onClick={() => void fetchDetail(sel)}>
                  <RefreshCw /> Try again
                </Button>
              }
            />
          </div>
        )}
        {detail && (
          <div className="space-y-6">
            <DetailsSection title="Details">
              <DescriptionList
                items={[
                  { label: "Process", value: detail.service.process_state || "Not reported" },
                  { label: "Restarts (lifetime)", value: detail.service.restart_count.toLocaleString() },
                  { label: "Restarts recently", value: `${detail.service.restarts_in_window.toLocaleString()} in ${windowWords(detail.service.restart_window_secs)}` },
                  { label: "Failures in a row", value: detail.service.consecutive_failures.toLocaleString() },
                  { label: "Backoff", value: detail.service.backoff_level > 0 ? `Level ${detail.service.backoff_level} (about ${Math.round(detail.service.backoff_ms / 1000)}s)` : "None" },
                  { label: "Next retry", value: until(detail.service.next_retry_at) },
                  { label: "First failure", value: ago(detail.service.first_failure_at) },
                  { label: "Last failure", value: ago(detail.service.last_failure_at) },
                  { label: "Last recovery", value: ago(detail.service.last_recovery_at) },
                  { label: "Last healthy", value: ago(detail.service.last_healthy_at) },
                  { label: "Exit", value: detail.service.last_exit_signal || (detail.service.last_exit_code != null ? `Exit code ${detail.service.last_exit_code}` : "Not reported") },
                  { label: "Dependency", value: detail.service.degraded_dependency ? serviceName(detail.service.degraded_dependency) : "None" },
                ]}
              />
              {detail.service.last_failure_reason && (
                <Callout tone="warning" title="Last failure">{detail.service.last_failure_reason}</Callout>
              )}
            </DetailsSection>

            {writable && (
              <DetailsSection
                title="Recent logs"
                description="Sanitised: secrets and client details are removed by the appliance."
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
                  <pre className={cn("max-h-72 overflow-auto rounded-md border border-border bg-surface p-3 font-mono text-2xs leading-relaxed text-foreground", FADE)}>
                    {logs.join("\n") || "No log lines."}
                  </pre>
                ) : busy === "logs:" + sel ? (
                  <Skeleton className="h-24 w-full" />
                ) : null}
              </DetailsSection>
            )}

            <DetailsSection title="Recovery history">
              <Timeline
                emptyLabel="No events recorded."
                items={(detail.recovery_events || []).map((e) => {
                  const w = eventWords(e.event);
                  const byOperator = !!e.actor && e.actor !== "system";
                  return {
                    key: String(e.id),
                    title: w.title,
                    when: ago(e.created_at, "Not recorded"),
                    tone: w.tone,
                    body: (
                      <>
                        {e.cause}
                        {e.result?.startsWith("failed") ? `${e.cause ? " " : ""}It did not succeed.` : ""}
                        {byOperator && (
                          <>
                            {e.cause || e.result?.startsWith("failed") ? " · " : ""}by an operator (see <Link className={LINK} href="/audit">Activity</Link>)
                          </>
                        )}
                      </>
                    ),
                  };
                })}
              />
            </DetailsSection>
          </div>
        )}
      </DetailsDrawer>

      <ConfirmDialog
        open={restarting !== null}
        onOpenChange={(v) => { if (!v) { setRestarting(null); setRestartErr(null); } }}
        title={restarting ? `Restart ${serviceName(restarting)}?` : "Restart service"}
        description={restarting
          ? (hospitality ? RESTART_IMPACT_HOSPITALITY[restarting] : undefined) ?? RESTART_IMPACT[restarting] ?? GENERIC_RESTART_IMPACT
          : undefined}
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

const RESTART_IMPACT: Record<string, string> = {
  scd: "Every client currently online is disconnected and has to reconnect. Enforcement of speed and data limits stops until it comes back.",
  edged: "This admin interface goes away for a few seconds and you may have to sign in again. Clients are not affected.",
  netd: "Network configuration changes cannot be applied while it is down. Clients already online stay online.",
  portald: "The client sign-in page stops loading, so nobody new can sign in. Clients already online stay online.",
  acctd: "Usage measurement pauses, so data and time allowances stop being counted for a few seconds.",
  "hotel-admin": "This admin interface reloads. Clients are not affected.",
  caddy: "Both the admin interface and the Client Portal are briefly unreachable. Clients already online stay online.",
  kea: "New devices cannot get an IP address until it returns, so new clients cannot connect. Existing devices keep their lease.",
  unbound: "Name lookups stop for clients, which looks to them like the internet is down, until it returns.",
  postgres: "Everything stops: client sign-in and this admin both depend on the database.",
};

const RESTART_IMPACT_HOSPITALITY: Record<string, string> = {
  postgres: "Everything stops: client sign-in, this admin and the PMS connection all depend on the database.",
};
const GENERIC_RESTART_IMPACT =
  "The service will be stopped and started again. Anything depending on it is interrupted until it returns.";
