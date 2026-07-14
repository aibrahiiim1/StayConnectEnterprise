"use client";

import { Fragment, useEffect, useState } from "react";
import { api, ListResp, FleetAppliance, TelemetryRow } from "@/lib/api";
import { useCustomer } from "@/lib/customer-context";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ChevronDown, ChevronRight } from "lucide-react";
import { formatRelative, formatDate, errMsg } from "@/lib/utils";

const statusTone = (s: string) =>
  s === "online" ? "ok" : s === "offline" ? "err" : "default";

const svcStateTone = (s: string) =>
  s === "healthy" ? "ok" : s === "recovering" || s === "starting" ? "info" : s === "degraded" ? "warn" : "err";

// applianceHealth reads the sanitized service_health telemetry the appliance
// pushes: overall state + degraded/failed/crash-loop counts + worst reason.
function applianceHealth(sh: any): { overall: string; tone: "ok" | "info" | "warn" | "err"; bad: number; worst?: string; services?: any[] } | null {
  if (!sh || typeof sh !== "object") return null;
  const counts = sh.counts || {};
  const bad = (counts.degraded || 0) + (counts.failed || 0) + (counts.crash_loop || 0);
  const overall = sh.overall || "unknown";
  const tone = overall === "healthy" ? "ok" : overall === "recovering" ? "info" : bad > 0 ? "err" : "warn";
  return { overall, tone, bad, worst: sh.worst_failure_reason, services: sh.services };
}

// certHealth reads the sanitized Hotel Admin TLS cert status the appliance pushes
// in its health telemetry, so the Fleet surfaces warning/critical/expired/renewal
// failures without ever seeing a private key.
function certHealth(last_health: unknown): { label: string; tone: "ok" | "info" | "warn" | "err" } | null {
  const c = (last_health as any)?.hotel_admin_cert;
  if (!c || typeof c !== "object") return null;
  const failed = c.last_renewal_result && String(c.last_renewal_result).includes("fail");
  const thr = String(c.status_threshold ?? "");
  if (thr === "expired") return { label: "cert expired", tone: "err" };
  if (thr === "emergency" || thr === "critical") return { label: `cert ${thr}`, tone: "err" };
  if (failed) return { label: "renewal failed", tone: "err" };
  if (thr === "warning") return { label: "cert warning", tone: "warn" };
  if (thr === "renewal_due") return { label: "cert renewal due", tone: "info" };
  if (thr === "healthy") return { label: `cert ${c.days_remaining ?? ""}d`, tone: "ok" };
  return null;
}

export default function FleetPage() {
  const { selectedTenantId: tenantID, ready } = useCustomer();
  const [rows, setRows] = useState<FleetAppliance[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [telemetry, setTelemetry] = useState<Record<string, TelemetryRow[] | "loading">>({});

  async function load() {
    if (!ready) return;
    try {
      const r = await api.get<ListResp<FleetAppliance>>(`/cloud/v1/fleet?tenant_id=${tenantID}`);
      setRows(r.data ?? []);
    } catch (e) { setErr(errMsg(e)); }
  }
  useEffect(() => { setRows(null); load(); }, [ready, tenantID]);

  async function loadTelemetry(id: string) {
    setTelemetry((t) => ({ ...t, [id]: "loading" }));
    try {
      const r = await api.get<ListResp<TelemetryRow>>(`/cloud/v1/fleet/${id}/telemetry?limit=20`);
      setTelemetry((t) => ({ ...t, [id]: r.data ?? [] }));
    } catch (e) {
      setErr(errMsg(e));
      setTelemetry((t) => { const n = { ...t }; delete n[id]; return n; });
    }
  }

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="mb-4">
        <div className="text-xs text-muted uppercase tracking-wider">Infrastructure</div>
        <h1 className="text-2xl font-semibold">Fleet</h1>
      </div>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      <Card>
        <CardBody className="p-0">
          {rows === null ? <EmptyState title="Loading…" /> : rows.length === 0 ? (
            <EmptyState title="No appliances" hint="Activated appliances and their health will appear here." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH></TH>
                  <TH>Appliance</TH><TH>Site</TH><TH>Status</TH><TH>Health</TH>
                  <TH>Version</TH><TH>Last seen</TH><TH>License</TH><TH>TLS cert</TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((f) => {
                  const isOpen = open === f.appliance_id;
                  const tel = telemetry[f.appliance_id];
                  return (
                    <Fragment key={f.appliance_id}>
                      <TR className="cursor-pointer"
                          onClick={() => setOpen(isOpen ? null : f.appliance_id)}>
                        <TD className="text-muted">{isOpen ? <ChevronDown size={14} /> : <ChevronRight size={14} />}</TD>
                        <TD>{f.name}<div className="text-xs text-muted font-mono">{f.appliance_id.slice(0, 8)}</div></TD>
                        <TD className="text-muted">{f.site_id ? f.site_id.slice(0, 8) : "—"}</TD>
                        <TD><Badge tone={statusTone(f.status) as any}>{f.status}</Badge></TD>
                        <TD>
                          {(() => {
                            const h = applianceHealth(f.last_service_health);
                            if (!h) return <span className="text-muted">—</span>;
                            return <span className="inline-flex items-center gap-1">
                              <Badge tone={h.tone as any}>{h.overall}</Badge>
                              {h.bad > 0 && <span className="text-xs text-err">{h.bad} affected</span>}
                            </span>;
                          })()}
                        </TD>
                        <TD className="text-muted text-xs font-mono">{f.version || "—"}</TD>
                        <TD className="text-muted">{f.last_seen_at ? formatRelative(f.last_seen_at) : "—"}</TD>
                        <TD>
                          {f.license_status
                            ? <Badge tone={(f.license_status === "active" ? "ok" : f.license_status === "suspended" ? "warn" : "default") as any}>{f.license_status}</Badge>
                            : <span className="text-muted">—</span>}
                          {f.license_valid_until && (
                            <div className="text-xs text-muted mt-0.5">until {formatDate(f.license_valid_until)}</div>
                          )}
                        </TD>
                        <TD>
                          {(() => {
                            const ch = certHealth(f.last_health);
                            return ch ? <Badge tone={ch.tone as any}>{ch.label}</Badge> : <span className="text-muted">—</span>;
                          })()}
                        </TD>
                      </TR>
                      {isOpen && (
                        <TR>
                          <TD colSpan={9} className="bg-panel2/40">
                            {(() => {
                              const h = applianceHealth(f.last_service_health);
                              if (!h?.services) return null;
                              return (
                                <div className="mb-4">
                                  <div className="text-xs text-muted uppercase tracking-wider mb-2">
                                    Service health {f.last_service_health_at ? `· ${formatRelative(f.last_service_health_at)}` : ""}
                                  </div>
                                  <div className="flex flex-wrap gap-2 mb-2">
                                    {h.services.map((s: any) => (
                                      <span key={s.service} className="inline-flex items-center gap-1 border border-border rounded px-2 py-0.5 text-xs">
                                        <Badge tone={svcStateTone(s.state) as any}>{s.state}</Badge>
                                        <span>{s.service}</span>
                                        {s.restarts_in_window > 3 && <span className="text-muted">·{s.restarts_in_window}r</span>}
                                        {s.backoff_level > 0 && <span className="text-warn">·L{s.backoff_level}</span>}
                                      </span>
                                    ))}
                                  </div>
                                  {h.worst && <div className="text-xs text-err">Worst: {h.worst}</div>}
                                </div>
                              );
                            })()}
                            <div className="text-xs text-muted uppercase tracking-wider mb-2">Last health</div>
                            {f.last_health ? (
                              <pre className="bg-panel2 border border-border rounded p-3 text-xs overflow-x-auto whitespace-pre-wrap break-all mb-4">
                                {JSON.stringify(f.last_health, null, 2)}
                              </pre>
                            ) : (
                              <div className="text-sm text-muted mb-4">No health telemetry received yet.</div>
                            )}
                            <div className="flex items-center justify-between mb-2">
                              <div className="text-xs text-muted uppercase tracking-wider">Telemetry</div>
                              <Button size="sm" variant="secondary"
                                onClick={(e) => { e.stopPropagation(); loadTelemetry(f.appliance_id); }}
                                disabled={tel === "loading"}>
                                {tel === "loading" ? "Loading…" : "Load telemetry"}
                              </Button>
                            </div>
                            {tel && tel !== "loading" && (
                              tel.length === 0 ? (
                                <div className="text-sm text-muted">No telemetry records.</div>
                              ) : (
                                <div className="space-y-2">
                                  {tel.map((t, i) => (
                                    <div key={i} className="border border-border rounded p-2">
                                      <div className="flex items-center gap-2 text-xs mb-1">
                                        <Badge tone="info">{t.kind}</Badge>
                                        <span className="text-muted">{formatDate(t.ts)}</span>
                                      </div>
                                      <pre className="text-xs overflow-x-auto whitespace-pre-wrap break-all">{JSON.stringify(t.payload, null, 2)}</pre>
                                    </div>
                                  ))}
                                </div>
                              )
                            )}
                          </TD>
                        </TR>
                      )}
                    </Fragment>
                  );
                })}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>
    </div>
  );
}
