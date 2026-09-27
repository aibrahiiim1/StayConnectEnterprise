"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { ArrowRight } from "lucide-react";
import { api, withStepUp, ApiError } from "@/lib/api";
import { ConfirmDialog } from "@/components/ui/dialog";

export type Blocker = {
  type: string;
  label: string;
  count: number;
  resource?: string;
  ids?: string[];
};

/**
 * DeleteDialog — the one permanent-delete flow (customer, site, appliance record).
 *
 * DELETE {deleteUrl} with { [confirmField]: typed value, reason }, wrapped in withStepUp: every delete in §6 is a
 * step-up write. It NEVER cascades — while dependent records exist the server answers 409 with the blocking
 * list, which is shown (with a link to each when the caller knows where they are listed). The action enables
 * only after the exact name/code/serial and a reason are typed.
 */
export function DeleteDialog({
  open, onClose, onDeleted,
  title, what, expected, confirmHint, deleteUrl,
  consequences, confirmField = "confirm", blockerHref,
}: {
  open: boolean;
  onClose: () => void;
  onDeleted: () => void;
  title: string;                 // e.g. 'Delete customer "Semantics"'
  what: string;                  // e.g. "Customer" | "Site" | "Appliance record"
  expected: string;              // the exact string the user must type
  confirmHint: string;           // e.g. "Type the customer name"
  deleteUrl: string;             // DELETE endpoint
  /** What this delete does. The last item should be "It cannot be undone." */
  consequences?: React.ReactNode[];
  /** The body field that carries the typed value: "confirm", or "confirm_serial" for an appliance. */
  confirmField?: string;
  /** Where a blocking record is listed. Without it the blocker is named but not linked. */
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
        <div className="rounded-md border border-destructive/30 bg-destructive-subtle p-3.5 text-sm text-destructive-subtle-foreground">
          <div className="mb-2 font-semibold">{what} cannot be deleted because it still has:</div>
          <ul className="space-y-1.5">
            {blockers.map((b) => {
              const href = blockerHref?.(b);
              return (
                <li key={b.type} className="flex items-center justify-between gap-3">
                  <span>{b.count} {b.label}</span>
                  {href && (
                    <Link href={href} className="inline-flex items-center gap-1 font-medium underline-offset-2 hover:underline">
                      View {b.label} <ArrowRight className="size-3.5 rtl:-scale-x-100" aria-hidden />
                    </Link>
                  )}
                </li>
              );
            })}
          </ul>
          <div className="mt-2 text-xs opacity-80">
            Remove these first, in order: appliances, then sites, then the customer.
          </div>
        </div>
      )}
    </ConfirmDialog>
  );
}
