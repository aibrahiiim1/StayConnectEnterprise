"use client";

// "DELETE…" FOR A PACKAGE OR A PLAN.
//
// A package or plan may be deleted permanently only when nothing has ever used it (migration 0091): no grant,
// purchase, portal offer, voucher, billing mapping, grace policy or guest account — and, for a plan, no package
// version built on it. The server decides, from the same list the database refuses on; this dialog shows its
// answer. When the answer is yes it asks for a reason and the operator's password and deletes. When it is no it
// shows what is attached, with real counts, and offers the thing that IS possible — disabling, which stops it
// being offered and keeps its history. If something starts using the item while the dialog is open, the delete
// is refused by the server and the dialog switches to the refusal it returned.

import { useEffect, useId, useState } from "react";
import { Trash2, Ban } from "lucide-react";
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogBody, DialogFooter,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Field, Input } from "@/components/ui/input";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { SkeletonRows } from "@/components/ui/misc";
import { ApiError } from "@/lib/api";
import type { Deletability } from "@/lib/api/commerce";

export function DeleteDialog({
  open,
  onOpenChange,
  kind,
  name,
  load,
  onDelete,
  onDisableInstead,
  disableLabel = "Disable instead",
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  kind: "package" | "plan";
  name: string;
  /** Fetches the deletability answer for this record. */
  load: () => Promise<Deletability>;
  /** Performs the deletion; omitted for a read-only operator. Throws the server's refusal. */
  onDelete?: (args: { reason: string; password: string }) => Promise<void>;
  /** Offered as the alternative; omitted when the record cannot be disabled (already disabled, or a plan). */
  onDisableInstead?: () => void;
  disableLabel?: string;
}) {
  const [answer, setAnswer] = useState<Deletability | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [actionErr, setActionErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [reason, setReason] = useState("");
  const [password, setPassword] = useState("");
  const reasonId = useId();
  const passwordId = useId();

  useEffect(() => {
    // Cleared on close: a password must not outlive the dialog that collected it.
    if (!open) { setAnswer(null); setErr(null); setActionErr(null); setReason(""); setPassword(""); return; }
    let live = true;
    load().then((d) => { if (live) setAnswer(d); }).catch((e) => { if (live) setErr(e); });
    return () => { live = false; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const noun = kind === "package" ? "package" : "service plan";
  const deletable = answer?.deletable === true;
  const counted = (answer?.reasons ?? []).filter((r) => typeof r.count === "number");
  const plain = (answer?.reasons ?? []).filter((r) => typeof r.count !== "number");
  const ready = reason.trim().length >= 4 && password !== "" && !busy;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!onDelete || !ready) return;
    setBusy(true); setActionErr(null);
    try {
      await onDelete({ reason: reason.trim(), password });
    } catch (x) {
      const refused = x instanceof ApiError && x.status === 409 ? (x.body?.deletability as Deletability | undefined) : undefined;
      if (refused && Array.isArray(refused.reasons)) { setAnswer(refused); setPassword(""); }
      else setActionErr(x);
    } finally { setBusy(false); }
  }

  return (
    <Dialog open={open} onOpenChange={(v) => { if (!busy) onOpenChange(v); }}>
      <DialogContent size="sm">
        <form onSubmit={submit}>
          <DialogHeader>
            <DialogTitle>
              <span className="inline-flex items-center gap-2"><Trash2 className="size-4" /> Delete {name}?</span>
            </DialogTitle>
            <DialogDescription>
              {!answer ? `Checking whether anything uses this ${noun}…`
                : deletable ? `Nothing has ever used this ${noun}, so it can be deleted permanently.`
                : `This ${noun} can't be deleted. Here is what is still attached to it.`}
            </DialogDescription>
          </DialogHeader>
          <DialogBody className="space-y-4">
            <ErrorBanner err={err} />
            {!answer && !err && <SkeletonRows rows={3} cols={1} />}
            {answer && deletable && (
              <>
                <Callout tone="warning" title="This cannot be undone">
                  The {noun} and all of its saved versions are removed. No guest, purchase, voucher or report
                  refers to it, so no history is lost.
                </Callout>
                {onDelete ? (
                  <>
                    <Field label="Why are you deleting it?" htmlFor={reasonId} required
                      hint="At least 4 characters. Kept in the audit log.">
                      <Input id={reasonId} value={reason} onChange={(e) => setReason(e.target.value)}
                        placeholder="Created by mistake" maxLength={500} required />
                    </Field>
                    <Field label="Confirm your password" htmlFor={passwordId} required>
                      <Input id={passwordId} type="password" autoComplete="current-password" value={password}
                        onChange={(e) => setPassword(e.target.value)} required />
                    </Field>
                    <ErrorBanner err={actionErr} />
                  </>
                ) : (
                  <p className="text-sm text-muted-foreground">Your role can view this {noun} but not delete it.</p>
                )}
              </>
            )}
            {answer && !deletable && (
              <>
                {counted.length > 0 && (
                  <ul className="space-y-2" data-testid="delete-reasons">
                    {counted.map((r) => (
                      <li key={r.code} className="flex items-start justify-between gap-3 text-sm">
                        <span>{r.message}</span>
                        <Badge tone="neutral">{(r.count ?? 0).toLocaleString()}</Badge>
                      </li>
                    ))}
                  </ul>
                )}
                {plain.map((r) => <p key={r.code} className="text-sm">{r.message}</p>)}
                <Callout tone="info" title="Why it can't be deleted">
                  {kind === "package"
                    ? "Those records must keep pointing at the exact package they were made under. Disable it instead — a disabled package is no longer offered to guests and keeps its history."
                    : "Those records must keep pointing at the exact plan they were made under. A plan stops reaching guests when no active package uses it — disable or edit those packages instead."}
                </Callout>
              </>
            )}
          </DialogBody>
          <DialogFooter>
            <Button type="button" variant="ghost" onClick={() => onOpenChange(false)} disabled={busy}>
              {deletable && onDelete ? "Cancel" : "Close"}
            </Button>
            {answer && !deletable && onDisableInstead && (
              <Button type="button" variant="secondary" onClick={onDisableInstead}>
                <Ban /> {disableLabel}
              </Button>
            )}
            {deletable && onDelete && (
              <Button type="submit" variant="danger" disabled={!ready}>
                <Trash2 /> {busy ? "Deleting…" : `Delete ${noun}`}
              </Button>
            )}
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
