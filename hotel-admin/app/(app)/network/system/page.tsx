"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import {
  api, SysNetState, SysNetProposal, SysNetValidateResp,
  SysNetApplyResp, SysNetAudit,
} from "@/lib/api";
import { Card, CardBody, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { ConfirmDialog } from "@/components/ui/dialog";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { PageShell, PageHeader } from "@/components/ui/page";
import { KeyValueGrid } from "@/components/ui/data";
import { Skeleton } from "@/components/ui/misc";
import { EmptyState } from "@/components/ui/empty-state";
import { PendingChangeBanner, ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { errMsg } from "@/lib/utils";
import { ValidationIssueList, useNetworkAccess } from "@/components/network/shared";
import { HelpList, HelpSection } from "@/components/help";
import {
  Router, Download, RefreshCw, Archive, Network, ArrowRight, Stethoscope, ChevronDown, History, CheckCircle2,
} from "lucide-react";

function Conn({ ok, label }: { ok: boolean; label: string }) {
  return <Badge tone={ok ? "ok" : "err"} dot>{label}: {ok ? "OK" : "Failing"}</Badge>;
}
function LinkBadge({ up }: { up: boolean }) {
  return <Badge tone={up ? "ok" : "err"}>{up ? "Link up" : "Link down"}</Badge>;
}
/** The history's timestamps come as text; show them localised when they parse, verbatim when they do not. */
function when(s: string): string {
  const t = Date.parse(/[+-]\d{2}$/.test(s) ? `${s}:00` : s);
  return Number.isFinite(t) ? new Date(t).toLocaleString() : s;
}
const mono = (v: React.ReactNode) => <span className="font-mono text-xs">{v}</span>;

export default function NetworkSettingsPage() {
  const { known, writable } = useNetworkAccess();
  const [state, setState] = useState<SysNetState | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();

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
  const [applyResp, setApplyResp] = useState<SysNetApplyResp | null>(null);
  const [countdown, setCountdown] = useState<number>(0);
  const [history, setHistory] = useState<SysNetAudit[] | null>(null);
  const [diag, setDiag] = useState<Record<string, string> | null>(null);
  const [diagBusy, setDiagBusy] = useState(false);
  const timer = useRef<ReturnType<typeof setInterval> | null>(null);

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
    catch { setHistory((h) => h ?? []); /* non-fatal */ }
  }

  useEffect(() => {
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
      setValidation(r);
    } catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
  }

  function startCountdown(deadlineUnix: number) {
    if (timer.current) clearInterval(timer.current);
    const tick = () => {
      const left = Math.max(0, deadlineUnix - Math.floor(Date.now() / 1000));
      setCountdown(left);
      if (left <= 0) { if (timer.current) clearInterval(timer.current); load(); }
    };
    tick(); timer.current = setInterval(tick, 1000);
  }

  // Applying a WAN/LAN change needs the operator's password (the server requires it). It is asked for in a
  // masked field inside the confirmation, which also says what applying will do.
  const [applying, setApplying] = useState(false);
  const [applyErr, setApplyErr] = useState<string | null>(null);

  async function doApply({ password }: { reason: string; password: string }) {
    if (!password) { setApplyErr("Confirm your password to apply a network change."); return; }
    setApplyErr(null); setErr(null); setBusy(true);
    try {
      const r = await api.post<SysNetApplyResp>("/network/system/apply", { proposal: proposal(), password });
      setApplyResp(r);
      setApplying(false);
      if (r.state === "pending_confirmation" && r.deadline_unix) startCountdown(r.deadline_unix);
      else if (r.state === "failed" || r.state === "rolled_back") setErr(r.message || `Apply ${r.state === "failed" ? "failed" : "rolled back"}.`);
      loadHistory();
    } catch (e) { setApplyErr(errMsg(e)); } finally { setBusy(false); }
  }

  async function doConfirm() {
    setBusy(true); setErr(null);
    try {
      await api.post("/network/system/confirm", {});
      if (timer.current) clearInterval(timer.current);
      setApplyResp(null); setCountdown(0); setValidation(null);
      toast.success("Configuration kept", "The new WAN / LAN settings are now permanent.");
      await load(); loadHistory();
    } catch (e) { setErr(errMsg(e)); } finally { setBusy(false); }
  }

  // Rolling back needs the password too (the server requires it). It is typed into a masked field — never a
  // browser prompt, which shows what is typed, often with somebody standing next to the operator.
  const [rollingBack, setRollingBack] = useState(false);
  const [rollbackErr, setRollbackErr] = useState<string | null>(null);

  async function doRollback({ password }: { reason: string; password: string }) {
    setBusy(true); setRollbackErr(null);
    try {
      await api.post("/network/system/rollback", { password });
      setRollingBack(false);
      if (timer.current) clearInterval(timer.current);
      setApplyResp(null); setCountdown(0);
      toast.success("Rolled back", "The previous WAN / LAN settings are back in place.");
      await load(); loadHistory();
    } catch (e) { setRollbackErr(errMsg(e)); } finally { setBusy(false); }
  }

  async function loadDiag() {
    setDiagBusy(true);
    try { setDiag((await api.get<{ diagnostics: Record<string, string> }>("/network/system/diagnostics")).diagnostics); }
    catch (e) { setErr(errMsg(e)); }
    finally { setDiagBusy(false); }
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

  const header = (
    <PageHeader
      icon={<Router />}
      eyebrow="Networking"
      title="WAN / LAN settings"
      description="The appliance's own internet uplink and management address."
      help={
        <>
          <HelpSection title="What this page covers">
            <p>
              Only the appliance&rsquo;s WAN / management uplink and the legacy base bridge. Guest Wi-Fi is configured
              under <strong>Guest networks</strong> (VLAN, gateway, sign-in page) and <strong>DHCP &amp; leases</strong>{" "}
              (address pools, lease times, reservations).
            </p>
          </HelpSection>
          <HelpSection title="How a change is applied">
            <HelpList
              items={[
                <><strong>Validate &amp; preview</strong> checks the change and shows the before and after.</>,
                <><strong>Apply change</strong> asks for your password and puts it live immediately.</>,
                "A confirmation timer then starts. Keep the change before it runs out, or the appliance rolls back on its own.",
                "Every apply, keep and roll back is recorded in the change history.",
              ]}
            />
          </HelpSection>
          <HelpSection title="Legacy base bridge">
            <p>
              The appliance&rsquo;s original LAN bridge. Guest networks do not use it, so it is normal for its DHCP to be
              off. DHCP has one source of truth, the guest network pages, and is not edited here.
            </p>
          </HelpSection>
          <HelpSection title="Diagnostics">
            <p>
              A read-only snapshot of addresses, routes and reachability. Download the report to send it to support.
            </p>
          </HelpSection>
        </>
      }
      actions={
        <Button variant="secondary" onClick={() => { setErr(null); load(); loadHistory(); }}>
          <RefreshCw /> Refresh
        </Button>
      }
    />
  );

  if (!state) {
    return (
      <PageShell>
        {header}
        <ErrorBanner err={err} className="mb-0" />
        <div className="grid gap-4 md:grid-cols-2" aria-busy={!err}>
          <span className="sr-only">{err ? "" : "Loading network settings"}</span>
          <Skeleton className="h-72 w-full" />
          <Skeleton className="h-72 w-full" />
        </div>
      </PageShell>
    );
  }

  const mgmtWillChange = wanIp !== state.wan.ip;
  const pendingNow = !!state.pending || applyResp?.state === "pending_confirmation";
  const reconnectUrl = applyResp?.management_url || state.pending?.management_url;

  return (
    <PageShell>
      {header}

      {known && !writable && <ReadOnlyNotice>Your role can view the WAN / LAN settings but not change them.</ReadOnlyNotice>}

      <ErrorBanner err={err} className="mb-0" />

      {pendingNow && (
        <PendingChangeBanner
          title="Change applied — confirmation required"
          description={
            <>
              The new configuration is live but will <strong>roll back automatically in {countdown}s</strong> unless
              you keep it.
              {reconnectUrl && (
                <> If you changed the WAN IP, reconnect at <strong className="font-mono">{reconnectUrl}</strong> and keep it from there.</>
              )}
            </>
          }
          secondsLeft={countdown}
          canAct={writable}
          busy={busy ? (rollingBack ? "rollback" : "confirm") : null}
          confirmLabel="Keep this configuration"
          rollbackLabel="Roll back now"
          onConfirm={doConfirm}
          onRollback={() => { setRollbackErr(null); setRollingBack(true); }}
        >
          {applyResp?.verify && Object.keys(applyResp.verify).length > 0 ? (
            <div className="space-y-2">
              <div className="text-label">Checks after applying</div>
              <div className="flex flex-wrap gap-2">
                {Object.entries(applyResp.verify).map(([k, ok]) => (
                  <Badge key={k} tone={ok ? "ok" : "err"}>{k}: {ok ? "passed" : "failed"}</Badge>
                ))}
              </div>
            </div>
          ) : undefined}
        </PendingChangeBanner>
      )}

      {/* status cards */}
      <div className="grid gap-5 md:grid-cols-2">
        <Card>
          <CardHeader>
            <div className="space-y-1">
              <CardTitle className="flex items-center gap-2"><Router className="size-4" aria-hidden /> WAN / Management</CardTitle>
              <CardDescription>The uplink to the internet, and the address you reach this admin on.</CardDescription>
            </div>
            <LinkBadge up={state.wan.link_up} />
          </CardHeader>
          <CardBody className="space-y-4">
            <div className="flex flex-wrap gap-2" aria-label="Connectivity">
              <Conn ok={state.wan.connectivity.gateway_reachable} label="Gateway" />
              <Conn ok={state.wan.connectivity.internet_ok} label="Internet" />
              <Conn ok={state.wan.connectivity.dns_ok} label="DNS" />
            </div>
            {state.wan.drift && (
              <Callout tone="warning" title="Running address differs from the saved one">
                The saved configuration says {mono(state.wan.persistent_ip)}. The appliance may return to it after a restart.
              </Callout>
            )}
            <KeyValueGrid
              items={[
                { label: "IP address", value: mono(`${state.wan.ip}/${state.wan.prefix_len}`) },
                { label: "IP mode", value: state.wan.mode === "static" ? "Static" : state.wan.mode === "dhcp" ? "DHCP" : state.wan.mode },
                { label: "Default gateway", value: mono(state.wan.gateway) },
                { label: "DNS", value: mono(state.wan.dns.join(", ") || "—") },
                { label: "Subnet mask", value: mono(state.wan.netmask) },
                { label: "Physical interface", value: mono(state.wan.interface) },
                { label: "MAC address", value: mono(state.wan.mac) },
                { label: "Outbound interface", value: mono(state.wan.outbound_interface) },
                {
                  label: "Management URL",
                  value: <a className="break-all font-mono text-xs text-primary underline" href={state.wan.management_url}>{state.wan.management_url}</a>,
                  wide: true,
                },
              ]}
            />
          </CardBody>
        </Card>

        {/* Guest networks pointer — the real guest-facing config lives there, NOT on the legacy base bridge below. */}
        <Card>
          <CardHeader>
            <div className="space-y-1">
              <CardTitle className="flex items-center gap-2"><Network className="size-4" aria-hidden /> Guest Wi-Fi is configured elsewhere</CardTitle>
              <CardDescription>This page covers only the uplink and the legacy base bridge.</CardDescription>
            </div>
          </CardHeader>
          <CardBody className="space-y-3 text-sm">
            <div className="grid gap-2">
              <Link href="/network" className="group flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2.5 hover:border-border-strong hover:bg-accent/50">
                <span><span className="font-medium">Guest networks</span><span className="block text-caption text-muted-foreground">Create and edit guest networks, gateways and portal</span></span>
                <ArrowRight className="size-4 shrink-0 text-muted-foreground rtl:rotate-180" aria-hidden />
              </Link>
              <Link href="/network/dhcp" className="group flex items-center justify-between gap-3 rounded-md border border-border px-3 py-2.5 hover:border-border-strong hover:bg-accent/50">
                <span><span className="font-medium">DHCP &amp; leases</span><span className="block text-caption text-muted-foreground">Address pools, reservations and active leases</span></span>
                <ArrowRight className="size-4 shrink-0 text-muted-foreground rtl:rotate-180" aria-hidden />
              </Link>
            </div>
          </CardBody>
        </Card>
      </div>

      {/* Legacy base bridge — clearly demarcated so it is never mistaken for an active guest network. */}
      <details className="group rounded-lg border border-border bg-card shadow-card">
        <summary className="flex cursor-pointer select-none flex-wrap items-center gap-2 px-5 py-4 text-emphasis [&::-webkit-details-marker]:hidden">
          <ChevronDown className="size-4 text-muted-foreground transition-transform group-open:rotate-180" aria-hidden />
          <Archive className="size-4 text-muted-foreground" aria-hidden /> Advanced · Base LAN / Legacy bridge
          <span className="font-mono text-xs font-normal text-muted-foreground">{state.lan.bridge}</span>
          <Badge tone="default">Legacy{state.lan.dhcp_enabled ? "" : " · unused"}</Badge>
        </summary>
        <div className="space-y-4 border-t border-border px-5 py-4">
          <p className="text-sm text-muted-foreground">
            Not a guest network — guests are managed under{" "}
            <Link href="/network" className="text-primary underline">Guest networks</Link>.
          </p>
          <KeyValueGrid
            columns={3}
            items={[
              { label: "Base gateway IP", value: mono(`${state.lan.ip}/${state.lan.prefix_len}`) },
              { label: "Subnet mask", value: mono(state.lan.netmask) },
              { label: "Link", value: <LinkBadge up={state.lan.link_up} /> },
              { label: "Physical interface", value: mono(state.lan.physical_interface) },
              { label: "Bridge", value: mono(state.lan.bridge) },
              { label: "MAC address", value: mono(state.lan.mac) },
              // DHCP on the legacy bridge is informational, NOT a warning — guests use guest networks.
              { label: "DHCP (this bridge)", value: <Badge tone="default">{state.lan.dhcp_enabled ? "Enabled" : "Off — guests use guest networks"}</Badge> },
              ...(state.lan.dhcp_enabled ? [
                { label: "DHCP range", value: mono(`${state.lan.dhcp_start} – ${state.lan.dhcp_end}`) },
                { label: "Lease time", value: `${state.lan.dhcp_lease_seconds}s` },
              ] : []),
              { label: "DNS to clients", value: mono(state.lan.dns.join(", ") || "—") },
              { label: "Bridge members", value: mono((state.lan.members ?? []).join(", ") || "—") },
            ]}
          />
        </div>
      </details>

      {/* edit form */}
      {writable && (
        <Card>
          <CardHeader>
            <div className="space-y-1">
              <CardTitle>Change configuration</CardTitle>
              <CardDescription>Validate &amp; preview first. Applying asks for your password and rolls back on its own unless you keep it.</CardDescription>
            </div>
          </CardHeader>
          <CardBody className="space-y-5">
            <div className="grid gap-5 md:grid-cols-2">
              <fieldset className="space-y-3 rounded-md border border-border p-4">
                <legend className="px-1 text-micro uppercase tracking-[0.06em] text-muted-foreground">
                  WAN / Management · <span className="font-mono normal-case">{state.wan.interface}</span>
                </legend>
                <Field label="IP address"><Input value={wanIp} onChange={(e) => { setWanIp(e.target.value);}} className="font-mono" /></Field>
                <Field label="Prefix length"><Input type="number" value={wanPrefix} onChange={(e) => { setWanPrefix(Number(e.target.value));}} /></Field>
                <Field label="Default gateway"><Input value={wanGw} onChange={(e) => { setWanGw(e.target.value);}} className="font-mono" /></Field>
                <Field label="DNS servers" hint="Separate several with commas."><Input value={wanDns} onChange={(e) => { setWanDns(e.target.value);}} className="font-mono" /></Field>
              </fieldset>
              <fieldset className="space-y-3 rounded-md border border-border p-4">
                <legend className="px-1 text-micro uppercase tracking-[0.06em] text-muted-foreground">
                  Base LAN / Legacy bridge · <span className="font-mono normal-case">{state.lan.bridge}</span>
                </legend>
                <Field label="Base gateway IP"><Input value={lanIp} onChange={(e) => { setLanIp(e.target.value);}} className="font-mono" /></Field>
                <Field label="Prefix length"><Input type="number" value={lanPrefix} onChange={(e) => { setLanPrefix(Number(e.target.value));}} /></Field>
                {/* DHCP has ONE source of truth: the guest network pages. Shown read-only here to avoid a second,
                    conflicting editor for the same scope. */}
                <p className="text-caption text-muted-foreground">
                  DHCP is not edited here; see{" "}
                  <Link href="/network/dhcp" className="text-primary underline">DHCP &amp; leases</Link>.
                </p>
              </fieldset>
            </div>

            {mgmtWillChange && (
              <Callout tone="warning" title="Changing the WAN IP changes the admin address">
                After applying you must reconnect at <strong className="font-mono">https://{wanIp}</strong> and keep the
                change there, or it rolls back automatically.
              </Callout>
            )}

            {/* validation + before/after */}
            {validation && (
              <div className="space-y-4">
                {validation.validation.ok ? (
                  <Callout tone="success" title="Configuration is valid">Review the change below, then apply it.</Callout>
                ) : (
                  <Callout tone="danger" title="Validation failed">
                    <ValidationIssueList issues={validation.validation.issues ?? []} className="mt-1" />
                  </Callout>
                )}
                <div className="overflow-hidden rounded-md border border-border">
                  <Table>
                    <THead><TR><TH>Setting</TH><TH>Before</TH><TH>After</TH></TR></THead>
                    <TBody>
                      <TR>
                        <TD className="font-medium">WAN</TD>
                        <TD className="font-mono text-xs text-muted-foreground">{`${state.wan.ip}/${state.wan.prefix_len} gw ${state.wan.gateway} dns ${state.wan.dns.join(",")}`}</TD>
                        <TD className="font-mono text-xs">{`${wanIp}/${wanPrefix} gw ${wanGw} dns ${wanDns}`}</TD>
                      </TR>
                      <TR>
                        <TD className="font-medium">LAN</TD>
                        <TD className="font-mono text-xs text-muted-foreground">{`${state.lan.ip}/${state.lan.prefix_len} dhcp ${state.lan.dhcp_start}-${state.lan.dhcp_end}`}</TD>
                        <TD className="font-mono text-xs">{`${lanIp}/${lanPrefix} dhcp ${dhcpEnabled ? `${dhcpStart}-${dhcpEnd}` : "off"}`}</TD>
                      </TR>
                      <TR>
                        <TD className="font-medium">Management URL</TD>
                        <TD className="font-mono text-xs text-muted-foreground">{state.wan.management_url}</TD>
                        <TD className="font-mono text-xs font-semibold">{validation.management_url}</TD>
                      </TR>
                    </TBody>
                  </Table>
                </div>
              </div>
            )}
          </CardBody>
          <CardFooter className="justify-end">
            <Button variant="secondary" onClick={doValidate} disabled={busy}>
              <CheckCircle2 /> {busy && !applying ? "Validating…" : "Validate & preview"}
            </Button>
            {validation?.validation.ok && (
              <Button onClick={() => { setApplyErr(null); setApplying(true); }} disabled={busy}>
                Apply change…
              </Button>
            )}
          </CardFooter>
        </Card>
      )}

      {/* diagnostics */}
      <Card>
        <CardHeader>
          <div className="space-y-1">
            <CardTitle className="flex items-center gap-2"><Stethoscope className="size-4" aria-hidden /> Diagnostics</CardTitle>
            <CardDescription>A read-only snapshot of addresses, routes and reachability.</CardDescription>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button variant="secondary" size="sm" onClick={loadDiag} disabled={diagBusy}>
              {diagBusy ? "Running…" : diag ? "Run again" : "Run diagnostics"}
            </Button>
            {diag && <Button variant="secondary" size="sm" onClick={downloadDiag}><Download /> Download report</Button>}
          </div>
        </CardHeader>
        {diag && (
          <CardBody className="space-y-3">
            {Object.entries(diag).map(([k, v]) => (
              <div key={k} className="space-y-1">
                <div className="text-micro uppercase tracking-[0.06em] text-muted-foreground">{k}</div>
                <pre className="max-h-72 overflow-auto rounded-md border border-border bg-surface p-3 font-mono text-xs text-foreground">{v}</pre>
              </div>
            ))}
          </CardBody>
        )}
      </Card>

      {/* history */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2"><History className="size-4" aria-hidden /> Change history</CardTitle>
        </CardHeader>
        {history === null ? (
          <CardBody><Skeleton className="h-20 w-full" /></CardBody>
        ) : history.length === 0 ? (
          <EmptyState icon={<History />} title="No changes recorded yet" hint="Every apply, keep and roll back of these settings is listed here." />
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>When</TH><TH>Operator</TH><TH className="hidden md:table-cell">Source</TH>
                <TH>Action</TH><TH className="hidden lg:table-cell">Target</TH><TH className="hidden sm:table-cell">Result</TH>
              </TR>
            </THead>
            <TBody>
              {history.map((h, n) => (
                <TR key={n}>
                  <TD className="whitespace-nowrap text-muted-foreground">{when(h.at)}</TD>
                  <TD>{h.actor}</TD>
                  <TD className="hidden font-mono text-xs md:table-cell">{h.source_ip}</TD>
                  <TD>{h.action}</TD>
                  <TD className="hidden font-mono text-xs lg:table-cell">{h.target}</TD>
                  <TD className="hidden sm:table-cell">{h.apply_result || h.confirm_result || h.rollback_result || h.failure_reason || "—"}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      </Card>

      <ConfirmDialog
        open={applying}
        onOpenChange={(v) => { if (!v) setApplying(false); }}
        title="Apply this network change?"
        description="The new WAN / LAN settings go live as soon as you confirm."
        consequences={[
          "The change takes effect immediately.",
          "If nobody keeps it before the timer runs out, the appliance puts the previous settings back on its own.",
          ...(mgmtWillChange ? [<>The admin address changes to <strong className="font-mono">https://{wanIp}</strong>. Reconnect there to keep the change.</>] : []),
        ]}
        confirmLabel="Apply change"
        busy={busy}
        error={applyErr}
        requirePassword
        passwordLabel="Confirm your password to apply"
        onConfirm={doApply}
      />

      <ConfirmDialog
        open={rollingBack}
        onOpenChange={(v) => !v && setRollingBack(false)}
        title="Roll back the network configuration?"
        description="The appliance returns to the WAN and LAN settings that were in force before this change."
        consequences={[
          "If you are connected through the address this change created, you will lose this page and have to reach the appliance on its previous address.",
        ]}
        confirmLabel="Roll back now"
        confirmVariant="danger"
        busy={busy}
        error={rollbackErr}
        requirePassword
        onConfirm={doRollback}
      />
    </PageShell>
  );
}
