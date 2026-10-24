"use client";

import { certCheckDetail } from "@/lib/cert-check";
import { useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { SectionCard } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { StatusBadge } from "@/components/ui/badge";
import { ConfirmDialog } from "@/components/ui/dialog";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { PageShell, PageHeader } from "@/components/ui/page";
import { DescriptionList } from "@/components/ui/data";
import { Skeleton } from "@/components/ui/misc";
import { EmptyState } from "@/components/ui/empty-state";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { errMsg, formatDate } from "@/lib/utils";
import { useNetworkAccess } from "@/components/network/shared";
import { HelpList, HelpSection } from "@/components/help";
import { RefreshCw, RotateCw, CheckCircle2, ShieldCheck } from "lucide-react";

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
  critical:    { tone: "err",  label: "Critical" },
  emergency:   { tone: "err",  label: "Emergency" },
  expired:     { tone: "err",  label: "Expired" },
};

const when = (s?: string) => (s ? formatDate(s) : "—");
const mono = (s?: string) => (s ? <span className="break-all font-mono text-xs">{s}</span> : "—");

export default function CertificatePage() {
  const { known, writable } = useNetworkAccess();
  const [st, setSt] = useState<CertStatus | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [result, setResult] = useState<{ tone: "success" | "danger"; text: string } | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [rotating, setRotating] = useState(false);
  const [rotateErr, setRotateErr] = useState<string | null>(null);
  const toast = useToast();

  async function load() {
    try {
      setSt(await api.get<CertStatus>("/hotel-admin-cert"));
    } catch (e) { setErr(errMsg(e)); }
  }
  useEffect(() => { load(); }, []);

  const thr = THRESH[st?.status_threshold ?? ""] ?? { tone: "default" as const, label: st?.status_threshold ?? "Unknown" };
  const days = typeof st?.days_remaining === "number" ? st.days_remaining : null;

  async function check() {
    setBusy("check"); setErr(null); setResult(null);
    try {
      const r = await api.post<{ ok: boolean; exit: number; output_tail?: string }>("/hotel-admin-cert/check", {});
      const detail = certCheckDetail(r.output_tail);
      if (r.ok) {
        setResult({ tone: "success", text: detail ? `Certificate validated. ${detail}` : "Certificate validated — no problems found." });
        toast.success("Certificate checked", detail || "No problems found.");
      } else {
        // Show the validator's reason rather than only its exit code.
        setResult({ tone: "danger", text: detail ? `Validation found a problem: ${detail}.` : `Validation found a problem (code ${r.exit}).` });
      }
      await load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(null); }
  }

  async function rotate({ reason, password }: { reason: string; password: string }) {
    setBusy("rotate"); setRotateErr(null); setResult(null);
    try {
      // The server requires the exact typed confirmation ROTATE.
      const r = await api.post<{ ok: boolean; exit: number }>("/hotel-admin-cert/rotate", {
        reason, password, confirmation: "ROTATE",
      });
      setRotating(false);
      if (r.ok) {
        setResult({ tone: "success", text: "Certificate rotated successfully." });
        toast.success("Certificate rotated");
      } else {
        setResult({ tone: "danger", text: `Rotation failed (exit ${r.exit}); the previous certificate is still serving.` });
      }
      await load();
    } catch (e) {
      if (e instanceof ApiError && e.body?.error === "reauth_required") setRotateErr("Password confirmation failed.");
      else setRotateErr(errMsg(e));
    } finally { setBusy(null); }
  }

  return (
    <PageShell>
      <PageHeader
        title="TLS certificate"
        description="The HTTPS certificate Admin Console itself is served with."
        help={
          <>
            <HelpSection title="What this certificate is">
              <p>
                The certificate your browser sees when it opens Admin Console. It covers the appliance&rsquo;s host name and
                its management IP address.
              </p>
            </HelpSection>
            <HelpSection title="Automatic renewal">
              <HelpList
                items={[
                  "Checked daily.",
                  "Renewed when 45 days are left.",
                  "Renewed when the management IP changes, or when the covered names drift from the configuration.",
                ]}
              />
            </HelpSection>
            <HelpSection title="Check and rotate">
              <p>
                <strong>Check certificate</strong> reads and validates the certificate now; it changes nothing.{" "}
                <strong>Rotate</strong> issues a new certificate straight away through the same safe lifecycle —
                validate, swap, reload, health check — and puts the previous one back if the health check fails. You
                cannot upload your own key.
              </p>
            </HelpSection>
          </>
        }
        actions={writable ? (
          <>
            <Button variant="ghost" size="icon" aria-label="Refresh" onClick={() => { setErr(null); load(); }}><RefreshCw /></Button>
            <Button variant="secondary" disabled={busy !== null} onClick={() => { setRotateErr(null); setRotating(true); }}>
              <RotateCw /> Rotate…
            </Button>
            <Button disabled={busy !== null} onClick={check}>
              <CheckCircle2 /> {busy === "check" ? "Checking…" : "Check certificate"}
            </Button>
          </>
        ) : (
          <Button variant="secondary" onClick={() => { setErr(null); load(); }}><RefreshCw /> Refresh</Button>
        )}
      />

      {known && !writable && (
        <ReadOnlyNotice>Your role can view the certificate but not check or rotate it.</ReadOnlyNotice>
      )}

      <ErrorBanner err={err} className="mb-0" />
      {result && <Callout tone={result.tone}>{result.text}</Callout>}

      <SectionCard
        title="Status"
        description={st && st.available !== false && st.expires_at ? `Expires ${when(st.expires_at)}` : undefined}
        actions={st && st.available !== false && (
          <StatusBadge tone={thr.tone}>
            {thr.label}{days !== null ? ` · ${days} ${days === 1 ? "day" : "days"} left` : ""}
          </StatusBadge>
        )}
        bodyClassName="space-y-4"
      >
        {!st ? (
          err ? null : (
            <div className="grid gap-4 sm:grid-cols-2" aria-busy="true">
              <span className="sr-only">Loading</span>
              {Array.from({ length: 6 }).map((_, i) => <Skeleton key={i} className="h-10 w-full" />)}
            </div>
          )
        ) : st.available === false ? (
          <EmptyState
            size="sm"
            icon={<ShieldCheck />}
            title="No certificate status yet"
            hint={writable ? "Run “Check certificate” to read the certificate now." : "The appliance has not reported the certificate yet."}
            action={writable ? <Button size="sm" variant="secondary" disabled={busy !== null} onClick={check}>Check certificate</Button> : undefined}
          />
        ) : (
          <>
            {st.last_error && <Callout tone="danger" title="Last renewal error">{st.last_error}</Callout>}
            {st.san_config_match === false && (
              <Callout tone="warning" title="The certificate does not match the current configuration">
                Its names or addresses differ from what the appliance is using now. It is renewed automatically; you can
                also rotate it.
              </Callout>
            )}
            <DescriptionList
              columns={2}
              items={[
                { label: "Subject", value: mono(st.subject) },
                { label: "Serial", value: mono(st.serial) },
                { label: "Host names covered", value: (st.dns_sans ?? []).length ? mono((st.dns_sans ?? []).join(", ")) : "—" },
                { label: "IP addresses covered", value: (st.ip_sans ?? []).length ? mono((st.ip_sans ?? []).join(", ")) : "—" },
                { label: "Current management IP", value: mono(st.current_management_ip) },
                {
                  label: "Matches configuration",
                  value: st.san_config_match ? <StatusBadge tone="ok">Yes</StatusBadge> : <StatusBadge tone="err">No</StatusBadge>,
                },
                { label: "Issued", value: when(st.issued_at) },
                { label: "Expires", value: when(st.expires_at) },
                { label: "SHA-256 fingerprint", value: mono(st.fingerprint_sha256), wide: true },
              ]}
            />
          </>
        )}
      </SectionCard>

      {st && st.available !== false && (
        <SectionCard title="Renewal" description="Checked daily and renewed automatically.">
          <DescriptionList
            layout="stacked"
            columns={3}
            items={[
              { label: "Last successful renewal", value: when(st.last_successful_renewal) },
              { label: "Last attempt", value: when(st.last_renewal_attempt) },
              { label: "Last result", value: st.last_renewal_result || "—" },
            ]}
          />
        </SectionCard>
      )}

      <ConfirmDialog
        open={rotating}
        onOpenChange={(v) => { if (!v) setRotating(false); }}
        title="Rotate the TLS certificate?"
        description="A new certificate is issued for Admin Console. You cannot upload a key."
        consequences={[
          "The new certificate goes through the safe lifecycle: validate, swap, reload, health check.",
          "If the health check fails, the previous certificate is put back automatically.",
        ]}
        confirmLabel="Rotate now"
        confirmVariant="danger"
        busy={busy === "rotate"}
        error={rotateErr}
        requireReason
        reasonPlaceholder="Why are you rotating?"
        confirmText="ROTATE"
        requirePassword
        onConfirm={rotate}
      />
    </PageShell>
  );
}

