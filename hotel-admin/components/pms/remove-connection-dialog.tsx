"use client";

// Removing a PMS connection. The server decides what removal means for this particular connection (delete it, retire it, or refuse) and the dialog says so before asking
// for confirmation: a connection that was only ever set up is deleted, a used one is retired and kept as history, and a live or routed one cannot be removed yet.

import { useEffect, useState } from "react";
import Link from "next/link";
import { api } from "@/lib/api";
import { ConfirmDialog, InfoDialog } from "@/components/ui/dialog";
import { Callout } from "@/components/ui/error-banner";
import { Skeleton } from "@/components/ui/misc";
import { Badge } from "@/components/ui/badge";
import { useToast } from "@/components/ui/toast";
import { pmsErrorText } from "@/lib/api/pms-connections";

export type RemovalOutcome = "DELETE" | "RETIRE" | "BLOCKED" | "RETIRED";

export type RemovalAssessment = {
  id: string;
  display_label: string;
  lifecycle_state: string;
  outcome: RemovalOutcome;
  blockers: { code: string; title: string; detail: string; networks?: { id: string; name: string; state: string }[] }[];
  facts: { stays: number; in_house_stays: number; live_entitlements: number; events: number; postings: number; revisions: number; ever_connected: boolean; ever_activated: boolean };
  consequences: string[];
  stale_routing?: { id: string; name: string; state: string }[];
};

export const REMOVAL_REASONS = [
  { value: "NO_LONGER_NEEDED", label: "No longer needed" },
  { value: "SET_UP_BY_MISTAKE", label: "Set up by mistake" },
  { value: "REPLACED_BY_ANOTHER", label: "Replaced by another connection" },
  { value: "PROPERTY_CLOSED", label: "The property or system is closed" },
];

export function removalVerb(a: Pick<RemovalAssessment, "outcome"> | null): string {
  return a?.outcome === "DELETE" ? "Delete" : "Retire";
}

export function RemoveConnectionDialog({
  open, onOpenChange, connection, onDone,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  connection: { id: string; display_label: string } | null;
  onDone: () => void | Promise<void>;
}) {
  const toast = useToast();
  const [assessment, setAssessment] = useState<RemovalAssessment | null>(null);
  const [loadErr, setLoadErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);

  useEffect(() => {
    if (!open || !connection) return;
    setAssessment(null); setLoadErr(null); setErr(null);
    let live = true;
    api.get<RemovalAssessment>(`/pms-interfaces/${connection.id}/removal`)
      .then((a) => { if (live) setAssessment(a); })
      .catch((e) => { if (live) setLoadErr(e); });
    return () => { live = false; };
  }, [open, connection]);

  if (!connection) return null;
  const label = connection.display_label || "this connection";

  if (!assessment || assessment.outcome === "BLOCKED" || assessment.outcome === "RETIRED") {
    return (
      <InfoDialog open={open} onOpenChange={onOpenChange} size="md"
        tone={assessment?.outcome === "BLOCKED" || loadErr ? "warning" : "info"}
        title={assessment?.outcome === "BLOCKED" ? `${label} cannot be removed yet`
          : assessment?.outcome === "RETIRED" ? `${label} is already retired` : `Remove ${label}`}
        closeLabel="Close">
        {loadErr ? <p>{pmsErrorText(loadErr)}</p>
          : !assessment ? <Skeleton className="h-24 w-full" />
            : assessment.outcome === "RETIRED" ? <p>It is retired: it keeps its history and cannot be used again.</p>
              : (
                <>
                  <p className="text-muted-foreground">Nothing was changed. Do the following first, then remove it.</p>
                  <ul className="space-y-3">
                    {assessment.blockers.map((b) => (
                      <li key={b.code} className="rounded-md border border-border p-3">
                        <div className="font-medium">{b.title}</div>
                        <p className="mt-1 text-muted-foreground">{b.detail}</p>
                        {b.networks && b.networks.length > 0 && (
                          <ul className="mt-2 flex flex-wrap gap-2">
                            {b.networks.map((n) => <li key={n.id}><Badge tone="info">{n.name}</Badge></li>)}
                          </ul>
                        )}
                        {b.code === "ROUTED" && (
                          <Link href="/pms-routing" className="mt-2 inline-block text-sm font-medium text-primary underline-offset-2 hover:underline">Open PMS routing</Link>
                        )}
                      </li>
                    ))}
                  </ul>
                </>
              )}
      </InfoDialog>
    );
  }

  const deleting = assessment.outcome === "DELETE";
  return (
    <ConfirmDialog
      open={open}
      onOpenChange={onOpenChange}
      title={deleting ? `Delete ${label}?` : `Retire ${label}?`}
      description={deleting
        ? "This connection was only ever set up. It never connected to a PMS and nothing refers to it, so it can be deleted completely."
        : "This connection has been used, so it cannot be deleted. Retiring it hides it and keeps everything it produced."}
      consequences={assessment.consequences}
      confirmLabel={deleting ? "Delete connection" : "Retire connection"}
      confirmVariant="danger"
      requirePassword
      confirmText={label}
      confirmTextLabel="Type the connection's name to confirm"
      requireReason
      reasonOptions={REMOVAL_REASONS}
      busy={busy}
      error={pmsErrorText(err)}
      onConfirm={async ({ reason, password }) => {
        setBusy(true); setErr(null);
        try {
          await api.post(`/pms-interfaces/${connection.id}/removal`, { outcome: assessment.outcome, reason_code: reason, password });
          toast.success(deleting ? "Connection deleted" : "Connection retired", deleting ? undefined : "Its history is kept.");
          onOpenChange(false);
          await onDone();
        } catch (e) {
          setErr(e);
          // The connection changed while the dialog was open: show what removing it means now.
          api.get<RemovalAssessment>(`/pms-interfaces/${connection.id}/removal`).then(setAssessment).catch(() => {});
        } finally { setBusy(false); }
      }}
    />
  );
}

/** Words for the pieces of a removal refusal the generic error text does not cover. */
export function removalCallout(a: RemovalAssessment | null) {
  if (!a || a.stale_routing === undefined || a.stale_routing.length === 0) return null;
  return (
    <Callout tone="info" title="Switched-off networks still point at this connection">
      {a.stale_routing.map((n) => n.name).join(", ")}
    </Callout>
  );
}
