"use client";

import { useEffect, useRef, useState } from "react";
import { api, ApiError, EnrollResult, SetupStatus, Whoami } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input, Label } from "@/components/ui/input";
import { canWrite } from "@/lib/roles";
import { errMsg } from "@/lib/utils";
import {
  ServerCog, RefreshCw, CheckCircle2, XCircle, Fingerprint, ShieldCheck,
  Radio, BadgeCheck, Network, Lock,
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

// Check — a single green/red network probe line derived from a real boolean.
function Check({ label, ok }: { label: string; ok?: boolean }) {
  return (
    <div className="flex items-center justify-between gap-4 border-b border-border py-1.5 text-sm last:border-0">
      <span className="text-muted">{label}</span>
      {ok ? (
        <span className="inline-flex items-center gap-1 text-ok">
          <CheckCircle2 size={15} /> pass
        </span>
      ) : (
        <span className="inline-flex items-center gap-1 text-err">
          <XCircle size={15} /> fail
        </span>
      )}
    </div>
  );
}

// StepHeader — numbered step with an icon, matching the wizard ordering.
function StepHeader({ n, title, icon }: { n: number; title: string; icon: React.ReactNode }) {
  return (
    <CardHeader>
      <CardTitle className="flex items-center gap-2">
        <span className="inline-flex h-6 w-6 items-center justify-center rounded-full bg-panel2 text-xs text-muted">
          {n}
        </span>
        {icon}
        {title}
      </CardTitle>
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

// STAGES is the production onboarding lifecycle shown to the operator. The
// current stage is derived purely from live appliance facts (never hardcoded);
// stages Central drives but the appliance cannot directly observe (claim, assign)
// are marked done once a later appliance-observable milestone is reached.
const STAGES = [
  "Awaiting enrollment",
  "Token submitted",
  "Identity generated",
  "Enrollment accepted",
  "Pending approval",
  "Claimed",
  "Assignment issued",
  "Assignment adopted",
  "Certificate requested",
  "Certificate issued",
  "API mTLS connected",
  "NATS mTLS connected",
  "Awaiting license",
  "License active",
  "Setup complete",
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
  if (assigned && mtls) return 13; // connected + assigned, awaiting license
  if (nats) return 12;
  if (mtls) return 11;
  if (hasCert) return 10;
  if (adopted) return 8;
  if (st.enrolled) return 5; // enrolled, waiting on Central to approve/claim/assign
  if (st.appliance_id) return 3;
  if (tokenSubmitted) return 2;
  return 1;
}

export default function SetupEnrollmentPage() {
  const [roles, setRoles] = useState<string[]>([]);
  const [st, setSt] = useState<SetupStatus | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);

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
      if (!serialPrefilled.current && s.serial) {
        setSerial(s.serial);
        serialPrefilled.current = true;
      }
    } catch (e) {
      // Bounded backoff when the appliance/Central is unreachable — the last known
      // stage stays on screen; we do not lose progress or resubmit.
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
      // 5s normal; back off up to 30s while erroring.
      const delay = 5000 * (failCount.current > 0 ? Math.min(2 ** failCount.current, 6) : 1);
      timer.current = setTimeout(tick, delay);
    };
    tick();
    return () => {
      stopped = true;
      if (timer.current) clearTimeout(timer.current);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const enrolled = st?.enrolled === true;
  const locked = st?.locked === true || enrolled;

  async function submitEnroll() {
    // Client-side guard: never POST a token when already enrolled / locked.
    if (locked || !writable || busy) return;
    if (!token.trim() || !serial.trim()) {
      setEnrollErr("Enrollment token and serial are both required.");
      return;
    }
    setBusy(true);
    setEnrollErr(null);
    setEnrollNote(null);
    try {
      const r = await api.post<EnrollResult>("/setup/enroll", {
        token: token.trim(),
        serial: serial.trim(),
      });
      setEnrollNote(r.note || "Enrollment accepted — finalizing…");
      setTokenSubmitted(true);
      setToken(""); // drop the token from memory the moment it is submitted
      await load();
    } catch (e) {
      // 409 = already enrolled; surface gracefully and refresh to lock the form.
      if (e instanceof ApiError && e.status === 409) {
        setEnrollErr(e.message || "Already enrolled — setup is locked.");
        await load();
      } else {
        setEnrollErr(errMsg(e));
      }
    } finally {
      setBusy(false);
    }
  }

  if (!loaded) return <div className="p-6 text-sm text-muted">Loading appliance setup…</div>;

  const api_mtls = st?.api_mtls;
  const nats = st?.nats_mtls;
  const lic = st?.license;
  const net = st?.network;

  // Overall completion is derived purely from live fields — never hardcoded.
  const licOk = ["active", "licensed", "grace", "graceperiod"].includes((lic?.state ?? "").toLowerCase());
  const complete = enrolled && api_mtls?.mtls_ready === true && nats?.connected === true && licOk;

  return (
    <div className="space-y-6 p-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="flex items-center gap-2 text-xl font-semibold">
            <ServerCog className="h-5 w-5" /> Appliance setup &amp; enrollment
          </h1>
          <p className="text-sm text-muted">
            Enroll this appliance with the Central Control Plane and verify its identity, certificates,
            transport and license. Every value below is read live from the appliance.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <Badge tone={complete ? "ok" : "warn"}>{complete ? "Setup complete" : "Setup incomplete"}</Badge>
          <Button variant="secondary" onClick={load}>
            <RefreshCw className="mr-1 h-4 w-4" /> Refresh
          </Button>
        </div>
      </div>

      {err && (
        <div className="rounded border border-[#6b2128] bg-[#3a1418] p-3 text-sm text-err">
          Could not read setup status (retrying automatically): {err}
        </div>
      )}

      {/* Onboarding lifecycle — auto-updates, no manual refresh needed */}
      <Card>
        <StepHeader n={0} title="Onboarding progress" icon={<ServerCog className="h-4 w-4" />} />
        <CardBody>
          {(() => {
            const cur = currentStage(st, tokenSubmitted);
            return (
              <ol className="grid gap-x-6 gap-y-1 sm:grid-cols-2 lg:grid-cols-3">
                {STAGES.map((label, i) => {
                  const n = i + 1;
                  const done = n < cur;
                  const active = n === cur;
                  return (
                    <li key={label} className="flex items-center gap-2 text-sm">
                      <span className={
                        "inline-flex h-5 w-5 shrink-0 items-center justify-center rounded-full text-[11px] " +
                        (done ? "bg-ok/20 text-ok" : active ? "bg-brand/25 text-brand" : "bg-panel2 text-muted")
                      }>
                        {done ? "✓" : n}
                      </span>
                      <span className={done ? "text-muted line-through" : active ? "text-text font-medium" : "text-muted"}>
                        {label}
                      </span>
                    </li>
                  );
                })}
              </ol>
            );
          })()}
        </CardBody>
      </Card>

      {/* Step 1 — Appliance identity */}
      <Card>
        <StepHeader n={1} title="Appliance identity" icon={<Fingerprint className="h-4 w-4" />} />
        <CardBody className="grid gap-x-8 gap-y-0 md:grid-cols-2">
          <div>
            <Row k="Serial" v={<code>{st?.serial || "—"}</code>} />
            <Row k="Appliance ID" v={<code>{st?.appliance_id || "—"}</code>} />
            <Row k="Version" v={<code>{st?.version || "—"}</code>} />
          </div>
          <div>
            <Row
              k="Identity key fingerprint"
              v={<code title={st?.identity_key_fingerprint}>{fp(st?.identity_key_fingerprint)}</code>}
            />
            <Row
              k="mTLS cert fingerprint"
              v={<code title={api_mtls?.cert_fingerprint}>{fp(api_mtls?.cert_fingerprint)}</code>}
            />
          </div>
          <p className="mt-2 text-xs text-muted md:col-span-2">
            The <b>identity key</b> fingerprint (the appliance&apos;s device key) and the <b>mTLS cert</b>{" "}
            fingerprint (its client certificate to Central) are distinct credentials shown separately.
          </p>
        </CardBody>
      </Card>

      {/* Step 2 — Network & Central checks */}
      <Card>
        <StepHeader n={2} title="Network &amp; Central checks" icon={<Network className="h-4 w-4" />} />
        <CardBody className="grid gap-x-8 gap-y-0 md:grid-cols-2">
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

      {/* Step 3 — Enrollment */}
      <Card>
        <StepHeader n={3} title="Enrollment" icon={<Lock className="h-4 w-4" />} />
        <CardBody>
          {locked ? (
            <div className="space-y-2">
              <Badge tone="ok">
                <ShieldCheck className="mr-1 h-3.5 w-3.5" /> Enrolled — setup locked
              </Badge>
              <p className="text-sm text-muted">
                This appliance is already enrolled. The enrollment token form is disabled and no token is
                shown. Re-enrollment is prevented on the appliance.
              </p>
            </div>
          ) : (
            <div className="max-w-xl space-y-3">
              <p className="text-sm text-muted">
                Paste the one-time enrollment token issued by the Central Control Plane. The token is
                write-only — it is never displayed or stored in the browser after submission.
              </p>
              <div>
                <Label htmlFor="enroll-token">Enrollment token</Label>
                <Input
                  id="enroll-token"
                  type="password"
                  autoComplete="off"
                  placeholder="paste enrollment token"
                  value={token}
                  onChange={(e) => setToken(e.target.value)}
                  disabled={!writable || busy}
                />
              </div>
              <div>
                <Label htmlFor="enroll-serial">Serial</Label>
                <Input
                  id="enroll-serial"
                  autoComplete="off"
                  placeholder="appliance serial"
                  value={serial}
                  onChange={(e) => setSerial(e.target.value)}
                  disabled={!writable || busy}
                />
              </div>
              {enrollErr && (
                <div className="rounded border border-[#6b2128] bg-[#3a1418] p-2 text-sm text-err">
                  {enrollErr}
                </div>
              )}
              {enrollNote && (
                <div className="rounded border border-[#6b4e1c] bg-[#3a2a0e] p-2 text-sm text-warn">
                  {enrollNote}
                </div>
              )}
              <div className="flex items-center gap-3">
                <Button onClick={submitEnroll} disabled={!writable || busy || !token.trim() || !serial.trim()}>
                  {busy ? "Enrolling…" : "Submit enrollment"}
                </Button>
                {!writable && (
                  <span className="text-xs text-muted">
                    Your role cannot enroll this appliance (network write required).
                  </span>
                )}
              </div>
            </div>
          )}
        </CardBody>
      </Card>

      {/* Step 4 — Certificate */}
      <Card>
        <StepHeader n={4} title="Certificate (API mTLS)" icon={<ShieldCheck className="h-4 w-4" />} />
        <CardBody>
          <Row
            k="Status"
            v={
              api_mtls?.mtls_ready ? (
                <Badge tone="ok">API mTLS ready</Badge>
              ) : (
                <Badge tone="warn">Not ready</Badge>
              )
            }
          />
          <Row
            k="Cert fingerprint"
            v={<code title={api_mtls?.cert_fingerprint}>{fp(api_mtls?.cert_fingerprint)}</code>}
          />
          <Row k="Expires (not after)" v={api_mtls?.not_after || "—"} />
        </CardBody>
      </Card>

      {/* Step 5 — NATS */}
      <Card>
        <StepHeader n={5} title="NATS transport" icon={<Radio className="h-4 w-4" />} />
        <CardBody>
          <Row
            k="Status"
            v={
              nats?.connected ? (
                <Badge tone="ok">NATS mTLS connected</Badge>
              ) : (
                <Badge tone="err">Disconnected</Badge>
              )
            }
          />
          <Row k="Mode" v={<Badge tone={nats?.mtls ? "ok" : "warn"}>{nats?.mtls ? "mTLS" : "legacy"}</Badge>} />
        </CardBody>
      </Card>

      {/* Step 6 — License */}
      <Card>
        <StepHeader n={6} title="License" icon={<BadgeCheck className="h-4 w-4" />} />
        <CardBody>
          <Row k="State" v={<Badge tone={licenseTone(lic?.state)}>{lic?.state || "unknown"}</Badge>} />
          <Row k="Plan" v={lic?.plan || "—"} />
          <Row k="Valid until" v={lic?.valid_until || "—"} />
          <Row
            k="Offline grace"
            v={lic?.offline_grace_days != null ? `${lic.offline_grace_days} days` : "—"}
          />
        </CardBody>
      </Card>

      {/* Step 7 — Completion */}
      <Card>
        <StepHeader n={7} title="Completion" icon={<CheckCircle2 className="h-4 w-4" />} />
        <CardBody className="grid gap-x-8 gap-y-0 md:grid-cols-2">
          <div>
            <Row k="Customer" v={st?.assignment?.tenant_name || "—"} />
            <Row k="Site" v={st?.assignment?.site_name || "—"} />
            <Row k="Assignment version" v={st?.assignment?.version ?? "—"} />
            <Row k="Appliance ID" v={<code>{st?.appliance_id || "—"}</code>} />
          </div>
          <div>
            <Row k="Enrolled" v={<Badge tone={enrolled ? "ok" : "err"}>{enrolled ? "yes" : "no"}</Badge>} />
            <Row
              k="Licensed"
              v={<Badge tone={licOk ? "ok" : "err"}>{licOk ? "yes" : "no"}</Badge>}
            />
            <Row
              k="Connected"
              v={<Badge tone={nats?.connected ? "ok" : "err"}>{nats?.connected ? "yes" : "no"}</Badge>}
            />
            <Row k="Outbox (pending/dead)" v={`${st?.outbox?.pending ?? 0} / ${st?.outbox?.dead ?? 0}`} />
            <Row k="Last Central sync" v={st?.assignment?.last_refresh_success || "—"} />
          </div>
          <div className="mt-3 md:col-span-2">
            <Badge tone={complete ? "ok" : "warn"}>
              {complete ? "Enrolled / Licensed / Connected" : "Setup not yet complete"}
            </Badge>
          </div>
        </CardBody>
      </Card>

      <p className="text-xs text-muted">
        Every field is read live from the appliance every 5 seconds and reflects real state — the
        completion summary is derived, never hardcoded. Secrets (enrollment token, private keys, NATS
        credentials) are never displayed.
      </p>
    </div>
  );
}
