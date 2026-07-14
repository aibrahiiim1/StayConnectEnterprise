"use client";

import { useState } from "react";
import Link from "next/link";
import { api, withStepUp, ApiError } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { AlertTriangle, X } from "lucide-react";

export type Blocker = {
  type: string;
  label: string;
  count: number;
  resource: string;
  ids?: string[];
};

/**
 * DeleteDialog is the shared, safe permanent-delete flow for owned resources
 * (Customer, Site, Appliance). It NEVER cascades: the server rejects a delete
 * while dependencies exist and returns the blocking list, which this dialog
 * renders with links to the relevant records. A successful delete requires a
 * typed confirmation, a reason, and a password step-up (handled by withStepUp).
 */
export function DeleteDialog({
  open, onClose, onDeleted,
  title, what, expected, confirmHint, deleteUrl, extraImpact,
}: {
  open: boolean;
  onClose: () => void;
  onDeleted: () => void;
  title: string;                 // e.g. 'Delete customer "Acme"'
  what: string;                  // e.g. "Customer" | "Site" | "Appliance"
  expected: string;              // the exact string the user must type
  confirmHint: string;           // e.g. "Type the customer name"
  deleteUrl: string;             // DELETE endpoint
  extraImpact?: React.ReactNode; // optional impact preview block
}) {
  const [confirm, setConfirm] = useState("");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [blockers, setBlockers] = useState<Blocker[] | null>(null);

  if (!open) return null;

  const linkFor = (b: Blocker) => `/${b.resource}`;

  async function submit() {
    setBusy(true); setErr(null); setBlockers(null);
    try {
      await withStepUp(() => api.del(deleteUrl, { confirm, reason }));
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
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" onClick={onClose}>
      <div className="w-full max-w-lg rounded-lg border border-border bg-panel shadow-xl" onClick={(e) => e.stopPropagation()}>
        <div className="flex items-center justify-between border-b border-border px-5 py-3">
          <div className="flex items-center gap-2 text-err"><AlertTriangle size={16} /> <span className="font-semibold">{title}</span></div>
          <button onClick={onClose} className="text-muted hover:text-text"><X size={16} /></button>
        </div>
        <div className="space-y-4 p-5">
          <p className="text-sm text-muted">
            This permanently deletes the {what.toLowerCase()}. It cannot be undone. Deletion is blocked while any
            owned records still exist — you&apos;ll see exactly what to remove first.
          </p>

          {extraImpact}

          {blockers && blockers.length > 0 && (
            <div className="rounded-md border border-err/40 bg-err/5 p-3">
              <div className="mb-2 text-sm font-medium text-err">{what} cannot be deleted because it still contains:</div>
              <ul className="space-y-1 text-sm">
                {blockers.map((b) => (
                  <li key={b.type} className="flex items-center justify-between">
                    <span>{b.count} {b.label}</span>
                    <Link href={linkFor(b)} className="text-brand hover:underline">View {b.label} →</Link>
                  </li>
                ))}
              </ul>
              <div className="mt-2 text-xs text-muted">Delete or archive these first, in order (Appliances → Site → Customer).</div>
            </div>
          )}

          <div>
            <Label>{confirmHint}: <span className="font-mono text-text">{expected}</span></Label>
            <Input value={confirm} onChange={(e) => setConfirm(e.target.value)} placeholder={expected} autoFocus />
          </div>
          <div>
            <Label>Reason (recorded in the audit log)</Label>
            <Input value={reason} onChange={(e) => setReason(e.target.value)} placeholder="e.g. decommissioned test tenant" />
          </div>

          {err && <div className="text-sm text-err">{err}</div>}

          <div className="flex justify-end gap-2">
            <Button variant="ghost" onClick={onClose}>Cancel</Button>
            <Button variant="danger" disabled={busy || confirm !== expected || !reason.trim()} onClick={submit}>
              {busy ? "Deleting…" : `Delete ${what}`}
            </Button>
          </div>
        </div>
      </div>
    </div>
  );
}
