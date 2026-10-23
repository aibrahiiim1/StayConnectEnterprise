"use client";

// Revealing a live code requires password and reason; the code works once on the requesting device only.
// The revealed code stays in this dialog's memory and is never stored here.

import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { formatDate } from "@/lib/utils";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/dialog";
import { ErrorBanner } from "@/components/ui/error-banner";
import { Field, Input } from "@/components/ui/input";
import { SkeletonRows } from "@/components/ui/misc";
import { Table, TBody, TD, TH, THead, TR, TableWrap } from "@/components/ui/table";
import { KeyRound, LifeBuoy } from "lucide-react";

export type CodeRequest = {
  challenge_id: string; channel: string; destination: string; issued_at: string; expires_at: string;
  consumed_at?: string; attempts: number; max_attempts: number; state: "PENDING" | "USED" | "EXPIRED" | "LOCKED";
  device_mac?: string; code_available: boolean;
  existing_client?: boolean; staff_revealed?: boolean; masked?: boolean;
};
export type CodeRequestsResp = {
  requests: CodeRequest[];
  code_copy_available: boolean;
};

const STATE: Record<CodeRequest["state"], { label: string; tone: "ok" | "warn" | "default" | "err" }> = {
  PENDING: { label: "Waiting for the code", tone: "warn" },
  USED: { label: "Signed in", tone: "ok" },
  EXPIRED: { label: "Expired", tone: "default" },
  LOCKED: { label: "Too many wrong tries", tone: "err" },
};

const CHANNEL: Record<string, string> = { sms: "SMS", whatsapp: "WhatsApp", email: "Email (former email code)" };

export function CodeRequestsCard({ roles }: { roles: string[] }) {
  const mayReveal = canWrite("code-reveal", roles);
  const [data, setData] = useState<CodeRequestsResp | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [revealing, setRevealing] = useState<CodeRequest | null>(null);
  const load = useCallback(async () => {
    try {
      const r = await api.get<Partial<CodeRequestsResp>>("/code-requests?hours=24");
      setData({ requests: r?.requests ?? [], code_copy_available: r?.code_copy_available ?? false });
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
            Clients who asked for a sign-in code by SMS or WhatsApp in the last 24 hours, and the device that asked. If a
            client cannot get their code, you can read it to them. That asks for your password and is recorded.
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
              <THead><TR><TH>Requested</TH><TH>Sent to</TH><TH>Code</TH><TH><span className="sr-only">Actions</span></TH></TR></THead>
              <TBody>
                {data.requests.map((r) => (
                  <TR key={r.challenge_id}>
                    <TD className="whitespace-nowrap">{formatDate(r.issued_at)}</TD>
                    <TD><div className="break-all">{r.destination}</div><div className="text-xs text-muted-foreground">{CHANNEL[r.channel] ?? r.channel}{r.device_mac ? ` · ${r.device_mac}` : ""}</div></TD>
                    <TD>
                      <Badge tone={STATE[r.state].tone}>{STATE[r.state].label}</Badge>
                      {r.state === "PENDING" && r.attempts > 0 && <div className="text-xs text-muted-foreground">{r.attempts} wrong {r.attempts === 1 ? "try" : "tries"}</div>}
                      {r.staff_revealed && <div className="text-xs text-muted-foreground">Shown by staff</div>}
                      {r.existing_client && r.state === "PENDING" && (
                        <div className="text-xs text-muted-foreground">Existing client — the code cannot be shown; they must use the code they received.</div>
                      )}
                    </TD>
                    <TD className="whitespace-nowrap text-right">
                      {mayReveal && r.code_available && (
                        <Button size="sm" variant="outline" onClick={() => setRevealing(r)}><KeyRound aria-hidden /> Show code</Button>
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
      <RevealDialog request={revealing} onClose={() => setRevealing(null)} />
    </Card>
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
        : "Only for a client in front of you or on the phone who asked for this code to " + (request?.destination ?? "") + ". The code works only on the device that asked for it. The client is signed in as a new client whose number is marked as vouched for by staff: it earns no remembered device until they prove it themselves. The reveal is recorded with your name and reason."}
      confirmLabel={code ? "Done" : "Show code"}
      busy={busy}
      error={error}
      onConfirm={async () => {
        if (!request) return;
        if (code) { onClose(); return; }
        setBusy(true); setError(null);
        try {
          const r = await api.post<{ code: string }>(`/code-reveal/${request.challenge_id}`, { reason: reason.trim(), password });
          if (!r?.code) throw new Error("The code is no longer available.");
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
