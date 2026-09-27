"use client";

// APPLIANCE & LICENCE — one page, one status document.
//
// Everything on this page comes from GET /central/status (docs/CENTRAL_CONTROL_PLANE.md section 8), which the
// appliance computes once from its VERIFIED activation, its locally evaluated licence and its record of when
// OneGate Central last answered. The page words those states; it derives none of them. It used to stitch four
// endpoints together into two tabs and a fifteen-stage wizard, and they disagreed about whether the appliance
// was licensed.
//
// The primary surface speaks the hotel's language: activated or not, what the licence allows, whether Central
// is reachable and whether that matters (it never matters to guests). Protocol facts -- fingerprints, versions,
// the endpoint, MAC addresses -- sit in the collapsed Technical details for support.

import { useCallback, useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import {
  CheckCircle2, CircleDashed, Download, FileUp, Loader2, RefreshCw, ServerCog, ShieldCheck, Upload, Unplug,
  Wrench, XCircle, Cloud,
} from "lucide-react";
import { api, ApiError, CentralRefresh, CentralStatus, Whoami } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Meter, Skeleton } from "@/components/ui/misc";
import { KeyValueGrid } from "@/components/ui/data";
import { CopyButton, LiveStatus, ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { canWrite } from "@/lib/roles";
import { usePoll } from "@/lib/use-poll";
import { errMsg, formatRelative } from "@/lib/utils";

type Tone = "ok" | "warn" | "err" | "default" | "info";

const POLL_MS = 15_000;
const POLL_ACTIVATING_MS = 5_000;

function day(s?: string | null): string {
  if (!s) return "—";
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleDateString(undefined, { year: "numeric", month: "long", day: "numeric" });
}

// A licence ends at the end of a calendar day (23:59:59Z). In a time zone east of UTC that instant is already
// the next day, so a licence issued to end on 1 December would read "2 December". Licence dates are shown as
// the UTC calendar day they were issued for; OneGate Central shows them the same way.
function licenceDay(s?: string | null): string {
  if (!s) return "—";
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleDateString(undefined, { year: "numeric", month: "long", day: "numeric", timeZone: "UTC" });
}

function daysWord(n?: number | null): string {
  if (n == null) return "";
  return n === 1 ? "1 day" : `${n} days`;
}

// ---- words for each state --------------------------------------------------------------------------------

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
          <>Give your OneGate vendor this serial number. Nothing needs to be typed here — this page updates by itself once they activate it.</>
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
        body: st.customer_name || st.site_name
          ? <>Licensed to <strong>{st.customer_name ?? "—"}</strong>{st.site_name ? <> · {st.site_name}</> : null}.</>
          : "This appliance is activated.",
      };
    case "retired":
      return {
        title: "Retired",
        tone: "err",
        body: "This appliance was retired in OneGate Central and no longer signs guests in. Contact your OneGate vendor.",
      };
    default:
      return { title: String(st.activation), tone: "default", body: null };
  }
}

export function licenceWords(l: CentralStatus["license"]): { title: string; tone: Tone; line: string } {
  const until = l.valid_until ? licenceDay(l.valid_until) : "";
  switch (l.state) {
    case "none":
      return { title: "No licence yet", tone: "warn", line: "Guests cannot sign in until this appliance is activated and licensed." };
    case "active":
      return { title: "Active", tone: "ok", line: until ? `Valid until ${until}${l.days_left != null ? ` · ${daysWord(l.days_left)} left` : ""}.` : "Valid." };
    case "expiring":
      return { title: "Expires soon", tone: "warn", line: `Valid until ${until} · ${daysWord(l.days_left)} left.` };
    case "grace":
      return { title: "Grace period", tone: "warn", line: `Ended ${until}. Guests keep signing in until ${licenceDay(l.grace_ends_at)}.` };
    case "expired":
      return { title: "Expired", tone: "err", line: until ? `Ended ${until}.` : "The licence has ended." };
    case "suspended":
      return { title: "Suspended", tone: "err", line: "Suspended by your OneGate vendor." };
    case "revoked":
      return { title: "Revoked", tone: "err", line: "Revoked by your OneGate vendor." };
    case "wrong_hardware":
      return { title: "Wrong appliance", tone: "err", line: "The installed licence was issued for a different appliance." };
    default:
      return { title: String(l.state), tone: "default", line: "" };
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
        line: "Guests are not affected: this appliance signs guests in by itself. Licence renewals wait until the connection returns.",
      };
    case "not_configured":
      return { title: "Not configured", tone: "default", line: "No OneGate Central address is set on this appliance." };
    default:
      return { title: String(c.state), tone: "default", line: "" };
  }
}

function StateIcon({ tone }: { tone: Tone }) {
  const cls = "size-5 shrink-0";
  if (tone === "ok") return <CheckCircle2 className={`${cls} text-success`} aria-hidden />;
  if (tone === "err") return <XCircle className={`${cls} text-destructive`} aria-hidden />;
  if (tone === "info") return <Loader2 className={`${cls} animate-spin text-primary motion-reduce:animate-none`} aria-hidden />;
  return <CircleDashed className={`${cls} text-warning`} aria-hidden />;
}

/** One labelled part of the status card: a heading row with the state, then a line of words. */
function Part({ icon, label, badge, children }: {
  icon: React.ReactNode; label: string; badge: React.ReactNode; children: React.ReactNode;
}) {
  return (
    <section className="space-y-2 py-5 first:pt-0 last:pb-0" aria-label={label}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="flex items-center gap-2 text-sm font-medium text-muted-foreground">{icon}{label}</h2>
        {badge}
      </div>
      <div className="space-y-3 text-sm">{children}</div>
    </section>
  );
}

// ---- the page ----------------------------------------------------------------------------------------------

export function ApplianceStatus() {
  const router = useRouter();
  // Read through a ref so load() keeps one identity across renders (its effect runs once).
  const routerRef = useRef(router);
  routerRef.current = router;
  const toast = useToast();
  const authGone = useRef(false);
  const [roles, setRoles] = useState<string[] | null>(null);
  const [st, setSt] = useState<CentralStatus | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [checking, setChecking] = useState(false);
  const [diag, setDiag] = useState<CentralRefresh["diagnostics"] | null>(null);
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
      setErr(null);
      setUpdatedAt(Date.now());
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        if (!authGone.current) {
          authGone.current = true;
          try { await api.post("/auth/logout"); } catch {}
          routerRef.current.replace("/login");
        }
        return;
      }
      setErr(errMsg(e));
    } finally {
      setLoaded(true);
    }
  }, []);

  useEffect(() => {
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => setRoles([]));
    void load();
  }, [load]);

  // Faster while the appliance is on its way to activated, so the operator watching sees it land.
  const activating = st?.activation === "activating" || st?.activation === "waiting";
  usePoll(() => void load(), activating ? POLL_ACTIVATING_MS : POLL_MS);

  async function checkNow() {
    setChecking(true);
    try {
      const r = await api.post<CentralRefresh>("/central/refresh");
      setDiag(r.diagnostics ?? null);
      const { diagnostics: _d, ...status } = r;
      setSt(status as CentralStatus);
      setErr(null);
      setUpdatedAt(Date.now());
      toast.success(r.central?.state === "connected" ? "OneGate Central answered" : "Checked");
    } catch (e) {
      toast.error(errMsg(e));
    } finally {
      setChecking(false);
    }
  }

  async function downloadRequest() {
    setFileErr(null); setFileMsg(null);
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
      setFileErr(errMsg(e));
    }
  }

  async function uploadFile(kind: "package" | "licence", f: File | null | undefined) {
    if (!f) return;
    setFileBusy(kind); setFileErr(null); setFileMsg(null);
    try {
      const text = (await f.text()).trim();
      if (kind === "package") {
        try { JSON.parse(text); } catch { throw new Error("That file is not an activation or licence package."); }
        const r = await api.postRaw<{ status?: string }>("/central/offline-package", text);
        setFileMsg(r.status === "activated"
          ? "Activated. The appliance is restarting its licensing service; this page updates in a few seconds."
          : "Package accepted and installed.");
      } else {
        await api.postRaw("/license", text);
        setFileMsg("Licence file accepted and installed.");
        toast.success("Licence installed");
      }
      await load();
    } catch (e) {
      setFileErr(errMsg(e));
    } finally {
      setFileBusy(null);
      if (pkgRef.current) pkgRef.current.value = "";
      if (licRef.current) licRef.current.value = "";
    }
  }

  if (!loaded) {
    return (
      <div className="space-y-5" aria-busy="true">
        <span className="sr-only">Loading the appliance status</span>
        <Skeleton className="h-64 w-full rounded-lg" />
        <Skeleton className="h-28 w-full rounded-lg" />
      </div>
    );
  }

  if (!st) {
    return <ErrorBanner err={err ? `Couldn't read the appliance status: ${err}` : "Couldn't read the appliance status."} />;
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
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-end gap-3">
        <LiveStatus updatedAt={updatedAt} intervalSeconds={(activating ? POLL_ACTIVATING_MS : POLL_MS) / 1000}
          error={!!err} onRefresh={() => void load()} />
        <Button variant="secondary" size="sm" onClick={() => void checkNow()} disabled={checking}>
          {checking ? <Loader2 className="animate-spin motion-reduce:animate-none" aria-hidden /> : <RefreshCw aria-hidden />}
          {checking ? "Checking…" : "Check now"}
        </Button>
      </div>

      <ErrorBanner err={err ? `Couldn't refresh the status (retrying): ${err}` : null} className="mb-0" />

      {/* ---- PROBLEMS, in words, with what to do ---- */}
      {d.permissive_blocked && (
        <Callout tone="danger" title="A blocked attempt to switch off licence enforcement">
          This appliance refused to run without a licence ({d.permissive_blocked}). Guests still need a real licence.
          The attempt was recorded in Activity.
        </Callout>
      )}
      {d.assignment_status === "unverifiable" && (
        <Callout tone="danger" title="This appliance holds an activation it cannot verify">
          It is ignoring it, and signs no guests in until a valid activation arrives. Contact your OneGate vendor.
        </Callout>
      )}
      {l.state === "grace" && (
        <Callout tone="warning" title="The licence is in its grace period">
          It ended {licenceDay(l.valid_until)}. Guests keep signing in until <strong>{licenceDay(l.grace_ends_at)}</strong>
          {l.days_left != null ? <> ({daysWord(l.days_left)} left)</> : null}. Ask your OneGate vendor to renew it; the
          renewal arrives by itself.
        </Callout>
      )}
      {(l.state === "expired" || l.state === "suspended" || l.state === "revoked") && (
        <Callout tone="danger" title={`The licence is ${l.state}`}>
          New guest sign-ins are refused; guests already online are not disconnected. The sign-in page, DHCP, DNS and
          this admin keep working. Contact your OneGate vendor{l.state === "expired" ? " to renew it" : ""}.
        </Callout>
      )}
      {l.state === "wrong_hardware" && (
        <Callout tone="danger" title="The licence belongs to a different appliance">
          New guest sign-ins are refused. Ask your OneGate vendor for a licence for serial number{" "}
          <strong>{st.serial || "—"}</strong>, then upload it below.
        </Callout>
      )}
      {l.hardware_notice && (
        <Callout tone="warning" title="The internet (WAN) network adapter has changed">
          The licence stays in force. If the adapter was replaced on purpose, ask your OneGate vendor to rebind the
          licence to the new adapter.
        </Callout>
      )}
      {limited && current != null && current >= max! && (
        <Callout tone="warning" title="Licensed capacity reached">
          {current} of {max} guests are online. New guests cannot sign in until someone goes offline; guests already
          online are not affected.
        </Callout>
      )}

      {/* ---- STATUS: activation, licence, Central ---- */}
      <Card>
        <CardBody className="divide-y divide-border">
          <Part icon={<ServerCog className="size-4" aria-hidden />} label="Activation"
            badge={<Badge tone={act.tone === "info" ? "info" : act.tone} dot>{act.title}</Badge>}>
            <div className="flex items-start gap-3">
              <StateIcon tone={act.tone} />
              <p className="text-base text-foreground">{act.body}</p>
            </div>
            {(st.activation === "waiting" || st.activation === "not_registered") && (
              <div className="flex flex-wrap items-center gap-2 ps-8">
                <span className="text-muted-foreground">Serial number</span>
                <code className="rounded-md border border-border bg-surface px-3 py-1.5 font-mono text-lg font-semibold tracking-wide">
                  {st.serial || "—"}
                </code>
                {st.serial && <CopyButton value={st.serial} label="Copy serial number" size="sm" />}
              </div>
            )}
          </Part>

          <Part icon={<ShieldCheck className="size-4" aria-hidden />} label="Licence"
            badge={<Badge tone={lic.tone === "info" ? "info" : lic.tone} dot>{lic.title}</Badge>}>
            {lic.line && <p className="text-foreground">{lic.line}</p>}
            {l.state !== "none" && (
              <div className="space-y-2">
                <div className="flex flex-wrap items-baseline justify-between gap-2">
                  <span className="text-muted-foreground">Guests online, all guest networks</span>
                  <span className="text-metric tabular">
                    {current ?? "—"}
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

          <Part icon={st.central.state === "unreachable" ? <Unplug className="size-4" aria-hidden /> : <Cloud className="size-4" aria-hidden />}
            label="OneGate Central" badge={<Badge tone={cen.tone === "info" ? "info" : cen.tone} dot>{cen.title}</Badge>}>
            <p className="text-foreground">{cen.line}</p>
            {st.central.state !== "not_configured" && (
              <KeyValueGrid
                columns={2}
                items={[
                  { label: "Last answered", value: st.central.last_contact_at ? formatRelative(st.central.last_contact_at) : "Not yet" },
                  ...(st.central.last_error ? [{ label: "Last problem", value: st.central.last_error }] : []),
                ]}
              />
            )}
            {diag && (
              <p className="text-caption text-muted-foreground" role="status">
                Checked just now: {diag.dns_ok === false ? "the Central address could not be resolved" : "address resolved"}
                {diag.central_https === false ? ", but no connection could be opened" : diag.central_https ? ", connection opened" : ""}.
              </p>
            )}
          </Part>
        </CardBody>
      </Card>

      {/* ---- FILES: offline activation and the licence upload ---- */}
      <Card>
        <CardHeader>
          <div className="space-y-0.5">
            <CardTitle className="flex items-center gap-2"><FileUp className="size-4" aria-hidden /> Files from your OneGate vendor</CardTitle>
            <CardDescription>For an appliance without an internet connection, or when your vendor sends you a file.</CardDescription>
          </div>
        </CardHeader>
        <CardBody className="space-y-4">
          {roles !== null && !canActivate && !canInstall ? (
            <ReadOnlyNotice>Your role can see this appliance&apos;s status but not install files.</ReadOnlyNotice>
          ) : (
            <div className="divide-y divide-border">
              {showOffline && canActivate && (
                <div className="space-y-2 pb-4">
                  <div className="text-emphasis">Offline activation</div>
                  <p className="text-sm text-muted-foreground">
                    {st.activation === "not_registered"
                      ? "1. Download this appliance's activation request and send it to your vendor. 2. Upload the activation package they return."
                      : "Upload an activation or licence package your vendor sent you."}
                  </p>
                  <div className="flex flex-wrap gap-2">
                    {st.activation === "not_registered" && (
                      <Button variant="secondary" size="sm" onClick={() => void downloadRequest()}>
                        <Download aria-hidden /> Download activation request
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
                <div className={showOffline && canActivate ? "space-y-2 pt-4" : "space-y-2"}>
                  <div className="text-emphasis">Licence file</div>
                  <p className="text-sm text-muted-foreground">
                    Renewals normally arrive by themselves. Upload a licence file only when your vendor sends you one.
                    The appliance refuses a file for another appliance or an older licence, and nothing changes.
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
          {fileMsg && <Callout tone="success">{fileMsg}</Callout>}
          <ErrorBanner err={fileErr} className="mb-0" />
        </CardBody>
      </Card>

      {/* ---- TECHNICAL DETAILS: for support, collapsed ---- */}
      <Card>
        <details className="group" data-testid="technical-details">
          <summary className="flex cursor-pointer select-none items-center gap-2 rounded-lg px-5 py-4 text-emphasis focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
            <Wrench className="size-4 text-muted-foreground" aria-hidden />
            Technical details
            <span className="ms-auto text-caption font-normal text-muted-foreground group-open:hidden">Show</span>
            <span className="ms-auto hidden text-caption font-normal text-muted-foreground group-open:inline">Hide</span>
          </summary>
          <div className="border-t border-border px-5 py-4">
            <KeyValueGrid
              columns={2}
              items={[
                { label: "Serial number", value: <code className="text-xs">{st.serial || "—"}</code> },
                { label: "Appliance ID", value: <code className="break-all text-xs">{st.appliance_id || "—"}</code> },
                { label: "Identity key fingerprint", value: <code className="break-all text-xs">{d.identity_key_fingerprint || "—"}</code> },
                { label: "Client certificate", value: <code className="break-all text-xs">{d.cert_fingerprint || "none"}</code>, hint: d.cert_not_after ? `Expires ${day(d.cert_not_after)}` : undefined },
                { label: "Assignment", value: d.assignment_version != null ? `Version ${d.assignment_version}` : "None", hint: d.assignment_status ? `Verification: ${d.assignment_status}` : undefined },
                { label: "Licence", value: d.license_version != null ? `Version ${d.license_version}` : "None", hint: d.license_id || undefined },
                { label: "WAN MAC address", value: <code className="text-xs">{d.wan_mac || "—"}</code> },
                { label: "LAN MAC address", value: <code className="text-xs">{d.lan_mac || "—"}</code> },
                { label: "Central endpoint", value: <code className="break-all text-xs">{d.central_endpoint || "—"}</code> },
                { label: "Software", value: d.software_version || "—", hint: d.build_profile ? `${d.build_profile} build` : undefined },
                ...(diag ? [{ label: "Last check", value: `DNS ${diag.dns_ok ? "ok" : "failed"} · HTTPS ${diag.central_https ? "open" : "closed"}${diag.central_mtls != null ? ` · mutual TLS ${diag.central_mtls ? "open" : "closed"}` : ""}` }] : []),
              ]}
            />
          </div>
        </details>
      </Card>
    </div>
  );
}
