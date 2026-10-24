"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { ArrowRight } from "lucide-react";
import { api, withStepUp, ApiError } from "@/lib/api";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Callout } from "@/components/ui/error-banner";

export type Blocker = {
  type: string;
  label: string;
  count: number;
  resource?: string;
  ids?: string[];
};

/** Deletes require password step-up and exact confirmation; dependencies block deletion instead of cascading. */
export function DeleteDialog({
  open, onClose, onDeleted,
  title, what, expected, confirmHint, deleteUrl,
  consequences, confirmField = "confirm", blockerHref,
}: {
  open: boolean;
  onClose: () => void;
  onDeleted: () => void;
  title: string;
  what: string;
  expected: string;
  confirmHint: string;
  deleteUrl: string;
  consequences?: React.ReactNode[];
  /** Appliances use confirm_serial; other deletes use confirm. */
  confirmField?: string;
  blockerHref?: (b: Blocker) => string | null;
}) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [blockers, setBlockers] = useState<Blocker[] | null>(null);

  useEffect(() => {
    if (!open) { setErr(null); setBlockers(null); setBusy(false); }
  }, [open]);

  async function submit({ reason }: { reason: string }) {
    setBusy(true); setErr(null); setBlockers(null);
    try {
      await withStepUp(() => api.del(deleteUrl, { [confirmField]: expected, reason }));
      onDeleted();
      onClose();
    } catch (e: any) {
      if (e instanceof ApiError && e.status === 409 && Array.isArray(e.body?.blocking)) {
        setBlockers(e.body.blocking as Blocker[]);
        setErr(e.body?.message ?? `${what} still has dependent records.`);
      } else if (e instanceof ApiError && e.status === 409) {
        setErr(e.body?.message ?? `${what} cannot be deleted yet.`);
      } else if (e instanceof ApiError && e.status === 400) {
        setErr(e.body?.message ?? "Confirmation did not match.");
      } else {
        setErr(e?.message ?? "Delete failed");
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <ConfirmDialog
      open={open}
      onOpenChange={(v) => { if (!v) onClose(); }}
      title={title}
      description={`This permanently deletes the ${what.toLowerCase()}.`}
      confirmLabel={`Delete ${what.toLowerCase()}`}
      confirmVariant="danger"
      busy={busy}
      error={err}
      consequences={consequences ?? [
        "Deletion is refused while anything still belongs to it — you will see exactly what to remove first.",
        "It cannot be undone.",
      ]}
      confirmText={expected}
      confirmTextLabel={confirmHint}
      requireReason
      reasonLabel="Reason (recorded in the audit log)"
      onConfirm={submit}
    >
      {blockers && blockers.length > 0 && (
        <Callout tone="neutral" title={`${what} cannot be deleted because it still has:`}>
          <ul className="mt-1 space-y-1">
            {blockers.map((b) => {
              const href = blockerHref?.(b);
              return (
                <li key={b.type} className="flex items-center justify-between gap-3">
                  <span className="tabular">{b.count} {b.label}</span>
                  {href && (
                    <Link href={href} className="inline-flex items-center gap-1 font-medium text-primary underline-offset-2 hover:underline">
                      View {b.label} <ArrowRight className="size-3.5 rtl:-scale-x-100" aria-hidden />
                    </Link>
                  )}
                </li>
              );
            })}
          </ul>
          <p className="mt-2 text-xs text-muted-foreground">Remove these first, in order: appliances, then sites, then the customer.</p>
        </Callout>
      )}
    </ConfirmDialog>
  );
}
