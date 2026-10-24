"use client";

// Disputes and chargebacks leave access unchanged until an operator records a step-up decision.
// A full provider refund ends the paid access automatically; OneGate never initiates refunds.

import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { formatDate } from "@/lib/utils";
import { SectionCard } from "@/components/ui/card";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { StatusBadge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
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
    <SectionCard
      flush
      title="Card disputes and chargebacks"
      description="A dispute or chargeback never ends a client's access by itself. Decide each case once. A full provider refund ends that access automatically; OneGate never starts a refund."
    >
      {error ? <div className="px-5 pt-4"><ErrorBanner err={error} /></div> : null}
      {!data && !error && <SkeletonRows rows={2} />}
      {data && data.cases.length === 0 && <EmptyState size="sm" title="No disputes or chargebacks" />}
      {data && data.cases.length > 0 && (
        <Table label="Card disputes and chargebacks">
          <THead><TR><TH>Opened</TH><TH>Case</TH><TH numeric>Payment</TH><TH>Access</TH><TH>Decision</TH></TR></THead>
          <TBody>
            {data.cases.map((c) => (
              <TR key={c.id}>
                <TD nowrap muted>{formatDate(c.opened_at)}</TD>
                <TD>{KIND[c.kind]}<div className="break-all text-xs text-muted-foreground">{c.provider_ref}</div></TD>
                <TD numeric>{money(c.amount_minor, c.currency, 2)}</TD>
                <TD nowrap>{c.entitlement_status ? <StatusBadge tone={c.entitlement_status === "ACTIVE" ? "ok" : "default"}>{c.entitlement_status === "ACTIVE" ? "Active" : "Ended"}</StatusBadge> : "—"}</TD>
                <TD>
                  {c.resolution ? (
                    <div className="text-sm">
                      {c.resolution === "KEEP_ACCESS" ? "Access kept" : "Access ended"}
                      <div className="text-xs text-muted-foreground">{c.resolved_by} · {c.resolved_at ? formatDate(c.resolved_at) : ""} · {c.resolution_reason}</div>
                    </div>
                  ) : canAct ? (
                    <div className="flex flex-wrap gap-2">
                      <Button size="xs" variant="secondary" onClick={() => setActing({ c, resolution: "KEEP_ACCESS" })}>Keep access</Button>
                      <Button size="xs" variant="danger-outline" onClick={() => setActing({ c, resolution: "END_ACCESS" })}>End access</Button>
                    </div>
                  ) : <StatusBadge tone="warn">Waiting for a decision</StatusBadge>}
                </TD>
              </TR>
            ))}
          </TBody>
        </Table>
      )}
      {data && data.refund_terminations.length > 0 && (
        <div className="border-t border-border">
          <p className="px-5 pb-1 pt-3 text-label">Access ended by a full provider refund</p>
          <Table label="Access ended by a full provider refund">
            <THead><TR><TH>When</TH><TH numeric>Charged</TH><TH numeric>Refunded</TH><TH>Entitlement</TH></TR></THead>
            <TBody>
              {data.refund_terminations.map((x) => (
                <TR key={x.entitlement_id}>
                  <TD nowrap muted>{formatDate(x.terminated_at)}</TD>
                  <TD numeric>{money(x.captured_minor, x.currency, 2)}</TD>
                  <TD numeric>{money(x.refunded_minor, x.currency, 2)}</TD>
                  <TD className="font-mono text-xs">{x.entitlement_id.slice(0, 8)}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
        </div>
      )}
      <ResolveDialog acting={acting} onClose={() => setActing(null)} onDone={async () => { setActing(null); await load(); }} />
    </SectionCard>
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
