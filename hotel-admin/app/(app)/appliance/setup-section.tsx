"use client";

// APPLIANCE SETUP — bring the appliance online in Velonet Central, and follow it until it is ready.
//
// Two ways to activate (Online, the default, needs nothing typed; Offline uses a file), one 3-phase progress
// while it happens, and "Advanced / recovery" collapsed underneath for the enrollment token and the 15-check
// diagnostics view. The page polls the appliance every 5 seconds (backing off while it cannot reach it).

import Link from "next/link";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { api, ApiError, EnrollResult, SetupStatus, Whoami } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Field, Input } from "@/components/ui/input";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Skeleton } from "@/components/ui/misc";
import { OptionCard, Stepper } from "@/components/ui/data";
import { CopyButton, LiveStatus, ReadOnlyNotice } from "@/components/ui/patterns";
import { canWrite } from "@/lib/roles";
import { cn, errMsg } from "@/lib/utils";
import {
  ServerCog, CheckCircle2, XCircle, Fingerprint, ShieldCheck, Radio, BadgeCheck, Network, Loader2,
  ChevronRight, Globe, FileUp, Download,
} from "lucide-react";

function fp(s?: string): string {
  if (!s) return "—";
  return s.length > 16 ? `${s.slice(0, 16)}…` : s;
}

function Row({ k, v }: { k: string; v: React.ReactNode }) {
  return (
    <div className="flex flex-wrap justify-between gap-x-4 gap-y-0.5 border-b border-border py-2 text-sm last:border-0">
      <span className="text-muted-foreground">{k}</span>
      <span className="min-w-0 text-end text-foreground">{v ?? "—"}</span>
    </div>
  );
}

/** A pass/fail check. The word is always there; the colour and icon only repeat it. */
function Check({ label, ok }: { label: string; ok?: boolean }) {
  return (
    <div className="flex items-center justify-between gap-4 border-b border-border py-2 text-sm last:border-0">
      <span className="text-muted-foreground">{label}</span>
      {ok ? (
        <span className="inline-flex items-center gap-1 font-medium text-success"><CheckCircle2 className="size-4" aria-hidden /> Pass</span>
      ) : (
        <span className="inline-flex items-center gap-1 font-medium text-destructive"><XCircle className="size-4" aria-hidden /> Fail</span>
      )}
    </div>
  );
}

function StepHeader({ title, icon }: { title: string; icon: React.ReactNode }) {
  return (
    <CardHeader>
      <CardTitle className="flex items-center gap-2">{icon}{title}</CardTitle>
    </CardHeader>
  );
}

function licenseTone(state?: string): "ok" | "warn" | "err" | "default" {
  const s = (state ?? "").toLowerCase();
  if (s === "active" || s === "licensed") return "ok";
  if (s === "grace" || s === "graceperiod") return "warn";
  if (s === "expired" || s === "suspended" || s === "revoked" || s === "restricted") return "err";
  return "default";
}

// The detailed 15-stage lifecycle stays available under Advanced / recovery.
//
// STAGE 12 IS NOT A STEP TOWARD COMPLETION, and treating it as one is what made this page hang forever.
//
// The appliance serves Central for LICENSING ONLY (T0071). The NATS transport is deliberately NOT OPENED --
// not broken, not pending, not waiting: a Product-Owner decision that the hotel's operations belong on the
// hotel's appliance. `nats_mtls.connected` is therefore false on a correctly activated appliance and will
// stay false forever.
//
// Completion follows the SAME rule the backend already uses for activation_status: enrolled + licensed +
// API mTLS. scd has always computed it that way; only this page disagreed.
const STAGES = [
  "Awaiting enrollment", "Enrollment submitted", "Identity generated", "Enrollment accepted",
  "Pending approval", "Claimed", "Assignment issued", "Assignment adopted",
  "Certificate requested", "Certificate issued", "API mTLS connected", "Real-time channel",
  "Awaiting license", "License active", "Setup complete",
];

function currentStage(st: SetupStatus | null, tokenSubmitted: boolean): number {
  if (!st) return 1;
  const licOk = ["active", "licensed", "grace", "graceperiod"].includes((st.license?.state ?? "").toLowerCase());
  const mtls = !!st.api_mtls?.mtls_ready;
  const nats = !!st.nats_mtls?.connected;
  const hasCert = !!st.api_mtls?.cert_fingerprint;
  const assigned = !!st.assignment?.assigned;
  const adopted = assigned && !!st.assignment?.adopted_at;
  // The backend's own verdict wins when it is present: scd sets activation_status="activated" on
  // enrolled + licensed + API mTLS, with no NATS term, and it is the authority on whether this appliance is
  // set up. The licOk && mtls fallback keeps the page working against an older scd that predates the field.
  if (st.activation_status === "activated" || (licOk && mtls)) return 15;
  if (licOk) return 14;
  if (assigned && mtls) return 13;
  if (nats) return 12;
  if (mtls) return 11;
  if (hasCert) return 10;
  if (adopted) return 8;
  if (st.enrolled) return 5;
  if (st.appliance_id) return 3;
  if (tokenSubmitted) return 2;
  return 1;
}

// The 15 technical stages roll up into 3 friendly phases the operator actually cares about.
const PHASES = [
  { title: "Connect", blurb: "Registering this appliance with Velonet Central" },
  { title: "Verify", blurb: "Issuing the security certificate and securing the link" },
  { title: "Ready", blurb: "The licence is arriving" },
];
// stage → phase index (0/1/2); stage 1 = not started (the form)
function phaseOf(stage: number): number {
  if (stage >= 14) return 2;
  if (stage >= 9) return 1;
  return 0;
}
function friendlyStatus(stage: number): string {
  switch (stage) {
    case 2: return "Sending your code to Velonet Central…";
    case 3: return "Creating this appliance's secure identity…";
    case 4:
    case 5: return "Waiting for Velonet Central to approve this appliance…";
    case 6: return "Approved — claiming the appliance…";
    case 7:
    case 8: return "Assigning it to your hotel…";
    case 9:
    case 10: return "Issuing the security certificate…";
    case 11: return "Securing the link (mTLS)…";
    case 12: return "Opening the real-time channel…";
    case 13: return "Activating your licence…";
    case 14: return "Licence active — finishing up…";
    case 15: return "All set — this appliance is connected.";
    default: return "Starting…";
  }
}

const POLL_SECONDS = 5;

export function ApplianceSetupSection() {
  const router = useRouter();
  const authGone = useRef(false);
  const [roles, setRoles] = useState<string[] | null>(null);
  const [st, setSt] = useState<SetupStatus | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [showDetails, setShowDetails] = useState(false);

  const [token, setToken] = useState("");
  const [serial, setSerial] = useState("");
  const [busy, setBusy] = useState(false);
  const [tokenSubmitted, setTokenSubmitted] = useState(false);
  // TWO PATHS, AND ONLY TWO. Online is the default and needs nothing typed. Offline is for an appliance with
  // no route to Central. The enrollment token is neither: it is a recovery lever, so it lives under Advanced
  // rather than in front of every installer.
  const [mode, setMode] = useState<"online" | "offline">("online");
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [, setPkgText] = useState("");
  const [pkgBusy, setPkgBusy] = useState(false);
  const [pkgErr, setPkgErr] = useState<string | null>(null);
  const [pkgOk, setPkgOk] = useState<string | null>(null);
  const [enrollErr, setEnrollErr] = useState<string | null>(null);
  const [enrollNote, setEnrollNote] = useState<string | null>(null);
  const serialPrefilled = useRef(false);
  const failCount = useRef(0);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const writable = roles ? canWrite("network", roles) : false;

  async function load() {
    try {
      const s = await api.get<SetupStatus>("/setup/status");
      setSt(s);
      setErr(null);
      setUpdatedAt(Date.now());
      failCount.current = 0;
      if (!serialPrefilled.current && s.serial) { setSerial(s.serial); serialPrefilled.current = true; }
    } catch (e) {
      // Session expired while sitting on this (long-lived) page → don't spin on a
      // misleading "couldn't reach the appliance" banner; clear the stale cookie
      // and bounce to the login screen, exactly like the layout's mount guard.
      if (e instanceof ApiError && e.status === 401) {
        if (!authGone.current) {
          authGone.current = true;
          try { await api.post("/auth/logout"); } catch {}
          router.replace("/login");
        }
        return;
      }
      setErr(errMsg(e));
      failCount.current = Math.min(failCount.current + 1, 5);
    } finally {
      setLoaded(true);
    }
  }

  useEffect(() => {
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => setRoles([]));
    let stopped = false;
    const tick = async () => {
      await load();
      if (stopped) return;
      const delay = 5000 * (failCount.current > 0 ? Math.min(2 ** failCount.current, 6) : 1);
      timer.current = setTimeout(tick, delay);
    };
    tick();
    return () => { stopped = true; if (timer.current) clearTimeout(timer.current); };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const enrolled = st?.enrolled === true;
  const locked = st?.locked === true || enrolled;

  // FIRST ACTIVATION, OFFLINE. Downloads the request this appliance emits; the operator carries it to
  // Central and brings back one package.
  async function downloadActivationRequest() {
    setPkgErr(null); setPkgOk(null);
    try {
      const req = await api.get<Record<string, unknown>>("/setup/activation-request");
      const blob = new Blob([JSON.stringify(req, null, 2)], { type: "application/json" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = `activation-request-${st?.serial || "appliance"}.json`;
      a.click();
      URL.revokeObjectURL(url);
      setPkgOk("Activation request downloaded. Import it in Velonet Central under Onboarding → Offline.");
    } catch (e: unknown) {
      setPkgErr(e instanceof Error ? e.message : String(e));
    }
  }

  // Applies either a first-activation package or a signed licence. The appliance decides which by shape and
  // verifies each on its own terms; a file for another appliance, a replay or an older licence is refused
  // and nothing changes.
  async function applyActivationPackage(text: string) {
    setPkgErr(null); setPkgOk(null); setPkgBusy(true);
    try {
      const pkg = JSON.parse(text);
      const isFirstActivation = typeof pkg?.request_id === "string" && pkg?.assignment != null;
      const r = await api.post<{ status?: string; tenant_id?: string; license_installed?: boolean }>(
        isFirstActivation ? "/setup/activation-package" : "/setup/offline-import", pkg);
      setPkgOk(isFirstActivation
        ? "Activated. Assignment, trust material and licence installed; the appliance is restarting."
        : (r.license_installed ? "Signed licence applied." : "Package applied."));
      setPkgText("");
      await load();
    } catch (e: unknown) {
      const m = e instanceof Error ? e.message : String(e);
      setPkgErr(m.includes("JSON") ? "That file is not a valid activation package." : m);
    } finally {
      setPkgBusy(false);
    }
  }

  function onPackageFile(f: File | null) {
    if (!f) return;
    const rd = new FileReader();
    rd.onload = () => { const t = String(rd.result ?? ""); setPkgText(t); void applyActivationPackage(t); };
    rd.readAsText(f);
  }

  async function submitEnroll() {
    if (locked || !writable || busy) return;
    if (!token.trim() || !serial.trim()) { setEnrollErr("Enter the enrollment token and serial."); return; }
    setBusy(true); setEnrollErr(null); setEnrollNote(null);
    try {
      const r = await api.post<EnrollResult>("/setup/enroll", { token: token.trim(), serial: serial.trim() });
      setEnrollNote(r.note || "Connecting…");
      setTokenSubmitted(true);
      setToken("");
      await load();
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) { setEnrollErr(e.message || "Already connected."); await load(); }
      else setEnrollErr(errMsg(e));
    } finally { setBusy(false); }
  }

  if (!loaded) {
    return (
      <div className="space-y-5" aria-busy="true">
        <span className="sr-only">Loading appliance setup</span>
        <Skeleton className="h-40 w-full rounded-lg" />
        <Skeleton className="h-24 w-full rounded-lg" />
      </div>
    );
  }

  const api_mtls = st?.api_mtls;
  const nats = st?.nats_mtls;
  const lic = st?.license;
  const net = st?.network;
  const licOk = ["active", "licensed", "grace", "graceperiod"].includes((lic?.state ?? "").toLowerCase());
  // Same rule as currentStage(), and for the same reason: requiring nats?.connected here held the whole page
  // in its "in progress" branch on a fully activated appliance, because the real-time channel is deliberately
  // never opened at a licensing-only site.
  const complete = st?.activation_status === "activated" || (enrolled && api_mtls?.mtls_ready === true && licOk);

  const stage = currentStage(st, tokenSubmitted);
  const inProgress = stage >= 2 && !complete;
  const activePhase = phaseOf(stage);

  const packageFeedback = (
    <>
      <ErrorBanner err={pkgErr} className="mb-0" />
      {pkgOk && <Callout tone="success">{pkgOk}</Callout>}
      {pkgBusy && (
        <div role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="size-4 animate-spin motion-reduce:animate-none" aria-hidden /> Checking and applying&hellip;
        </div>
      )}
    </>
  );

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-end gap-3">
        <LiveStatus updatedAt={updatedAt} intervalSeconds={POLL_SECONDS} error={!!err && st !== null} onRefresh={() => void load()} />
      </div>

      <ErrorBanner err={err ? `Couldn't reach the appliance (retrying): ${err}` : null} className="mb-0" />

      {roles !== null && !writable && !complete && (
        <ReadOnlyNotice>Your role can follow this appliance&apos;s setup but not activate it.</ReadOnlyNotice>
      )}

      {/* ---------- SUCCESS ---------- */}
      {complete && (
        <Card>
          <CardBody className="flex flex-col items-center gap-3 py-8 text-center">
            <span className="inline-flex size-12 items-center justify-center rounded-full bg-success-subtle text-success" aria-hidden>
              <CheckCircle2 className="size-6" />
            </span>
            <div className="text-subtitle">This appliance is connected</div>
            <p className="max-w-md text-sm text-muted-foreground">
              {st?.assignment?.tenant_name && st?.assignment?.site_name
                ? <>Bound to <strong>{st.assignment.tenant_name}</strong> · {st.assignment.site_name}. </>
                : null}
              The licence is active and the secure link is up. You can now create your guest networks.
            </p>
            <Badge tone="ok" dot>Setup complete</Badge>
          </CardBody>
        </Card>
      )}

      {/* ---------- ACTIVATION: TWO PATHS ---------- */}
      {!enrolled && !locked && (
        <Card>
          <CardHeader>
            <div className="space-y-0.5">
              <CardTitle>Activate this appliance</CardTitle>
              <CardDescription>
                Choose how this appliance reaches Velonet Central. Everything else — claiming, assignment,
                certificates — happens on its own and is shown under Advanced / recovery.
              </CardDescription>
            </div>
          </CardHeader>
          <CardBody className="space-y-4">
            <div role="radiogroup" aria-label="How this appliance reaches Velonet Central" className="grid gap-3 sm:grid-cols-2">
              <OptionCard
                name="activation-mode"
                value="online"
                checked={mode === "online"}
                onChange={() => setMode("online")}
                icon={<Globe />}
                title="Online"
                badge={<Badge tone="accent">Recommended</Badge>}
                description="This appliance can reach Velonet Central. Nothing to type."
              />
              <OptionCard
                name="activation-mode"
                value="offline"
                checked={mode === "offline"}
                onChange={() => setMode("offline")}
                icon={<FileUp />}
                title="Offline"
                description="No route to Velonet Central; activate with a file."
              />
            </div>

            {mode === "online" ? (
              <div className="space-y-3 rounded-md border border-border bg-surface p-4">
                <div className="text-emphasis">Nothing to do here.</div>
                <p className="text-sm text-muted-foreground">
                  This appliance registers itself as soon as it reaches Velonet Central. Ask your Velonet contact
                  to open <strong>Onboarding</strong> there, find it under <em>Pending activation</em> by its
                  serial, choose the customer, site and licence terms, and press <strong>Activate</strong> once.
                  This page then follows along by itself.
                </p>
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-label text-muted-foreground">Serial</span>
                  <code className="rounded-md border border-border bg-card px-2.5 py-1 font-mono text-sm">{st?.serial || "—"}</code>
                  {st?.serial && <CopyButton value={st.serial} size="xs" />}
                </div>
                <div className="max-w-sm">
                  <Check label="Velonet Central reachable" ok={net?.central_https_443} />
                </div>
                {net?.central_https_443 === false && (
                  <Callout tone="warning">
                    This appliance cannot reach Velonet Central right now. Fix connectivity under{" "}
                    <strong>WAN / LAN settings</strong>, or use the Offline path.
                  </Callout>
                )}
              </div>
            ) : (
              <ol className="space-y-4 rounded-md border border-border bg-surface p-4">
                <li className="space-y-1.5">
                  <div className="text-emphasis">Step 1 — download the activation request</div>
                  <p className="text-sm text-muted-foreground">
                    This appliance creates its own identity and writes a request describing it. The request
                    contains no secret: the private key stays on this appliance and never leaves it.
                  </p>
                  {writable && (
                    <Button className="mt-1" variant="secondary" onClick={() => void downloadActivationRequest()}>
                      <Download /> Download activation request
                    </Button>
                  )}
                </li>
                <li className="space-y-1.5 border-t border-border pt-4">
                  <div className="text-emphasis">Step 2 — in Velonet Central</div>
                  <p className="text-sm text-muted-foreground">
                    Open <strong>Onboarding → Offline activation</strong>, import the request, choose the customer,
                    site and licence terms, then generate the activation package.
                  </p>
                </li>
                <li className="space-y-1.5 border-t border-border pt-4">
                  <div className="text-emphasis">Step 3 — upload the activation package</div>
                  <p className="text-sm text-muted-foreground">
                    One file completes activation: the signed assignment, the trust material and the signed
                    licence. It is bound to this appliance, single-use and expiring. Anything else — another
                    appliance, a replay, a tampered or older file — is refused and nothing is changed.
                  </p>
                  {writable && (
                    <Field label="Activation package file" className="max-w-md pt-1">
                      <Input type="file" accept=".json,application/json" disabled={pkgBusy} className="h-auto py-2"
                        onChange={(e) => onPackageFile(e.target.files?.[0] ?? null)} />
                    </Field>
                  )}
                </li>
                <li className="list-none space-y-2">{packageFeedback}</li>
              </ol>
            )}
          </CardBody>
        </Card>
      )}

      {/* ---------- LICENCE, ONCE ONBOARDING IS DONE ---------- */}
      {enrolled && (
        <Card>
          <CardHeader>
            {/*
              TWO JOBS, AND ONLY ONE OF THEM IS THIS SCREEN'S.

              Setup ONBOARDS an appliance: it establishes assignment, trust material and the first licence,
              which is why the offline path above accepts a signed activation package -- that package carries
              all three together and there is nowhere else it could go.

              Renewing a licence afterwards is a different job and belongs to the Licence tab. So once the
              appliance is ACTIVATED this becomes a status line and a pointer. Before activation the upload
              stays, because an appliance part-way through onboarding may legitimately still need to complete
              the licence half of it here.
            */}
            <CardTitle className="flex items-center gap-2"><BadgeCheck className="size-4" aria-hidden /> Licence</CardTitle>
            <Link href="/appliance?section=license" className="text-label text-primary hover:underline">
              Capacity, expiry and identity <span aria-hidden>&rarr;</span>
            </Link>
          </CardHeader>
          <CardBody className="space-y-3">
            <div className="flex flex-wrap items-center gap-2 text-sm">
              <Badge tone={licenseTone(lic?.state)} dot>{lic?.state === "GracePeriod" ? "Grace period" : lic?.state || "Unknown"}</Badge>
              {lic?.valid_until && <span className="text-muted-foreground">valid until {lic.valid_until}</span>}
            </div>
            {complete ? (
              <p className="text-sm text-muted-foreground">
                This appliance is activated, so renewals happen in one place: the{" "}
                <Link href="/appliance?section=license" className="text-primary underline underline-offset-2">Licence</Link>{" "}
                tab. Velonet generates the new licence file; upload it there. An older licence than the one installed
                is refused, so a renewal can never roll you backwards.
              </p>
            ) : (
              <>
                <p className="text-sm text-muted-foreground">
                  Onboarding is not finished, so the licence half of it can still be completed here. Velonet
                  generates the licence file in Central; upload it below. An older licence than the one installed is
                  refused.
                </p>
                {writable && (
                  <Field label="Licence file" className="max-w-md">
                    <Input type="file" accept=".json,application/json" disabled={pkgBusy} className="h-auto py-2"
                      onChange={(e) => onPackageFile(e.target.files?.[0] ?? null)} />
                  </Field>
                )}
                {packageFeedback}
              </>
            )}
          </CardBody>
        </Card>
      )}

      {/* ---------- 3-PHASE PROGRESS ---------- */}
      {inProgress && (
        <Card>
          <CardBody className="space-y-4">
            <Stepper steps={PHASES.map((p) => p.title)} current={activePhase} />
            <div role="status" aria-live="polite" className="rounded-md border border-border bg-surface px-4 py-3">
              <div className="flex items-center gap-2 text-sm font-medium">
                <Loader2 className="size-4 animate-spin text-primary motion-reduce:animate-none" aria-hidden />
                {friendlyStatus(stage)}
              </div>
              <div className="mt-1 text-caption text-muted-foreground">{PHASES[activePhase]?.blurb}</div>
            </div>
            {enrollNote && <p className="text-caption text-muted-foreground">{enrollNote}</p>}
          </CardBody>
        </Card>
      )}

      {/* ---------- ALREADY ENROLLED but form locked & not in-progress edge ---------- */}
      {locked && !inProgress && !complete && (
        <Callout tone="neutral">This appliance is enrolled; waiting on Velonet Central to finish setup…</Callout>
      )}

      {/* ---------- ADVANCED / RECOVERY ---------- */}
      <div className="rounded-lg border border-border bg-card">
        <button
          type="button"
          aria-expanded={showAdvanced}
          className="flex w-full items-center gap-2 rounded-lg px-5 py-3.5 text-start text-emphasis focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          onClick={() => setShowAdvanced((v) => !v)}
        >
          <ChevronRight className={cn("size-4 text-muted-foreground transition-transform rtl:-scale-x-100", showAdvanced && "rotate-90 rtl:rotate-90")} aria-hidden />
          Advanced / recovery
        </button>

        {showAdvanced && (
          <div className="space-y-4 border-t border-border p-4 sm:p-5">
            {!enrolled && (
              <Card>
                <CardHeader>
                  <div className="space-y-0.5">
                    <CardTitle>Enrollment token</CardTitle>
                    <CardDescription>
                      <strong>Not part of normal activation.</strong> An appliance registers itself online and is
                      activated from Velonet Central. A token is for recovery — an appliance that cannot
                      self-register, or one being re-attached deliberately. It is created in Central under{" "}
                      <strong>Appliances → Enrollment token</strong> and should be locked to this serial.
                    </CardDescription>
                  </div>
                </CardHeader>
                <CardBody className="space-y-4">
                  {writable ? (
                    <>
                      <Field label="Enrollment code">
                        <Input id="enroll-token" type="password" autoComplete="off" placeholder="Paste the token"
                          value={token} onChange={(e) => setToken(e.target.value)} disabled={busy} />
                      </Field>
                      <Field label="Serial" hint="Give this serial to whoever creates the token so it locks to this appliance.">
                        <Input id="enroll-serial" autoComplete="off" placeholder="Appliance serial"
                          value={serial} onChange={(e) => setSerial(e.target.value)} disabled={busy} />
                      </Field>
                      <ErrorBanner err={enrollErr} className="mb-0" />
                      <Button onClick={submitEnroll} disabled={busy || !token.trim() || !serial.trim()} className="w-full sm:w-auto">
                        {busy ? <><Loader2 className="animate-spin motion-reduce:animate-none" aria-hidden /> Connecting…</> : "Connect with token"}
                      </Button>
                    </>
                  ) : (
                    <ReadOnlyNotice>Your role cannot connect this appliance with a token.</ReadOnlyNotice>
                  )}
                </CardBody>
              </Card>
            )}

            <button
              type="button"
              aria-expanded={showDetails}
              className="inline-flex items-center gap-1.5 rounded-md text-label text-muted-foreground hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              onClick={() => setShowDetails((v) => !v)}
            >
              <ChevronRight className={cn("size-4 transition-transform rtl:-scale-x-100", showDetails && "rotate-90 rtl:rotate-90")} aria-hidden />
              {`Diagnostics (${STAGES.length} checks)`}
            </button>

            {showDetails && (
              <div className="space-y-4">
                <Card>
                  <StepHeader title="Onboarding progress" icon={<ServerCog className="size-4" aria-hidden />} />
                  <CardBody>
                    <ol className="grid gap-x-6 gap-y-1.5 sm:grid-cols-2">
                      {STAGES.map((label, i) => {
                        const n = i + 1; const done = n < stage; const active = n === stage;
                        return (
                          <li key={label} className="flex items-center gap-2 text-sm" aria-current={active ? "step" : undefined}>
                            <span className={cn(
                              "inline-flex size-5 shrink-0 items-center justify-center rounded-full text-[11px] tabular",
                              done ? "bg-success-subtle text-success-subtle-foreground"
                                : active ? "bg-primary text-primary-foreground" : "bg-surface text-muted-foreground",
                            )}>
                              {done ? "✓" : n}
                            </span>
                            <span className={done ? "text-muted-foreground line-through" : active ? "font-medium text-foreground" : "text-muted-foreground"}>
                              {label}
                              {done && <span className="sr-only"> (done)</span>}
                              {active && <span className="sr-only"> (current)</span>}
                            </span>
                          </li>
                        );
                      })}
                    </ol>
                  </CardBody>
                </Card>

                <Card>
                  <StepHeader title="Appliance identity" icon={<Fingerprint className="size-4" aria-hidden />} />
                  <CardBody className="grid gap-x-8 md:grid-cols-2">
                    <div>
                      <Row k="Serial" v={<code>{st?.serial || "—"}</code>} />
                      <Row k="Appliance ID" v={<code className="break-all">{st?.appliance_id || "—"}</code>} />
                      <Row k="Version" v={<code>{st?.version || "—"}</code>} />
                    </div>
                    <div>
                      <Row k="Identity key fingerprint" v={<code title={st?.identity_key_fingerprint}>{fp(st?.identity_key_fingerprint)}</code>} />
                      <Row k="Certificate fingerprint" v={<code title={api_mtls?.cert_fingerprint}>{fp(api_mtls?.cert_fingerprint)}</code>} />
                    </div>
                  </CardBody>
                </Card>

                <Card>
                  <StepHeader title="Network & Central checks" icon={<Network className="size-4" aria-hidden />} />
                  <CardBody className="grid gap-x-8 md:grid-cols-2">
                    <div>
                      <Check label="DNS resolution" ok={net?.dns_ok} />
                      <Check label="Central HTTPS :443" ok={net?.central_https_443} />
                      <Check label="Clock in sync" ok={net?.clock} />
                    </div>
                    <div>
                      <Check label="API mTLS :9443" ok={net?.mtls_9443} />
                      <Check label="NATS mTLS :4223" ok={net?.nats_4223} />
                    </div>
                  </CardBody>
                </Card>

                <Card>
                  <StepHeader title="Certificate (API mTLS)" icon={<ShieldCheck className="size-4" aria-hidden />} />
                  <CardBody>
                    <Row k="Status" v={api_mtls?.mtls_ready ? <Badge tone="ok">API mTLS ready</Badge> : <Badge tone="warn">Not ready</Badge>} />
                    <Row k="Certificate fingerprint" v={<code title={api_mtls?.cert_fingerprint}>{fp(api_mtls?.cert_fingerprint)}</code>} />
                    <Row k="Expires (not after)" v={api_mtls?.not_after || "—"} />
                  </CardBody>
                </Card>

                <Card>
                  <StepHeader title="Real-time channel" icon={<Radio className="size-4" aria-hidden />} />
                  <CardBody>
                    {/* NOT AN ERROR, AND IT MUST NOT LOOK LIKE ONE. This appliance serves Central for licensing
                        only; the NATS transport is deliberately not opened. A red "Disconnected" badge here sent
                        operators looking for a network fault that does not exist. */}
                    <Row
                      k="Status"
                      v={
                        nats?.connected ? (
                          <Badge tone="ok">Connected</Badge>
                        ) : (
                          <Badge tone="default">Not used at this site</Badge>
                        )
                      }
                    />
                    <Row
                      k="Why"
                      v={
                        <span className="text-sm">
                          {nats?.connected
                            ? "The real-time channel is open."
                            : "This hotel's operations run on this appliance. Central is used for licensing only, so the real-time channel is intentionally closed — nothing is wrong and nothing is pending."}
                        </span>
                      }
                    />
                  </CardBody>
                </Card>

                <Card>
                  <StepHeader title="Licence" icon={<BadgeCheck className="size-4" aria-hidden />} />
                  <CardBody>
                    <Row k="State" v={<Badge tone={licenseTone(lic?.state)}>{lic?.state || "unknown"}</Badge>} />
                    <Row k="Max online guests" v={lic?.max_concurrent_online_guests == null ? "—" : lic.max_concurrent_online_guests === -1 ? "Unlimited" : String(lic.max_concurrent_online_guests)} />
                    <Row k="Valid until" v={lic?.valid_until || "—"} />
                    <Row k="Offline grace" v={lic?.offline_grace_days != null ? `${lic.offline_grace_days} days` : "—"} />
                  </CardBody>
                </Card>

                <Card>
                  <StepHeader title="Completion" icon={<CheckCircle2 className="size-4" aria-hidden />} />
                  <CardBody className="grid gap-x-8 md:grid-cols-2">
                    <div>
                      <Row k="Customer" v={st?.assignment?.tenant_name || "—"} />
                      <Row k="Site" v={st?.assignment?.site_name || "—"} />
                      <Row k="Assignment version" v={st?.assignment?.version ?? "—"} />
                    </div>
                    {/*
                      COMPLETION MEANS COMPLETE, AND MUST NOT ARGUE WITH THE SCREEN IT IS ON. What completion
                      consists of is the two facts below: this appliance has an identity Central recognises, and
                      a licence that authorises guests. Neither depends on a transport this property does not use.
                    */}
                    <div>
                      <Row k="Enrolled" v={<Badge tone={enrolled ? "ok" : "err"}>{enrolled ? "Yes" : "No"}</Badge>} />
                      <Row k="Licensed" v={<Badge tone={licOk ? "ok" : "err"}>{licOk ? "Yes" : "No"}</Badge>} />
                    </div>
                  </CardBody>
                </Card>

                <p className="text-caption text-muted-foreground">
                  Every field is read live from the appliance every {POLL_SECONDS}s. Secrets (enrollment token,
                  private keys, channel credentials) are never displayed.
                </p>
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
