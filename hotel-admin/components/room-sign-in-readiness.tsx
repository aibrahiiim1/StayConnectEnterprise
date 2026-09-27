"use client";

// ROOM SIGN-IN READINESS — whether Room sign-in can actually serve a guest right now, which is not the same
// question as whether it is switched on.
//
// Shared by two screens: Hotel → Room sign-in carries the full callout (which guest networks are affected and
// why), and Client Portal → Sign-in methods carries a one-line warning beside the switch, so an operator
// turning the method on during an outage is not left believing it works. The data is loaded here once, by the
// same rule for both, so the two screens cannot disagree about whether there is an outage.
//
// Guest authentication requires a live PMS feed for the network a guest is on, so an interface that is
// disconnected or still loading its guest list refuses every guest — with the uniform message, which looks
// exactly like a wrong surname. The per-network decision itself lives in lib/pms-availability.ts.

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { ArrowUpRight } from "lucide-react";
import { api, ListResp, PmsInterface, PmsInterfaceHealth, PmsGuestNetworkRoute } from "@/lib/api";
import { roomSignInReadiness, type RoomSignInReadiness } from "@/lib/pms-availability";
import { Callout } from "@/components/ui/error-banner";

// The reasons read as sentence fragments so they can be listed after a network name; the single-outage copy
// puts one at the start of a sentence instead.
const capitalise = (t: string) => t.charAt(0).toUpperCase() + t.slice(1);

/**
 * Loads the PMS interfaces, their health and the network routing, and returns the readiness verdict.
 * Readiness is ADVISORY: any failure leaves it "unknown", which shows nothing, and never blocks the screen.
 */
export function useRoomSignInReadiness(): { readiness: RoomSignInReadiness; reload: () => Promise<void> } {
  const [ifaces, setIfaces] = useState<PmsInterface[] | null>(null);
  const [health, setHealth] = useState<PmsInterfaceHealth[] | null>(null);
  const [routes, setRoutes] = useState<PmsGuestNetworkRoute[] | null>(null);

  const reload = useCallback(async () => {
    try {
      const [list, routing] = await Promise.all([
        api.get<ListResp<PmsInterface>>("/pms-interfaces"),
        api.get<{ routes: PmsGuestNetworkRoute[] }>("/pms-routing"),
      ]);
      const all = list.data ?? [];
      // Health is read for ACTIVE interfaces only: the others cannot serve a guest whatever their axes say,
      // and asking is a request per interface.
      const healths = await Promise.all(
        all.filter((i) => i.lifecycle_state === "ACTIVE").map((i) =>
          api.get<{ health: PmsInterfaceHealth }>(`/pms-interfaces/${i.id}/health`)
            .then((h) => h.health)
            .catch(() => null)),
      );
      setIfaces(all);
      setHealth(healths.filter(Boolean) as PmsInterfaceHealth[]);
      setRoutes(routing?.routes ?? []);
    } catch { /* readiness unknown; the notice is simply not shown */ }
  }, []);
  useEffect(() => { reload(); }, [reload]);

  const readiness = useMemo(() => roomSignInReadiness(ifaces, health, routes), [ifaces, health, routes]);
  return { readiness, reload };
}

/** True when there is something the operator must be told. */
export function roomSignInImpaired(r: RoomSignInReadiness): boolean {
  return r.state === "down" || r.state === "partial";
}

/**
 * The full outage callout: which guest networks cannot sign in by room, and why. Renders nothing when Room
 * sign-in is working or its readiness is unknown.
 *
 * WHY THIS IS SEPARATE FROM THE SETTING. The method can be switched on and correctly configured while the live
 * PMS feed it depends on is missing, and there is nothing on the settings screen to fix. PARTIAL IS ITS OWN
 * CASE: when one guest network is affected and another is fine, "not working" would be false for half the
 * property, so the networks are named.
 */
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
        // Neutral, and never counted as an outage. A health read that failed is absence of evidence, and one
        // of those networks may be perfectly fine.
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
