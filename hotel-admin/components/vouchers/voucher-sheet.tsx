"use client";

// ONE CARD, in full: what it grants, whether it can still be used, what has happened to it, and the two
// actions an operator can take on it. Both actions are step-up: the server re-checks the password and
// records the reason, and this sheet says so before, not after.

import * as React from "react";
import { Ban, Eye, EyeOff, Layers, Ticket } from "lucide-react";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { KeyValueGrid, Timeline } from "@/components/ui/data";
import { MonoId, Skeleton } from "@/components/ui/misc";
import { Sheet, SheetBody, SheetContent, SheetFooter, SheetHeader, SheetSection } from "@/components/ui/sheet";
import { useToast } from "@/components/ui/toast";
import {
  batchLabel,
  canCancel,
  effectiveStatus,
  maskedCode,
  STATUS_EXPLAIN,
  validityWords,
  type VoucherHistory,
  type VoucherReveal,
  type VoucherRow,
} from "@/lib/api/vouchers";
import { reasonProblem, StatusBadge, stepUpError, When } from "./shared";

export function VoucherSheet({
  row,
  onOpenChange,
  canIssue,
  canRevealCodes,
  reveals,
  onChanged,
  onShowBatch,
}: {
  row: VoucherRow | null;
  onOpenChange: (open: boolean) => void;
  canIssue: boolean;
  canRevealCodes: boolean;
  reveals: VoucherReveal[] | null;
  onChanged: () => void;
  onShowBatch: (batchId: string) => void;
}) {
  return (
    <Sheet open={row !== null} onOpenChange={onOpenChange}>
      <SheetContent
        width="md"
        // Focus the panel itself, not its first focusable element: that is a timestamp with a tooltip, which
        // would open on arrival and swallow the first Escape.
        onOpenAutoFocus={(e) => {
          e.preventDefault();
          (e.currentTarget as HTMLElement).focus();
        }}
      >
        {row && (
          <VoucherDetail
            key={row.id}
            row={row}
            canIssue={canIssue}
            canRevealCodes={canRevealCodes}
            reveals={reveals}
            onChanged={onChanged}
            onShowBatch={onShowBatch}
          />
        )}
      </SheetContent>
    </Sheet>
  );
}

function VoucherDetail({
  row,
  canIssue,
  canRevealCodes,
  reveals,
  onChanged,
  onShowBatch,
}: {
  row: VoucherRow;
  canIssue: boolean;
  canRevealCodes: boolean;
  reveals: VoucherReveal[] | null;
  onChanged: () => void;
  onShowBatch: (batchId: string) => void;
}) {
  const toast = useToast();
  // Local copy so a cancellation shows at once, before the list reloads.
  const [current, setCurrent] = React.useState<VoucherRow>(row);
  const status = effectiveStatus(current);
  const [history, setHistory] = React.useState<VoucherHistory | null>(null);
  const [historyErr, setHistoryErr] = React.useState<unknown>(null);
  const [action, setAction] = React.useState<"reveal" | "cancel" | null>(null);
  const [busy, setBusy] = React.useState(false);
  const [err, setErr] = React.useState<unknown>(null);
  // The revealed code lives here only. This component unmounts when the sheet closes, and the code with it.
  const [code, setCode] = React.useState<string | null>(null);

  React.useEffect(() => {
    api
      .get<VoucherHistory>(`/vouchers/${current.id}/history`)
      .then(setHistory)
      .catch(setHistoryErr);
  }, [current.id, current.state]);

  const myReveals = (reveals ?? []).filter((r) => r.voucher_id === current.id);

  async function reveal({ reason, password }: { reason: string; password: string }) {
    const bad = reasonProblem(reason);
    if (bad) return setErr(new Error(bad));
    setBusy(true);
    setErr(null);
    try {
      const out = await api.post<{ voucher_id: string; code: string }>(`/voucher-codes/${current.id}/reveal`, {
        password,
        reason,
      });
      setCode(out.code);
      setAction(null);
      toast.success("Code shown", "This reveal was recorded with your name and reason.");
      onChanged();
    } catch (e) {
      setErr(stepUpError(e, "The code was not revealed."));
    } finally {
      setBusy(false);
    }
  }

  async function cancel({ reason, password }: { reason: string; password: string }) {
    const bad = reasonProblem(reason);
    if (bad) return setErr(new Error(bad));
    setBusy(true);
    setErr(null);
    try {
      await api.post(`/vouchers/${current.id}/revoke`, { password, reason });
      setCurrent({ ...current, state: "REVOKED", effective_state: "cancelled" });
      setAction(null);
      toast.success(`Card ${maskedCode(current.code_last4)} cancelled`, "It can no longer be used to sign in.");
      onChanged();
    } catch (e) {
      setErr(stepUpError(e, "The card was not cancelled."));
    } finally {
      setBusy(false);
    }
  }

  const timeline: React.ComponentProps<typeof Timeline>["items"] = [
    {
      key: "issued",
      title: "Issued",
      when: <When iso={current.created_at} />,
      body: current.issued_by_label ? `by ${current.issued_by_label}` : undefined,
      tone: "info",
    },
  ];
  if (current.redemption_valid_from) {
    const future = Date.parse(current.redemption_valid_from) > Date.now();
    timeline.push({
      key: "from",
      title: future ? "Becomes valid" : "Became valid",
      when: <When iso={current.redemption_valid_from} />,
      tone: "neutral",
    });
  }
  for (const [i, e] of (history?.entitlements ?? []).entries()) {
    if (e.activated_at)
      timeline.push({
        key: `used-${i}`,
        title: "Used by a guest",
        when: <When iso={e.activated_at} />,
        body:
          e.status === "ACTIVE"
            ? e.window_ends_at
              ? <>Access active, ends <When iso={e.window_ends_at} /></>
              : "Access active"
            : e.terminated_at
              ? <>Access ended <When iso={e.terminated_at} /></>
              : undefined,
        tone: "ok",
      });
  }
  if (current.state === "REDEEMED" && history && history.entitlements.every((e) => !e.activated_at)) {
    timeline.push({ key: "used", title: "Used by a guest", body: "The time it was used is not recorded.", tone: "ok" });
  }
  if (history?.cancelled) {
    timeline.push({
      key: "cancelled",
      title: "Cancelled",
      when: <When iso={history.cancelled.at} />,
      body: [history.cancelled.by && `by ${history.cancelled.by}`, history.cancelled.reason && `“${history.cancelled.reason}”`]
        .filter(Boolean)
        .join(" · "),
      tone: "err",
    });
  } else if (current.state === "REVOKED") {
    timeline.push({ key: "cancelled", title: "Cancelled", tone: "err" });
  }
  if (status === "expired" && current.redemption_valid_until) {
    timeline.push({
      key: "expired",
      title: "Expired without being used",
      when: <When iso={current.redemption_valid_until} />,
      tone: "warn",
    });
  }
  for (const [i, r] of myReveals.entries()) {
    timeline.push({
      key: `rev-${i}`,
      title: "Code shown to an operator",
      when: <When iso={r.revealed_at} />,
      body: `${r.operator_label} · “${r.reason}”`,
      tone: "neutral",
    });
  }

  const spent = status === "redeemed" || status === "cancelled" || status === "expired";

  return (
    <>
      <SheetHeader
        eyebrow="Voucher"
        icon={<Ticket />}
        title={<span className="font-mono tracking-widest">{maskedCode(current.code_last4)}</span>}
        description={STATUS_EXPLAIN[status]}
        badges={<StatusBadge status={status} plain />}
      />
      <SheetBody>
        {code && (
          <Callout tone="warning" title="Full code">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <span className="font-mono text-2xl font-semibold tracking-[0.25em]" data-testid="revealed-code">
                {code}
              </span>
              <Button size="xs" variant="secondary" onClick={() => setCode(null)}>
                <EyeOff /> Hide
              </Button>
            </div>
            <p className="mt-1 text-xs">Recorded with your name and reason. It disappears when you close this panel.</p>
          </Callout>
        )}
        <SheetSection title="Details">
          <KeyValueGrid
            items={[
              {
                label: "Package",
                value: current.package_name
                  ? `${current.package_name}${current.package_revision_no ? ` (version ${current.package_revision_no})` : ""}`
                  : "A package version that is no longer listed",
              },
              { label: "Validity", value: validityWords(current.redemption_valid_from, current.redemption_valid_until) },
              { label: "Issued", value: <When iso={current.created_at} /> },
              { label: "Issued by", value: current.issued_by_label ?? (current.issued_by ? "An operator account" : null) },
              {
                label: "Batch",
                value: current.batch_id ? (
                  <Button variant="link" size="xs" className="h-auto" onClick={() => onShowBatch(current.batch_id!)}>
                    <Layers /> {batchLabel({ batch_id: current.batch_id, created_at: current.created_at })}
                  </Button>
                ) : (
                  "Printed before batches were recorded"
                ),
              },
              { label: "Card reference", value: <MonoId value={current.id} title="Card reference" /> },
              { label: "Note", value: current.notes, wide: true },
            ]}
          />
        </SheetSection>
        <SheetSection title="History">
          {historyErr ? (
            <ErrorBanner err={historyErr} />
          ) : history === null ? (
            <div className="space-y-2" aria-busy="true">
              <Skeleton className="h-4 w-48" />
              <Skeleton className="h-4 w-40" />
            </div>
          ) : (
            <>
              <Timeline items={timeline} />
              {(!history.redemption_available || !history.cancellation_available) && (
                <p className="text-xs text-muted-foreground">
                  Part of this card&apos;s history could not be read just now, so it may be incomplete.
                </p>
              )}
              {canRevealCodes && reveals && reveals.length >= 200 && (
                <p className="text-xs text-muted-foreground">Code views shown are from the latest 200 records.</p>
              )}
            </>
          )}
        </SheetSection>
      </SheetBody>
      {(canRevealCodes || canIssue) && (
        <SheetFooter>
          {canIssue && (
            <Button
              variant="danger"
              size="sm"
              disabled={!canCancel(status)}
              title={canCancel(status) ? undefined : "Only an unused card that has not expired can be cancelled."}
              onClick={() => {
                setErr(null);
                setAction("cancel");
              }}
            >
              <Ban /> Cancel card
            </Button>
          )}
          {canRevealCodes && (
            <Button
              size="sm"
              variant="secondary"
              onClick={() => {
                setErr(null);
                setAction("reveal");
              }}
            >
              <Eye /> Show full code
            </Button>
          )}
        </SheetFooter>
      )}

      <ConfirmDialog
        open={action === "reveal"}
        onOpenChange={(v) => !v && setAction(null)}
        title={`Show the full code of ${maskedCode(current.code_last4)}`}
        description="Showing a code is recorded permanently with your name and your reason."
        confirmLabel="Show code"
        busy={busy}
        error={err}
        requireReason
        reasonLabel="Reason (recorded)"
        reasonPlaceholder="Guest at the desk, card unreadable"
        requirePassword
        onConfirm={reveal}
      >
        {spent && (
          <Callout tone="neutral">
            This card is {status === "redeemed" ? "already used" : status === "cancelled" ? "cancelled" : "expired"}, so its
            code no longer lets anyone sign in. You can still view it, for example to answer a guest query; the view is
            recorded all the same.
          </Callout>
        )}
      </ConfirmDialog>

      <ConfirmDialog
        open={action === "cancel"}
        onOpenChange={(v) => !v && setAction(null)}
        title={`Cancel card ${maskedCode(current.code_last4)}`}
        description="The card stops working immediately and cannot be reinstated."
        confirmLabel="Cancel the card"
        confirmVariant="danger"
        busy={busy}
        error={err}
        requireReason
        reasonLabel="Reason (recorded)"
        reasonPlaceholder="Card reported lost"
        requirePassword
        onConfirm={cancel}
      >
        <p className="text-sm text-muted-foreground">
          Nothing else changes: other cards in the batch keep working, and no guest who is already online is
          disconnected. A card that has already been used cannot be cancelled here.
        </p>
      </ConfirmDialog>
    </>
  );
}
