"use client";

// HISTORY — every save, and a way back.
//
// Each save already appends an immutable entry (who, when) to the design's history; that is how a bad change
// is recovered and how the audit says who changed the page guests type their room number into. This shows it,
// newest first, and lets an operator restore an earlier one. Restoring does not rewind anything: it saves that
// earlier design again as a NEW entry, so the history still says what actually happened. It asks for the
// operator's password, because it can change the custom CSS and HTML as much as any save can.
//
// The vocabulary is the hotel's -- "saved", "restore" -- not a release manager's.

import { useState } from "react";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Timeline } from "@/components/ui/data";
import { ConfirmDialog } from "@/components/ui/dialog";
import { useToast } from "@/components/ui/toast";
import { ApiError } from "@/lib/api";
import { formatDate, formatRelative } from "@/lib/utils";
import { History as HistoryIcon, RotateCcw } from "lucide-react";
import { restoreRevision, type RevisionSummary } from "@/lib/api/portal-design";

/** A server note, in the screen's words. The only note the server writes itself is the restore marker. */
function describe(note?: string) {
  const m = note?.match(/^rolled back to version (\d+)$/);
  if (m) return `Restored save #${m[1]}`;
  return note || "";
}

export function HistorySection({ revisions, writable, dirty, onRestored }: {
  revisions: RevisionSummary[];
  writable: boolean;
  dirty: boolean;
  onRestored: () => Promise<void> | void;
}) {
  const toast = useToast();
  const [target, setTarget] = useState<RevisionSummary | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const current = revisions[0]?.version;

  async function restore({ password }: { password: string }) {
    if (!target) return;
    setBusy(true); setErr(null);
    try {
      await restoreRevision(target.version, password);
      toast.success(`Save #${target.version} restored`, "Guests see it now. It was added to the history as a new save.");
      setTarget(null);
      await onRestored();
    } catch (e) {
      setErr(e instanceof ApiError && e.code === "reauth_required" ? "That password was not accepted." : e);
    } finally { setBusy(false); }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center gap-2"><HistoryIcon className="h-4 w-4" aria-hidden /> History</CardTitle>
        <CardDescription>
          The most recent saves of this page (up to 20), newest first. Restoring one makes it what guests see
          again.
        </CardDescription>
      </CardHeader>
      <CardBody>
        <Timeline
          emptyLabel="Nothing has been saved yet. Your first save will appear here."
          items={revisions.map((r) => ({
            key: String(r.version),
            tone: r.version === current ? "ok" : "neutral",
            title: (
              <span className="inline-flex flex-wrap items-center gap-2">
                Save #{r.version}
                {r.version === current && <Badge tone="ok" dot>Guests see this</Badge>}
              </span>
            ),
            when: <span title={formatDate(r.published_at)}>{formatRelative(r.published_at)}</span>,
            body: (
              <div className="flex flex-wrap items-center justify-between gap-2">
                <span>{[r.published_by, describe(r.note)].filter(Boolean).join(" · ") || "—"}</span>
                {r.version !== current && writable && (
                  <Button size="xs" variant="outline" onClick={() => { setErr(null); setTarget(r); }}>
                    <RotateCcw className="mr-1 h-3 w-3" aria-hidden /> Restore
                  </Button>
                )}
              </div>
            ),
          }))}
        />
      </CardBody>
      <ConfirmDialog
        open={!!target}
        onOpenChange={(o) => { if (!o) setTarget(null); }}
        title={target ? `Restore save #${target.version}?` : "Restore"}
        description={
          <>
            Guests will see that design immediately, including its custom CSS and HTML. Nothing is deleted: it is
            added to the history as a new save, and today&apos;s design stays in the history too.
            {dirty && " Your unsaved changes on this screen will be replaced."}
          </>
        }
        confirmLabel="Restore"
        busy={busy}
        error={err}
        requirePassword
        onConfirm={restore}
      />
    </Card>
  );
}
