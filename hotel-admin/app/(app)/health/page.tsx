"use client";

// Reading service logs needs diagnostics WRITE since journal lines can include client details; Restart also needs
// a reason and password confirmation, enforced by edged.

import Link from "next/link";
import { useEffect, useState, useCallback } from "react";
import { api, ApiError, inBackground, Whoami } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { cn } from "@/lib/utils";
import { PageShell, PageHeader, StatCard, StatGrid } from "@/components/ui/page";
import { SectionCard } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { StatusBadge } from "@/components/ui/badge";
import { RowActions } from "@/components/ui/row-actions";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { EmptyState } from "@/components/ui/empty-state";
import { Skeleton, SkeletonRows } from "@/components/ui/misc";
import { DescriptionList, Timeline } from "@/components/ui/data";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { LiveStatus, ReadOnlyNotice, refreshingClass } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { HelpList, HelpSection } from "@/components/help";
import { moduleLicensed, useCapabilities } from "@/lib/capabilities";
import { FileText, CheckCircle2, XCircle, Server, Hourglass } from "lucide-react";

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

type Tone = "ok" | "warn" | "err" | "default" | "info";

function stateTone(s: string): Tone {
  switch (s) {
    case "healthy": return "ok";
    // "waiting" is not a fault: the service is idle until its prerequisite exists.
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

const TILES: { keys: string[]; label: string; tone: "ok" | "warn" | "err" | "default" }[] = [
  { keys: ["healthy"], label: "Healthy", tone: "ok" },
  { keys: ["waiting"], label: "Waiting", tone: "default" },
  { keys: ["starting", "recovering"], label: "Starting or recovering", tone: "default" },
  { keys: ["degraded"], label: "Degraded", tone: "warn" },
  { keys: ["crash_loop", "failed"], label: "Crash-loop or failed", tone: "err" },
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
  const hospitality = moduleLicensed(useCapabilities(), "hospitality");
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
  useEffect(() => { load(); const t = setInterval(() => void inBackground(load), POLL_SECONDS * 1000); return () => clearInterval(t); }, [load]);

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
                  <><strong>Logs</strong> — recent log lines, sanitised by the appliance (secrets and client details removed). Every role that can open this page can read them.</>,
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
          <>
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
        <ReadOnlyNotice>Your role can view diagnostics, but not read service logs, recheck or restart services.</ReadOnlyNotice>
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

      {(sum?.checks ?? []).filter((c) => c.level !== "ok").map((c) => (
        <Callout
          key={c.name}
          tone={c.level === "critical" ? "danger" : "warning"}
          title={c.name === "portal_https" ? "Client Portal HTTPS" : c.name}
        >
          {c.message}{" "}
          {c.name === "portal_https" && <Link className="underline" href="/portal-https">Open Portal HTTPS</Link>}
        </Callout>
      ))}

      <StatGrid cols={5} className={cn(refreshing && sum && refreshingClass)}>
        {loading
          ? TILES.map((t) => <Skeleton key={t.label} className="h-[4.75rem] rounded-lg" />)
          : TILES.map((t) => {
              const n = t.keys.reduce((a, k) => a + (c[k] ?? 0), 0);
              return <StatCard key={t.label} label={t.label} value={n} tone={n > 0 ? t.tone : "default"} />;
            })}
      </StatGrid>

      <SectionCard flush title="Services" description="Select a service for its details, logs and recovery history.">
        <div className={cn(refreshing && sum && refreshingClass)}>
          {loading ? (
            <SkeletonRows rows={6} cols={5} />
          ) : services.length === 0 ? (
            <EmptyState
              size="sm"
              icon={<Server />}
              title={sum ? "No services reported" : "Services could not be read"}
              hint={sum ? "The appliance did not report any monitored services." : "The list appears here once the appliance answers."}
            />
          ) : (
            <Table label="Services">
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
                    <TD nowrap className="max-w-[12rem]">
                      <div className="truncate font-medium text-foreground" title={s.service}>{s.service}</div>
                      <div className="truncate text-caption text-muted-foreground" title={s.process_state}>{s.process_state}</div>
                    </TD>
                    <TD nowrap>
                      <StatusBadge tone={stateTone(s.state)}>{stateWord(s.state)}</StatusBadge>
                      {/* The Backoff column needs a 2xl screen; below it the retry sits under the state. */}
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
                          {s.degraded_dependency ? <span className="text-warning-subtle-foreground">Depends on {s.degraded_dependency} — </span> : ""}
                          {s.health_detail || "—"}
                        </span>
                      </div>
                    </TD>
                    <TD numeric className="hidden text-xs lg:table-cell">
                      {s.restart_count}
                      <div className="text-caption text-muted-foreground">{s.restarts_in_window} in {s.restart_window_secs}s</div>
                      {s.consecutive_failures > 0 && (
                        <div className="text-caption text-destructive">{s.consecutive_failures} failures in a row</div>
                      )}
                    </TD>
                    <TD nowrap className="hidden text-xs 2xl:table-cell">
                      {s.backoff_level > 0
                        ? <span className="text-warning-subtle-foreground">Level {s.backoff_level} · retry {until(s.next_retry_at)}</span>
                        : <span className="text-muted-foreground">—</span>}
                    </TD>
                    <TD className="hidden max-w-[14rem] text-xs text-muted-foreground 2xl:table-cell" title={s.last_failure_reason}>
                      {s.last_failure_reason
                        ? <><div className="truncate">{s.last_failure_reason}</div><div className="text-caption">{ago(s.last_failure_at)}</div></>
                        : "—"}
                    </TD>
                    <TD nowrap muted className="hidden text-xs sm:table-cell">
                      {s.state === "healthy" ? ago(s.last_healthy_at) : (s.last_recovery_at ? "recovered " + ago(s.last_recovery_at) : "—")}
                    </TD>
                    {writable && (
                      <TD className="w-0">
                        <RowActions
                          subject={s.service}
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
        title={sel ?? "Service"}
        badges={detail ? <StatusBadge tone={stateTone(detail.service.state)}>{stateWord(detail.service.state)}</StatusBadge> : undefined}
        loading={sel !== null && !detail}
      >
        {detail && (
          <div className="space-y-6">
            <DetailsSection title="Details">
              <DescriptionList
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
                  <pre className="max-h-72 overflow-auto rounded-md border border-border bg-surface p-3 font-mono text-2xs leading-relaxed text-foreground">
                    {logs.join("\n") || "(no logs)"}
                  </pre>
                ) : busy === "logs:" + sel ? (
                  <Skeleton className="h-24 w-full" />
                ) : null}
              </DetailsSection>
            )}

            <DetailsSection title="Recovery history">
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
            </DetailsSection>
          </div>
        )}
      </DetailsDrawer>

      <ConfirmDialog
        open={restarting !== null}
        onOpenChange={(v) => { if (!v) { setRestarting(null); setRestartErr(null); } }}
        title={restarting ? `Restart ${restarting}?` : "Restart service"}
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

// An unrecognised service falls back to the generic warning rather than claiming something specific.
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

/** Name the PMS as a casualty only where hospitality is licensed. */
const RESTART_IMPACT_HOSPITALITY: Record<string, string> = {
  postgres: "Everything stops: client sign-in, this admin and the PMS connection all depend on the database.",
};
const GENERIC_RESTART_IMPACT =
  "The service will be stopped and started again. Anything depending on it is interrupted until it returns.";
