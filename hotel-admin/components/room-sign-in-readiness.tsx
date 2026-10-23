"use client";

// Enabled room sign-in still fails uniformly when its network lacks a live PMS feed.

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { ArrowUpRight } from "lucide-react";
import { api, PmsInterface, PmsInterfaceHealth, PmsGuestNetworkRoute } from "@/lib/api";
import { roomSignInReadiness, type RoomSignInReadiness } from "@/lib/pms-availability";
import { Callout } from "@/components/ui/error-banner";

const capitalise = (t: string) => t.charAt(0).toUpperCase() + t.slice(1);

/** Readiness is advisory; failed checks leave it unknown and never block the screen. */
export function useRoomSignInReadiness(enabled = true): { readiness: RoomSignInReadiness; reload: () => Promise<void> } {
  const [ifaces, setIfaces] = useState<PmsInterface[] | null>(null);
  const [health, setHealth] = useState<PmsInterfaceHealth[] | null>(null);
  const [routes, setRoutes] = useState<PmsGuestNetworkRoute[] | null>(null);

  const reload = useCallback(async () => {
    // Sites without Hospitality never call PMS surfaces.
    if (!enabled) return;
    try {
      const [list, routing] = await Promise.all([
        api.get<{ interfaces?: PmsInterface[] }>("/pms-interfaces"),
        api.get<{ routes: PmsGuestNetworkRoute[] }>("/pms-routing"),
      ]);
      // Read the interfaces field; treating the response as .data falsely reports healthy PMS as unavailable.
      const all = list.interfaces ?? [];
      // Query health only for active interfaces because inactive ones cannot serve guests.
      const healths = await Promise.all(
        all.filter((i) => i.lifecycle_state === "ACTIVE").map((i) =>
          api.get<{ health: PmsInterfaceHealth }>(`/pms-interfaces/${i.id}/health`)
            .then((h) => h.health)
            .catch(() => null)),
      );
      setIfaces(all);
      setHealth(healths.filter(Boolean) as PmsInterfaceHealth[]);
      setRoutes(routing?.routes ?? []);
    } catch { /* Failed checks leave advisory readiness unknown. */ }
  }, [enabled]);
  useEffect(() => { reload(); }, [reload]);

  const readiness = useMemo(() => roomSignInReadiness(ifaces, health, routes), [ifaces, health, routes]);
  return { readiness, reload };
}

export function roomSignInImpaired(r: RoomSignInReadiness): boolean {
  return r.state === "down" || r.state === "partial";
}

/** Name affected networks so a partial PMS outage is not reported as a property-wide failure. */
export function RoomSignInReadinessCallout({ readiness }: { readiness: RoomSignInReadiness }) {
  if (readiness.state !== "down" && readiness.state !== "partial") return null;
  return (
    <Callout
      tone="warning"
      title={readiness.state === "down"
        ? "Room sign-in is not working at the moment"
        : "Room sign-in is not working on some client networks"}
    >
      {readiness.state === "down" ? (
        <p>
          {capitalise(readiness.reason)}. Guests cannot sign in with their room number until the property
          management system is connected to OneGate again; they can still use any other sign-in method that is
          switched on. Nothing here needs changing — this setting is kept as it is and starts working again on
          its own once the connection returns.
        </p>
      ) : (
        <>
          <p>
            Guests on the networks below cannot sign in with their room number. Everywhere else is working
            normally. Nothing here needs changing — each one starts working again on its own once its property
            management system is connected.
          </p>
          <ul className="mt-1 list-disc space-y-0.5 ps-4">
            {readiness.affected.map((a) => (
              <li key={`${a.guestNetwork}-${a.pmsInterface}`}>
                <span className="font-medium">{a.guestNetwork}</span> (via {a.pmsInterface}) — {a.reason}
              </li>
            ))}
          </ul>
        </>
      )}
      {readiness.unchecked.length > 0 && (
        // Unknown health is not evidence of an outage.
        <p className="mt-1">Readiness could not be checked for {readiness.unchecked.join(", ")}.</p>
      )}
      <p className="mt-1">
        <Link href="/pms-interfaces" className="inline-flex items-center gap-0.5 font-medium underline">
          Check the PMS connection <ArrowUpRight className="size-3.5" aria-hidden />
        </Link>
      </p>
    </Callout>
  );
}
