"use client";

// CODE REQUESTS — staff help for a client who asked for a sign-in code (contract §6.7, 2026-10-12).
//
// The list: recent code requests with the device that asked, the code's state and the device's temporary
// internet window. Two actions, each behind the operator's password and a reason, each audited by edged:
//   * Let them try again (code-requests WRITE) — the device's ENDED windows stop counting towards the limits and
//     the wait. Nothing is deleted; no access is granted; the client still signs in themselves.
//   * Show code (code-reveal WRITE) — reads a LIVE code so the desk can tell it to the client. The code works only
//     on the device that asked, once, until it expires. It is shown once in this dialog and never stored here.

import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { formatBytes, formatDate } from "@/lib/utils";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/dialog";
import { ErrorBanner } from "@/components/ui/error-banner";
import { Field, Input } from "@/components/ui/input";
import { SkeletonRows } from "@/components/ui/misc";
import { Table, TBody, TD, TH, THead, TR, TableWrap } from "@/components/ui/table";
import { useToast } from "@/components/ui/toast";
import { KeyRound, RotateCcw, LifeBuoy } from "lucide-react";

export type CodeRequestWindow = {
  started_at: string; expires_at: string; ended_at?: string; end_reason?: string; used_bytes: number;
  data_cap_bytes: number; cleared_at?: string; cleared_by?: string; live: boolean;
};
export type CodeRequest = {
  challenge_id: string; channel: string; destination: string; issued_at: string; expires_at: string;
  consumed_at?: string; attempts: number; max_attempts: number; state: "PENDING" | "USED" | "EXPIRED" | "LOCKED";
  device_mac?: string; code_available: boolean; window?: CodeRequestWindow;
  device_windows_24h: number; retry_wait_seconds: number;
};
export type CodeRequestsResp = {
  requests: CodeRequest[];
  limits: { per_device_daily: number; per_destination_daily: number; retry_after_minutes: number; max_concurrent: number };
  code_copy_available: boolean;
};

const STATE: Record<CodeRequest["state"], { label: string; tone: "ok" | "warn" | "default" | "err" }> = {
  PENDING: { label: "Waiting for the code", tone: "warn" },
  USED: { label: "Signed in", tone: "ok" },
  EXPIRED: { label: "Expired", tone: "default" },
  LOCKED: { label: "Too many wrong tries", tone: "err" },
};
const END: Record<string, string> = {
  VERIFIED: "signed in", SIGNED_IN: "signed in", EXPIRED: "time ran out", DATA_EXHAUSTED: "data used up",
  CANCELLED: "changed address", ADMIN: "ended by staff", RESTORE_FAILED: "could not be restored",
};

export function windowSummary(r: CodeRequest, limits: CodeRequestsResp["limits"]): string {
  const w = r.window;
  const parts: string[] = [];
  if (!w) parts.push("No window");
  else if (w.live) parts.push(`Online now · ${formatBytes(w.used_bytes)} of ${formatBytes(w.data_cap_bytes)}`);
  else parts.push(`Window ended (${END[w.end_reason ?? ""] ?? "ended"}) · ${formatBytes(w.used_bytes)} used`);
  if (r.device_mac) {
    if (r.retry_wait_seconds > 0) parts.push(`next window in ${Math.ceil(r.retry_wait_seconds / 60)} min`);
    if (r.device_windows_24h >= limits.per_device_daily) parts.push(`daily limit reached (${r.device_windows_24h}/${limits.per_device_daily})`);
  }
  if (w?.cleared_at) parts.push(`cleared by ${w.cleared_by}`);
  return parts.join(" · ");
}

export function CodeRequestsCard({ roles }: { roles: string[] }) {
  const mayClear = canWrite("code-requests", roles);
  const mayReveal = canWrite("code-reveal", roles);
  const [data, setData] = useState<CodeRequestsResp | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [clearing, setClearing] = useState<CodeRequest | null>(null);
  const [revealing, setRevealing] = useState<CodeRequest | null>(null);
  const load = useCallback(async () => {
    try {
      const r = await api.get<Partial<CodeRequestsResp>>("/code-requests?hours=24");
      setData({
        requests: r?.requests ?? [],
        limits: r?.limits ?? { per_device_daily: 3, per_destination_daily: 3, retry_after_minutes: 30, max_concurrent: 50 },
        code_copy_available: r?.code_copy_available ?? false,
      });
      setError(null);
    } catch (e) { setError(e); }
  }, []);
  useEffect(() => { void load(); }, [load]);

  return (
    <Card>
      <CardHeader className="items-start">
        <div className="min-w-0 space-y-1">
          <CardTitle className="flex items-center gap-2 [&_svg]:size-4"><LifeBuoy aria-hidden /> Code requests</CardTitle>
          <CardDescription>
            Clients who asked for a sign-in code in the last 24 hours, the device that asked, and its temporary internet.
            If a client cannot get their code, you can let their device try again, or read them the code. Both ask for your
            password and are recorded.
          </CardDescription>
        </div>
        <Button variant="outline" size="sm" onClick={() => void load()}>Refresh</Button>
      </CardHeader>
      <CardBody>
        {error ? <ErrorBanner err={error} /> : null}
        {!data && !error && <SkeletonRows rows={3} />}
        {data && data.requests.length === 0 && <p className="text-sm text-muted-foreground">No code requests in the last 24 hours.</p>}
        {data && data.requests.length > 0 && (
          <TableWrap>
            <Table>
              <THead><TR><TH>Requested</TH><TH>Sent to</TH><TH>Code</TH><TH>Temporary internet</TH><TH><span className="sr-only">Actions</span></TH></TR></THead>
              <TBody>
                {data.requests.map((r) => (
                  <TR key={r.challenge_id}>
                    <TD className="whitespace-nowrap">{formatDate(r.issued_at)}</TD>
                    <TD><div className="break-all">{r.destination}</div><div className="text-xs text-muted-foreground">{r.channel}{r.device_mac ? ` · ${r.device_mac}` : ""}</div></TD>
                    <TD><Badge tone={STATE[r.state].tone}>{STATE[r.state].label}</Badge>{r.state === "PENDING" && r.attempts > 0 && <div className="text-xs text-muted-foreground">{r.attempts} wrong {r.attempts === 1 ? "try" : "tries"}</div>}</TD>
                    <TD className="text-sm">{windowSummary(r, data.limits)}</TD>
                    <TD className="whitespace-nowrap text-right">
                      {mayReveal && r.code_available && (
                        <Button size="sm" variant="outline" onClick={() => setRevealing(r)}><KeyRound aria-hidden /> Show code</Button>
                      )}
                      {mayClear && r.device_mac && (r.retry_wait_seconds > 0 || r.device_windows_24h >= data.limits.per_device_daily) && (
                        <Button size="sm" variant="outline" className="ml-2" onClick={() => setClearing(r)}><RotateCcw aria-hidden /> Let them try again</Button>
                      )}
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          </TableWrap>
        )}
        {data && !data.code_copy_available && mayReveal && (
          <p className="mt-3 text-xs text-muted-foreground">Codes cannot be shown on this appliance: its local sealing key is missing.</p>
        )}
      </CardBody>
      <ClearDialog request={clearing} onClose={() => setClearing(null)} onDone={async () => { setClearing(null); await load(); }} />
      <RevealDialog request={revealing} onClose={() => setRevealing(null)} />
    </Card>
  );
}

function ClearDialog({ request, onClose, onDone }: { request: CodeRequest | null; onClose: () => void; onDone: () => void | Promise<void> }) {
  const [reason, setReason] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const toast = useToast();
  useEffect(() => { setReason(""); setPassword(""); setError(null); }, [request]);
  return (
    <ConfirmDialog
      open={request !== null}
      onOpenChange={(v) => !v && onClose()}
      title="Let this device try again?"
      description="Its earlier temporary internet windows stop counting towards the limits and the wait, so the next code it asks for opens a new window. Nothing is deleted and no access is granted: the client still signs in with their code."
      confirmLabel="Let them try again"
      busy={busy}
      error={error}
      onConfirm={async () => {
        if (!request?.device_mac) return;
        setBusy(true); setError(null);
        try {
          await api.post("/code-requests/clear", { device_mac: request.device_mac, reason: reason.trim(), password });
          toast.success("The device can ask for a code again.");
          await onDone();
        } catch (e) { setError(e); } finally { setBusy(false); }
      }}
    >
      <Field label="Reason" required hint="Recorded in the audit log.">
        <Input value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} aria-label="Reason" />
      </Field>
      <Field label="Confirm your password" required>
        <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} aria-label="Confirm your password" />
      </Field>
    </ConfirmDialog>
  );
}

function RevealDialog({ request, onClose }: { request: CodeRequest | null; onClose: () => void }) {
  const [reason, setReason] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [code, setCode] = useState<string | null>(null);
  useEffect(() => { setReason(""); setPassword(""); setError(null); setCode(null); }, [request]);
  return (
    <ConfirmDialog
      open={request !== null}
      onOpenChange={(v) => !v && onClose()}
      title={code ? "The client's code" : "Show this client's code?"}
      description={code
        ? "Tell it to the client. It works only on the device that asked for it, once, until it expires."
        : "Only for a client in front of you or on the phone who asked for this code to " + (request?.destination ?? "") + ". Reading it to them signs them in on an address they have not proved themselves. The reveal is recorded with your name and reason."}
      confirmLabel={code ? "Done" : "Show code"}
      busy={busy}
      error={error}
      onConfirm={async () => {
        if (!request) return;
        if (code) { onClose(); return; }
        setBusy(true); setError(null);
        try {
          const r = await api.post<{ code: string }>(`/code-reveal/${request.challenge_id}`, { reason: reason.trim(), password });
          setCode(r.code);
          setPassword("");
        } catch (e) { setError(e); } finally { setBusy(false); }
      }}
    >
      {code ? (
        <p className="text-center font-mono text-3xl tracking-[0.3em]" aria-label="Code">{code}</p>
      ) : (
        <>
          <Field label="Reason" required hint="Recorded in the audit log.">
            <Input value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} aria-label="Reason" />
          </Field>
          <Field label="Confirm your password" required>
            <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} aria-label="Confirm your password" />
          </Field>
        </>
      )}
    </ConfirmDialog>
  );
}
