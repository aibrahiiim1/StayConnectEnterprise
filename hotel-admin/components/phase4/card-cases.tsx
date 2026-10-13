"use client";

// CARD DISPUTES AND CHARGEBACKS (Product-Owner policy 2026-10-12, migration 0115).
//
// A chargeback or a dispute never ends a client's access by itself: each is a case a payments operator resolves
// once, explicitly — Keep access, or End access (the access that charge paid for). Both ask for the password and a
// reason and are recorded. A FULL provider refund needs no case: the access it paid for was ended automatically,
// and those terminations are listed below as evidence. OneGate never initiates a refund or a reversal.

import { useCallback, useEffect, useState } from "react";
import { Scale } from "lucide-react";
import { api } from "@/lib/api";
import { formatDate } from "@/lib/utils";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, TBody, TD, TH, THead, TR, TableWrap } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/dialog";
import { ErrorBanner } from "@/components/ui/error-banner";
import { Field, Input } from "@/components/ui/input";
import { SkeletonRows } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { money } from "./format";

export type CardCase = {
  id: string; kind: "DISPUTE" | "CHARGEBACK"; settlement_id: string; charge_transaction_id: string; provider_ref: string;
  opened_at: string; amount_minor: number; currency: string; entitlement_status?: string;
  resolution?: "KEEP_ACCESS" | "END_ACCESS"; resolved_at?: string; resolved_by?: string; resolution_reason?: string;
};
export type RefundEnd = {
  entitlement_id: string; settlement_id: string; captured_minor: number; refunded_minor: number; currency: string; terminated_at: string;
};
type Resp = { cases: CardCase[]; refund_terminations: RefundEnd[] };

const KIND: Record<CardCase["kind"], string> = {
  DISPUTE: "Dispute (no money moved yet)",
  CHARGEBACK: "Chargeback (money returned to the cardholder)",
};

export function CardCasesCard({ canAct }: { canAct: boolean }) {
  const [data, setData] = useState<Resp | null>(null);
  const [error, setError] = useState<unknown>(null);
  const [acting, setActing] = useState<{ c: CardCase; resolution: "KEEP_ACCESS" | "END_ACCESS" } | null>(null);
  const load = useCallback(async () => {
    try {
      const r = await api.get<Partial<Resp>>("/financial-review/card-cases");
      setData({ cases: r?.cases ?? [], refund_terminations: r?.refund_terminations ?? [] });
      setError(null);
    } catch (e) { setError(e); }
  }, []);
  useEffect(() => { void load(); }, [load]);

  return (
    <Card>
      <CardHeader className="items-start">
        <div className="min-w-0 space-y-1">
          <CardTitle className="flex items-center gap-2 [&_svg]:size-4"><Scale aria-hidden /> Card disputes and chargebacks</CardTitle>
          <CardDescription>
            A dispute or chargeback never ends a client&apos;s access by itself. Decide each case once: keep the access, or end
            the access that payment paid for. A full refund from the provider ends that access automatically and is listed
            below. OneGate never starts a refund.
          </CardDescription>
        </div>
      </CardHeader>
      <CardBody className="space-y-4">
        {error ? <ErrorBanner err={error} /> : null}
        {!data && !error && <SkeletonRows rows={2} />}
        {data && data.cases.length === 0 && <p className="text-sm text-muted-foreground">No disputes or chargebacks.</p>}
        {data && data.cases.length > 0 && (
          <TableWrap>
            <Table>
              <THead><TR><TH>Opened</TH><TH>Case</TH><TH>Payment</TH><TH>Access</TH><TH>Decision</TH></TR></THead>
              <TBody>
                {data.cases.map((c) => (
                  <TR key={c.id}>
                    <TD className="whitespace-nowrap">{formatDate(c.opened_at)}</TD>
                    <TD>{KIND[c.kind]}<div className="text-xs text-muted-foreground break-all">{c.provider_ref}</div></TD>
                    <TD className="whitespace-nowrap">{money(c.amount_minor, c.currency, 2)}</TD>
                    <TD>{c.entitlement_status ? <Badge tone={c.entitlement_status === "ACTIVE" ? "ok" : "default"}>{c.entitlement_status === "ACTIVE" ? "Active" : "Ended"}</Badge> : "—"}</TD>
                    <TD>
                      {c.resolution ? (
                        <div className="text-sm">
                          {c.resolution === "KEEP_ACCESS" ? "Access kept" : "Access ended"}
                          <div className="text-xs text-muted-foreground">{c.resolved_by} · {c.resolved_at ? formatDate(c.resolved_at) : ""} · {c.resolution_reason}</div>
                        </div>
                      ) : canAct ? (
                        <div className="flex flex-wrap gap-2">
                          <Button size="sm" variant="outline" onClick={() => setActing({ c, resolution: "KEEP_ACCESS" })}>Keep access</Button>
                          <Button size="sm" variant="outline" onClick={() => setActing({ c, resolution: "END_ACCESS" })}>End access</Button>
                        </div>
                      ) : <Badge tone="warn">Waiting for a decision</Badge>}
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          </TableWrap>
        )}
        {data && data.refund_terminations.length > 0 && (
          <div className="space-y-2">
            <p className="text-sm font-medium">Access ended by a full provider refund</p>
            <TableWrap>
              <Table>
                <THead><TR><TH>When</TH><TH>Charged</TH><TH>Refunded</TH><TH>Entitlement</TH></TR></THead>
                <TBody>
                  {data.refund_terminations.map((x) => (
                    <TR key={x.entitlement_id}>
                      <TD className="whitespace-nowrap">{formatDate(x.terminated_at)}</TD>
                      <TD>{money(x.captured_minor, x.currency, 2)}</TD>
                      <TD>{money(x.refunded_minor, x.currency, 2)}</TD>
                      <TD className="font-mono text-xs">{x.entitlement_id.slice(0, 8)}</TD>
                    </TR>
                  ))}
                </TBody>
              </Table>
            </TableWrap>
          </div>
        )}
      </CardBody>
      <ResolveDialog acting={acting} onClose={() => setActing(null)} onDone={async () => { setActing(null); await load(); }} />
    </Card>
  );
}

function ResolveDialog({ acting, onClose, onDone }: {
  acting: { c: CardCase; resolution: "KEEP_ACCESS" | "END_ACCESS" } | null; onClose: () => void; onDone: () => void | Promise<void>;
}) {
  const [reason, setReason] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const toast = useToast();
  useEffect(() => { setReason(""); setPassword(""); setError(null); }, [acting]);
  const end = acting?.resolution === "END_ACCESS";
  return (
    <ConfirmDialog
      open={acting !== null}
      onOpenChange={(v) => !v && onClose()}
      title={end ? "End the access this payment paid for?" : "Keep the client's access?"}
      description={end
        ? "The client's access bought with this payment ends now, on every device. Other purchases are not affected. The decision is final and recorded with your name and reason."
        : "Nothing else changes. The decision is final and recorded with your name and reason."}
      confirmLabel={end ? "End access" : "Keep access"}
      busy={busy}
      error={error}
      onConfirm={async () => {
        if (!acting) return;
        setBusy(true); setError(null);
        try {
          await api.post(`/financial-review/card-cases/${acting.c.id}/resolve`, { resolution: acting.resolution, reason: reason.trim(), password });
          toast.success(end ? "Access ended." : "Access kept.");
          await onDone();
        } catch (e) { setError(e); } finally { setBusy(false); }
      }}
    >
      <Field label="Reason" required hint="Recorded on the case and in the audit log.">
        <Input value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} aria-label="Reason" />
      </Field>
      <Field label="Confirm your password" required>
        <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} aria-label="Confirm your password" />
      </Field>
    </ConfirmDialog>
  );
}
