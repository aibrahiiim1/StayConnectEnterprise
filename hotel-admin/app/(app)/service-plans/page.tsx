"use client";

// SERVICE PLANS — the technical service behind a guest-facing Internet package.
//
// THIS REPLACES AN EXISTING SURFACE RATHER THAN ADDING A MISSING ONE. Service plans were already manageable
// from a tab inside "Commercial packages": it listed plans, showed revision history and published new
// revisions. What it could not do was say what any plan actually GRANTED — the row carried a code, a
// revision count and a revision UUID — so choosing a plan on the package form, the most consequential field
// there, meant opening each plan's history in turn.
//
// The form was also in wire units: "Down kbps", "Time quota seconds", "Data quota bytes", so publishing a
// 50 Mbps / 5 GB / 24 hour plan meant typing 50000, 5000000000 and 86400 correctly by hand. Plan revisions
// are immutable, so a slipped zero is permanent and has to be superseded rather than corrected.
//
// This is that surface, promoted to its own page beside Internet packages, showing what each plan grants and
// collecting operator units that are converted on submit. The API and wire contract are unchanged.

import { useCallback, useEffect, useState, Fragment } from "react";
import { api, ApiError, ListResp } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { Plus, X, Gauge } from "lucide-react";
import {
  formatSpeed, formatData, formatDuration, formatDevices, mbpsToKbps, gbToBytes,
  durationToSeconds, DEVICE_LIMIT_POLICIES, TIME_ACCOUNTING_MODES,
} from "@/lib/units";

type PlanSummary = {
  plan_id: string; code: string; enabled: boolean;
  current_revision_id: string; revision_count: number;
  name?: string | null;
  down_kbps?: number | null; up_kbps?: number | null;
  max_concurrent_devices?: number | null; device_limit_policy?: string | null;
  idle_timeout_seconds?: number | null; max_continuous_session_seconds?: number | null;
  time_quota_seconds?: number | null; data_quota_bytes?: number | null;
  time_accounting_mode?: string | null;
  speed_allocation?: string | null;
  used_by_active_packages?: number;
};
type PackageRef = {
  package_id: string; code: string; name?: string | null; active: boolean;
  service_plan_id?: string | null; service_plan_revision_id?: string | null;
};
type RevisionInfo = { revision_id: string; revision_no: number; is_current: boolean; label?: string };
type PackageCurrentDTO = {
  code: string;
  display?: Record<string, unknown> | null;
  duration_policy?: Record<string, unknown> | null;
  eligibility_rules?: { type?: string; Type?: string; value?: Record<string, unknown>; Value?: Record<string, unknown> }[] | null;
  grant_tiers?: { order?: number; Order?: number; value?: Record<string, unknown>; Value?: Record<string, unknown> }[] | null;
  visible_from?: string | null; visible_until?: string | null;
};

export default function ServicePlansPage() {
  // The capability being OFF is not an error the operator can act on, so it gets its own state rather than a
  // red banner. edged answers 503 while the surface is dark; that is the authority, not this flag.
  const [rows, setRows] = useState<PlanSummary[] | null>(null);
  const [packages, setPackages] = useState<PackageRef[]>([]);
  const [revs, setRevs] = useState<Record<string, RevisionInfo[]>>({});
  const [err, setErr] = useState<string | null>(null);
  const [unavailable, setUnavailable] = useState(false);
  const writable = true; // edged enforces write permission server-side and answers 403 if the role lacks it.
  const [showNew, setShowNew] = useState(false);
  const [prefill, setPrefill] = useState<PlanSummary | null>(null);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<string | null>(null);
  // THE REPIN STEP. Publishing new plan settings does NOT move any package onto them — packages pin a
  // specific revision, and that is what keeps a guest's terms stable. So after saving, the operator is asked
  // which packages should use the new settings for FUTURE guests. Nothing is repinned unless they say so,
  // and no existing Entitlement changes either way.
  const [repin, setRepin] = useState<{ planRevisionID: string; planLabel: string; packages: PackageRef[]; chosen: Record<string, boolean> } | null>(null);

  const load = useCallback(async () => {
    try {
      const [r, pk] = await Promise.all([
        api.get<ListResp<PlanSummary>>("/commercial-packages/plans"),
        api.get<ListResp<PackageRef>>("/commercial-packages"),
      ]);
      setRows(r.data ?? []); setPackages(pk.data ?? []); setUnavailable(false);
    } catch (e) {
      if (e instanceof ApiError && e.status === 503) { setUnavailable(true); setRows([]); return; }
      setRows([]);
      setErr((e as Error)?.message ?? "Could not load service plans");
    }
  }, []);
  useEffect(() => { load(); }, [load]);

  async function toggleRevs(id: string) {
    if (revs[id]) { setRevs((r) => { const n = { ...r }; delete n[id]; return n; }); return; }
    try {
      const r = await api.get<ListResp<RevisionInfo>>(`/commercial-packages/plans/${id}/revisions`);
      setRevs((s) => ({ ...s, [id]: r.data ?? [] }));
    } catch (e) { setErr((e as Error)?.message ?? "Could not load revision history"); }
  }

  function startNew(from?: PlanSummary) {
    setPrefill(from ?? null);
    setShowNew(true);
  }

  async function onPublish(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault(); setBusy(true); setErr(null);
    const el = e.currentTarget; const f = new FormData(el);
    const str = (k: string) => ((f.get(k) as string) || "").trim();
    const int = (k: string) => { const v = str(k); return v === "" ? undefined : Number(v); };
    try {
      const res = await api.post<{ current_revision_id: string }>("/commercial-packages/plans", {
        code: str("code"),
        name: str("name"),
        // Operator units in, wire units out.
        down_kbps: mbpsToKbps(str("down_mbps")),
        up_kbps: mbpsToKbps(str("up_mbps")),
        max_concurrent_devices: int("max_concurrent_devices") ?? 1,
        device_limit_policy: str("device_limit_policy") || "REJECT_NEW_DEVICE",
        idle_timeout_seconds: durationToSeconds(str("idle_timeout"), "minutes"),
        max_continuous_session_seconds: durationToSeconds(str("max_session"), "hours"),
        time_quota_seconds: durationToSeconds(str("time_quota"), (str("time_quota_unit") as "hours" | "days") || "hours"),
        data_quota_bytes: gbToBytes(str("data_quota_gb")),
        time_accounting_mode: str("time_accounting_mode") || "VALIDITY_WINDOW",
        speed_allocation: str("speed_allocation") || "PER_DEVICE",
      });
      el.reset(); setShowNew(false);
      const affected = prefill
        ? packages.filter((k) => k.active && k.service_plan_id === prefill.plan_id)
        : [];
      if (affected.length > 0 && res?.current_revision_id) {
        setRepin({
          planRevisionID: res.current_revision_id,
          planLabel: prefill?.name || prefill?.code || "this plan",
          packages: affected,
          chosen: Object.fromEntries(affected.map((k) => [k.package_id, false])),
        });
      } else {
        setNotice("Changes saved. Existing guest access is unchanged; the new settings apply to future grants.");
      }
      setPrefill(null); await load();
    } catch (e) { setErr((e as Error)?.message ?? "Could not publish this plan revision"); }
    finally { setBusy(false); }
  }

  // applyRepin republishes ONLY the chosen packages, each pinned to the new plan revision. Every package is
  // republished from its own CURRENT configuration, so nothing but the plan pin changes — the eligibility
  // rules and grant tiers it already had are carried across rather than dropped.
  async function applyRepin() {
    if (!repin) return;
    const chosen = repin.packages.filter((k) => repin.chosen[k.package_id]);
    setBusy(true); setErr(null);
    try {
      for (const k of chosen) {
        const cur = await api.get<PackageCurrentDTO>(`/commercial-packages/${k.package_id}/current`);
        await api.post("/commercial-packages", {
          code: cur.code,
          service_plan_revision_id: repin.planRevisionID,
          display: cur.display ?? { name: k.name || k.code },
          duration_policy: cur.duration_policy ?? { end_mode: "MANUAL_END" },
          eligibility_rules: (cur.eligibility_rules ?? []).map((r) => ({
            type: r.type ?? r.Type, value: r.value ?? r.Value ?? {},
          })),
          grant_tiers: (cur.grant_tiers ?? []).map((t) => ({
            order: t.order ?? t.Order ?? 10, grant: t.value ?? t.Value ?? {},
          })),
          visible_from: cur.visible_from ?? undefined,
          visible_until: cur.visible_until ?? undefined,
        });
      }
      setRepin(null);
      setNotice(chosen.length === 0
        ? "Changes saved. No packages were updated, so guests continue to receive the settings they do now."
        : `Changes saved. ${chosen.length} package${chosen.length === 1 ? "" : "s"} updated. Existing guest access is unchanged; the new settings apply to future grants.`);
      await load();
    } catch (e) { setErr((e as Error)?.message ?? "Could not update those packages"); }
    finally { setBusy(false); }
  }

  return (
    <div className="space-y-4">
      <div className="flex items-start justify-between gap-4 flex-wrap">
        <div>
          <h1 className="text-lg font-semibold">Service plans</h1>
          <p className="text-sm text-muted mt-1 max-w-2xl">
            A service plan is the technical service a guest receives: speed, how many devices, and how long
            access lasts. Internet packages are what a guest sees and chooses; each one uses a service plan.
          </p>
        </div>
        {writable && !unavailable && (
          <Button onClick={() => (showNew ? setShowNew(false) : startNew())}>
            {showNew ? <X size={16} /> : <Plus size={16} />}{showNew ? "Cancel" : "Add plan"}
          </Button>
        )}
      </div>

      {err && <ErrorBanner err={err} />}
      {notice && (
        <div className="text-sm rounded-md border border-border bg-panel2 px-3 py-2" role="status">{notice}</div>
      )}

      {repin && (
        <Card>
          <CardHeader><CardTitle>Apply these settings to packages?</CardTitle></CardHeader>
          <CardBody>
            <p className="text-sm text-muted mb-3">
              The new settings for <strong>{repin.planLabel}</strong> are saved. Choose which packages should
              give them to <strong>future</strong> guests. Guests already connected are not affected either
              way, and any package you leave unticked keeps the settings it has now.
            </p>
            <div className="space-y-2 mb-4">
              {repin.packages.map((k) => (
                <label key={k.package_id} className="flex items-center gap-2 text-sm">
                  <input type="checkbox" aria-label={`repin-${k.code}`}
                    checked={!!repin.chosen[k.package_id]}
                    onChange={(e) => setRepin((s2) => s2 && ({ ...s2, chosen: { ...s2.chosen, [k.package_id]: e.target.checked } }))} />
                  <span>{k.name || k.code}</span>
                  <span className="text-xs text-muted">{k.code}</span>
                </label>
              ))}
            </div>
            <div className="flex gap-2">
              <Button onClick={applyRepin} disabled={busy}>{busy ? "Applying…" : "Apply to selected packages"}</Button>
              <Button variant="ghost" disabled={busy} onClick={() => {
                setRepin(null);
                setNotice("Changes saved. No packages were updated, so guests continue to receive the settings they do now.");
              }}>Not now</Button>
            </div>
          </CardBody>
        </Card>
      )}

      {unavailable ? (
        <Card><CardBody>
          <EmptyState
            title="The internet offering is not switched on for this appliance"
            hint="Service plans and internet packages become available once this capability is enabled for the site. Contact your StayConnect administrator." />
        </CardBody></Card>
      ) : (
        <>
          {showNew && (
            <Card>
              <CardHeader>
                <CardTitle>{prefill ? `Edit ${prefill.name || prefill.code}` : "Add service plan"}</CardTitle>
              </CardHeader>
              <CardBody>
                {/* Editing means publishing a NEW revision — the previous one stays intact so that anything
                    already sold under it keeps the terms it was sold under. Saying so here prevents the
                    reasonable assumption that this form edits the plan in place. */}
                <p className="text-sm text-muted mb-3">
                  {prefill
                    ? "Saving records these settings as the plan's current settings. Guests already connected keep the terms they were given, and packages using this plan are only updated if you choose them below."
                    : "This creates the plan and its first settings."}
                </p>
                <form onSubmit={onPublish} className="grid gap-3 sm:grid-cols-2">
                  <div>
                    <Label>Plan code</Label>
                    <Input name="code" required defaultValue={prefill?.code ?? ""} readOnly={!!prefill}
                      placeholder="GOLD" />
                    <p className="text-xs text-muted mt-1">A short identifier. It cannot be changed later.</p>
                  </div>
                  <div>
                    <Label>Display name</Label>
                    <Input name="name" defaultValue={prefill?.name ?? ""} placeholder="Premium Wi-Fi" />
                  </div>

                  <div>
                    <Label>Download speed (Mbps)</Label>
                    <Input name="down_mbps" type="number" min={0} step="0.1"
                      defaultValue={prefill?.down_kbps ? prefill.down_kbps / 1000 : ""} placeholder="Leave empty for unlimited" />
                  </div>
                  <div>
                    <Label>Upload speed (Mbps)</Label>
                    <Input name="up_mbps" type="number" min={0} step="0.1"
                      defaultValue={prefill?.up_kbps ? prefill.up_kbps / 1000 : ""} placeholder="Leave empty for unlimited" />
                  </div>

                  <div>
                    <Label>Devices at once</Label>
                    <Input name="max_concurrent_devices" type="number" min={1}
                      defaultValue={prefill?.max_concurrent_devices ?? 1} required />
                  </div>
                  <div>
                    <Label>When the device limit is reached</Label>
                    <select name="device_limit_policy" defaultValue={prefill?.device_limit_policy ?? "REJECT_NEW_DEVICE"}
                      className="w-full bg-panel2 border border-border rounded-md px-2 py-2 text-sm">
                      {Object.entries(DEVICE_LIMIT_POLICIES).map(([v, label]) => (
                        <option key={v} value={v}>{label}</option>
                      ))}
                    </select>
                  </div>

                  <div>
                    <Label>Total time allowance</Label>
                    <div className="flex gap-2">
                      <Input name="time_quota" type="number" min={0} placeholder="Unlimited" className="flex-1" />
                      <select name="time_quota_unit" className="bg-panel2 border border-border rounded-md px-2 text-sm">
                        <option value="hours">hours</option>
                        <option value="days">days</option>
                      </select>
                    </div>
                  </div>
                  <div>
                    <Label>Data allowance (GB)</Label>
                    <Input name="data_quota_gb" type="number" min={0} step="0.1" placeholder="Unlimited" />
                  </div>

                  <div>
                    <Label>Disconnect after inactivity (minutes)</Label>
                    <Input name="idle_timeout" type="number" min={0} placeholder="Never" />
                  </div>
                  <div>
                    <Label>Maximum single session (hours)</Label>
                    <Input name="max_session" type="number" min={0} placeholder="No limit" />
                  </div>

                  <div className="sm:col-span-2">
                    <Label>How the speed is shared</Label>
                    <select name="speed_allocation" defaultValue={prefill?.speed_allocation ?? "PER_DEVICE"}
                      className="w-full bg-panel2 border border-border rounded-md px-2 py-2 text-sm">
                      <option value="PER_DEVICE">Per device — every device gets the full speed</option>
                      <option value="SHARED">Shared — all the guest&rsquo;s devices share the speed</option>
                    </select>
                    <p className="text-xs text-muted mt-1">
                      Shared gives the whole allowance to whichever devices are actually using it, so one
                      device alone still gets the full speed. It is not divided into fixed portions.
                      Shared needs a download and upload speed to share.
                    </p>
                  </div>

                  <div className="sm:col-span-2">
                    <Label>How time is counted</Label>
                    <select name="time_accounting_mode" defaultValue="VALIDITY_WINDOW"
                      className="w-full bg-panel2 border border-border rounded-md px-2 py-2 text-sm">
                      {/* Only VALIDITY_WINDOW is implemented end to end; AGGREGATE_ONLINE_TIME exists in the
                          schema but is inert, so offering it would be a control that silently does nothing. */}
                      <option value="VALIDITY_WINDOW">{TIME_ACCOUNTING_MODES.VALIDITY_WINDOW}</option>
                    </select>
                  </div>

                  <div className="sm:col-span-2 flex gap-2">
                    <Button type="submit" disabled={busy}>{busy ? "Saving…" : prefill ? "Save changes" : "Add plan"}</Button>
                    <Button type="button" variant="ghost" onClick={() => { setShowNew(false); setPrefill(null); }}>Cancel</Button>
                  </div>
                </form>
              </CardBody>
            </Card>
          )}

          <Card><CardBody>
            {rows === null ? (
              <div className="text-sm text-muted py-6 text-center">Loading service plans…</div>
            ) : rows.length === 0 ? (
              <EmptyState
                title="No service plans yet"
                hint="A service plan defines the speed, device count and duration a guest receives. Create one, then attach it to an internet package." />
            ) : (
              <Table>
                <THead><TR>
                  <TH>Plan</TH><TH>Speed</TH><TH>Devices</TH><TH>Time</TH><TH>Data</TH><TH>Used by</TH><TH>Status</TH><TH></TH>
                </TR></THead>
                <tbody>
                  {rows.map((p) => (
                    <Fragment key={p.plan_id}>
                      <TR>
                        <TD>
                          <div className="font-medium flex items-center gap-2"><Gauge size={14} className="text-muted" />{p.name || p.code}</div>
                          <div className="text-xs text-muted">{p.code}</div>
                        </TD>
                        <TD>
                          <div>{formatSpeed(p.down_kbps)} down{p.speed_allocation === "SHARED" ? " (shared)" : ""}</div>
                          <div className="text-xs text-muted">{formatSpeed(p.up_kbps)} up</div>
                        </TD>
                        <TD>
                          <div>{formatDevices(p.max_concurrent_devices)}</div>
                          <div className="text-xs text-muted">
                            {DEVICE_LIMIT_POLICIES[p.device_limit_policy ?? ""] ?? "—"}
                          </div>
                        </TD>
                        <TD>{formatDuration(p.time_quota_seconds)}</TD>
                        <TD>{formatData(p.data_quota_bytes)}</TD>
                        <TD className="text-xs text-muted">
                          {(p.used_by_active_packages ?? 0) === 0
                            ? "No active packages"
                            : `${p.used_by_active_packages} active package${p.used_by_active_packages === 1 ? "" : "s"}`}
                        </TD>
                        <TD>{p.enabled ? <Badge tone="ok">Active</Badge> : <Badge tone="default">Disabled</Badge>}</TD>
                        <TD className="whitespace-nowrap">
                          {writable && <Button variant="ghost" onClick={() => startNew(p)}>Edit</Button>}
                          <button className="underline text-muted text-xs ml-2" onClick={() => toggleRevs(p.plan_id)}>
                            History
                          </button>
                        </TD>
                      </TR>
                      {revs[p.plan_id] && (
                        <TR>
                          <TD colSpan={8} className="text-xs bg-panel2/40">
                            <div className="font-medium mb-1">History</div>
                            {revs[p.plan_id].map((r) => (
                              <div key={r.revision_id} className="py-0.5">
                                #{r.revision_no}{" "}
                                {r.is_current
                                  ? <Badge tone="info">in force</Badge>
                                  : <span className="text-muted">superseded</span>}{" "}
                                {r.label}
                              </div>
                            ))}
                            <p className="text-muted mt-1">
                              Every saved change is kept permanently. A guest keeps the terms that applied when they connected.
                            </p>
                          </TD>
                        </TR>
                      )}
                    </Fragment>
                  ))}
                </tbody>
              </Table>
            )}
          </CardBody></Card>
        </>
      )}
    </div>
  );
}
