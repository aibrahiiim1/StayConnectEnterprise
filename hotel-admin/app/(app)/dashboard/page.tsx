"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { api, EdgeHealth, ReportsSummary } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { KpiCard } from "@/components/kpi-card";
import { Badge } from "@/components/ui/badge";
import { formatBytes, errMsg } from "@/lib/utils";

const licenseTone = (state?: string | null) => {
  switch (state) {
    case "Active":      return "ok";
    case "GracePeriod": return "warn";
    case "Restricted": // legacy pre-v3 state
    case "Suspended":   return "warn";
    case "Expired":
    case "Revoked":
    case "Unlicensed":  return "err";
    default:            return "default";
  }
};

export default function DashboardPage() {
  const [summary, setSummary] = useState<ReportsSummary | null>(null);
  const [health, setHealth] = useState<EdgeHealth | null>(null);
  const [err, setErr] = useState<string | null>(null);

  async function load() {
    try {
      const [s, h] = await Promise.all([
        api.get<ReportsSummary>("/reports/summary"),
        api.get<EdgeHealth>("/health"),
      ]);
      setSummary(s);
      setHealth(h);
    } catch (e) {
      setErr(errMsg(e));
    }
  }
  useEffect(() => {
    load();
    const id = setInterval(load, 30_000);
    return () => clearInterval(id);
  }, []);

  const dataToday =
    summary?.total_bytes_today ??
    (summary ? (summary.bytes_up_today ?? 0) + (summary.bytes_down_today ?? 0) : undefined);

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Overview</div>
          <h1 className="text-2xl font-semibold">Dashboard</h1>
        </div>
        {health && (
          <div className="text-sm text-muted flex items-center gap-2">
            <Badge tone={health.status === "ok" ? "ok" : "warn"}>{health.status}</Badge>
            <span className="font-mono text-xs">edged {health.version}</span>
          </div>
        )}
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4 mb-6">
        <KpiCard
          label="Active sessions"
          value={summary?.active_sessions ?? "—"}
          hint="Devices online right now"
        />
        <KpiCard
          label="Sessions today"
          value={summary?.sessions_today ?? "—"}
          hint="Since local midnight"
        />
        <KpiCard
          label="Data today"
          value={dataToday !== undefined ? formatBytes(dataToday) : "—"}
          hint={
            summary
              ? `${formatBytes(summary.bytes_down_today ?? 0)} down · ${formatBytes(summary.bytes_up_today ?? 0)} up`
              : undefined
          }
        />
        <KpiCard
          label="License"
          value={
            health == null ? (
              <Badge tone="default" className="text-sm h-6 px-3">—</Badge>
            ) : health.license_installed ? (
              // A real signed license is installed → show its enforcement state.
              <Badge tone={licenseTone(health.license_state) as any} className="text-sm h-6 px-3">
                {health.license_state ?? "—"}
              </Badge>
            ) : (
              // No real license — the "Active" reported by the permissive
              // unlicensed-dev licstate is NOT activation. Surface it clearly.
              <Badge tone="warn" className="text-sm h-6 px-3">Pending activation</Badge>
            )
          }
          hint={
            <Link href="/license" className="hover:text-text">
              {health && !health.license_installed ? "Activate this appliance →" : "View license →"}
            </Link>
          }
        />
      </div>

      <Card>
        <CardHeader><CardTitle>Appliance health</CardTitle></CardHeader>
        <CardBody>
          {!health ? (
            <div className="text-sm text-muted">Loading…</div>
          ) : (
            <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4 text-sm">
              <div>
                <div className="text-xs text-muted uppercase tracking-wider mb-1">Site database</div>
                <Badge tone={health.db ? "ok" : "err"}>{health.db ? "reachable" : "down"}</Badge>
              </div>
              <div>
                <div className="text-xs text-muted uppercase tracking-wider mb-1">Session controller (scd)</div>
                <Badge tone={health.scd ? "ok" : "err"}>{health.scd ? "reachable" : "down"}</Badge>
              </div>
              <div>
                <div className="text-xs text-muted uppercase tracking-wider mb-1">Cloud sync outbox</div>
                {health.sync_outbox?.enabled ? (
                  <span>
                    <Badge tone={(health.sync_outbox.dead ?? 0) > 0 ? "warn" : "ok"}>
                      {health.sync_outbox.pending ?? 0} pending
                    </Badge>
                    {(health.sync_outbox.dead ?? 0) > 0 && (
                      <span className="text-xs text-err ml-2">{health.sync_outbox.dead} dead</span>
                    )}
                  </span>
                ) : (
                  <Badge tone="default">disabled</Badge>
                )}
              </div>
              <div>
                <div className="text-xs text-muted uppercase tracking-wider mb-1">Site ID</div>
                <span className="font-mono text-xs">{health.site_id}</span>
              </div>
            </div>
          )}
        </CardBody>
      </Card>
    </div>
  );
}
