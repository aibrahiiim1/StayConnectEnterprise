"use client";

import { useEffect, useRef, useState } from "react";
import Link from "next/link";
import {
  api, inBackground, SysNetState, SysNetProposal, SysNetValidateResp,
  SysNetApplyResp, SysNetAudit,
} from "@/lib/api";
import { SectionCard } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Field, FieldGrid, Input } from "@/components/ui/input";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { ConfirmDialog } from "@/components/ui/dialog";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { EmptyState } from "@/components/ui/empty-state";
import { PageShell, PageHeader } from "@/components/ui/page";
import { DescriptionList } from "@/components/ui/data";
import { Skeleton } from "@/components/ui/misc";
import { ChangeDiff, HistoryList } from "@/components/ui/history";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { LiveStatus, PendingChangeBanner, ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { errMsg } from "@/lib/utils";
import { usePoll } from "@/lib/use-poll";
import { ValidationIssueList, useNetworkAccess } from "@/components/network/shared";
import { HelpList, HelpSection } from "@/components/help";
import { AlertTriangle, Download, RefreshCw, ChevronDown, CheckCircle2 } from "lucide-react";

function Conn({ ok, label }: { ok: boolean; label: string }) {
  return <StatusBadge tone={ok ? "ok" : "err"}>{label}: {ok ? "OK" : "Failing"}</StatusBadge>;
}
function LinkBadge({ up }: { up: boolean }) {
  return <StatusBadge tone={up ? "ok" : "err"}>{up ? "Link up" : "Link down"}</StatusBadge>;
}
function when(s: string): string {
  const t = Date.parse(/[+-]\d{2}$/.test(s) ? `${s}:00` : s);
  return Number.isFinite(t) ? new Date(t).toLocaleString() : s;
}
function clock(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}
function duration(seconds: number): string {
  if (seconds % 86400 === 0) return `${seconds / 86400} day${seconds === 86400 ? "" : "s"}`;
  if (seconds % 3600 === 0) return `${seconds / 3600} hour${seconds === 3600 ? "" : "s"}`;
  if (seconds % 60 === 0) return `${seconds / 60} minutes`;
  return `${seconds} seconds`;
}
function prefixProblem(v: string, min: number, max: number): string | undefined {
  const n = Number(v);
  if (!v.trim() || !Number.isInteger(n)) return "Enter a whole number.";
  if (n < min || n > max) return `Use a value from ${min} to ${max}.`;
  return undefined;
}

const ACTION_WORDS: Record<string, string> = {
  "network.apply.requested": "Apply requested",
  "network.apply.rejected": "Apply refused",
  "network.apply.started": "Apply started",
  "network.apply.failed": "Apply failed",
  "network.apply.verify_failed": "Checks after applying failed",
  "network.apply.pending_confirmation": "Applied, waiting to be kept",
  "network.apply.confirmed": "Kept",
  "network.apply.confirmation_timeout": "Not kept in time",
  "network.rollback.automatic.started": "Automatic roll back started",
  "network.rollback.automatic.succeeded": "Rolled back automatically",
  "network.rollback.automatic.failed": "Automatic roll back failed",
  "network.rollback.manual.started": "Roll back started",
  "network.rollback.manual.succeeded": "Rolled back",
  "network.rollback.manual.failed": "Roll back failed",
  "network.reconcile.pending_within_window": "Pending change found after a restart",
  "network.reconcile.stale_pending": "Expired pending change cleared after a restart",
};
const actionWords = (a: string) => ACTION_WORDS[a] ?? a;

const mono = (v: React.ReactNode) => <span className="font-mono text-xs">{v}</span>;
const same = <span className="text-xs text-muted-foreground">No change</span>;
const auditResult = (h: SysNetAudit) => h.apply_result || h.confirm_result || h.rollback_result || h.failure_reason || "";
const HISTORY_PREVIEW = 8;

type Busy = null | "validate" | "apply" | "confirm" | "rollback";

export default function NetworkSettingsPage() {
  const { known, writable } = useNetworkAccess();
  const [state, setState] = useState<SysNetState | null>(null);
  const [loadErr, setLoadErr] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState<Busy>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const toast = useToast();

  const [wanIp, setWanIp] = useState("");
  const [wanPrefix, setWanPrefix] = useState("24");
  const [wanGw, setWanGw] = useState("");
  const [wanDns, setWanDns] = useState("");
  const [lanIp, setLanIp] = useState("");
  const [lanPrefix, setLanPrefix] = useState("24");

  const [validation, setValidation] = useState<SysNetValidateResp | null>(null);
  const [validatedFor, setValidatedFor] = useState<string | null>(null);
  const [applyResp, setApplyResp] = useState<SysNetApplyResp | null>(null);
  const [countdown, setCountdown] = useState<number>(0);
  const [history, setHistory] = useState<SysNetAudit[] | null>(null);
  const [allHistory, setAllHistory] = useState(false);
  const [entry, setEntry] = useState<SysNetAudit | null>(null);
  const [diag, setDiag] = useState<Record<string, string> | null>(null);
  const [diagBusy, setDiagBusy] = useState(false);
  const [diagErr, setDiagErr] = useState<string | null>(null);
  const timer = useRef<ReturnType<typeof setInterval> | null>(null);

  const formKey = JSON.stringify([wanIp.trim(), wanPrefix.trim(), wanGw.trim(), wanDns.trim(), lanIp.trim(), lanPrefix.trim()]);
  const stateKey = (s: SysNetState) =>
    JSON.stringify([s.wan.ip, String(s.wan.prefix_len), s.wan.gateway, (s.wan.dns || []).join(", "), s.lan.ip, String(s.lan.prefix_len)]);
  const dirty = state !== null && formKey !== stateKey(state);
  const dirtyRef = useRef(false);
  dirtyRef.current = dirty;

  function seed(s: SysNetState) {
    setWanIp(s.wan.ip); setWanPrefix(String(s.wan.prefix_len)); setWanGw(s.wan.gateway);
    setWanDns((s.wan.dns || []).join(", "));
    setLanIp(s.lan.ip); setLanPrefix(String(s.lan.prefix_len));
  }

  async function load() {
    setRefreshing(true);
    try {
      const s = await api.get<SysNetState>("/network/system");
      setState(s);
      if (!dirtyRef.current) seed(s);
      if (s.pending) startCountdown(s.pending.deadline_unix);
      setLoadErr(null);
      setUpdatedAt(Date.now());
    } catch (e) { setLoadErr(errMsg(e)); }
    finally { setRefreshing(false); }
  }
  async function loadHistory() {
    try { setHistory((await api.get<{ history: SysNetAudit[] }>("/network/system/history")).history ?? []); }
    catch { setHistory((h) => h ?? []); }
  }

  useEffect(() => {
    load(); loadHistory();
    return () => { if (timer.current) clearInterval(timer.current); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const pendingNow = !!state?.pending || applyResp?.state === "pending_confirmation";
  usePoll(() => { void load(); }, 30000, { enabled: !!state && !pendingNow && busy === null });

  function proposal(): SysNetProposal {
    return {
      wan: {
        mode: "static", ip: wanIp.trim(), prefix_len: Number(wanPrefix), gateway: wanGw.trim(),
        dns: wanDns.split(",").map((s) => s.trim()).filter(Boolean),
      },
      lan: {
        ip: lanIp.trim(), prefix_len: Number(lanPrefix), dhcp_enabled: state?.lan.dhcp_enabled ?? false,
        dhcp_start: state?.lan.dhcp_start ?? "", dhcp_end: state?.lan.dhcp_end ?? "",
        dhcp_lease_seconds: state?.lan.dhcp_lease_seconds ?? 0,
      },
    };
  }

  const wanPrefixErr = prefixProblem(wanPrefix, 1, 32);
  const lanPrefixErr = prefixProblem(lanPrefix, 19, 30);
  const wanIpErr = wanIp.trim() ? undefined : "Enter the WAN IP address.";
  const wanGwErr = wanGw.trim() ? undefined : "Enter the default gateway.";
  const formBlocked = !!(wanPrefixErr || lanPrefixErr || wanIpErr || wanGwErr);
  const valid = validation && validatedFor === formKey ? validation : null;

  async function doValidate() {
    if (formBlocked) return;
    setErr(null); setBusy("validate"); setApplyResp(null);
    const key = formKey;
    try {
      const r = await api.post<SysNetValidateResp>("/network/system/validate", proposal());
      setValidation(r);
      setValidatedFor(key);
    } catch (e) { setErr(errMsg(e)); } finally { setBusy(null); }
  }

  function startCountdown(deadlineUnix: number) {
    if (timer.current) clearInterval(timer.current);
    const tick = () => {
      const left = Math.max(0, deadlineUnix - Math.floor(Date.now() / 1000));
      setCountdown(left);
      if (left <= 0) {
        if (timer.current) clearInterval(timer.current);
        setApplyResp(null);
        void inBackground(async () => { await load(); await loadHistory(); });
      }
    };
    tick(); timer.current = setInterval(tick, 1000);
  }

  const [applying, setApplying] = useState(false);
  const [applyErr, setApplyErr] = useState<string | null>(null);

  async function doApply({ password }: { reason: string; password: string }) {
    if (!password) { setApplyErr("Confirm your password to apply a network change."); return; }
    if (!valid?.validation.ok) { setApplyErr("Validate the change again before applying it."); return; }
    setApplyErr(null); setErr(null); setBusy("apply");
    try {
      const r = await api.post<SysNetApplyResp>("/network/system/apply", { proposal: proposal(), password });
      setApplyResp(r);
      setApplying(false);
      if (r.state === "pending_confirmation" && r.deadline_unix) startCountdown(r.deadline_unix);
      else if (r.state === "failed" || r.state === "rolled_back") setErr(r.message || `Apply ${r.state === "failed" ? "failed" : "rolled back"}.`);
      loadHistory();
    } catch (e) { setApplyErr(errMsg(e)); } finally { setBusy(null); }
  }

  async function doConfirm() {
    setBusy("confirm"); setErr(null);
    try {
      const revision = applyResp?.revision_id ?? state?.pending?.revision_id;
      await api.post("/network/system/confirm", revision ? { revision_id: revision } : {});
      if (timer.current) clearInterval(timer.current);
      setApplyResp(null); setCountdown(0); setValidation(null); setValidatedFor(null);
      toast.success("Configuration kept", "The new WAN / LAN settings are now permanent.");
      dirtyRef.current = false;
      await load(); loadHistory();
    } catch (e) { setErr(errMsg(e)); } finally { setBusy(null); }
  }

  const [rollingBack, setRollingBack] = useState(false);
  const [rollbackErr, setRollbackErr] = useState<string | null>(null);

  async function doRollback({ password }: { reason: string; password: string }) {
    setBusy("rollback"); setRollbackErr(null);
    try {
      await api.post("/network/system/rollback", { password });
      setRollingBack(false);
      if (timer.current) clearInterval(timer.current);
      setApplyResp(null); setCountdown(0); setValidation(null); setValidatedFor(null);
      toast.success("Rolled back", "The previous WAN / LAN settings are back in place.");
      dirtyRef.current = false;
      await load(); loadHistory();
    } catch (e) { setRollbackErr(errMsg(e)); } finally { setBusy(null); }
  }

  async function loadDiag() {
    setDiagBusy(true); setDiagErr(null);
    try { setDiag((await api.get<{ diagnostics: Record<string, string> }>("/network/system/diagnostics")).diagnostics); }
    catch (e) { setDiagErr(errMsg(e)); }
    finally { setDiagBusy(false); }
  }
  function downloadDiag() {
    if (!diag) return;
    const report = Object.entries(diag).map(([k, v]) => `===== ${k} =====\n${v}\n`).join("\n");
    const url = URL.createObjectURL(new Blob([report], { type: "text/plain" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = `network-diagnostics-${new Date().toISOString().slice(0, 19).replace(/:/g, "-")}.txt`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 0);
  }

  const header = (
    <PageHeader
      title="WAN / LAN settings"
      description={
        <>
          The appliance&rsquo;s own internet uplink and management address. Client Wi-Fi is under{" "}
          <Link href="/network" className="font-medium text-foreground underline underline-offset-2">Client networks</Link>.
        </>
      }
      meta={state && (
        <LiveStatus
          updatedAt={updatedAt}
          refreshing={refreshing}
          onRefresh={() => { setErr(null); void load(); void loadHistory(); }}
          intervalSeconds={pendingNow ? undefined : 30}
          error={!!loadErr}
        />
      )}
      help={
        <>
          <HelpSection title="What this page covers">
            <p>
              Only the appliance&rsquo;s WAN / management uplink and the legacy base bridge. Client Wi-Fi is configured
              under <strong>Client networks</strong> (VLAN, gateway, sign-in page) and <strong>DHCP &amp; leases</strong>{" "}
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
              The appliance&rsquo;s original LAN bridge. Client networks do not use it, so it is normal for its DHCP to be
              off. DHCP has one source of truth, the client network pages, and is not edited here.
            </p>
          </HelpSection>
          <HelpSection title="Diagnostics">
            <p>
              A read-only snapshot of addresses, routes and reachability. Download the report to send it to support.
            </p>
          </HelpSection>
        </>
      }
    />
  );

  if (!state) {
    return (
      <PageShell>
        {header}
        {loadErr ? (
          <EmptyState
            icon={<AlertTriangle />}
            title="The network settings could not be loaded"
            hint={loadErr}
            action={<Button variant="secondary" onClick={() => { void load(); void loadHistory(); }}><RefreshCw /> Try again</Button>}
          />
        ) : (
          <div className="grid gap-5 md:grid-cols-2" aria-busy="true">
            <span className="sr-only">Loading network settings</span>
            <Skeleton className="h-72 w-full" />
            <Skeleton className="h-72 w-full" />
          </div>
        )}
      </PageShell>
    );
  }

  const newUrl = valid?.management_url || `https://${wanIp.trim()}`;
  const mgmtWillChange = wanIp.trim() !== state.wan.ip;
  const reconnectUrl = applyResp?.management_url || state.pending?.management_url;
  const shownHistory = history ? (allHistory ? history : history.slice(0, HISTORY_PREVIEW)) : [];
  const wanBefore = `${state.wan.ip}/${state.wan.prefix_len} gw ${state.wan.gateway} dns ${state.wan.dns.join(", ")}`;
  const wanAfter = `${wanIp.trim()}/${wanPrefix.trim()} gw ${wanGw.trim()} dns ${wanDns.split(",").map((s) => s.trim()).filter(Boolean).join(", ")}`;
  const lanBefore = `${state.lan.ip}/${state.lan.prefix_len}`;
  const lanAfter = `${lanIp.trim()}/${lanPrefix.trim()}`;
  const urlAfter = valid?.management_url ?? state.wan.management_url;

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
              The new configuration is live but will <strong>roll back automatically in {clock(countdown)}</strong> unless
              you keep it.
              {reconnectUrl && (
                <> If you changed the WAN IP, reconnect at <strong className="font-mono">{reconnectUrl}</strong> and keep it from there.</>
              )}
            </>
          }
          secondsLeft={countdown}
          canAct={writable}
          busy={busy === "confirm" ? "confirm" : busy === "rollback" ? "rollback" : null}
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
                  <StatusBadge key={k} tone={ok ? "ok" : "err"}>{k}: {ok ? "passed" : "failed"}</StatusBadge>
                ))}
              </div>
            </div>
          ) : undefined}
        </PendingChangeBanner>
      )}

      <SectionCard
        title="WAN / management"
        description="The uplink to the internet, and the address you reach this admin on."
        actions={<LinkBadge up={state.wan.link_up} />}
        bodyClassName="space-y-4"
      >
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
        <DescriptionList
          columns={2}
          items={[
            { label: "IP address", value: mono(`${state.wan.ip}/${state.wan.prefix_len}`) },
            { label: "IP mode", value: state.wan.mode === "static" ? "Static" : state.wan.mode === "dhcp" ? "DHCP" : `Other (${state.wan.mode})` },
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
      </SectionCard>

      <details className="disclosure-motion group rounded-lg border border-border bg-card shadow-card">
        <summary className="flex cursor-pointer select-none flex-wrap items-center gap-2 rounded-lg px-5 py-3.5 text-emphasis focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden">
          <ChevronDown className="size-4 text-muted-foreground transition-transform duration-base ease-onegate group-open:rotate-180 motion-reduce:transition-none" aria-hidden />
          Base LAN / legacy bridge
          <span className="font-mono text-xs font-normal text-muted-foreground">{state.lan.bridge}</span>
          <Badge tone="default">Legacy{state.lan.dhcp_enabled ? "" : " · unused"}</Badge>
        </summary>
        <div className="space-y-3 border-t border-border px-5 py-4">
          <p className="text-sm text-muted-foreground">Not a client network; clients do not use it.</p>
          <DescriptionList
            columns={2}
            items={[
              { label: "Base gateway IP", value: mono(`${state.lan.ip}/${state.lan.prefix_len}`) },
              { label: "Subnet mask", value: mono(state.lan.netmask) },
              { label: "Link", value: <LinkBadge up={state.lan.link_up} /> },
              { label: "Physical interface", value: mono(state.lan.physical_interface) },
              { label: "Bridge", value: mono(state.lan.bridge) },
              { label: "MAC address", value: mono(state.lan.mac) },
              { label: "DHCP (this bridge)", value: state.lan.dhcp_enabled ? "Enabled" : "Off — clients use client networks" },
              { label: "DHCP range", value: mono(`${state.lan.dhcp_start} – ${state.lan.dhcp_end}`), hidden: !state.lan.dhcp_enabled },
              {
                label: "Lease time",
                value: <span className="tabular">{state.lan.dhcp_lease_seconds} s · {duration(state.lan.dhcp_lease_seconds)}</span>,
                hidden: !state.lan.dhcp_enabled,
              },
              { label: "DNS to clients", value: mono(state.lan.dns.join(", ") || "—") },
              { label: "Bridge members", value: mono((state.lan.members ?? []).join(", ") || "—") },
            ]}
          />
        </div>
      </details>

      {writable && (
        <SectionCard
          title="Change configuration"
          description="Validate & preview first. Applying asks for your password and rolls back on its own unless you keep it."
          bodyClassName="space-y-5"
          footer={
            <>
              <Button variant="secondary" onClick={doValidate} disabled={busy !== null || pendingNow || formBlocked}>
                <CheckCircle2 /> {busy === "validate" ? "Validating…" : "Validate & preview"}
              </Button>
              {valid?.validation.ok && !pendingNow ? (
                <Button onClick={() => { setApplyErr(null); setApplying(true); }} disabled={busy !== null}>
                  Apply change…
                </Button>
              ) : !pendingNow && (
                <span className="text-caption text-muted-foreground">
                  {validation && validatedFor !== formKey
                    ? "The settings changed since the last check. Validate again to apply."
                    : "Apply appears once the change passes validation."}
                </span>
              )}
            </>
          }
        >
          {pendingNow && (
            <Callout tone="info" title="A change is waiting to be kept">
              Keep it or roll it back above before making another change.
            </Callout>
          )}
          <fieldset disabled={pendingNow} className="grid min-w-0 gap-5 md:grid-cols-2">
            <fieldset className="min-w-0 space-y-3">
              <legend className="mb-3 text-label">
                WAN / management <span className="ms-1 font-mono text-xs font-normal text-muted-foreground">{state.wan.interface}</span>
              </legend>
              <FieldGrid cols={2}>
                <Field label="IP address" error={wanIpErr}>
                  <Input aria-required="true" value={wanIp} onChange={(e) => setWanIp(e.target.value)} className="font-mono" />
                </Field>
                <Field label="Prefix length" hint="1 to 32." error={wanPrefixErr}>
                  <Input type="number" inputMode="numeric" min={1} max={32} value={wanPrefix} onChange={(e) => setWanPrefix(e.target.value)} />
                </Field>
              </FieldGrid>
              <Field label="Default gateway" error={wanGwErr}>
                <Input aria-required="true" value={wanGw} onChange={(e) => setWanGw(e.target.value)} className="font-mono" />
              </Field>
              <Field label="DNS servers" optional hint="Separate several with commas.">
                <Input value={wanDns} onChange={(e) => setWanDns(e.target.value)} className="font-mono" />
              </Field>
            </fieldset>
            <fieldset className="min-w-0 space-y-3">
              <legend className="mb-3 text-label">
                Base LAN / legacy bridge <span className="ms-1 font-mono text-xs font-normal text-muted-foreground">{state.lan.bridge}</span>
              </legend>
              <FieldGrid cols={2}>
                <Field label="Base gateway IP">
                  <Input aria-required="true" value={lanIp} onChange={(e) => setLanIp(e.target.value)} className="font-mono" />
                </Field>
                <Field label="Prefix length" hint="19 to 30. /19 (8,190 addresses) is the widest a client network can be." error={lanPrefixErr}>
                  <Input type="number" inputMode="numeric" min={19} max={30} value={lanPrefix} onChange={(e) => setLanPrefix(e.target.value)} />
                </Field>
              </FieldGrid>
              <p className="text-caption text-muted-foreground">
                DHCP is not edited here; see{" "}
                <Link href="/network/dhcp" className="text-primary underline">DHCP &amp; leases</Link>.
              </p>
            </fieldset>
          </fieldset>

          {mgmtWillChange && !pendingNow && (
            <Callout tone="warning" title="Changing the WAN IP changes the admin address">
              After applying you must reconnect at <strong className="font-mono">{newUrl}</strong> and keep the
              change there, or it rolls back automatically.
              {!valid && " Validate to see the exact address."}
            </Callout>
          )}

          {valid && (
            <div className="space-y-3">
              {valid.validation.ok ? (
                <Callout tone="success" title="Configuration is valid">Review the change below, then apply it.</Callout>
              ) : (
                <Callout tone="danger" title="Validation failed">
                  <ValidationIssueList issues={valid.validation.issues ?? []} className="mt-1" />
                </Callout>
              )}
              <ChangeDiff
                changes={[
                  { field: "WAN", before: mono(wanBefore), after: wanAfter === wanBefore ? same : mono(wanAfter) },
                  { field: "Base LAN", before: mono(lanBefore), after: lanAfter === lanBefore ? same : mono(lanAfter) },
                  {
                    field: "Management URL",
                    before: mono(state.wan.management_url),
                    after: urlAfter === state.wan.management_url ? same : <span className="font-mono text-xs font-semibold">{urlAfter}</span>,
                  },
                ]}
              />
            </div>
          )}
        </SectionCard>
      )}

      <div className="grid gap-5 lg:grid-cols-2">
        <SectionCard
          title="Diagnostics"
          description="A read-only snapshot of addresses, routes and reachability."
          actions={
            <>
              <Button variant="secondary" size="sm" onClick={loadDiag} disabled={diagBusy}>
                {diagBusy ? "Running…" : diag ? "Run again" : "Run diagnostics"}
              </Button>
              {diag && <Button variant="secondary" size="sm" onClick={downloadDiag}><Download /> Download report</Button>}
            </>
          }
          bodyClassName={diag || diagErr ? "space-y-3" : "hidden"}
        >
          <ErrorBanner err={diagErr} className="mb-0" />
          {diag && Object.entries(diag).map(([k, v]) => (
            <div key={k} className="space-y-1">
              <div className="text-label text-muted-foreground">{k}</div>
              <pre className="max-h-72 overflow-auto rounded-md border border-border bg-surface p-3 font-mono text-xs text-foreground">{v}</pre>
            </div>
          ))}
        </SectionCard>

        <SectionCard flush title="Change history" description="Every apply, keep and roll back of these settings.">
          {history === null ? (
            <div className="px-5 py-4"><Skeleton className="h-20 w-full" /></div>
          ) : (
            <>
              <HistoryList
                label="WAN / LAN change history"
                emptyLabel="No changes recorded yet."
                items={shownHistory.map((h, n) => ({
                  id: `${h.at}|${h.action}|${n}`,
                  title: actionWords(h.action),
                  actor: h.actor,
                  at: when(h.at),
                  summary: [h.target, auditResult(h)].filter(Boolean).join(" · ") || undefined,
                  tone: h.failure_reason || h.rollback_result ? "warn" : "neutral",
                }))}
                onSelect={(it) => setEntry(shownHistory.find((h, n) => `${h.at}|${h.action}|${n}` === it.id) ?? null)}
              />
              {history.length > HISTORY_PREVIEW && (
                <div className="border-t border-border px-4 py-2.5">
                  <Button size="sm" variant="ghost" onClick={() => setAllHistory((v) => !v)}>
                    {allHistory ? "Show fewer" : `View all ${history.length}`}
                  </Button>
                </div>
              )}
            </>
          )}
        </SectionCard>
      </div>

      <DetailsDrawer
        open={entry !== null}
        onOpenChange={(v) => !v && setEntry(null)}
        eyebrow="WAN / LAN change"
        title={entry ? actionWords(entry.action) : ""}
        description={entry ? when(entry.at) : undefined}
      >
        {entry && (
          <DetailsSection title="Record">
            <DescriptionList
              items={[
                { label: "Operator", value: entry.actor || "—" },
                { label: "Source", value: mono(entry.source_ip || "—") },
                { label: "Target", value: mono(entry.target || "—") },
                { label: "Apply result", value: entry.apply_result, hidden: !entry.apply_result },
                { label: "Keep result", value: entry.confirm_result, hidden: !entry.confirm_result },
                { label: "Roll back result", value: entry.rollback_result, hidden: !entry.rollback_result },
                { label: "Failure", value: <span className="text-destructive">{entry.failure_reason}</span>, hidden: !entry.failure_reason },
                { label: "Backup", value: mono(entry.backup_path), hidden: !entry.backup_path },
                { label: "Recorded as", value: mono(entry.action) },
              ]}
            />
          </DetailsSection>
        )}
      </DetailsDrawer>

      <ConfirmDialog
        open={applying}
        onOpenChange={(v) => { if (!v) setApplying(false); }}
        title="Apply this network change?"
        description="The new WAN / LAN settings go live as soon as you confirm."
        consequences={[
          "The change takes effect immediately.",
          "If nobody keeps it before the timer runs out, the appliance puts the previous settings back on its own.",
          ...(mgmtWillChange ? [<>The admin address changes to <strong className="font-mono">{newUrl}</strong>. Reconnect there to keep the change.</>] : []),
        ]}
        confirmLabel="Apply change"
        busy={busy === "apply"}
        error={applyErr}
        requirePassword strictPassword
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
        busy={busy === "rollback"}
        error={rollbackErr}
        requirePassword strictPassword
        onConfirm={doRollback}
      />
    </PageShell>
  );
}
