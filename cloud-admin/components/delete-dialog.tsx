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
  resource: string;
  ids?: string[];
};

/**
 * DeleteDialog — the one permanent-delete flow for owned resources (Customer, Site, Appliance).
 *
 * Each caller keeps its endpoint's contract. By default: DELETE {deleteUrl} with { confirm, reason }, wrapped in
 * withStepUp so the server can ask for the password again. An endpoint that takes no body and no step-up (the
 * customer-scoped DELETE /v1/appliances/{id}) passes `takesReason={false}` and `stepUp={false}`: the request is
 * then exactly `DELETE {deleteUrl}` and no reason field is shown, but the dialog looks and reads the same — the
 * consequence list, "It cannot be undone." and the typed confirmation. It NEVER cascades — while dependencies exist the server answers 409 with the
 * blocking list, which is rendered with a link to each. What changed is the presentation, now on the shared
 * ConfirmDialog: it says it cannot be undone, previews what is affected, lists blockers, and requires the typed
 * name/code/serial plus a reason before the button enables.
 */
export function DeleteDialog({
  open, onClose, onDeleted,
  title, what, expected, confirmHint, deleteUrl, extraImpact,
  consequences, takesReason = true, stepUp = true,
}: {
  open: boolean;
  onClose: () => void;
  onDeleted: () => void;
  title: string;                 // e.g. 'Delete customer "Semantics"'
  what: string;                  // e.g. "Customer" | "Site" | "Appliance"
  expected: string;              // the exact string the user must type
  confirmHint: string;           // e.g. "Type the customer name"
  deleteUrl: string;             // DELETE endpoint
  extraImpact?: React.ReactNode; // optional impact preview block
  /** What this delete does. The last item should be "It cannot be undone." Defaults to the owned-records wording. */
  consequences?: React.ReactNode[];
  /** Does the endpoint take { confirm, reason }? When false the request carries no body and no reason is asked. */
  takesReason?: boolean;
  /** Wrap the request in withStepUp (the endpoint may answer reauth_required). */
  stepUp?: boolean;
}) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [blockers, setBlockers] = useState<Blocker[] | null>(null);

  useEffect(() => {
    if (!open) { setErr(null); setBlockers(null); setBusy(false); }
  }, [open]);

  const linkFor = (b: Blocker) => `/${b.resource}`;

  async function submit({ reason }: { reason: string }) {
    setBusy(true); setErr(null); setBlockers(null);
    try {
      const call = () => (takesReason ? api.del(deleteUrl, { confirm: expected, reason }) : api.del(deleteUrl));
      await (stepUp ? withStepUp(call) : call());
      onDeleted();
      onClose();
    } catch (e: any) {
      if (e instanceof ApiError && e.status === 409 && Array.isArray(e.body?.blocking)) {
        setBlockers(e.body.blocking as Blocker[]);
        setErr(e.body?.message ?? `${what} still has dependent records.`);
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
        "Deletion is blocked while any owned records still exist — you will see exactly what to remove first.",
        "It cannot be undone.",
      ]}
      confirmText={expected}
      confirmTextLabel={confirmHint}
      requireReason={takesReason}
      reasonLabel="Reason (recorded in the audit log)"
      reasonPlaceholder="e.g. decommissioned test customer"
      onConfirm={submit}
    >
      {extraImpact}
      {blockers && blockers.length > 0 && (
        <div className="rounded-md border border-destructive/30 bg-destructive-subtle p-3.5 text-sm text-destructive-subtle-foreground">
          <div className="mb-2 font-semibold">{what} cannot be deleted because it still contains:</div>
          <ul className="space-y-1.5">
            {blockers.map((b) => (
              <li key={b.type} className="flex items-center justify-between gap-3">
                <span>{b.count} {b.label}</span>
                <Link href={linkFor(b)} className="inline-flex items-center gap-1 font-medium underline-offset-2 hover:underline">
                  View {b.label} <ArrowRight className="size-3.5 rtl:-scale-x-100" aria-hidden />
                </Link>
              </li>
            ))}
          </ul>
          <div className="mt-2 text-xs opacity-80">
            Delete or archive these first, in order (Appliances → Site → Customer).
          </div>
        </div>
      )}
    </ConfirmDialog>
  );
}
