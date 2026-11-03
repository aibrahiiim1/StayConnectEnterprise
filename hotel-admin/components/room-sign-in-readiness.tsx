"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { usePathname } from "next/navigation";
import { ArrowUpRight } from "lucide-react";
import { api } from "@/lib/api";
import type { RoomSignInAssessment } from "@/lib/api/pms-routing";
import { onlyUndecided, readinessFromAssessment, type RoomSignInReadiness } from "@/lib/pms-availability";
import { Notice } from "@/components/ui/error-banner";
import { InfoHint } from "@/components/ui/quiet";

export function useRoomSignInReadiness(enabled = true): {
  readiness: RoomSignInReadiness;
  assessment: RoomSignInAssessment | null;
  reload: () => Promise<void>;
} {
  const [assessment, setAssessment] = useState<RoomSignInAssessment | null>(null);

  const reload = useCallback(async () => {
    if (!enabled) return;
    try {
      const a = await api.get<RoomSignInAssessment>("/pms-routing/readiness");
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

function RoutingLinks({ withConnections }: { withConnections: boolean }) {
  const here = usePathname();
  return (
    <>
      {here !== "/pms-routing" && (
        <Link href="/pms-routing" className="inline-flex items-center gap-0.5 font-medium underline">
          PMS routing <ArrowUpRight className="size-3.5" aria-hidden />
        </Link>
      )}
      {withConnections && here !== "/pms-interfaces" && (
        <Link href="/pms-interfaces" className="inline-flex items-center gap-0.5 font-medium underline">
          PMS connections <ArrowUpRight className="size-3.5" aria-hidden />
        </Link>
      )}
    </>
  );
}

export function RoomSignInReadinessCallout({ readiness }: { readiness: RoomSignInReadiness }) {
  if (readiness.state !== "down" && readiness.state !== "partial") return null;
  if (onlyUndecided(readiness)) {
    return (
      <Notice tone="warning" title="Room sign-in needs a decision" actions={<RoutingLinks withConnections={false} />}>
        Nobody has chosen how room sign-in works on {readiness.affected.map((a) => a.guestNetwork).join(", ")}.
      </Notice>
    );
  }
  return (
    <Notice
      tone={readiness.state === "down" ? "danger" : "warning"}
      title={readiness.state === "down"
        ? "Room sign-in is not working on any network that offers it"
        : "Room sign-in is not working on some client networks"}
      actions={<RoutingLinks withConnections />}
    >
      {readiness.affected.map((a, i) => (
        <span key={a.networkId ?? `${a.guestNetwork}-${a.pmsInterface}`}>
          {i > 0 && " · "}
          <strong>{a.guestNetwork}</strong>: {a.reason}
        </span>
      ))}
      {readiness.unchecked.length > 0 && <> · Not checked: {readiness.unchecked.join(", ")}</>}
      <InfoHint>Other sign-in methods and networks are not affected. Each warning clears once its cause is corrected.</InfoHint>
    </Notice>
  );
}