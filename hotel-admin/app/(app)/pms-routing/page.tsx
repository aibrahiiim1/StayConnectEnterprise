"use client";

// Phase 3 (DARK) — GUEST NETWORK → PMS ROUTING.
//
// Which PMS a device is checked against is decided by the guest network it is on. Getting that wrong does not
// fail loudly: it resolves a guest against a different property's occupancy, and the guest simply cannot get
// online while everything reports healthy.
//
// The page is read-only on purpose. The mapping follows the network topology, so it is changed where the
// networks themselves are configured — not here, where an operator would be editing it without the context
// that makes it correct.
//
// The unmapped list is the point of the page. A guest network with no mapping resolves against nothing, and
// that absence is invisible in any list that only shows what exists.

import { useEffect, useState } from "react";
import { api, PmsGuestNetworkRoute } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";

type Unmapped = { guest_network_id: string; guest_network_name?: string };

export default function PMSRoutingPage() {
  const [routes, setRoutes] = useState<PmsGuestNetworkRoute[] | null>(null);
  const [unmapped, setUnmapped] = useState<Unmapped[]>([]);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    (async () => {
      try {
        const r = await api.get<{ routes: PmsGuestNetworkRoute[]; unmapped_guest_networks: Unmapped[] }>(
          "/pms-routing",
        );
        if (!alive) return;
        setRoutes(r.routes ?? []);
        setUnmapped(r.unmapped_guest_networks ?? []);
      } catch (e: any) {
        if (!alive) return;
        setErr(e?.message ?? "Failed to load PMS routing");
        setRoutes([]);
      }
    })();
    return () => {
      alive = false;
    };
  }, []);

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-semibold">Guest network routing</h1>
      <p className="text-sm text-muted">
        Which PMS interface each guest network is resolved against. Change these where the networks are
        configured.
      </p>

      {err && (
        <p role="alert" className="text-sm text-red-600">
          {err}
        </p>
      )}

      <Card>
        <CardBody>
          <h2 className="text-lg font-medium">Mapped</h2>
          {routes === null ? (
            <p className="text-sm">Loading…</p>
          ) : routes.length === 0 ? (
            <EmptyState
              title="No guest network is mapped"
              hint="No device on any guest network is resolved against a PMS interface."
            />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Guest network</TH>
                  <TH>PMS interface</TH>
                  <TH>Mode</TH>
                  <TH>Default</TH>
                </TR>
              </THead>
              <tbody>
                {routes.map((r) => (
                  <TR key={r.guest_network_id}>
                    <TD>{r.guest_network_name || r.guest_network_id}</TD>
                    <TD>{r.pms_interface_label || r.pms_interface_id}</TD>
                    <TD>{r.routing_mode.replace(/_/g, " ").toLowerCase()}</TD>
                    <TD>{r.is_default ? <Badge tone="info">default</Badge> : "—"}</TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      <Card>
        <CardBody>
          <h2 className="text-lg font-medium">Not mapped</h2>
          {unmapped.length === 0 ? (
            <p className="mt-1 text-sm">Every guest network is mapped to a PMS interface.</p>
          ) : (
            <>
              <p className="mt-1 text-sm">
                Devices on {unmapped.length === 1 ? "this network" : "these networks"} are resolved against no
                PMS interface, so no guest on {unmapped.length === 1 ? "it" : "them"} can be verified.
              </p>
              <ul className="mt-2 text-sm">
                {unmapped.map((u) => (
                  <li key={u.guest_network_id}>
                    <Badge tone="warn">unmapped</Badge>{" "}
                    {u.guest_network_name || u.guest_network_id}
                  </li>
                ))}
              </ul>
            </>
          )}
        </CardBody>
      </Card>
    </div>
  );
}
