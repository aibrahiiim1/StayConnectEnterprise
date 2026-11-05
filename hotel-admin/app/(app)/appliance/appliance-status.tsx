"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import {
  AlertTriangle, CheckCircle2, ChevronDown, CircleDashed, Download, RefreshCw, ServerCog, ShieldCheck, Upload, Unplug,
  XCircle, Cloud } from "lucide-react";
import { api, CentralRefresh, CentralStatus } from "@/lib/api";
import { EmptyState } from "@/components/ui/empty-state";
import { Card, SectionCard } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { StatusBadge } from "@/components/ui/badge";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Meter, Skeleton } from "@/components/ui/misc";
import { DescriptionList } from "@/components/ui/data";
import { CopyButton, LiveStatus, ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { canWrite } from "@/lib/roles";
import { usePoll } from "@/lib/use-poll";
import { cn, formatRelative } from "@/lib/utils";
import { useOperatorRoles } from "@/lib/whoami-context";
import { LicenceClockReset } from "./licence-clock-reset";
import { OneGateLoader } from "@/components/brand";
import { checkNowError, licenceFileError, packageError, requestError } from "./appliance-errors";

type Tone = "ok" | "warn" | "err" | "default" | "info";

const POLL_MS = 15_000;
const POLL_ACTIVATING_MS = 5_000;
const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";
const NOT_KNOWN = "Not known";

function day(s?: string | null): string {
  if (!s) return NOT_KNOWN;
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleDateString(undefined, { year: "numeric", month: "long", day: "numeric" });
}

function licenceDay(s?: string | null): string {
  if (!s) return NOT_KNOWN;
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleDateString(undefined, { year: "numeric", month: "long", day: "numeric", timeZone: "UTC" });
}

function daysWord(n?: number | null): string {
  if (n == null) return "";
  return n === 1 ? "1 day" : `${n} days`;
}

export function activationWords(st: CentralStatus): { title: string; tone: Tone; body: React.ReactNode } {
  switch (st.activation) {
    case "not_registered":
      return {
        title: "Not registered yet",
        tone: "warn",
        body:
          st.central.state === "not_configured"
            ? "This appliance has no OneGate Central address configured. Use offline activation below."
            : "This appliance has not reached OneGate Central yet. It keeps trying by itself. If it has no internet connection, use offline activation below.",
      };
    case "waiting":
      return {
        title: "Waiting for activation",
        tone: "info",
        body: (
          <>Give your OneGate vendor this serial number and the activation code below; they need both to activate this appliance. Nothing needs to be typed here. This page updates by itself once they activate it.</>
        ),
      };
    case "activating":
      return {
        title: "Finishing activation…",
        tone: "info",
        body: "Your vendor has activated this appliance. It is collecting its licence now; this usually takes under a minute.",
      };
    case "activated":
      return {
        title: "Activated",
        tone: "ok",
        body: st.customer_name
          ? <>Licensed to <strong>{st.customer_name}</strong>{st.site_name ? <> · {st.site_name}</> : null}.</>
          : st.site_name ? <>Licensed for <strong>{st.site_name}</strong>.</> : "This appliance is activated.",
      };
    case "retired":
      if (st.details?.reason === "removed_from_central") {
        return {
          title: "Removed from OneGate Central",
          tone: "err",
          body: (
            <>
              This appliance was removed from OneGate Central. To use it again, factory-reset it and have your vendor
              activate it. Clients already online are not disconnected; nobody new can sign in. The factory-clean
              procedure is described in <strong>Factory-clean install and disaster recovery</strong> (ask your
              OneGate vendor).
            </>
          ),
        };
      }
      return {
        title: "Retired",
        tone: "err",
        body: "This appliance was retired in OneGate Central and no longer signs clients in. Contact your OneGate vendor.",
      };
    default:
      return { title: "Not recognised", tone: "default", body: "The appliance reported an activation state this console does not recognise. Contact your OneGate vendor." };
  }
}

export function licenceWords(l: CentralStatus["license"]): { title: string; tone: Tone; line: string } {
  const until = l.valid_until ? licenceDay(l.valid_until) : "";
  switch (l.state) {
    case "none":
      return { title: "No licence yet", tone: "warn", line: "Clients cannot sign in until this appliance is activated and licensed." };
    case "active":
      return { title: "Active", tone: "ok", line: until ? `Valid until ${until}${l.days_left != null ? ` · ${daysWord(l.days_left)} left` : ""}.` : "Valid." };
    case "expiring":
      return { title: "Expires soon", tone: "warn", line: `Valid until ${until} · ${daysWord(l.days_left)} left.` };
    case "grace":
      return { title: "Grace period", tone: "warn", line: `Ended ${until}. Clients keep signing in until ${licenceDay(l.grace_ends_at)}.` };
    case "expired":
      return { title: "Expired", tone: "err", line: until ? `Ended ${until}.` : "The licence has ended." };
    case "suspended":
      return { title: "Suspended", tone: "err", line: "Suspended by your OneGate vendor." };
    case "revoked":
      return { title: "Revoked", tone: "err", line: "Revoked by your OneGate vendor." };
    case "wrong_hardware":
      return { title: "Wrong appliance", tone: "err", line: "The installed licence was issued for a different appliance." };
    default:
      return { title: "Not recognised", tone: "default", line: "The appliance reported a licence state this console does not recognise." };
  }
}

export function centralWords(c: CentralStatus["central"]): { title: string; tone: Tone; line: string } {
  switch (c.state) {
    case "connected":
      return { title: "Connected", tone: "ok", line: "OneGate Central is answering." };
    case "unreachable":
      return {
        title: "Temporarily unreachable",
        tone: "warn",
        line: "Clients are not affected: this appliance signs clients in by itself. Licence renewals wait until the connection returns.",
      };
    case "credential_refused":
      return {
        title: "Renewing its certificate",
        tone: "warn",
        line: "OneGate Central is answering but no longer accepts this appliance's certificate, usually after a certificate reissue in Central. The appliance is requesting a new one by itself. Clients are not affected.",
      };
    case "not_configured":
      return { title: "Not configured", tone: "default", line: "No OneGate Central address is set on this appliance." };
    case "identity_not_loaded":
      return {
        title: "Not contacted yet",
        tone: "default",
        line: "This appliance's identity or certificate is not loaded yet, so it has not spoken to OneGate Central since it started. Clients are not affected.",
      };
    default:
      return { title: "Not recognised", tone: "default", line: "The appliance reported a connection state this console does not recognise." };
  }
}

function StateIcon({ tone }: { tone: Tone }) {
  const cls = "size-5 shrink-0";
  if (tone === "ok") return <CheckCircle2 className={`${cls} text-success`} aria-hidden />;
  if (tone === "err") return <XCircle className={`${cls} text-destructive`} aria-hidden />;
  if (tone === "info") return <OneGateLoader className={cls} />;
  return <CircleDashed className={`${cls} text-warning`} aria-hidden />;
}

function Part({ icon, label, badge, className, children }: {
  icon: React.ReactNode; label: string; badge: React.ReactNode; className?: string; children: React.ReactNode;
}) {
  return (
    <Card className={className}>
      <section className="space-y-3 px-5 py-4" aria-label={label}>
        <div className="flex flex-wrap items-center justify-between gap-2">
          <h2 className="flex items-center gap-2 text-label text-muted-foreground [&_svg]:size-4">{icon}{label}</h2>
          {badge}
        </div>
        <div className="space-y-3 text-sm">{children}</div>
      </section>
    </Card>
  );
}

const CHECK_TOAST: Record<string, string> = {
  connected: "OneGate Central answered",
  unreachable: "OneGate Central did not answer. Clients are not affected.",
  credential_refused: "OneGate Central answered. The appliance is renewing its certificate by itself.",
  not_configured: "No OneGate Central address is set on this appliance.",
  identity_not_loaded: "This appliance has not contacted OneGate Central since it started.",
};

const ASSIGNMENT_WORDS: Record<string, string> = {
  absent: "None held",
  unverifiable: "Could not be verified",
  not_granting: "Verified, not in force",
  granted: "Verified and in force",
};

function isSentence(s: string): boolean {
  return /^[A-Z]/.test(s) && /\.$/.test(s) && !s.includes("://");
}

export function ApplianceStatus({ header }: { header?: (actions: React.ReactNode) => React.ReactNode }) {
  const router = useRouter();
  const routerRef = useRef(router);
  routerRef.current = router;
  const toast = useToast();
  const authGone = useRef(false);
  const roles = useOperatorRoles();
  const [st, setSt] = useState<CentralStatus | null>(null);
  const [err, setErr] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [checking, setChecking] = useState(false);
  const [diag, setDiag] = useState<CentralRefresh["diagnostics"] | null>(null);
  const [checkedAt, setCheckedAt] = useState<string | null>(null);
  const [requesting, setRequesting] = useState(false);
  const [fileBusy, setFileBusy] = useState<"package" | "licence" | null>(null);
  const [fileMsg, setFileMsg] = useState<string | null>(null);
  const [fileErr, setFileErr] = useState<string | null>(null);
  const pkgRef = useRef<HTMLInputElement>(null);
  const licRef = useRef<HTMLInputElement>(null);

  const canActivate = roles ? canWrite("license", roles) || canWrite("network", roles) : false;
  const canInstall = roles ? canWrite("license", roles) : false;

  const load = useCallback(async () => {
    try {
      const s = await api.get<CentralStatus>("/central/status");
      setSt(s);
      setErr(false);
      setUpdatedAt(Date.now());
    } catch (e) {
      if ((e as { status?: number } | null)?.status === 401) {
        if (!authGone.current) {
          authGone.current = true;
          try { await api.post("/auth/logout"); } catch {}
          routerRef.current.replace("/login");
        }
        return;
      }
      setErr(true);
    } finally {
      setLoaded(true);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const activating = st?.activation === "activating" || st?.activation === "waiting";
  usePoll(() => load(), activating ? POLL_ACTIVATING_MS : POLL_MS);

  async function checkNow() {
    setChecking(true);
    try {
      const r = await api.post<CentralRefresh>("/central/refresh");
      setDiag(r.diagnostics ?? null);
      setCheckedAt(new Date().toISOString());
      const { diagnostics: _d, ...status } = r;
      setSt(status as CentralStatus);
      setErr(false);
      setUpdatedAt(Date.now());
      const words = CHECK_TOAST[r.central?.state ?? ""] ?? "Checked. The status below is up to date.";
      if (r.central?.state === "connected") toast.success(words);
      else toast.toast({ tone: "info", title: words });
    } catch (e) {
      toast.error(checkNowError(e));
    } finally {
      setChecking(false);
    }
  }

  async function downloadRequest() {
    setFileErr(null); setFileMsg(null);
    setRequesting(true);
    try {
      const req = await api.get<Record<string, unknown>>("/central/offline-request");
      const blob = new Blob([JSON.stringify(req, null, 2)], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `activation-request-${st?.serial || "appliance"}.json`;
      a.click();
      URL.revokeObjectURL(url);
      setFileMsg("Activation request downloaded. Send it to your OneGate vendor; they return an activation package.");
    } catch (e) {
      setFileErr(requestError(e));
    } finally {
      setRequesting(false);
    }
  }

  async function uploadFile(kind: "package" | "licence", f: File | null | undefined) {
    if (!f) return;
    setFileBusy(kind); setFileErr(null); setFileMsg(null);
    try {
      const text = (await f.text()).trim();
      if (kind === "package") {
        try { JSON.parse(text); } catch { throw Object.assign(new Error("not an activation or licence package"), { status: 400 }); }
        const r = await api.postRaw<{ status?: string }>("/central/offline-package", text);
        setFileMsg(r.status === "activated"
          ? "Activated. The appliance is restarting its licensing service; this page updates in a few seconds."
          : "Package accepted and installed.");
      } else {
        await api.postRaw("/license", text);
        setFileMsg("Licence file accepted and installed.");
      }
      await load();
    } catch (e) {
      setFileErr(kind === "package" ? packageError(e, st?.serial) : licenceFileError(e, st?.serial));
    } finally {
      setFileBusy(null);
      if (pkgRef.current) pkgRef.current.value = "";
      if (licRef.current) licRef.current.value = "";
    }
  }

  const headerActions = loaded && st ? (
    <>
      <LiveStatus updatedAt={updatedAt} intervalSeconds={(activating ? POLL_ACTIVATING_MS : POLL_MS) / 1000}
        error={err} onRefresh={() => void load()} />
      <Button variant="secondary" onClick={() => void checkNow()} disabled={checking}>
        {checking ? <OneGateLoader tone="current" /> : <RefreshCw aria-hidden />}
        {checking ? "Checking…" : "Check now"}
      </Button>
    </>
  ) : null;

  if (!loaded) {
    return (
      <>
        {header?.(null)}
        <div className="space-y-5" aria-busy="true">
          <span className="sr-only">Loading the appliance status</span>
          <div className="grid gap-5 lg:grid-cols-3">
            <Skeleton className="h-40 w-full rounded-lg" />
            <Skeleton className="h-40 w-full rounded-lg" />
            <Skeleton className="h-40 w-full rounded-lg" />
          </div>
          <Skeleton className="h-28 w-full rounded-lg" />
        </div>
      </>
    );
  }

  if (!st) {
    return (
      <>
        {header?.(null)}
        <Card role="alert" className={FADE}>
          <EmptyState
            size="sm"
            icon={<AlertTriangle />}
            title="The appliance status could not be loaded"
            hint="Check the appliance is reachable, then try again. Nothing has been changed."
            action={
              <Button size="sm" variant="secondary" onClick={() => { setLoaded(false); void load(); }}>
                <RefreshCw aria-hidden /> Try again
              </Button>
            }
          />
        </Card>
      </>
    );
  }

  const act = activationWords(st);
  const lic = licenceWords(st.license);
  const cen = centralWords(st.central);
  const l = st.license;
  const max = l.max_concurrent_online_guests;
  const current = l.current_online_guests;
  const limited = max != null && max > 0;
  const pct = limited && current != null ? (current / max!) * 100 : 0;
  const meterTone = pct >= 100 ? "err" : pct >= 80 ? "warn" : "ok";
  const showOffline = st.activation !== "activated" || st.central.state !== "connected";
  const d = st.details;

  return (
    <>
      {header ? header(headerActions) : <div className="flex flex-wrap items-center justify-end gap-3">{headerActions}</div>}

      {err && (
        <div className={FADE}>
          <Callout tone="warning">The appliance status could not be refreshed. What is shown may be out of date.</Callout>
        </div>
      )}

      {d.permissive_blocked && (
        <Callout className={FADE} tone="danger" title="A blocked attempt to switch off licence enforcement">
          This appliance refused to run without a licence ({d.permissive_blocked}). Clients still need a real licence.
          The attempt was recorded in Activity.
        </Callout>
      )}
      {d.assignment_status === "unverifiable" && (
        <Callout className={FADE} tone="danger" title="This appliance holds an activation it cannot verify">
          It is ignoring it, and signs no clients in until a valid activation arrives. Contact your OneGate vendor.
        </Callout>
      )}
      {l.state === "grace" && (
        <Callout className={FADE} tone="warning" title="The licence is in its grace period">
          It ended {licenceDay(l.valid_until)}. Clients keep signing in until <strong>{licenceDay(l.grace_ends_at)}</strong>
          {l.days_left != null ? <> ({daysWord(l.days_left)} left)</> : null}. Ask your OneGate vendor to renew it; the
          renewal arrives by itself.
        </Callout>
      )}
      {(l.state === "expired" || l.state === "suspended" || l.state === "revoked") && (
        <Callout className={FADE} tone="danger" title={`The licence is ${l.state}`}>
          New client sign-ins are refused; clients already online are not disconnected. The sign-in page, DHCP, DNS and
          this admin keep working. Contact your OneGate vendor{l.state === "expired" ? " to renew it" : ""}.
        </Callout>
      )}
      {l.state === "wrong_hardware" && (
        <Callout className={FADE} tone="danger" title="The licence belongs to a different appliance">
          New client sign-ins are refused. Ask your OneGate vendor for a licence for{" "}
          {st.serial ? <>serial number <strong>{st.serial}</strong></> : "this appliance"}, then upload it below.
        </Callout>
      )}
      {l.hardware_notice && (
        <Callout className={FADE} tone="warning" title="The internet (WAN) network adapter has changed">
          The licence stays in force. If the adapter was replaced on purpose, ask your OneGate vendor to rebind the
          licence to the new adapter.
        </Callout>
      )}
      {limited && current != null && current >= max! && (
        <Callout className={FADE} tone="warning" title="Licensed capacity reached">
          {current} of {max} clients are online. New clients cannot sign in until someone goes offline; clients already
          online are not affected.
        </Callout>
      )}

      <div className="grid gap-5 lg:grid-cols-3">
        <Part icon={<ServerCog aria-hidden />} label="Activation"
          badge={<StatusBadge tone={act.tone}>{act.title}</StatusBadge>}>
          <div className="flex items-start gap-3">
            <StateIcon tone={act.tone} />
            <p className="text-foreground">{act.body}</p>
          </div>
          {(st.activation === "waiting" || st.activation === "not_registered") && (
            <div className="space-y-2">
              <div className="text-xs text-muted-foreground">Serial number</div>
              <div className="flex flex-wrap items-center gap-2">
                <code className="rounded-md border border-border bg-surface px-3 py-1.5 font-mono text-lg font-semibold tracking-wide">
                  {st.serial || NOT_KNOWN}
                </code>
                {st.serial && <CopyButton value={st.serial} label="Copy serial number" size="sm" />}
              </div>
              {d.activation_code && (
                <>
                  <div className="pt-2 text-xs text-muted-foreground">Activation code</div>
                  <div className="flex flex-wrap items-center gap-2">
                    <code data-testid="activation-code" className="rounded-md border border-border bg-surface px-3 py-1.5 font-mono text-lg font-semibold tracking-wide">
                      {d.activation_code}
                    </code>
                    <CopyButton value={d.activation_code} label="Copy activation code" size="sm" />
                  </div>
                </>
              )}
            </div>
          )}
        </Part>

        <Part icon={<ShieldCheck aria-hidden />} label="Licence"
          badge={<StatusBadge tone={lic.tone}>{lic.title}</StatusBadge>}>
          {lic.line && <p className="text-foreground">{lic.line}</p>}
          {l.state !== "none" && (
            <div className="space-y-2">
              <div className="flex flex-wrap items-baseline justify-between gap-2">
                <span className="text-muted-foreground">Clients online</span>
                <span className="text-metric tabular">
                  {current ?? NOT_KNOWN}
                  <span className="text-sm font-normal text-muted-foreground"> / {limited ? max : "Unlimited"}</span>
                </span>
              </div>
              {limited && (
                <Meter value={current ?? 0} max={max!} tone={meterTone}
                  caption={`${Math.round(pct)}% of the licensed capacity${meterTone === "err" ? " · full" : meterTone === "warn" ? " · nearly full" : ""}`} />
              )}
            </div>
          )}
        </Part>

        <Part icon={st.central.state === "unreachable" ? <Unplug aria-hidden /> : <Cloud aria-hidden />}
          label="OneGate Central" badge={<StatusBadge tone={cen.tone}>{cen.title}</StatusBadge>}>
          <p className="text-foreground">{cen.line}</p>
          {st.central.state !== "not_configured" && (
            <DescriptionList
              items={[
                { label: "Last answered", value: st.central.last_contact_at ? formatRelative(st.central.last_contact_at) : "Not yet" },
                {
                  label: "Last problem",
                  value: st.central.last_error && isSentence(st.central.last_error)
                    ? st.central.last_error
                    : "OneGate Central could not be reached. The exact error is under Technical details.",
                  hidden: !st.central.last_error,
                },
              ]}
            />
          )}
          {diag && checkedAt && (
            <p className={cn("text-caption text-muted-foreground", FADE)} role="status">
              Checked {formatRelative(checkedAt)}: {diag.dns_ok === false ? "the Central address could not be resolved" : "address resolved"}
              {diag.central_https === false ? ", but no connection could be opened" : diag.central_https ? ", connection opened" : ""}.
            </p>
          )}
        </Part>
      </div>

      <SectionCard
        title="Files from your OneGate vendor"
        description="For an appliance without an internet connection, or when your vendor sends you a file."
        bodyClassName="space-y-4"
      >
        {roles !== null && !canActivate && !canInstall ? (
          <ReadOnlyNotice>Your role can see this appliance&apos;s status but not install files.</ReadOnlyNotice>
        ) : (
          <div className="grid gap-5 md:grid-cols-2">
            {showOffline && canActivate && (
              <div className="space-y-2">
                <div className="text-label">Offline activation</div>
                <p className="text-caption text-muted-foreground">
                  {st.activation === "not_registered"
                    ? "Download the activation request, send it to your vendor, then upload the package they return."
                    : "Upload an activation or licence package your vendor sent you."}
                </p>
                <div className="flex flex-wrap gap-2">
                  {st.activation === "not_registered" && (
                    <Button variant="secondary" size="sm" disabled={requesting} onClick={() => void downloadRequest()}>
                      <Download aria-hidden /> {requesting ? "Downloading…" : "Download activation request"}
                    </Button>
                  )}
                  <input ref={pkgRef} id="pkg-file" type="file" accept=".json,application/json" className="sr-only" tabIndex={-1}
                    aria-label="Activation package" onChange={(e) => void uploadFile("package", e.target.files?.[0])} />
                  <Button variant="secondary" size="sm" disabled={fileBusy !== null} onClick={() => pkgRef.current?.click()}>
                    <Upload aria-hidden /> {fileBusy === "package" ? "Checking…" : "Upload activation package"}
                  </Button>
                </div>
              </div>
            )}
            {canInstall && (
              <div className="space-y-2">
                <div className="text-label">Licence file</div>
                <p className="text-caption text-muted-foreground">
                  Renewals normally arrive by themselves. A file for another appliance, or an older licence, is refused.
                </p>
                <input ref={licRef} id="lic-file" type="file" accept=".license,.json,application/json" className="sr-only" tabIndex={-1}
                  aria-label="Licence file" onChange={(e) => void uploadFile("licence", e.target.files?.[0])} />
                <Button variant="secondary" size="sm" disabled={fileBusy !== null} onClick={() => licRef.current?.click()}>
                  <Upload aria-hidden /> {fileBusy === "licence" ? "Installing…" : "Upload licence file"}
                </Button>
              </div>
            )}
          </div>
        )}
        {fileMsg && <Callout className={FADE} tone="success">{fileMsg}</Callout>}
        <ErrorBanner err={fileErr} className="mb-0" />
      </SectionCard>

      {canInstall && <LicenceClockReset onDone={() => void load()} />}

      <Card>
        <details className="group" data-testid="technical-details">
          <summary className="flex cursor-pointer select-none items-center gap-2 rounded-lg px-5 py-3.5 text-emphasis focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring [&::-webkit-details-marker]:hidden">
            <ChevronDown className="size-4 text-muted-foreground transition-transform group-open:rotate-180" aria-hidden />
            Technical details
          </summary>
          <div className="border-t border-border px-5 py-4">
            <DescriptionList
              columns={2}
              items={[
                { label: "Serial number", value: st.serial ? <code className="text-xs">{st.serial}</code> : NOT_KNOWN },
                { label: "Appliance ID", value: st.appliance_id ? <code className="break-all text-xs">{st.appliance_id}</code> : NOT_KNOWN },
                { label: "Identity key fingerprint", value: d.identity_key_fingerprint ? <code className="break-all text-xs">{d.identity_key_fingerprint}</code> : NOT_KNOWN },
                { label: "Client certificate", value: d.cert_fingerprint ? <code className="break-all text-xs">{d.cert_fingerprint}</code> : "None", hint: d.cert_not_after ? `Expires ${day(d.cert_not_after)}` : undefined },
                { label: "Assignment", value: d.assignment_version != null ? `Version ${d.assignment_version}` : "None", hint: d.assignment_status ? ASSIGNMENT_WORDS[d.assignment_status] ?? "Not recognised" : undefined },
                { label: "Licence", value: d.license_version != null ? `Version ${d.license_version}` : "None", hint: d.license_id || undefined },
                { label: "WAN MAC address", value: d.wan_mac ? <code className="text-xs">{d.wan_mac}</code> : NOT_KNOWN },
                { label: "LAN MAC address", value: d.lan_mac ? <code className="text-xs">{d.lan_mac}</code> : NOT_KNOWN },
                { label: "Central endpoint", value: d.central_endpoint ? <code className="break-all text-xs">{d.central_endpoint}</code> : NOT_KNOWN },
                { label: "Software", value: d.software_version || NOT_KNOWN, hint: d.build_profile ? `${d.build_profile} build` : undefined },
                {
                  label: "Last error from Central",
                  hidden: !st.central.last_error || isSentence(st.central.last_error),
                  value: <code className="break-all text-xs">{st.central.last_error}</code>,
                },
                {
                  label: "Last check",
                  hidden: !diag,
                  value: diag ? `${diag.dns_ok ? "Address found" : "Address not found"} · ${diag.central_https ? "Connection opened" : "No connection"}${diag.central_mtls != null ? ` · ${diag.central_mtls ? "Mutual TLS accepted" : "Mutual TLS refused"}` : ""}` : null,
                  hint: checkedAt ? formatRelative(checkedAt) : undefined,
                },
              ]}
            />
          </div>
        </details>
      </Card>
    </>
  );
}
