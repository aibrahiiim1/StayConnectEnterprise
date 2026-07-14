"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { api, ApiError, EnrollResult, SetupStatus, Whoami } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input, Label } from "@/components/ui/input";
import { canWrite } from "@/lib/roles";
import { errMsg } from "@/lib/utils";
import {
  ServerCog, RefreshCw, CheckCircle2, XCircle, Fingerprint, ShieldCheck,
  Radio, BadgeCheck, Network, Lock, Loader2, ChevronRight, Wifi, PartyPopper,
} from "lucide-react";

function fp(s?: string): string {
  if (!s) return "—";
  return s.length > 16 ? `${s.slice(0, 16)}…` : s;
}

function Row({ k, v }: { k: string; v: React.ReactNode }) {
  return (
    <div className="flex justify-between gap-4 border-b border-border py-1.5 text-sm last:border-0">
      <span className="text-muted">{k}</span>
      <span className="text-right text-text">{v ?? "—"}</span>
    </div>
  );
}

function Check({ label, ok }: { label: string; ok?: boolean }) {
  return (
    <div className="flex items-center justify-between gap-4 border-b border-border py-1.5 text-sm last:border-0">
      <span className="text-muted">{label}</span>
      {ok ? (
        <span className="inline-flex items-center gap-1 text-ok"><CheckCircle2 size={15} /> pass</span>
      ) : (
        <span className="inline-flex items-center gap-1 text-err"><XCircle size={15} /> fail</span>
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

// The detailed 15-stage lifecycle stays available behind "Show technical details".
const STAGES = [
  "Awaiting enrollment", "Enrollment submitted", "Identity generated", "Enrollment accepted",
  "Pending approval", "Claimed", "Assignment issued", "Assignment adopted",
  "Certificate requested", "Certificate issued", "API mTLS connected", "NATS mTLS connected",
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
  if (licOk && mtls && nats) return 15;
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
  { title: "Connect", icon: Wifi, blurb: "Sending your code and registering with the control panel" },
  { title: "Verify", icon: ShieldCheck, blurb: "Issuing the security certificate and securing the connection" },
  { title: "Ready", icon: PartyPopper, blurb: "License active — this appliance is connected" },
];
// stage → phase index (0/1/2); stage 1 = not started (the form)
function phaseOf(stage: number): number {
  if (stage >= 14) return 2;
  if (stage >= 9) return 1;
  return 0;
}
function friendlyStatus(stage: number): string {
  switch (stage) {
    case 2: return "Sending your code to the control panel…";
    case 3: return "Creating this appliance's secure identity…";
    case 4:
    case 5: return "Waiting for the control panel to approve this appliance…";
    case 6: return "Approved — claiming the appliance…";
    case 7:
    case 8: return "Assigning to your hotel…";
    case 9:
    case 10: return "Issuing the security certificate…";
    case 11: return "Securing the connection (mTLS)…";
    case 12: return "Connecting the real-time channel…";
    case 13: return "Activating your license…";
    case 14: return "License active — finishing up…";
    case 15: return "All set — this appliance is connected.";
    default: return "Starting…";
  }
}

export default function SetupEnrollmentPage() {
  const router = useRouter();
  const authGone = useRef(false);
  const [roles, setRoles] = useState<string[]>([]);
  const [st, setSt] = useState<SetupStatus | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [showDetails, setShowDetails] = useState(false);

  const [token, setToken] = useState("");
  const [serial, setSerial] = useState("");
  const [busy, setBusy] = useState(false);
  const [tokenSubmitted, setTokenSubmitted] = useState(false);
  const [enrollErr, setEnrollErr] = useState<string | null>(null);
  const [enrollNote, setEnrollNote] = useState<string | null>(null);
  const serialPrefilled = useRef(false);
  const failCount = useRef(0);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const writable = canWrite("network", roles);

  async function load() {
    try {
      const s = await api.get<SetupStatus>("/setup/status");
      setSt(s);
      setErr(null);
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
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => {});
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

  if (!loaded) return <div className="p-6 text-sm text-muted">Loading appliance setup…</div>;

  const api_mtls = st?.api_mtls;
  const nats = st?.nats_mtls;
  const lic = st?.license;
  const net = st?.network;
  const licOk = ["active", "licensed", "grace", "graceperiod"].includes((lic?.state ?? "").toLowerCase());
  const complete = enrolled && api_mtls?.mtls_ready === true && nats?.connected === true && licOk;

  const stage = currentStage(st, tokenSubmitted);
  const inProgress = stage >= 2 && !complete;
  const activePhase = phaseOf(stage);

  return (
    <div className="mx-auto max-w-2xl space-y-6 p-6">
      <div className="flex items-center justify-between">
        <h1 className="flex items-center gap-2 text-xl font-semibold">
          <ServerCog className="h-5 w-5" /> Connect this appliance
        </h1>
        <Button variant="ghost" onClick={load}><RefreshCw className="mr-1 h-4 w-4" /> Refresh</Button>
      </div>

      {err && (
        <div className="rounded border border-[#6b2128] bg-[#3a1418] p-3 text-sm text-err">
          Couldn&apos;t reach the appliance (retrying): {err}
        </div>
      )}

      {/* ---------- SUCCESS ---------- */}
      {complete && (
        <Card>
          <CardBody className="flex flex-col items-center gap-3 py-8 text-center">
            <PartyPopper className="h-10 w-10 text-ok" />
            <div className="text-lg font-semibold">This appliance is connected</div>
            <div className="text-sm text-muted">
              {st?.assignment?.tenant_name && st?.assignment?.site_name
                ? <>Bound to <b>{st.assignment.tenant_name}</b> · {st.assignment.site_name}. </>
                : null}
              License is active and the secure connection is up. You can now create your guest networks.
            </div>
            <Badge tone="ok">Setup complete</Badge>
          </CardBody>
        </Card>
      )}

      {/* ---------- CONNECT FORM (not yet enrolled) ---------- */}
      {!enrolled && !locked && (
        <Card>
          <CardBody className="space-y-4">
            <div>
              <div className="text-base font-semibold">Connect this appliance to the control panel</div>
              <p className="text-sm text-muted">
                Most appliances connect <b>automatically</b> once they reach the internet — no code needed. The box
                self-registers and appears in the control panel under <b>Onboarding</b> as <em>Pending activation</em>,
                where an operator activates it. Only use the manual code below if your installer gave you an
                <b> enrollment token</b> (minted in the control panel under <b>Appliances → Enrollment token</b>).
              </p>
            </div>
            <div>
              <Label htmlFor="enroll-token">Enrollment code</Label>
              <Input id="enroll-token" type="password" autoComplete="off" placeholder="paste the token"
                value={token} onChange={(e) => setToken(e.target.value)} disabled={!writable || busy} />
            </div>
            <div>
              <Label htmlFor="enroll-serial">Serial</Label>
              <Input id="enroll-serial" autoComplete="off" placeholder="appliance serial"
                value={serial} onChange={(e) => setSerial(e.target.value)} disabled={!writable || busy} />
              <p className="mt-1 text-xs text-muted">Give this serial to whoever generates the token so it locks to this box.</p>
            </div>
            {enrollErr && <div className="rounded border border-[#6b2128] bg-[#3a1418] p-2 text-sm text-err">{enrollErr}</div>}
            <Button onClick={submitEnroll} disabled={!writable || busy || !token.trim() || !serial.trim()} className="w-full">
              {busy ? <><Loader2 className="mr-1 h-4 w-4 animate-spin" /> Connecting…</> : "Connect"}
            </Button>
            {!writable && <p className="text-xs text-muted">Your role can&apos;t enroll this appliance (network write required).</p>}
          </CardBody>
        </Card>
      )}

      {/* ---------- 3-PHASE PROGRESS ---------- */}
      {inProgress && (
        <Card>
          <CardBody className="space-y-5">
            <div className="flex items-center justify-between">
              {PHASES.map((p, i) => {
                const done = i < activePhase;
                const active = i === activePhase;
                const Icon = p.icon;
                return (
                  <div key={p.title} className="flex flex-1 items-center">
                    <div className="flex flex-col items-center gap-1 text-center">
                      <span className={"inline-flex h-10 w-10 items-center justify-center rounded-full " +
                        (done ? "bg-ok/20 text-ok" : active ? "bg-brand/25 text-brand" : "bg-panel2 text-muted")}>
                        {done ? <CheckCircle2 className="h-5 w-5" /> : active ? <Loader2 className="h-5 w-5 animate-spin" /> : <Icon className="h-5 w-5" />}
                      </span>
                      <span className={"text-xs " + (active ? "font-medium text-text" : "text-muted")}>{p.title}</span>
                    </div>
                    {i < PHASES.length - 1 && <ChevronRight className="mx-1 h-4 w-4 shrink-0 text-muted/40" />}
                  </div>
                );
              })}
            </div>
            <div className="rounded-md border border-border bg-panel2 px-4 py-3 text-center">
              <div className="flex items-center justify-center gap-2 text-sm">
                <Loader2 className="h-4 w-4 animate-spin text-brand" />
                {friendlyStatus(stage)}
              </div>
              <div className="mt-1 text-xs text-muted">{PHASES[activePhase]?.blurb}</div>
            </div>
            {enrollNote && <div className="text-center text-xs text-muted">{enrollNote}</div>}
          </CardBody>
        </Card>
      )}

      {/* ---------- ALREADY ENROLLED but form locked & not in-progress edge ---------- */}
      {locked && !inProgress && !complete && (
        <Card><CardBody className="text-sm text-muted">This appliance is enrolled; waiting on the control panel to finish setup…</CardBody></Card>
      )}

      {/* ---------- TECHNICAL DETAILS (collapsed) ---------- */}
      <div>
        <button className="inline-flex items-center gap-1 text-sm text-muted hover:text-text" onClick={() => setShowDetails((v) => !v)}>
          <ChevronRight className={"h-4 w-4 transition-transform " + (showDetails ? "rotate-90" : "")} />
          {showDetails ? "Hide technical details" : `Show technical details (${STAGES.length} checks)`}
        </button>
      </div>

      {showDetails && (
        <div className="space-y-4">
          <Card>
            <StepHeader title="Onboarding progress" icon={<ServerCog className="h-4 w-4" />} />
            <CardBody>
              <ol className="grid gap-x-6 gap-y-1 sm:grid-cols-2">
                {STAGES.map((label, i) => {
                  const n = i + 1; const done = n < stage; const active = n === stage;
                  return (
                    <li key={label} className="flex items-center gap-2 text-sm">
                      <span className={"inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-full text-[11px] " +
                        (done ? "bg-ok/20 text-ok" : active ? "bg-brand/25 text-brand" : "bg-panel2 text-muted")}>
                        {done ? "✓" : n}
                      </span>
                      <span className={done ? "text-muted line-through" : active ? "text-text font-medium" : "text-muted"}>{label}</span>
                    </li>
                  );
                })}
              </ol>
            </CardBody>
          </Card>

          <Card>
            <StepHeader title="Appliance identity" icon={<Fingerprint className="h-4 w-4" />} />
            <CardBody className="grid gap-x-8 md:grid-cols-2">
              <div>
                <Row k="Serial" v={<code>{st?.serial || "—"}</code>} />
                <Row k="Appliance ID" v={<code>{st?.appliance_id || "—"}</code>} />
                <Row k="Version" v={<code>{st?.version || "—"}</code>} />
              </div>
              <div>
                <Row k="Identity key fingerprint" v={<code title={st?.identity_key_fingerprint}>{fp(st?.identity_key_fingerprint)}</code>} />
                <Row k="mTLS cert fingerprint" v={<code title={api_mtls?.cert_fingerprint}>{fp(api_mtls?.cert_fingerprint)}</code>} />
              </div>
            </CardBody>
          </Card>

          <Card>
            <StepHeader title="Network & Central checks" icon={<Network className="h-4 w-4" />} />
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
            <StepHeader title="Certificate (API mTLS)" icon={<ShieldCheck className="h-4 w-4" />} />
            <CardBody>
              <Row k="Status" v={api_mtls?.mtls_ready ? <Badge tone="ok">API mTLS ready</Badge> : <Badge tone="warn">Not ready</Badge>} />
              <Row k="Cert fingerprint" v={<code title={api_mtls?.cert_fingerprint}>{fp(api_mtls?.cert_fingerprint)}</code>} />
              <Row k="Expires (not after)" v={api_mtls?.not_after || "—"} />
            </CardBody>
          </Card>

          <Card>
            <StepHeader title="NATS transport" icon={<Radio className="h-4 w-4" />} />
            <CardBody>
              <Row k="Status" v={nats?.connected ? <Badge tone="ok">NATS mTLS connected</Badge> : <Badge tone="err">Disconnected</Badge>} />
              <Row k="Mode" v={<Badge tone={nats?.mtls ? "ok" : "warn"}>{nats?.mtls ? "mTLS" : "legacy"}</Badge>} />
            </CardBody>
          </Card>

          <Card>
            <StepHeader title="License" icon={<BadgeCheck className="h-4 w-4" />} />
            <CardBody>
              <Row k="State" v={<Badge tone={licenseTone(lic?.state)}>{lic?.state || "unknown"}</Badge>} />
              <Row k="Max online guests" v={lic?.max_concurrent_online_guests == null ? "—" : lic.max_concurrent_online_guests === -1 ? "Unlimited" : String(lic.max_concurrent_online_guests)} />
              <Row k="Valid until" v={lic?.valid_until || "—"} />
              <Row k="Offline grace" v={lic?.offline_grace_days != null ? `${lic.offline_grace_days} days` : "—"} />
            </CardBody>
          </Card>

          <Card>
            <StepHeader title="Completion" icon={<CheckCircle2 className="h-4 w-4" />} />
            <CardBody className="grid gap-x-8 md:grid-cols-2">
              <div>
                <Row k="Customer" v={st?.assignment?.tenant_name || "—"} />
                <Row k="Site" v={st?.assignment?.site_name || "—"} />
                <Row k="Assignment version" v={st?.assignment?.version ?? "—"} />
              </div>
              <div>
                <Row k="Enrolled" v={<Badge tone={enrolled ? "ok" : "err"}>{enrolled ? "yes" : "no"}</Badge>} />
                <Row k="Licensed" v={<Badge tone={licOk ? "ok" : "err"}>{licOk ? "yes" : "no"}</Badge>} />
                <Row k="Connected" v={<Badge tone={nats?.connected ? "ok" : "err"}>{nats?.connected ? "yes" : "no"}</Badge>} />
                <Row k="Outbox (pending/dead)" v={`${st?.outbox?.pending ?? 0} / ${st?.outbox?.dead ?? 0}`} />
              </div>
            </CardBody>
          </Card>

          <p className="text-xs text-muted">
            Every field is read live from the appliance every 5s and reflects real state. Secrets
            (enrollment token, private keys, NATS credentials) are never displayed.
          </p>
        </div>
      )}
    </div>
  );
}
