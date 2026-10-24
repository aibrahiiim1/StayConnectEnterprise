"use client";

// Restoring saves the earlier design again as a new entry, so history stays truthful. It asks for the operator's
// password because it can change custom CSS and HTML as much as any save.

import { useState } from "react";
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { DescriptionList } from "@/components/ui/data";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { HistoryList } from "@/components/ui/history";
import { ConfirmDialog } from "@/components/ui/dialog";
import { useToast } from "@/components/ui/toast";
import { ApiError } from "@/lib/api";
import { formatDate, formatRelative } from "@/lib/utils";
import { RotateCcw } from "lucide-react";
import { restoreRevision, type RevisionSummary } from "@/lib/api/portal-design";

/** The only note the server writes itself is the restore marker. */
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
  const [open, setOpen] = useState<RevisionSummary | null>(null);
  const [target, setTarget] = useState<RevisionSummary | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const current = revisions[0]?.version;

  async function restore({ password }: { password: string }) {
    if (!target) return;
    setBusy(true); setErr(null);
    try {
      await restoreRevision(target.version, password);
      toast.success(`Save #${target.version} restored`, "Clients see it now. It was added to the history as a new save.");
      setTarget(null);
      await onRestored();
    } catch (e) {
      setErr(e instanceof ApiError && e.code === "reauth_required" ? "That password was not accepted." : e);
    } finally { setBusy(false); }
  }

  return (
    <Card className="overflow-hidden">
      <CardHeader>
        <div className="min-w-0">
          <CardTitle>History</CardTitle>
          <CardDescription>The latest saves of this page (up to 20), newest first. Open one to restore it.</CardDescription>
        </div>
      </CardHeader>
      <HistoryList
        label="Saves of this page"
        emptyLabel="Nothing has been saved yet. Your first save will appear here."
        onSelect={(it) => setOpen(revisions.find((r) => String(r.version) === it.id) ?? null)}
        selectedId={open ? String(open.version) : null}
        items={revisions.map((r) => ({
          id: String(r.version),
          title: `Save #${r.version}`,
          badge: r.version === current ? <Badge tone="ok" dot>Clients see this</Badge> : undefined,
          actor: r.published_by || undefined,
          at: <span title={formatDate(r.published_at)}>{formatRelative(r.published_at)}</span>,
          summary: describe(r.note) || undefined,
          tone: r.version === current ? "ok" : "neutral",
        }))}
      />

      <DetailsDrawer
        open={open !== null}
        onOpenChange={(o) => { if (!o) setOpen(null); }}
        eyebrow="Portal design save"
        title={open ? `Save #${open.version}` : ""}
        badges={open && open.version === current ? <Badge tone="ok" dot>Clients see this</Badge> : undefined}
        actions={open && open.version !== current && writable ? (
          <Button onClick={() => { const r = open; setOpen(null); setErr(null); setTarget(r); }}>
            <RotateCcw aria-hidden /> Restore this save…
          </Button>
        ) : undefined}
      >
        {open && (
          <DetailsSection title="Record">
            <DescriptionList items={[
              { label: "Saved", value: formatDate(open.published_at) },
              { label: "Saved by", value: open.published_by || "—" },
              { label: "Note", value: describe(open.note) || "—" },
            ]} />
          </DetailsSection>
        )}
      </DetailsDrawer>

      <ConfirmDialog
        open={!!target}
        onOpenChange={(o) => { if (!o) setTarget(null); }}
        title={target ? `Restore save #${target.version}?` : "Restore"}
        description={
          <>
            Clients will see that design immediately, including its custom CSS and HTML. Nothing is deleted: it is
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
