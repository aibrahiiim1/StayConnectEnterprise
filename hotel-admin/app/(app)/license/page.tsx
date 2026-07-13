"use client";

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { api, ApiError, SetupStatus, LicenseStatus, LicenseFeatures } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { errMsg, formatDate } from "@/lib/utils";
import { canWrite } from "@/lib/roles";
import {
  BadgeCheck, Copy, Check, Cpu, Upload, ChevronRight, ShieldCheck, Building2,
} from "lucide-react";

function fp(s?: string): string {
  if (!s) return "—";
  return s.length > 20 ? `${s.slice(0, 20)}…` : s;
}

function activationTone(a?: string): "ok" | "warn" | "err" | "default" {
  switch (a) {
    case "activated": case "licensed": return "ok";
    case "pending_activation": return "warn";
    case "unlicensed": return "err";
    default: return "default";
  }
}
function activationLabel(a?: string): string {
  switch (a) {
    case "activated": return "Activated";
    case "licensed": return "Licensed";
    case "pending_activation": return "Pending activation";
    case "unlicensed": return "Not activated";
    default: return a || "unknown";
  }
}
function licenseTone(state?: string): "ok" | "warn" | "err" | "default" {
  switch (state) {
    case "Active": return "ok";
    case "GracePeriod": case "Restricted": case "Suspended": return "warn";
    case "Expired": case "Revoked": return "err";
    default: return "default";
  }
}

// CopyField — a labelled value with a one-click copy button (Serial, MACs).
function CopyField({ label, value, big }: { label: string; value?: string; big?: boolean }) {
  const [copied, setCopied] = useState(false);
  const v = value || "—";
  return (
    <div className="space-y-1">
      <div className="text-xs uppercase tracking-wide text-muted">{label}</div>
      <div className="flex items-center gap-2">
        <code className={(big ? "text-lg " : "text-sm ") + "font-mono break-all rounded-md border border-border bg-panel2 px-3 py-1.5"}>{v}</code>
        {value && (
          <Button size="sm" variant="ghost" onClick={() => { navigator.clipboard?.writeText(value); setCopied(true); setTimeout(() => setCopied(false), 1400); }}>
            {copied ? <Check className="h-4 w-4 text-ok" /> : <Copy className="h-4 w-4" />}
          </Button>
        )}
      </div>
    </div>
  );
}

function Row({ k, v }: { k: string; v: React.ReactNode }) {
  return (
    <div className="flex justify-between gap-4 border-b border-border py-1.5 text-sm last:border-0">
      <span className="text-muted">{k}</span>
      <span className="text-right text-text">{v ?? "—"}</span>
    </div>
  );
}

const FEATURE_LABELS: Record<keyof LicenseFeatures, string> = {
  pms: "PMS integration", paid_wifi: "Paid WiFi", sms_otp: "SMS OTP", email_otp: "Email OTP",
  social_login: "Social login", ha: "High availability", white_label: "White label",
};

export default function LicensePage() {
  const router = useRouter();
  const [roles, setRoles] = useState<string[]>([]);
  const [st, setSt] = useState<SetupStatus | null>(null);
  const [ls, setLs] = useState<LicenseStatus | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [showAdvanced, setShowAdvanced] = useState(false);
  const [uploadMsg, setUploadMsg] = useState<string | null>(null);
  const [uploadErr, setUploadErr] = useState<string | null>(null);
  const [uploading, setUploading] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const authGone = useRef(false);
  const writable = canWrite("license", roles) || canWrite("network", roles);

  async function load() {
    try {
      const [s, l] = await Promise.all([
        api.get<SetupStatus>("/setup/status"),
        api.get<LicenseStatus>("/license/status").catch(() => null),
      ]);
      setSt(s); if (l) setLs(l); setErr(null);
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        if (!authGone.current) { authGone.current = true; try { await api.post("/auth/logout"); } catch {} router.replace("/login"); }
        return;
      }
      setErr(errMsg(e));
    } finally { setLoaded(true); }
  }

  useEffect(() => {
    api.get<{ roles?: string[] }>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => {});
    load();
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function onUpload(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    if (!file) return;
    setUploading(true); setUploadErr(null); setUploadMsg(null);
    try {
      const text = await file.text();
      await api.postRaw("/license/install", text.trim());
      setUploadMsg("License file accepted and installed.");
      await load();
    } catch (err) {
      setUploadErr(errMsg(err));
    } finally {
      setUploading(false);
      if (fileRef.current) fileRef.current.value = "";
    }
  }

  if (!loaded) return <div className="p-6 text-sm text-muted">Loading license…</div>;

  const hw = st?.hardware;
  const activation = st?.activation_status;
  const lic = st?.license;
  const asg = st?.assignment;
  const activated = activation === "activated" || activation === "licensed";

  return (
    <div className="mx-auto max-w-3xl space-y-6 p-6">
      <div className="flex items-center justify-between">
        <h1 className="flex items-center gap-2 text-xl font-semibold"><BadgeCheck className="h-5 w-5" /> License &amp; Activation</h1>
        <Badge tone={activationTone(activation)}>{activationLabel(activation)}</Badge>
      </div>

      {err && <div className="rounded border border-[#6b2128] bg-[#3a1418] p-3 text-sm text-err">Couldn&apos;t read status (retrying): {err}</div>}

      {/* ---- Activate: the two values the operator sends to StayConnect ---- */}
      <Card>
        <CardHeader><CardTitle className="flex items-center gap-2"><Cpu className="h-4 w-4" /> Appliance identity</CardTitle></CardHeader>
        <CardBody className="space-y-4">
          {!activated && (
            <p className="text-sm text-muted">
              To activate this appliance, send these two values to StayConnect:
              your <b>Serial Number</b> and <b>WAN MAC Address</b>.
            </p>
          )}
          <div className="grid gap-4 sm:grid-cols-2">
            <CopyField label="StayConnect Serial Number" value={hw?.serial || st?.serial} big />
            <CopyField label="WAN MAC Address" value={hw?.wan_mac} big />
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <CopyField label="LAN MAC Address" value={hw?.lan_mac} />
            <div className="space-y-1">
              <div className="text-xs uppercase tracking-wide text-muted">Appliance</div>
              <div className="text-sm">
                <div>{hw?.model || "—"}</div>
                <div className="text-muted">host: {hw?.hostname || "—"} · WAN {hw?.wan_interface || "—"} · LAN {hw?.lan_interface || "—"}</div>
              </div>
            </div>
          </div>
        </CardBody>
      </Card>

      {/* ---- License status ---- */}
      <Card>
        <CardHeader><CardTitle className="flex items-center gap-2"><ShieldCheck className="h-4 w-4" /> License</CardTitle></CardHeader>
        <CardBody className="grid gap-x-8 md:grid-cols-2">
          <div>
            <Row k="Activation" v={<Badge tone={activationTone(activation)}>{activationLabel(activation)}</Badge>} />
            <Row k="License status" v={<Badge tone={licenseTone(ls?.state ?? lic?.state)}>{ls?.state ?? lic?.state ?? "—"}</Badge>} />
            <Row k="Plan" v={lic?.plan || ls?.commercial_plan_code || "—"} />
            <Row k="Valid from" v={ls?.issued_at ? formatDate(ls.issued_at) : "—"} />
          </div>
          <div>
            <Row k="Valid until" v={lic?.valid_until ? formatDate(lic.valid_until) : (ls?.valid_until ? formatDate(ls.valid_until) : "—")} />
            <Row k="Offline grace" v={lic?.offline_grace_days != null ? `${lic.offline_grace_days} days` : (ls?.offline_grace_days != null ? `${ls.offline_grace_days} days` : "—")} />
            <Row k="Customer" v={<span className="inline-flex items-center gap-1"><Building2 className="h-3.5 w-3.5 text-muted" />{asg?.tenant_name || "—"}</span>} />
            <Row k="Hotel / Site" v={asg?.site_name || "—"} />
          </div>
        </CardBody>
      </Card>

      {/* ---- Offline activation: upload a signed license file ---- */}
      <Card>
        <CardHeader><CardTitle className="flex items-center gap-2"><Upload className="h-4 w-4" /> Offline activation</CardTitle></CardHeader>
        <CardBody className="space-y-3">
          <p className="text-sm text-muted">
            No connection to Central? Get a signed license file from StayConnect (generated for this Serial + WAN MAC) and upload it here.
            The appliance verifies it is bound to this exact hardware before accepting.
          </p>
          <input ref={fileRef} type="file" accept=".license,.json,application/json" onChange={onUpload} disabled={!writable || uploading} className="hidden" id="lic-file" />
          <Button variant="secondary" disabled={!writable || uploading} onClick={() => fileRef.current?.click()}>
            {uploading ? "Installing…" : "Upload license file"}
          </Button>
          {uploadMsg && <div className="rounded border border-[#2d5a3d] bg-[#12261a] p-2 text-sm text-ok">{uploadMsg}</div>}
          {uploadErr && <div className="rounded border border-[#6b2128] bg-[#3a1418] p-2 text-sm text-err">{uploadErr}</div>}
          {!writable && <p className="text-xs text-muted">Your role cannot install a license.</p>}
        </CardBody>
      </Card>

      {/* ---- Advanced (technical details) ---- */}
      <div>
        <button className="inline-flex items-center gap-1 text-sm text-muted hover:text-text" onClick={() => setShowAdvanced((v) => !v)}>
          <ChevronRight className={"h-4 w-4 transition-transform " + (showAdvanced ? "rotate-90" : "")} />
          {showAdvanced ? "Hide technical details" : "Show technical details"}
        </button>
      </div>

      {showAdvanced && (
        <div className="space-y-4">
          <Card>
            <CardHeader><CardTitle>Identity &amp; transport</CardTitle></CardHeader>
            <CardBody className="grid gap-x-8 md:grid-cols-2">
              <div>
                <Row k="Appliance ID" v={<code>{st?.appliance_id || "—"}</code>} />
                <Row k="Identity key fingerprint" v={<code title={st?.identity_key_fingerprint}>{fp(st?.identity_key_fingerprint)}</code>} />
                <Row k="mTLS cert fingerprint" v={<code title={st?.api_mtls?.cert_fingerprint}>{fp(st?.api_mtls?.cert_fingerprint)}</code>} />
                <Row k="License ID" v={<code>{lic?.license_id || ls?.license_id || "—"}</code>} />
              </div>
              <div>
                <Row k="API mTLS" v={<Badge tone={st?.api_mtls?.mtls_ready ? "ok" : "warn"}>{st?.api_mtls?.mtls_ready ? "ready" : "not ready"}</Badge>} />
                <Row k="NATS mTLS" v={<Badge tone={st?.nats_mtls?.connected ? "ok" : "err"}>{st?.nats_mtls?.connected ? "connected" : "down"}</Badge>} />
                <Row k="Assignment version" v={asg?.version ?? "—"} />
                <Row k="Tenant / Site id" v={<code className="text-xs">{(st?.tenant_id || "—") + " / " + (st?.site_id || "—")}</code>} />
              </div>
            </CardBody>
          </Card>

          {ls?.features && (
            <Card>
              <CardHeader><CardTitle>Entitlements</CardTitle></CardHeader>
              <CardBody className="p-0">
                <Table>
                  <THead><TR><TH>Feature</TH><TH>Enabled</TH></TR></THead>
                  <tbody>
                    {(Object.keys(FEATURE_LABELS) as (keyof LicenseFeatures)[]).map((k) => (
                      <TR key={k}>
                        <TD>{FEATURE_LABELS[k]}</TD>
                        <TD>{ls.features?.[k] ? <Badge tone="ok">yes</Badge> : <span className="text-muted">—</span>}</TD>
                      </TR>
                    ))}
                  </tbody>
                </Table>
              </CardBody>
            </Card>
          )}
        </div>
      )}
    </div>
  );
}
