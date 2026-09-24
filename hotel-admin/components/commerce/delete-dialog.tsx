"use client";

// "DELETE…" FOR A PACKAGE OR A PLAN — the honest version.
//
// A package's saved versions are permanent by design: every grant a guest received points at the exact
// version it was given under, and removing those records needs a database change the Product Owner has not
// approved. So this dialog never deletes anything. It asks the server what is still attached, shows it with
// real counts, says why removal is not possible, and offers the thing that IS possible — disabling the package,
// which stops it being offered and keeps its history.

import { useEffect, useState } from "react";
import { Trash2, Ban } from "lucide-react";
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogBody, DialogFooter,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { SkeletonRows } from "@/components/ui/misc";
import type { Deletability } from "@/lib/api/commerce";

export function DeleteDialog({
  open,
  onOpenChange,
  kind,
  name,
  load,
  onDisableInstead,
  disableLabel = "Disable instead",
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  kind: "package" | "plan";
  name: string;
  /** Fetches the deletability answer for this record. */
  load: () => Promise<Deletability>;
  /** Offered as the alternative; omitted when the record cannot be disabled (already disabled, or a plan). */
  onDisableInstead?: () => void;
  disableLabel?: string;
}) {
  const [answer, setAnswer] = useState<Deletability | null>(null);
  const [err, setErr] = useState<unknown>(null);

  useEffect(() => {
    if (!open) { setAnswer(null); setErr(null); return; }
    let live = true;
    load().then((d) => { if (live) setAnswer(d); }).catch((e) => { if (live) setErr(e); });
    return () => { live = false; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const counted = (answer?.reasons ?? []).filter((r) => typeof r.count === "number");
  const closing = (answer?.reasons ?? []).filter((r) => typeof r.count !== "number");
  const noun = kind === "package" ? "package" : "service plan";

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent size="sm">
        <DialogHeader>
          <DialogTitle>
            <span className="inline-flex items-center gap-2"><Trash2 className="size-4" /> Delete {name}?</span>
          </DialogTitle>
          <DialogDescription>
            This {noun} can&rsquo;t be deleted. Here is what is still attached to it.
          </DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-4">
          <ErrorBanner err={err} />
          {!answer && !err && <SkeletonRows rows={3} cols={1} />}
          {answer && (
            <>
              {counted.length > 0 ? (
                <ul className="space-y-2" data-testid="delete-reasons">
                  {counted.map((r) => (
                    <li key={r.code} className="flex items-start justify-between gap-3 text-sm">
                      <span>{r.message}</span>
                      <Badge tone="neutral">{(r.count ?? 0).toLocaleString()}</Badge>
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="text-sm text-muted-foreground">
                  Nothing that this screen can see is using it — but its saved versions are still kept.
                </p>
              )}
              {closing.map((r) => (
                <Callout key={r.code} tone="info" title="Why it can't be deleted">
                  {r.message}
                </Callout>
              ))}
            </>
          )}
        </DialogBody>
        <DialogFooter>
          <Button variant="ghost" onClick={() => onOpenChange(false)}>Close</Button>
          {onDisableInstead && (
            <Button variant="secondary" onClick={onDisableInstead}>
              <Ban /> {disableLabel}
            </Button>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
