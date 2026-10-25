"use client";

// Room sign-in is a fact about a client network and the PMS interfaces it is routed to. The server assesses it once (GET /pms-routing/readiness); every screen reads that
// verdict through this hook, so the dashboard, PMS connections, PMS routing, the Default Network and Client networks cannot disagree.

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { ArrowUpRight } from "lucide-react";
import { api } from "@/lib/api";
import type { RoomSignInAssessment } from "@/lib/api/pms-routing";
import { readinessFromAssessment, type RoomSignInReadiness } from "@/lib/pms-availability";
import { Callout } from "@/components/ui/error-banner";

/** Readiness is advisory; a failed read leaves it unknown and never blocks the screen. */
export function useRoomSignInReadiness(enabled = true): {
  readiness: RoomSignInReadiness;
  assessment: RoomSignInAssessment | null;
  reload: () => Promise<void>;
} {
  const [assessment, setAssessment] = useState<RoomSignInAssessment | null>(null);

  const reload = useCallback(async () => {
    // Sites without Hospitality never call PMS surfaces.
    if (!enabled) return;
    try {
      const a = await api.get<RoomSignInAssessment>("/pms-routing/readiness");
      // A response that is not an assessment is an unknown, never a verdict.
      setAssessment(a && Array.isArray(a.networks) && Array.isArray(a.interfaces) ? a : null);
    } catch {
      setAssessment(null);
    }
  }, [enabled]);
  useEffect(() => { void reload(); }, [reload]);

  const readiness = useMemo(() => readinessFromAssessment(assessment), [assessment]);
  return { readiness, assessment, reload };
}

export function roomSignInImpaired(r: RoomSignInReadiness): boolean {
  return r.state === "down" || r.state === "partial";
}

/** Names the affected networks so a partial problem is not reported as a property-wide failure. */
export function RoomSignInReadinessCallout({ readiness }: { readiness: RoomSignInReadiness }) {
  if (readiness.state !== "down" && readiness.state !== "partial") return null;
  return (
    <Callout
      tone={readiness.state === "down" ? "danger" : "warning"}
      title={readiness.state === "down"
        ? "Room sign-in is not working on any network that offers it"
        : "Room sign-in is not working on some client networks"}
    >
      <p>
        Guests on the networks below cannot sign in with their room number. Other sign-in methods and the rest of the networks are
        not affected. Each warning clears by itself once its cause is corrected.
      </p>
      <ul className="mt-1 list-disc space-y-0.5 ps-4">
        {readiness.affected.map((a) => (
          <li key={a.networkId ?? `${a.guestNetwork}-${a.pmsInterface}`}>
            <span className="font-medium">{a.guestNetwork}</span> ({a.pmsInterface}) — {a.reason}
          </li>
        ))}
      </ul>
      {readiness.unchecked.length > 0 && (
        // Unknown health is not evidence of an outage.
        <p className="mt-1">Readiness could not be checked for {readiness.unchecked.join(", ")}.</p>
      )}
      <p className="mt-1 flex flex-wrap gap-x-4">
        <Link href="/pms-routing" className="inline-flex items-center gap-0.5 font-medium underline">
          Open PMS routing <ArrowUpRight className="size-3.5" aria-hidden />
        </Link>
        <Link href="/pms-interfaces" className="inline-flex items-center gap-0.5 font-medium underline">
          Check the PMS connections <ArrowUpRight className="size-3.5" aria-hidden />
        </Link>
      </p>
    </Callout>
  );
}
