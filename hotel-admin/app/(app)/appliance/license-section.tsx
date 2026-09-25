"use client";

// LICENCE — what this appliance is allowed to do, and the two values Semantics needs to issue a licence for it.
//
// The one licence rule an operator must never be misled about is at the top, in words: when a licence stops
// being in good standing NEW guest sign-ins are refused and existing guest sessions are NOT dropped. Central
// serves this appliance for licensing only; a switched-off reporting link is a decision and is never shown as a
// broken connection.

import { useEffect, useRef, useState } from "react";
import { useRouter } from "next/navigation";
import { api, ApiError, SetupStatus, LicenseStatus, LicenseFeatures } from "@/lib/api";
import { Card, CardBody, CardHeader, CardTitle, CardDescription } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Meter, Skeleton } from "@/components/ui/misc";
import { KeyValueGrid } from "@/components/ui/data";
import { CopyButton, LiveStatus, ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { errMsg, formatDate } from "@/lib/utils";
import { canWrite } from "@/lib/roles";
import { Cpu, Upload, ShieldCheck, Cloud, Wrench } from "lucide-react";

type Tone = "ok" | "warn" | "err" | "default";

function fp(s?: string): string {
  if (!s) return "—";
  return s.length > 20 ? `${s.slice(0, 20)}…` : s;
}

function activationTone(a?: string): Tone {
  switch (a) {
    case "activated": case "licensed": return "ok";
    case "pending_activation": case "mismatch": return "warn";
    case "unlicensed": return "err";
    default: return "default";
  }
}
function activationLabel(a?: string): string {
  switch (a) {
    case "activated": return "Active";
    case "licensed": return "Licensed";
    case "pending_activation": return "Pending activation";
    case "mismatch": return "Hardware mismatch";
    case "unlicensed": return "Not activated";
    default: return a || "Unknown";
  }
}
function licenseTone(state?: string): Tone {
  switch (state) {
    case "Active": return "ok";
    case "GracePeriod": case "Restricted": case "Suspended": return "warn";
    case "Expired": case "Revoked": case "Unlicensed": return "err";
    default: return "default";
  }
}
function licenseWord(state?: string): string {
  if (!state) return "—";
  return state === "GracePeriod" ? "Grace period" : state;
}

/** A large, copyable identifier — the two values an operator reads out to Semantics. */
function Identifier({ label, value, big }: { label: string; value?: string; big?: boolean }) {
  return (
    <div className="min-w-0 space-y-1.5">
      <div className="text-label text-muted-foreground">{label}</div>
      <div className="flex flex-wrap items-center gap-2">
        <code
          className={
            (big ? "text-lg font-semibold tracking-wide sm:text-xl " : "text-sm ") +
            "min-w-0 break-all rounded-md border border-border bg-surface px-3 py-1.5 font-mono"
          }
        >
          {value || "—"}
        </code>
        {value && <CopyButton value={value} label={`Copy`} size="sm" />}
      </div>
    </div>
  );
}

const FEATURE_LABELS: Record<keyof LicenseFeatures, string> = {
  pms: "PMS integration", paid_wifi: "Paid Wi-Fi", sms_otp: "SMS one-time code", email_otp: "Email one-time code",
  social_login: "Social login", ha: "High availability", white_label: "White label",
};

const POLL_SECONDS = 5;

export function LicenseSection() {
  const router = useRouter();
  const toast = useToast();
  const [roles, setRoles] = useState<string[] | null>(null);
  const [st, setSt] = useState<SetupStatus | null>(null);
  const [ls, setLs] = useState<LicenseStatus | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [uploadMsg, setUploadMsg] = useState<string | null>(null);
  const [uploadErr, setUploadErr] = useState<string | null>(null);
  const [uploading, setUploading] = useState(false);
  const fileRef = useRef<HTMLInputElement>(null);
  const authGone = useRef(false);
  const writable = roles ? canWrite("license", roles) || canWrite("network", roles) : false;

  async function load() {
    try {
      const [s, l] = await Promise.all([
        api.get<SetupStatus>("/setup/status"),
        // edged serves the license at "/license" -- there is no "/license/status". The wrong path 404ed on
        // every load, and because the call is wrapped in .catch(() => null) it failed SILENTLY: the screen
        // rendered as though the appliance had no license at all, which is the one thing a licensing page
        // must never say incorrectly.
        api.get<LicenseStatus>("/license").catch(() => null),
      ]);
      setSt(s); if (l) setLs(l); setErr(null);
      setUpdatedAt(Date.now());
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        if (!authGone.current) { authGone.current = true; try { await api.post("/auth/logout"); } catch {} router.replace("/login"); }
        return;
      }
      setErr(errMsg(e));
    } finally { setLoaded(true); }
  }

  useEffect(() => {
    api.get<{ roles?: string[] }>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => setRoles([]));
    load();
    const t = setInterval(load, POLL_SECONDS * 1000);
    return () => clearInterval(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function onUpload(e: React.ChangeEvent<HTMLInputElement>) {
    const file = e.target.files?.[0];
    if (!file) return;
    setUploading(true); setUploadErr(null); setUploadMsg(null);
    try {
      const text = await file.text();
      // Install is POST "/license", not "/license/install". Uploading a real licence file answered 404,
      // so the operator saw an upload failure with no reason that pointed anywhere useful.
      await api.postRaw("/license", text.trim());
      setUploadMsg("Licence file accepted and installed.");
      toast.success("Licence installed");
      await load();
    } catch (err) {
      setUploadErr(errMsg(err));
    } finally {
      setUploading(false);
      if (fileRef.current) fileRef.current.value = "";
    }
  }

  if (!loaded) {
    return (
      <div className="space-y-5" aria-busy="true">
        <span className="sr-only">Loading the licence</span>
        <Skeleton className="h-36 w-full rounded-lg" />
        <Skeleton className="h-48 w-full rounded-lg" />
        <Skeleton className="h-28 w-full rounded-lg" />
      </div>
    );
  }

  const hw = st?.hardware;
  const activation = st?.activation_status;
  const lic = st?.license;
  const asg = st?.assignment;
  const activated = activation === "activated" || activation === "licensed";
  const state = ls?.state ?? lic?.state;

  const max = lic?.max_concurrent_online_guests;
  const limited = max != null && max > 0;
  const current = lic?.current_online_guests;
  const pct = limited
    ? (lic?.usage_percent ?? (current != null ? (current / max!) * 100 : 0))
    : 0;
  // The capacity bar's colour is the licence's own rule: green under 80%, amber from 80%, red when full.
  const meterTone = pct >= 100 ? "err" : pct >= 80 ? "warn" : "ok";
  const capacityReached = limited && current != null && current >= max!;

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="flex flex-wrap items-center gap-2 text-sm">
          <span className="text-muted-foreground">Activation</span>
          <Badge tone={activationTone(activation)} dot>{activationLabel(activation)}</Badge>
        </div>
        <LiveStatus updatedAt={updatedAt} intervalSeconds={POLL_SECONDS} error={!!err && st !== null} onRefresh={() => void load()} />
      </div>

      <ErrorBanner err={err ? `Couldn't read the licence status (retrying): ${err}` : null} className="mb-0" />

      {/* ---- STATE BANNERS: what the licence currently stops, in words ---- */}
      {st?.permissive_blocked && (
        <Callout tone="danger" title="A blocked attempt to switch off licence enforcement">
          This production appliance refused to run in an unlicensed mode ({st.permissive_blocked}). Guest
          internet still requires a real signed licence. Remove the misconfiguration and investigate — the attempt
          was recorded in Activity and reported to OneGate Central.
        </Callout>
      )}

      {lic?.state === "GracePeriod" && (
        <Callout tone="warning" title="Licence in its grace period">
          It expired {lic?.valid_until ? formatDate(lic.valid_until) : ""} and guests keep signing in until{" "}
          <strong>{lic?.grace_ends_at ? formatDate(lic.grace_ends_at) : "the grace period ends"}</strong>. Renew
          now to avoid interruption.
        </Callout>
      )}
      {(lic?.state === "Expired" || lic?.state === "Revoked" || lic?.state === "Suspended") && (
        <Callout tone="danger" title={`Licence ${lic.state.toLowerCase()}`}>
          New guest logins are refused; existing guest sessions are not dropped. DHCP, DNS, the sign-in page and
          this admin stay available.
          {lic?.valid_until ? <> Expired {formatDate(lic.valid_until)}{lic?.grace_ends_at ? <>; grace ended {formatDate(lic.grace_ends_at)}</> : null}.</> : null}
        </Callout>
      )}
      {capacityReached && (
        <Callout tone="warning" title="Licensed capacity reached">
          {current} of {max} concurrent online guests. New guest logins are refused until someone goes offline;
          guests already online are not affected.
        </Callout>
      )}
      {activation === "mismatch" && (
        <Callout tone="warning" title="Hardware mismatch">
          This licence is bound to a different WAN network adapter than the one now present
          {st?.hardware_mismatch ? <> ({st.hardware_mismatch})</> : null}. The hotel keeps running on a
          time-limited grace. If the WAN adapter was genuinely replaced, ask Semantics to authorise a{" "}
          <strong>rebind</strong> — a new licence will be issued.
        </Callout>
      )}

      {/* ---- IDENTITY: the two values the operator sends to Semantics ---- */}
      <Card>
        <CardHeader>
          <div className="space-y-0.5">
            <CardTitle className="flex items-center gap-2"><Cpu className="size-4" aria-hidden /> Appliance identity</CardTitle>
            <CardDescription>
              {activated
                ? "The licence is bound to these values."
                : "To activate this appliance, send these two values to Semantics."}
            </CardDescription>
          </div>
        </CardHeader>
        <CardBody className="space-y-5">
          <div className="grid gap-5 md:grid-cols-2">
            <Identifier label="Serial number" value={hw?.serial || st?.serial} big />
            <Identifier label="WAN MAC address" value={hw?.wan_mac} big />
          </div>
          <KeyValueGrid
            columns={2}
            items={[
              { label: "LAN MAC address", value: hw?.lan_mac ? <span className="font-mono">{hw.lan_mac}</span> : "—" },
              {
                label: "Appliance",
                value: hw?.model || "—",
                hint: `Host ${hw?.hostname || "—"} · WAN ${hw?.wan_interface || "—"} · LAN ${hw?.lan_interface || "—"}`,
              },
            ]}
          />
        </CardBody>
      </Card>

      {/* ---- LICENCE: one appliance, one cap, one window ---- */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2"><ShieldCheck className="size-4" aria-hidden /> Licence</CardTitle>
          <Badge tone={licenseTone(state)} dot>{licenseWord(state)}</Badge>
        </CardHeader>
        <CardBody className="space-y-5">
          <div className="space-y-2">
            <div className="flex flex-wrap items-baseline justify-between gap-2">
              <span className="text-sm text-muted-foreground">Online guests, all guest networks</span>
              <span className="text-metric tabular">
                {current ?? "—"}
                <span className="text-sm font-normal text-muted-foreground"> / {limited ? max : "Unlimited"}</span>
              </span>
            </div>
            {limited && (
              <Meter
                value={current ?? 0}
                max={max!}
                tone={meterTone}
                caption={
                  <>
                    {Math.round(pct)}% in use
                    {lic?.remaining_capacity != null ? ` · ${lic.remaining_capacity} free` : ""}
                    {meterTone === "err" ? " · full" : meterTone === "warn" ? " · nearly full" : ""}
                  </>
                }
              />
            )}
          </div>
          <KeyValueGrid
            columns={2}
            items={[
              { label: "Licence state", value: <Badge tone={licenseTone(state)}>{licenseWord(state)}</Badge> },
              { label: "Max concurrent online guests", value: limited ? String(max) : "Unlimited" },
              { label: "Valid from", value: lic?.valid_from ? formatDate(lic.valid_from) : (ls?.issued_at ? formatDate(ls.issued_at) : "—") },
              { label: "Valid until", value: lic?.valid_until ? formatDate(lic.valid_until) : (ls?.valid_until ? formatDate(ls.valid_until) : "—") },
              { label: "Grace period", value: lic?.grace_period_days != null ? `${lic.grace_period_days} days` : "—" },
              { label: "Grace ends", value: lic?.grace_ends_at ? formatDate(lic.grace_ends_at) : "—" },
              { label: "Customer", value: asg?.tenant_name || "—" },
              { label: "Site", value: asg?.site_name || "—" },
            ]}
          />
        </CardBody>
      </Card>

      {/* ---- UPLOAD: the one place a licence file is installed after activation ---- */}
      <Card>
        <CardHeader>
          <div className="space-y-0.5">
            <CardTitle className="flex items-center gap-2"><Upload className="size-4" aria-hidden /> Upload licence file</CardTitle>
            <CardDescription>
              For renewals, or when this appliance has no connection to OneGate Central.
            </CardDescription>
          </div>
        </CardHeader>
        <CardBody className="space-y-3">
          {roles !== null && !writable ? (
            <ReadOnlyNotice>Your role can view the licence but not install one.</ReadOnlyNotice>
          ) : writable ? (
            <>
              <input
                ref={fileRef}
                type="file"
                accept=".license,.json,application/json"
                onChange={onUpload}
                disabled={uploading}
                className="sr-only"
                id="lic-file"
                aria-label="Licence file"
                tabIndex={-1}
              />
              <Button variant="secondary" disabled={uploading} onClick={() => fileRef.current?.click()}>
                <Upload /> {uploading ? "Installing…" : "Upload licence file"}
              </Button>
            </>
          ) : null}
          {uploadMsg && <Callout tone="success">{uploadMsg}</Callout>}
          <ErrorBanner err={uploadErr} className="mb-0" />
        </CardBody>
      </Card>

      {/* CONNECTION TO CENTRAL — what the separate "Cloud connection" page used to show.
          It is on THIS page because licensing is the only thing the link serves. The appliance talks to
          Central for registration, certificates, the licence itself, licence enforcement and the signed
          tenant/site binding; the NATS transport is not opened and the telemetry outbox is stopped, both by
          decision (T0071). Nothing here is shown as broken because reporting is off. */}
      <Card>
        <CardHeader>
          <div className="space-y-0.5">
            <CardTitle className="flex items-center gap-2"><Cloud className="size-4" aria-hidden /> Connection to Central</CardTitle>
            <CardDescription>
              OneGate Central issues and renews this appliance&apos;s licence and certificate.
            </CardDescription>
          </div>
        </CardHeader>
        <CardBody>
          <KeyValueGrid
            columns={2}
            items={[
              { label: "Used for", value: <span className="text-emphasis">Licensing only</span> },
              {
                label: "Reachable now",
                value: st?.network?.central_https_443 === false
                  ? <Badge tone="warn">Not reachable</Badge>
                  : <Badge tone="ok">Yes</Badge>,
              },
              {
                label: "Secure channel",
                value: <Badge tone={st?.api_mtls?.mtls_ready ? "ok" : "warn"}>{st?.api_mtls?.mtls_ready ? "Established" : "Not ready"}</Badge>,
              },
              { label: "Certificate expires", value: st?.api_mtls?.not_after || "—" },
              {
                label: "Enrolment",
                value: <Badge tone={st?.enrolled ? "ok" : "err"}>{st?.enrolled ? "Enrolled" : "Not enrolled"}</Badge>,
              },
              {
                label: "Site binding",
                value: <Badge tone={asg?.assigned ? "ok" : "warn"}>{asg?.assigned ? "Signed and adopted" : "Not assigned"}</Badge>,
              },
            ]}
          />
        </CardBody>
      </Card>

      {/* ---- TECHNICAL DETAILS: for support, collapsed ---- */}
      <Card>
        <details className="group">
          <summary className="flex cursor-pointer select-none items-center gap-2 rounded-lg px-5 py-4 text-emphasis focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring">
            <Wrench className="size-4 text-muted-foreground" aria-hidden />
            Technical details
            <span className="ms-auto text-caption font-normal text-muted-foreground group-open:hidden">Show</span>
            <span className="ms-auto hidden text-caption font-normal text-muted-foreground group-open:inline">Hide</span>
          </summary>
          <div className="space-y-5 border-t border-border px-5 py-4">
            <KeyValueGrid
              columns={2}
              items={[
                { label: "Appliance ID", value: <code className="break-all text-xs">{st?.appliance_id || "—"}</code> },
                { label: "Licence ID", value: <code className="break-all text-xs">{lic?.license_id || ls?.license_id || "—"}</code> },
                { label: "Identity key fingerprint", value: <code className="text-xs" title={st?.identity_key_fingerprint}>{fp(st?.identity_key_fingerprint)}</code> },
                { label: "Certificate fingerprint", value: <code className="text-xs" title={st?.api_mtls?.cert_fingerprint}>{fp(st?.api_mtls?.cert_fingerprint)}</code> },
                { label: "API mTLS", value: <Badge tone={st?.api_mtls?.mtls_ready ? "ok" : "warn"}>{st?.api_mtls?.mtls_ready ? "Ready" : "Not ready"}</Badge> },
                // NOT "down". The real-time channel is deliberately closed at a licensing-only site; a red badge
                // here described a decision as a fault.
                { label: "Real-time channel", value: <Badge tone={st?.nats_mtls?.connected ? "ok" : "default"}>{st?.nats_mtls?.connected ? "Connected" : "Not used at this site"}</Badge> },
                { label: "Assignment version", value: asg?.version ?? "—" },
                { label: "Customer / site id", value: <code className="break-all text-xs">{(st?.tenant_id || "—") + " / " + (st?.site_id || "—")}</code> },
              ]}
            />

            {ls?.features && (
              <div className="space-y-2">
                <div className="text-label">Feature entitlements</div>
                <p className="text-caption text-muted-foreground">
                  Shown for support. A standard OneGate licence includes every product feature.
                </p>
                <div className="overflow-hidden rounded-md border border-border">
                  <Table>
                    <THead><TR><TH>Feature</TH><TH>Included</TH></TR></THead>
                    <TBody>
                      {(Object.keys(FEATURE_LABELS) as (keyof LicenseFeatures)[]).map((k) => (
                        <TR key={k}>
                          <TD>{FEATURE_LABELS[k]}</TD>
                          <TD>{ls.features?.[k] ? <Badge tone="ok">Included</Badge> : <span className="text-muted-foreground">Not included</span>}</TD>
                        </TR>
                      ))}
                    </TBody>
                  </Table>
                </div>
              </div>
            )}
          </div>
        </details>
      </Card>
    </div>
  );
}
