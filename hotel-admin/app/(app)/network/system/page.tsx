"use client";

import { useEffect, useRef, useState } from "react";
import {
  api, Whoami, SysNetState, SysNetProposal, SysNetValidateResp,
  SysNetApplyResp, SysNetAudit,
} from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { canWrite } from "@/lib/roles";
import { errMsg } from "@/lib/utils";
import { Router, AlertTriangle, CheckCircle2, XCircle, Download, RefreshCw, Archive, Network, ExternalLink } from "lucide-react";

function Dot({ ok }: { ok: boolean }) {
  return <span className={ok ? "text-emerald-600" : "text-red-600"}>{ok ? "●" : "●"}</span>;
}
function StateBadge({ ok, label }: { ok: boolean; label: string }) {
  return <Badge className={ok ? "bg-emerald-100 text-emerald-800" : "bg-red-100 text-red-800"}>{label}</Badge>;
}

export default function NetworkSettingsPage() {
  const [roles, setRoles] = useState<string[]>([]);
  const [state, setState] = useState<SysNetState | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // edit form (seeded from current state)
  const [wanIp, setWanIp] = useState("");
  const [wanPrefix, setWanPrefix] = useState(24);
  const [wanGw, setWanGw] = useState("");
  const [wanDns, setWanDns] = useState("");
  const [lanIp, setLanIp] = useState("");
  const [lanPrefix, setLanPrefix] = useState(24);
  const [dhcpEnabled, setDhcpEnabled] = useState(true);
  const [dhcpStart, setDhcpStart] = useState("");
  const [dhcpEnd, setDhcpEnd] = useState("");
  const [dhcpLease, setDhcpLease] = useState(3600);

  const [validation, setValidation] = useState<SysNetValidateResp | null>(null);
  const [newMgmtUrl, setNewMgmtUrl] = useState("");
  const [password, setPassword] = useState("");
  const [applyResp, setApplyResp] = useState<SysNetApplyResp | null>(null);
  const [countdown, setCountdown] = useState<number>(0);
  const [history, setHistory] = useState<SysNetAudit[]>([]);
  const [diag, setDiag] = useState<Record<string, string> | null>(null);
  const timer = useRef<any>(null);

  const writable = canWrite("network", roles);

  function seed(s: SysNetState) {
    setWanIp(s.wan.ip); setWanPrefix(s.wan.prefix_len); setWanGw(s.wan.gateway);
    setWanDns((s.wan.dns || []).join(", "));
    setLanIp(s.lan.ip); setLanPrefix(s.lan.prefix_len);
    setDhcpEnabled(s.lan.dhcp_enabled); setDhcpStart(s.lan.dhcp_start);
    setDhcpEnd(s.lan.dhcp_end); setDhcpLease(s.lan.dhcp_lease_seconds);
  }

  async function load() {
    try {
      const s = await api.get<SysNetState>("/network/system");
      setState(s); seed(s);
      if (s.pending) startCountdown(s.pending.deadline_unix);
    } catch (e) { setErr(errMsg(e)); }
  }
  async function loadHistory() {
    try { setHistory((await api.get<{ history: SysNetAudit[] }>("/network/system/history")).history ?? []); }
    catch { /* non-fatal */ }
  }

  useEffect(() => {
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => {});
    load(); loadHistory();
    return () => { if (timer.current) clearInterval(timer.current); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  function proposal(): SysNetProposal {
    return {
      wan: {
        mode: "static", ip: wanIp, prefix_len: Number(wanPrefix), gateway: wanGw,
        dns: wanDns.split(",").map((s) => s.trim()).filter(Boolean),
      },
      lan: {
        ip: lanIp, prefix_len: Number(lanPrefix), dhcp_enabled: dhcpEnabled,
        dhcp_start: dhcpStart, dhcp_end: dhcpEnd, dhcp_lease_seconds: Number(dhcpLease),
      },
    };
  }

  async function doValidate() {
    setErr(null); setBusy(true); setApplyResp(null);
    try {
      const r = await api.post<SysNetValidateResp>("/network/system/validate", proposal());
      setValidation(r); setNewMgmtUrl(r.management_url);
    } catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
  }

  function startCountdown(deadlineUnix: number) {
    if (timer.current) clearInterval(timer.current);
    const tick = () => {
      const left = Math.max(0, deadlineUnix - Math.floor(Date.now() / 1000));
      setCountdown(left);
      if (left <= 0) { clearInterval(timer.current); load(); }
    };
    tick(); timer.current = setInterval(tick, 1000);
  }

  async function doApply() {
    if (!password) { setErr("Confirm your password to apply a network change."); return; }
    setErr(null); setBusy(true);
    try {
      const r = await api.post<SysNetApplyResp>("/network/system/apply", { proposal: proposal(), password });
      setApplyResp(r); setPassword("");
      if (r.state === "pending_confirmation" && r.deadline_unix) startCountdown(r.deadline_unix);
      loadHistory();
    } catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
  }

  async function doConfirm() {
    setBusy(true); setErr(null);
    try {
      await api.post("/network/system/confirm", {});
      if (timer.current) clearInterval(timer.current);
      setApplyResp(null); setCountdown(0);
      await load(); loadHistory();
    } catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
  }

  async function doRollback() {
    const pw = prompt("Confirm your password to roll back:");
    if (!pw) return;
    setBusy(true); setErr(null);
    try {
      await api.post("/network/system/rollback", { password: pw });
      if (timer.current) clearInterval(timer.current);
      setApplyResp(null); setCountdown(0);
      await load(); loadHistory();
    } catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
  }

  async function loadDiag() {
    try { setDiag((await api.get<{ diagnostics: Record<string, string> }>("/network/system/diagnostics")).diagnostics); }
    catch (e) { setErr(errMsg(e)); }
  }
  function downloadDiag() {
    if (!diag) return;
    const report = Object.entries(diag).map(([k, v]) => `===== ${k} =====\n${v}\n`).join("\n");
    const blob = new Blob([report], { type: "text/plain" });
    const a = document.createElement("a");
    a.href = URL.createObjectURL(blob);
    a.download = `network-diagnostics-${new Date().toISOString().slice(0, 19)}.txt`;
    a.click();
  }

  if (!state) return <div className="p-6 text-sm text-neutral-500">{err ?? "Loading network settings…"}</div>;

  const wanChanged = wanIp !== state.wan.ip || Number(wanPrefix) !== state.wan.prefix_len || wanGw !== state.wan.gateway || wanDns !== (state.wan.dns || []).join(", ");
  const mgmtWillChange = wanIp !== state.wan.ip;

  return (
    <div className="space-y-6 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-xl font-semibold">Network settings</h1>
          <p className="text-sm text-neutral-500">Configure the appliance WAN / management and guest LAN. Changes preview, apply with automatic rollback, and are audited.</p>
        </div>
        <Button variant="secondary" onClick={() => { load(); loadHistory(); }}><RefreshCw className="mr-1 h-4 w-4" />Refresh</Button>
      </div>

      {err && <div className="rounded border border-red-200 bg-red-50 p-3 text-sm text-red-700">{err}</div>}

      {/* pending banner */}
      {state.pending || applyResp?.state === "pending_confirmation" ? (
        <div className="rounded border border-amber-300 bg-amber-50 p-4">
          <div className="flex items-center gap-2 font-medium text-amber-900"><AlertTriangle className="h-5 w-5" />Change applied — confirmation required</div>
          <p className="mt-1 text-sm text-amber-800">
            The new configuration is live but will <b>automatically roll back in {countdown}s</b> unless you confirm.
            {(applyResp?.management_url || state.pending?.management_url) && (
              <> If you changed the WAN IP, reconnect at <b>{applyResp?.management_url || state.pending?.management_url}</b> and confirm there.</>
            )}
          </p>
          <div className="mt-3 flex gap-2">
            <Button onClick={doConfirm} disabled={busy}>Keep this configuration</Button>
            <Button variant="secondary" onClick={doRollback} disabled={busy}>Roll back now</Button>
          </div>
        </div>
      ) : null}

      {/* status cards */}
      <div className="grid gap-4 md:grid-cols-2">
        {/* WAN card */}
        <Card>
          <CardHeader><CardTitle className="flex items-center gap-2"><Router className="h-4 w-4" />WAN / Management <span className="text-xs font-normal text-neutral-400">({state.wan.interface})</span></CardTitle></CardHeader>
          <CardBody className="space-y-1 text-sm">
            <Row k="Physical interface" v={<code>{state.wan.interface}</code>} />
            <Row k="MAC address" v={<code>{state.wan.mac}</code>} />
            <Row k="Link" v={<StateBadge ok={state.wan.link_up} label={state.wan.link_up ? "up" : "down"} />} />
            <Row k="IP mode" v={state.wan.mode} />
            <Row k="IP address" v={<code>{state.wan.ip}/{state.wan.prefix_len}</code>} />
            <Row k="Subnet mask" v={<code>{state.wan.netmask}</code>} />
            <Row k="Default gateway" v={<code>{state.wan.gateway}</code>} />
            <Row k="DNS" v={<code>{state.wan.dns.join(", ")}</code>} />
            <Row k="Management URL" v={<a className="text-blue-600 underline" href={state.wan.management_url}>{state.wan.management_url}</a>} />
            <Row k="Outbound interface" v={<code>{state.wan.outbound_interface}</code>} />
            <Row k="Connectivity" v={<span className="flex gap-3">
              <span><Dot ok={state.wan.connectivity.gateway_reachable} /> gateway</span>
              <span><Dot ok={state.wan.connectivity.internet_ok} /> internet</span>
              <span><Dot ok={state.wan.connectivity.dns_ok} /> DNS</span>
            </span>} />
            {state.wan.drift && <div className="text-amber-700">⚠ runtime IP differs from saved config ({state.wan.persistent_ip})</div>}
          </CardBody>
        </Card>

        {/* Guest Networks pointer — the real guest-facing config lives here, NOT
            on the legacy base bridge below. */}
        <Card>
          <CardHeader><CardTitle className="flex items-center gap-2"><Network className="h-4 w-4" />Guest Networks</CardTitle></CardHeader>
          <CardBody className="space-y-3 text-sm">
            <p className="text-muted">
              Guest WiFi is served by the <strong>Guest Networks</strong> you create — each has its own VLAN, bridge,
              gateway, DHCP pool and captive-portal settings. Configure and check them here (this page only covers the
              WAN uplink and the legacy base bridge):
            </p>
            <div className="flex flex-col gap-2">
              <a href="/network" className="inline-flex items-center gap-2 text-brand hover:underline">
                <ExternalLink size={14} /> Guest Networks — create / edit VLANs, gateways, portal
              </a>
              <a href="/network/dhcp" className="inline-flex items-center gap-2 text-brand hover:underline">
                <ExternalLink size={14} /> DHCP &amp; leases — per-network DHCP pools, reservations, leases
              </a>
            </div>
            <p className="text-xs text-muted">
              Example: a guest network <code>CHR</code> → VLAN 90 → <code>ens192.90</code> → bridge <code>br-g90</code>
              {" "}→ gateway <code>10.20.0.1/22</code> → DHCP pool <code>10.20.0.100–10.20.3.250</code>. Each guest
              network shows its own DHCP status on those pages.
            </p>
          </CardBody>
        </Card>
      </div>

      {/* Legacy base bridge — clearly demarcated so it is never mistaken for an
          active Guest VLAN. Behavior is unchanged; this is presentation only. */}
      <details className="rounded-lg border border-border bg-panel">
        <summary className="cursor-pointer select-none px-4 py-3 text-sm font-medium flex items-center gap-2">
          <Archive size={14} /> Advanced · Base LAN / Legacy Bridge
          <span className="text-xs font-normal text-muted">({state.lan.bridge})</span>
          <Badge tone="default">Legacy{state.lan.dhcp_enabled ? "" : " · unused"}</Badge>
        </summary>
        <div className="px-4 pb-4">
          <p className="mb-3 text-sm text-muted">
            This is the appliance&apos;s <strong>legacy base bridge</strong>. Configured Guest VLANs, gateways, DHCP
            pools, and captive-portal settings are managed under{" "}
            <a href="/network" className="text-brand hover:underline">Guest Networks</a> and{" "}
            <a href="/network/dhcp" className="text-brand hover:underline">DHCP &amp; leases</a> — not here. It is normal
            for this bridge to have DHCP off when your guests are served by Guest Networks.
          </p>
          <div className="space-y-1 text-sm">
            <Row k="Physical interface" v={<code>{state.lan.physical_interface}</code>} />
            <Row k="Bridge" v={<code>{state.lan.bridge}</code>} />
            <Row k="MAC address" v={<code>{state.lan.mac}</code>} />
            <Row k="Link" v={<StateBadge ok={state.lan.link_up} label={state.lan.link_up ? "up" : "down"} />} />
            <Row k="Base gateway IP" v={<code>{state.lan.ip}/{state.lan.prefix_len}</code>} />
            <Row k="Subnet mask" v={<code>{state.lan.netmask}</code>} />
            {/* DHCP on the legacy bridge is informational, NOT a warning — guests
                use Guest Networks, so "off" here is expected. */}
            <Row k="DHCP (this bridge)" v={<Badge tone="default">{state.lan.dhcp_enabled ? "enabled" : "off — guests use Guest Networks"}</Badge>} />
            {state.lan.dhcp_enabled && <Row k="DHCP range" v={<code>{state.lan.dhcp_start} – {state.lan.dhcp_end}</code>} />}
            {state.lan.dhcp_enabled && <Row k="Lease time" v={`${state.lan.dhcp_lease_seconds}s`} />}
            <Row k="DNS to clients" v={<code>{state.lan.dns.join(", ")}</code>} />
            <Row k="Bridge members" v={<code>{state.lan.members.join(", ") || "—"}</code>} />
          </div>
        </div>
      </details>

      {/* edit form */}
      {writable && (
        <Card>
          <CardHeader><CardTitle>Change configuration</CardTitle></CardHeader>
          <CardBody className="space-y-4">
            <div className="grid gap-4 md:grid-cols-2">
              <fieldset className="space-y-2 rounded border border-neutral-200 p-3">
                <legend className="px-1 text-xs font-semibold uppercase text-neutral-500">WAN / Management ({state.wan.interface})</legend>
                <Field label="IP address"><Input value={wanIp} onChange={(e) => setWanIp(e.target.value)} /></Field>
                <Field label="Prefix length"><Input type="number" value={wanPrefix} onChange={(e) => setWanPrefix(Number(e.target.value))} /></Field>
                <Field label="Default gateway"><Input value={wanGw} onChange={(e) => setWanGw(e.target.value)} /></Field>
                <Field label="DNS (comma separated)"><Input value={wanDns} onChange={(e) => setWanDns(e.target.value)} /></Field>
              </fieldset>
              <fieldset className="space-y-2 rounded border border-neutral-200 p-3">
                <legend className="px-1 text-xs font-semibold uppercase text-neutral-500">Base LAN / Legacy bridge ({state.lan.bridge})</legend>
                <Field label="Base gateway IP"><Input value={lanIp} onChange={(e) => setLanIp(e.target.value)} /></Field>
                <Field label="Prefix length"><Input type="number" value={lanPrefix} onChange={(e) => setLanPrefix(Number(e.target.value))} /></Field>
                {/* Carryover A — DHCP has ONE source of truth: the Guest Networks
                    pages (Site DB → Kea). Shown read-only here to avoid a second,
                    conflicting editor for the same Kea scope. */}
                <div className="rounded border border-neutral-200 bg-neutral-50 p-2 text-xs text-neutral-600">
                  <div className="mb-1 font-semibold uppercase text-neutral-500">DHCP (read-only)</div>
                  <div>This is the legacy base bridge. Guest VLAN DHCP scopes, lease times, reservations and Option 114
                    are managed per guest network in{" "}
                    <a href="/network/dhcp" className="text-blue-600 underline">Guest Networks → DHCP &amp; leases</a> (single source of truth).</div>
                </div>
              </fieldset>
            </div>

            {mgmtWillChange && (
              <div className="rounded border border-amber-300 bg-amber-50 p-3 text-sm text-amber-800">
                <b>⚠ Changing the WAN IP changes the management URL.</b> After applying you must reconnect at{" "}
                <b>https://{wanIp}</b> and confirm there, or the change auto-rolls-back.
              </div>
            )}

            <div className="flex gap-2">
              <Button variant="secondary" onClick={doValidate} disabled={busy}>Validate &amp; preview</Button>
            </div>

            {/* validation + before/after */}
            {validation && (
              <div className="space-y-3">
                {validation.validation.ok ? (
                  <div className="flex items-center gap-2 text-emerald-700"><CheckCircle2 className="h-4 w-4" />Configuration is valid.</div>
                ) : (
                  <div className="space-y-1">
                    <div className="flex items-center gap-2 text-red-700"><XCircle className="h-4 w-4" />Validation failed:</div>
                    <ul className="ml-6 list-disc text-sm text-red-700">
                      {validation.validation.issues?.map((i, n) => <li key={n}><code>{i.field}</code> — {i.message}</li>)}
                    </ul>
                  </div>
                )}
                <div className="grid gap-3 md:grid-cols-2">
                  <BeforeAfter title="WAN" before={`${state.wan.ip}/${state.wan.prefix_len} gw ${state.wan.gateway} dns ${state.wan.dns.join(",")}`} after={`${wanIp}/${wanPrefix} gw ${wanGw} dns ${wanDns}`} />
                  <BeforeAfter title="LAN" before={`${state.lan.ip}/${state.lan.prefix_len} dhcp ${state.lan.dhcp_start}-${state.lan.dhcp_end}`} after={`${lanIp}/${lanPrefix} dhcp ${dhcpEnabled ? `${dhcpStart}-${dhcpEnd}` : "off"}`} />
                </div>
                <div className="text-sm">New management URL: <b>{validation.management_url}</b></div>

                {validation.validation.ok && (
                  <div className="flex items-end gap-2 rounded border border-neutral-200 bg-neutral-50 p-3">
                    <Field label="Confirm password to apply">
                      <Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} placeholder="your password" />
                    </Field>
                    <Button onClick={doApply} disabled={busy || !password}>Apply change</Button>
                  </div>
                )}
              </div>
            )}
          </CardBody>
        </Card>
      )}

      {/* diagnostics */}
      <Card>
        <CardHeader><CardTitle className="flex items-center justify-between">Diagnostics
          <span className="flex gap-2">
            <Button variant="secondary" onClick={loadDiag}>Run diagnostics</Button>
            {diag && <Button variant="secondary" onClick={downloadDiag}><Download className="mr-1 h-4 w-4" />Download report</Button>}
          </span>
        </CardTitle></CardHeader>
        {diag && (
          <CardBody className="space-y-3">
            {Object.entries(diag).map(([k, v]) => (
              <div key={k}>
                <div className="text-xs font-semibold uppercase text-neutral-500">{k}</div>
                <pre className="overflow-x-auto rounded bg-neutral-900 p-2 text-xs text-neutral-100">{v}</pre>
              </div>
            ))}
          </CardBody>
        )}
      </Card>

      {/* history */}
      <Card>
        <CardHeader><CardTitle>Change history</CardTitle></CardHeader>
        <CardBody>
          <Table>
            <THead><TR><TH>When</TH><TH>Actor</TH><TH>Source</TH><TH>Action</TH><TH>Target</TH><TH>Result</TH></TR></THead>
            <tbody>
              {history.map((h, n) => (
                <TR key={n}>
                  <TD>{h.at}</TD><TD>{h.actor}</TD><TD>{h.source_ip}</TD><TD>{h.action}</TD><TD>{h.target}</TD>
                  <TD>{h.apply_result || h.confirm_result || h.rollback_result || h.failure_reason || "—"}</TD>
                </TR>
              ))}
              {history.length === 0 && <TR><TD colSpan={6} className="text-neutral-400">No changes recorded yet.</TD></TR>}
            </tbody>
          </Table>
        </CardBody>
      </Card>
    </div>
  );
}

function Row({ k, v }: { k: string; v: React.ReactNode }) {
  return <div className="flex justify-between gap-4 border-b border-neutral-100 py-1"><span className="text-neutral-500">{k}</span><span className="text-right">{v}</span></div>;
}
function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return <div className="space-y-1"><Label>{label}</Label>{children}</div>;
}
function BeforeAfter({ title, before, after }: { title: string; before: string; after: string }) {
  return (
    <div className="rounded border border-neutral-200 p-2 text-xs">
      <div className="font-semibold">{title}</div>
      <div className="text-neutral-500">before: <code>{before}</code></div>
      <div className="text-neutral-900">after: <code>{after}</code></div>
    </div>
  );
}
