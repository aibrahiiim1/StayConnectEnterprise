"use client";

import { useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ConfirmDialog } from "@/components/ui/dialog";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { PageShell, PageHeader } from "@/components/ui/page";
import { KeyValueGrid } from "@/components/ui/data";
import { Skeleton } from "@/components/ui/misc";
import { EmptyState } from "@/components/ui/empty-state";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { errMsg, formatDate } from "@/lib/utils";
import { useNetworkAccess } from "@/components/network/shared";
import { Lock, RefreshCw, RotateCw, CheckCircle2, ShieldCheck } from "lucide-react";

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
      const r = await api.post<{ ok: boolean; exit: number }>("/hotel-admin-cert/check", {});
      if (r.ok) {
        setResult({ tone: "success", text: "Certificate validated — no problems found." });
        toast.success("Certificate checked", "No problems found.");
      } else {
        setResult({ tone: "danger", text: `Validation reported a problem (exit ${r.exit}).` });
      }
      await load();
    } catch (e) { setErr(errMsg(e)); }
    finally { setBusy(null); }
  }

  async function rotate({ reason, password }: { reason: string; password: string }) {
    setBusy("rotate"); setRotateErr(null); setResult(null);
    try {
      // The server requires the typed word itself: `confirmation` must be exactly ROTATE. The dialog only
      // enables its button once that is what was typed.
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
    <PageShell width="narrow">
      <PageHeader
        icon={<Lock />}
        eyebrow="Networking"
        title="TLS certificate"
        description="The HTTPS certificate Hotel Admin itself is served with, for its host name and management IP. Renewal is automatic: checked daily, renewed at 45 days left, when the management IP changes, or when the covered names drift."
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

      <Card>
        <CardHeader>
          <div className="space-y-1">
            <CardTitle>Status</CardTitle>
            {st && st.available !== false && st.expires_at && (
              <CardDescription>Expires {when(st.expires_at)}</CardDescription>
            )}
          </div>
          {st && st.available !== false && (
            <Badge tone={thr.tone} dot>
              {thr.label}{days !== null ? ` · ${days} ${days === 1 ? "day" : "days"} left` : ""}
            </Badge>
          )}
        </CardHeader>
        <CardBody className="space-y-5">
          {!st ? (
            err ? null : (
              <div className="grid gap-4 sm:grid-cols-2" aria-busy="true">
                <span className="sr-only">Loading</span>
                {Array.from({ length: 6 }).map((_, i) => <Skeleton key={i} className="h-10 w-full" />)}
              </div>
            )
          ) : st.available === false ? (
            <EmptyState
              icon={<ShieldCheck />}
              title="No certificate status yet"
              hint={writable ? "Run “Check certificate” to read the certificate now." : "The appliance has not reported the certificate yet."}
              action={writable ? <Button size="sm" disabled={busy !== null} onClick={check}>Check certificate</Button> : undefined}
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
              <KeyValueGrid
                items={[
                  { label: "Subject", value: mono(st.subject) },
                  { label: "Serial", value: mono(st.serial) },
                  { label: "Host names covered", value: (st.dns_sans ?? []).length ? mono((st.dns_sans ?? []).join(", ")) : "—" },
                  { label: "IP addresses covered", value: (st.ip_sans ?? []).length ? mono((st.ip_sans ?? []).join(", ")) : "—" },
                  { label: "Current management IP", value: mono(st.current_management_ip) },
                  {
                    label: "Matches configuration",
                    value: st.san_config_match ? <Badge tone="ok">Yes</Badge> : <Badge tone="err">No</Badge>,
                  },
                  { label: "Issued", value: when(st.issued_at) },
                  { label: "Expires", value: when(st.expires_at) },
                  { label: "Days remaining", value: days !== null ? <span className="tabular">{days}</span> : "—" },
                  { label: "SHA-256 fingerprint", value: mono(st.fingerprint_sha256), wide: true },
                ]}
              />
            </>
          )}
        </CardBody>
      </Card>

      {st && st.available !== false && (
        <Card>
          <CardHeader><CardTitle>Renewal</CardTitle></CardHeader>
          <CardBody>
            <KeyValueGrid
              columns={3}
              items={[
                { label: "Last successful renewal", value: when(st.last_successful_renewal) },
                { label: "Last attempt", value: when(st.last_renewal_attempt) },
                { label: "Last result", value: st.last_renewal_result || "—" },
              ]}
            />
          </CardBody>
        </Card>
      )}

      <ConfirmDialog
        open={rotating}
        onOpenChange={(v) => { if (!v) setRotating(false); }}
        title="Rotate the TLS certificate?"
        description="A new certificate is issued for Hotel Admin. You cannot upload a key."
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
