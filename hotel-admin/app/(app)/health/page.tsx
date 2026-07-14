"use client";

import { useEffect, useState, useCallback } from "react";
import { api, ApiError, Whoami } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Activity, RefreshCw, RotateCw, FileText, X } from "lucide-react";

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

function stateTone(s: string): "ok" | "warn" | "err" | "default" | "info" {
  switch (s) {
    case "healthy": return "ok";
    case "recovering": case "starting": return "info";
    case "degraded": return "warn";
    case "crash_loop": case "failed": return "err";
    default: return "default";
  }
}
function overallTone(s: string) { return s === "healthy" ? "ok" : s === "recovering" ? "info" : "err"; }

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

export default function HealthPage() {
  const [sum, setSum] = useState<Summary | null>(null);
  const [me, setMe] = useState<Whoami | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [sel, setSel] = useState<string | null>(null);
  const [detail, setDetail] = useState<{ service: ServiceHealth; recovery_events: RecoveryEvent[] } | null>(null);
  const [logs, setLogs] = useState<string[] | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const writable = me ? canWrite("diagnostics", me.roles) : false;

  const load = useCallback(async () => {
    try { setSum(await api.get<Summary>("/diagnostics/services")); }
    catch (e) { setErr(e instanceof ApiError ? e.message : "failed to load health"); }
  }, []);

  useEffect(() => { api.get<Whoami>("/auth/whoami").then(setMe).catch(() => {}); }, []);
  useEffect(() => { load(); const t = setInterval(load, 10000); return () => clearInterval(t); }, [load]);

  async function openDetail(name: string) {
    setSel(name); setLogs(null);
    try { setDetail(await api.get(`/diagnostics/services/${name}`)); } catch { setDetail(null); }
  }
  async function recheck(name: string) {
    setBusy("recheck:" + name);
    try { await api.post(`/diagnostics/services/${name}/recheck`); await load(); if (sel === name) await openDetail(name); }
    catch (e) { setErr(e instanceof ApiError ? e.message : "recheck failed"); }
    finally { setBusy(null); }
  }
  async function viewLogs(name: string) {
    setBusy("logs:" + name);
    try { const r = await api.get<{ lines: string[] }>(`/diagnostics/services/${name}/logs`); setLogs(r.lines || []); }
    catch (e) { setErr(e instanceof ApiError ? e.message : "logs failed"); }
    finally { setBusy(null); }
  }
  async function restart(name: string) {
    const reason = window.prompt(`Restart ${name}? This is audited. Reason:`);
    if (!reason) return;
    const password = window.prompt("Confirm your password to authorize the restart:");
    if (!password) return;
    setBusy("restart:" + name);
    try {
      await api.post(`/diagnostics/services/${name}/restart`, { reason, password });
      await load();
    } catch (e) {
      setErr(e instanceof ApiError ? (e.body?.error === "reauth_required" ? "Password confirmation failed." : e.message) : "restart failed");
    } finally { setBusy(null); }
  }

  const c = sum?.counts || {};
  return (
    <div className="mx-auto max-w-6xl space-y-6 p-6">
      <div className="flex items-center justify-between">
        <h1 className="flex items-center gap-2 text-xl font-semibold"><Activity className="h-5 w-5" /> Diagnostics &amp; Service Health</h1>
        <div className="flex items-center gap-3">
          {sum && <Badge tone={overallTone(sum.overall) as any}>Appliance: {sum.overall}</Badge>}
          <Button variant="ghost" onClick={load}><RefreshCw className="h-4 w-4" /> Refresh</Button>
        </div>
      </div>

      {err && <div className="rounded border border-[#6b2128] bg-[#3a1418] p-3 text-sm text-err">{err}</div>}

      {sum?.boot && !sum.boot.converged && (
        <div className="rounded border border-[#6b4e1c] bg-[#3a2a0e] p-3 text-sm text-warn">
          <b>Appliance still converging after boot.</b> Waiting on: {sum.boot.pending?.join(", ") || "—"}.
          {sum.boot.alert_open && <> This has exceeded the expected convergence time — check the pending services below.</>}
        </div>
      )}

      {/* Summary tiles */}
      <div className="grid grid-cols-3 gap-3 sm:grid-cols-6">
        {[["healthy", "Healthy", "text-ok"], ["degraded", "Degraded", "text-warn"], ["recovering", "Recovering", "text-info"],
          ["crash_loop", "Crash-loop", "text-err"], ["failed", "Failed", "text-err"], ["starting", "Starting", "text-muted"]].map(([k, label, tone]) => (
          <div key={k} className="rounded-md border border-border bg-panel2/40 p-3">
            <div className={`text-2xl font-semibold ${tone}`}>{c[k] ?? 0}</div>
            <div className="mt-1 text-[10px] uppercase tracking-wider text-muted">{label}</div>
          </div>
        ))}
      </div>

      {/* Service table */}
      <Card>
        <CardHeader><CardTitle>Services</CardTitle></CardHeader>
        <CardBody className="p-0 overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="border-b border-border text-left text-xs uppercase tracking-wider text-muted">
              <tr>
                <th className="px-4 py-2">Service</th><th className="px-2">State</th><th className="px-2">Health check</th>
                <th className="px-2">Restarts</th><th className="px-2">Backoff / next</th><th className="px-2">Last failure</th>
                <th className="px-2">Uptime</th><th className="px-4 text-right">Actions</th>
              </tr>
            </thead>
            <tbody>
              {(sum?.services || []).map((s) => (
                <tr key={s.service} className="border-b border-border/50 hover:bg-panel2/30">
                  <td className="px-4 py-2 font-medium">
                    <button className="hover:underline" onClick={() => openDetail(s.service)}>{s.service}</button>
                    <div className="text-[10px] text-muted">{s.process_state}</div>
                  </td>
                  <td className="px-2"><Badge tone={stateTone(s.state) as any}>{s.state}</Badge></td>
                  <td className="px-2 text-xs text-muted max-w-[16rem] truncate" title={s.health_detail}>
                    {s.health_ok === false ? <span className="text-err">✗ </span> : s.health_ok ? <span className="text-ok">✓ </span> : ""}
                    {s.degraded_dependency ? <span className="text-warn">dep: {s.degraded_dependency} — </span> : ""}
                    {s.health_detail}
                  </td>
                  <td className="px-2 text-xs">{s.restart_count}<span className="text-muted"> ({s.restarts_in_window}/{s.restart_window_secs}s)</span>
                    {s.consecutive_failures > 0 && <div className="text-err text-[10px]">{s.consecutive_failures} consec.</div>}</td>
                  <td className="px-2 text-xs">{s.backoff_level > 0 ? <span className="text-warn">L{s.backoff_level} · {until(s.next_retry_at)}</span> : <span className="text-muted">—</span>}</td>
                  <td className="px-2 text-xs text-muted max-w-[14rem] truncate" title={s.last_failure_reason}>
                    {s.last_failure_reason ? <>{s.last_failure_reason}<div className="text-[10px]">{ago(s.last_failure_at)}</div></> : "—"}</td>
                  <td className="px-2 text-xs text-muted">{s.state === "healthy" ? ago(s.last_healthy_at).replace(" ago", "") : (s.last_recovery_at ? "rec " + ago(s.last_recovery_at) : "—")}</td>
                  <td className="px-4 py-2 text-right">
                    <div className="flex justify-end gap-1">
                      <Button size="sm" variant="ghost" disabled={busy === "recheck:" + s.service} onClick={() => recheck(s.service)}><RefreshCw className="h-3 w-3" /></Button>
                      <Button size="sm" variant="ghost" disabled={busy === "logs:" + s.service} onClick={() => { openDetail(s.service); viewLogs(s.service); }}><FileText className="h-3 w-3" /></Button>
                      {writable && <Button size="sm" variant="danger" disabled={busy === "restart:" + s.service} onClick={() => restart(s.service)}><RotateCw className="h-3 w-3" /></Button>}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </CardBody>
      </Card>

      {/* Detail drawer */}
      {sel && detail && (
        <Card>
          <CardHeader className="flex items-center justify-between">
            <CardTitle>{sel} — detail &amp; recovery history</CardTitle>
            <Button variant="ghost" size="sm" onClick={() => { setSel(null); setDetail(null); setLogs(null); }}><X className="h-4 w-4" /></Button>
          </CardHeader>
          <CardBody className="space-y-4">
            <div className="grid grid-cols-2 gap-x-8 gap-y-1 text-sm sm:grid-cols-3">
              <KV k="State" v={<Badge tone={stateTone(detail.service.state) as any}>{detail.service.state}</Badge>} />
              <KV k="Process" v={detail.service.process_state} />
              <KV k="Restarts (lifetime)" v={String(detail.service.restart_count)} />
              <KV k="Restarts in window" v={`${detail.service.restarts_in_window} / ${detail.service.restart_window_secs}s`} />
              <KV k="Consecutive failures" v={String(detail.service.consecutive_failures)} />
              <KV k="Backoff level" v={detail.service.backoff_level > 0 ? `L${detail.service.backoff_level} (~${Math.round(detail.service.backoff_ms / 1000)}s)` : "none"} />
              <KV k="Next retry" v={until(detail.service.next_retry_at)} />
              <KV k="First failure" v={ago(detail.service.first_failure_at)} />
              <KV k="Last failure" v={ago(detail.service.last_failure_at)} />
              <KV k="Last recovery" v={ago(detail.service.last_recovery_at)} />
              <KV k="Last healthy" v={ago(detail.service.last_healthy_at)} />
              <KV k="Exit" v={detail.service.last_exit_signal || (detail.service.last_exit_code != null ? `code ${detail.service.last_exit_code}` : "—")} />
              <KV k="Dependency" v={detail.service.degraded_dependency || "—"} />
            </div>
            {detail.service.last_failure_reason && <div className="text-sm text-warn">Reason: {detail.service.last_failure_reason}</div>}

            {logs && (
              <div>
                <div className="mb-1 text-xs uppercase tracking-wider text-muted">Recent logs (sanitized)</div>
                <pre className="max-h-64 overflow-auto rounded bg-black/40 p-3 text-[11px] leading-relaxed text-muted">{logs.join("\n") || "(no logs)"}</pre>
              </div>
            )}

            <div>
              <div className="mb-1 text-xs uppercase tracking-wider text-muted">Recovery history</div>
              <div className="space-y-1">
                {(detail.recovery_events || []).length === 0 && <div className="text-sm text-muted">No events recorded.</div>}
                {(detail.recovery_events || []).map((e) => (
                  <div key={e.id} className="flex items-center gap-3 border-b border-border/40 py-1 text-xs">
                    <span className="w-32 shrink-0 text-muted">{ago(e.created_at)}</span>
                    <Badge tone={e.event.includes("recover") || e.event.includes("converged") ? "ok" : e.event.includes("crash") || e.event.includes("not_converged") ? "err" : e.event.includes("manual") ? "info" : "warn"} >{e.event}</Badge>
                    <span className="flex-1 truncate text-muted" title={`${e.cause} ${e.action} ${e.result}`}>{e.action || e.cause || e.result}{e.actor && e.actor !== "system" ? ` · by ${e.actor.slice(0, 8)}` : ""}</span>
                  </div>
                ))}
              </div>
            </div>
          </CardBody>
        </Card>
      )}
    </div>
  );
}

function KV({ k, v }: { k: string; v: React.ReactNode }) {
  return <div><span className="text-muted">{k}: </span>{v}</div>;
}
