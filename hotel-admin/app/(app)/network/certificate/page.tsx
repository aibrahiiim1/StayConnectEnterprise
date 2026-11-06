"use client";

import { expiryState } from "@/lib/cert-expiry";
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
import { LiveStatus, ReadOnlyNotice } from "@/components/ui/patterns";
import { errMsg, formatDate } from "@/lib/utils";
import { useNetworkAccess } from "@/components/network/shared";
import { HelpList, HelpSection } from "@/components/help";
import { AlertTriangle, RefreshCw, RotateCw, CheckCircle2, ShieldCheck } from "lucide-react";

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
const words = (t?: string) => {
  if (!t) return "—";
  const w = t.replace(/[_-]+/g, " ").trim();
  return w.charAt(0).toUpperCase() + w.slice(1);
};
const plural = (n: number) => `${n} ${n === 1 ? "day" : "days"}`;

function daysWords(s: NonNullable<ReturnType<typeof expiryState>>): string {
  if (s.kind === "expired") return s.days === 0 ? "Expired less than a day ago" : `Expired ${plural(s.days)} ago`;
  if (s.kind === "within_day") return "Expires within a day";
  return `${plural(s.days)} left`;
}
function expiresIn(s: ReturnType<typeof expiryState>, at?: string): string | undefined {
  if (!s) return at ? `Expires ${formatDate(at)}` : undefined;
  if (s.kind !== "days") return daysWords(s);
  return `Expires in ${plural(s.days)}. Renews automatically when 45 days are left.`;
}
// The error is dated only when the recorded result says a renewal produced it: a failed check keeps the last renewal's time, not its own.
function errorTitle(s: { last_renewal_result?: string; last_renewal_attempt?: string }): string {
  if (s.last_renewal_result === "renewal_failed") return s.last_renewal_attempt ? `Last renewal error · ${when(s.last_renewal_attempt)}` : "Last renewal error";
  if (s.last_renewal_result === "check_failed") return "Last certificate check failed";
  return "Last error";
}
const mono = (s?: string) => (s ? <span className="break-all font-mono text-xs">{s}</span> : "—");

export default function CertificatePage() {
  const { known, writable } = useNetworkAccess();
  const [st, setSt] = useState<CertStatus | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [result, setResult] = useState<{ tone: "success" | "danger"; text: string } | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [rotating, setRotating] = useState(false);
  const [rotateErr, setRotateErr] = useState<string | null>(null);
  const [loadErr, setLoadErr] = useState<string | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);

  async function load(): Promise<CertStatus | null> {
    setRefreshing(true);
    try {
      const s = await api.get<CertStatus>("/hotel-admin-cert");
      setSt(s);
      setLoadErr(null);
      setUpdatedAt(Date.now());
      return s;
    } catch (e) { setLoadErr(errMsg(e)); return null; }
    finally { setRefreshing(false); }
  }
  useEffect(() => { load(); }, []);

  const thr = THRESH[st?.status_threshold ?? ""] ?? {
    tone: "default" as const,
    label: st?.status_threshold ? `Other (${words(st.status_threshold)})` : "Not reported",
  };
  const days = typeof st?.days_remaining === "number" ? st.days_remaining : null;
  const expiry = expiryState(st?.expires_at, days, Date.now());

  async function check() {
    setBusy("check"); setErr(null); setResult(null);
    try {
      const r = await api.post<{ ok: boolean; exit: number; output_tail?: string }>("/hotel-admin-cert/check", {});
      const detail = certCheckDetail(r.output_tail);
      if (r.ok) {
        setResult({ tone: "success", text: detail ? `Certificate validated. ${detail}` : "Certificate validated — no problems found." });
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
      const after = await load();
      if (r.ok) {
        setResult({
          tone: "success",
          text:
            "Certificate rotated. This page is still using the previous certificate until you reload it, and your browser may ask you to trust the new one." +
            (after?.fingerprint_sha256 ? ` New SHA-256 fingerprint: ${after.fingerprint_sha256}.` : ""),
        });
      } else {
        setResult({
          tone: "danger",
          text: after?.last_error
            ? `Rotation failed; the previous certificate is still serving. Reason recorded: ${after.last_error}`
            : `Rotation failed (code ${r.exit}); the previous certificate is still serving. No reason was recorded.`,
        });
      }
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
        meta={st && <LiveStatus updatedAt={updatedAt} refreshing={refreshing} onRefresh={() => void load()} error={!!loadErr} />}
        actions={writable ? (
          <>
            <Button variant="secondary" disabled={busy !== null} onClick={() => { setRotateErr(null); setRotating(true); }}>
              <RotateCw /> Rotate…
            </Button>
            <Button disabled={busy !== null} onClick={check}>
              <CheckCircle2 /> {busy === "check" ? "Checking…" : "Check certificate"}
            </Button>
          </>
        ) : undefined}
      />

      {known && !writable && (
        <ReadOnlyNotice>Your role can view the certificate but not check or rotate it.</ReadOnlyNotice>
      )}

      <ErrorBanner err={err} className="mb-0" />
      {result && <Callout tone={result.tone}>{result.text}</Callout>}

      <SectionCard
        title="Status"
        description={st && st.available !== false ? expiresIn(expiry, st.expires_at) : undefined}
        actions={st && st.available !== false && (
          <StatusBadge tone={thr.tone}>
            {thr.label}{expiry ? ` · ${daysWords(expiry)}` : ""}
          </StatusBadge>
        )}
        bodyClassName="space-y-4"
      >
        {!st ? (
          loadErr ? (
            <EmptyState
              size="sm"
              icon={<AlertTriangle />}
              title="The certificate status could not be loaded"
              hint={loadErr}
              action={<Button size="sm" variant="secondary" onClick={() => void load()}><RefreshCw /> Try again</Button>}
            />
          ) : (
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
            {st.last_error && (
              <Callout tone="danger" title={errorTitle(st)}>
                {st.last_error}
              </Callout>
            )}
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
                  value: st.san_config_match === true ? <StatusBadge tone="ok">Yes</StatusBadge>
                    : st.san_config_match === false ? <StatusBadge tone="err">No</StatusBadge>
                    : <StatusBadge tone="neutral">Not reported</StatusBadge>,
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
        <SectionCard title="Renewal" description="Renewed automatically.">
          <DescriptionList
            layout="stacked"
            columns={3}
            items={[
              { label: "Last successful renewal", value: when(st.last_successful_renewal) },
              { label: "Last attempt", value: when(st.last_renewal_attempt) },
              { label: "Last result", value: words(st.last_renewal_result) },
            ]}
          />
        </SectionCard>
      )}

      <ConfirmDialog
        open={rotating}
        onOpenChange={(v) => { if (!v) setRotating(false); }}
        title="Rotate the TLS certificate?"
        description="A new certificate is issued for Admin Console straight away."
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
        requirePassword strictPassword
        onConfirm={rotate}
      />
    </PageShell>
  );
}

