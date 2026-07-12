"use client";

import { useEffect, useState } from "react";
import { api, ApiError, Whoami } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { errMsg } from "@/lib/utils";
import { ShieldCheck, RefreshCw, RotateCw, CheckCircle2 } from "lucide-react";

type CertStatus = {
  available?: boolean;
  subject?: string;
  issuer?: string;
  serial?: string;
  fingerprint_sha256?: string;
  dns_sans?: string[];
  ip_sans?: string[];
  issued_at?: string;
  expires_at?: string;
  days_remaining?: number;
  status_threshold?: string;
  current_management_ip?: string;
  san_config_match?: boolean;
  last_renewal_attempt?: string;
  last_successful_renewal?: string;
  last_renewal_result?: string;
  last_error?: string;
};

const THRESH: Record<string, { tone: "ok" | "info" | "warn" | "err"; label: string }> = {
  healthy:     { tone: "ok",   label: "Healthy" },
  renewal_due: { tone: "info", label: "Renewal due" },
  warning:     { tone: "warn", label: "Warning" },
  critical:    { tone: "warn", label: "Critical" },
  emergency:   { tone: "err",  label: "Emergency" },
  expired:     { tone: "err",  label: "Expired" },
};

function Row({ k, v }: { k: string; v: React.ReactNode }) {
  return (
    <div className="flex justify-between gap-4 border-b border-border py-1.5 text-sm last:border-0">
      <span className="text-muted">{k}</span>
      <span className="text-right text-text break-all">{v ?? "—"}</span>
    </div>
  );
}

export default function CertificatePage() {
  const [me, setMe] = useState<Whoami | null>(null);
  const [st, setSt] = useState<CertStatus | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  // rotate form
  const [showRotate, setShowRotate] = useState(false);
  const [reason, setReason] = useState("");
  const [password, setPassword] = useState("");
  const [confirm, setConfirm] = useState("");

  async function load() {
    try {
      setSt(await api.get<CertStatus>("/hotel-admin-cert"));
    } catch (e) { setErr(errMsg(e)); }
  }
  useEffect(() => {
    (async () => {
      try { setMe(await api.get<Whoami>("/auth/whoami")); } catch { /* layout guards */ }
      load();
    })();
  }, []);

  const writable = me ? canWrite("network", me.roles) : false;
  const thr = THRESH[st?.status_threshold ?? ""] ?? { tone: "default" as any, label: st?.status_threshold ?? "—" };

  async function check() {
    setBusy("check"); setErr(null); setNote(null);
    try {
      const r = await api.post<{ ok: boolean; exit: number }>("/hotel-admin-cert/check", {});
      setNote(r.ok ? "Certificate validated — no problems found." : `Validation reported a problem (exit ${r.exit}).`);
      await load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(null); }
  }

  async function rotate(e: React.FormEvent) {
    e.preventDefault();
    setBusy("rotate"); setErr(null); setNote(null);
    try {
      const r = await api.post<{ ok: boolean; exit: number }>("/hotel-admin-cert/rotate", {
        reason, password, confirmation: confirm,
      });
      setNote(r.ok ? "Certificate rotated successfully." : `Rotation failed (exit ${r.exit}); the previous certificate is still serving.`);
      setShowRotate(false); setReason(""); setPassword(""); setConfirm("");
      await load();
    } catch (e) {
      if (e instanceof ApiError && e.body?.error === "reauth_required") setErr("Password confirmation failed.");
      else setErr(errMsg(e));
    } finally { setBusy(null); }
  }

  return (
    <div className="p-6 max-w-3xl mx-auto">
      <div className="flex items-baseline justify-between mb-1">
        <h1 className="text-2xl font-semibold flex items-center gap-2"><ShieldCheck className="h-5 w-5" /> Hotel Admin TLS certificate</h1>
        <Button variant="ghost" size="sm" onClick={load}><RefreshCw size={14} /> Refresh</Button>
      </div>
      <p className="text-sm text-muted mb-4">
        The dual-SAN certificate for <code>hotel.stayconnect.local</code> and the management IP. Renewal is
        automatic (checked daily; renews at 45 days, on IP change, or SAN drift). It is issued from the local
        StayConnect CA — never the vendor appliance PKI.
      </p>

      {err && <div className="text-err text-sm mb-3">{err}</div>}
      {note && <div className="text-sm mb-3 inline-flex items-center gap-1 text-ok"><CheckCircle2 size={14} /> {note}</div>}

      <Card className="mb-4">
        <CardHeader>
          <CardTitle>Status</CardTitle>
          <Badge tone={thr.tone as any}>{thr.label}{typeof st?.days_remaining === "number" ? ` · ${st.days_remaining}d left` : ""}</Badge>
        </CardHeader>
        <CardBody>
          {!st ? <div className="text-sm text-muted">Loading…</div> : st.available === false ? (
            <div className="text-sm text-warn">No certificate status available yet. Run “Check certificate”.</div>
          ) : (
            <>
              <Row k="Subject" v={<code>{st.subject}</code>} />
              <Row k="Issuer" v={<code>{st.issuer}</code>} />
              <Row k="Serial" v={<code>{st.serial}</code>} />
              <Row k="SHA-256 fingerprint" v={<code className="text-xs">{st.fingerprint_sha256}</code>} />
              <Row k="DNS SANs" v={(st.dns_sans ?? []).join(", ")} />
              <Row k="IP SANs" v={(st.ip_sans ?? []).join(", ")} />
              <Row k="Current management IP" v={<code>{st.current_management_ip}</code>} />
              <Row k="SAN configuration match" v={st.san_config_match ? <span className="text-ok">yes</span> : <span className="text-err">no</span>} />
              <Row k="Issued at" v={st.issued_at} />
              <Row k="Expires at" v={st.expires_at} />
              <Row k="Days remaining" v={st.days_remaining} />
              <Row k="Last successful renewal" v={st.last_successful_renewal || "—"} />
              <Row k="Last renewal attempt" v={st.last_renewal_attempt || "—"} />
              <Row k="Last renewal result" v={st.last_renewal_result || "—"} />
              {st.last_error ? <Row k="Last error" v={<span className="text-err">{st.last_error}</span>} /> : null}
            </>
          )}
        </CardBody>
      </Card>

      <div className="flex items-center gap-2">
        <Button variant="secondary" disabled={!writable || busy !== null} onClick={check}>
          {busy === "check" ? "Checking…" : <><CheckCircle2 size={14} /> Check certificate</>}
        </Button>
        <Button disabled={!writable || busy !== null} onClick={() => setShowRotate((s) => !s)}>
          <RotateCw size={14} /> Rotate Hotel Admin certificate
        </Button>
      </div>
      {!writable && <p className="text-xs text-muted mt-2">Rotation and diagnostics require the Hotel IT (network) role.</p>}

      {showRotate && (
        <Card className="mt-4 border-warn">
          <CardHeader><CardTitle>Rotate certificate</CardTitle></CardHeader>
          <CardBody>
            <p className="text-sm text-muted mb-3">
              Mints a new dual-SAN certificate through the same safe lifecycle (validate → atomic swap →
              reload → health check → automatic rollback on failure). You cannot upload a key.
            </p>
            <form onSubmit={rotate} className="space-y-3">
              <div><Label>Reason</Label><Input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="why are you rotating?" required /></div>
              <div><Label>Confirm your password</Label><Input type="password" value={password} onChange={(e) => setPassword(e.target.value)} required /></div>
              <div><Label>Type ROTATE to confirm</Label><Input value={confirm} onChange={(e) => setConfirm(e.target.value)} placeholder="ROTATE" required /></div>
              <div className="flex justify-end gap-2">
                <Button type="button" variant="ghost" onClick={() => setShowRotate(false)}>Cancel</Button>
                <Button type="submit" disabled={busy !== null}>{busy === "rotate" ? "Rotating…" : "Rotate now"}</Button>
              </div>
            </form>
          </CardBody>
        </Card>
      )}
    </div>
  );
}
