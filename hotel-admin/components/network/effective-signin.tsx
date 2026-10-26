"use client";

// What each client network actually offers at sign-in. The switches on this page are site-wide; a network can narrow them, and room sign-in depends on that network's PMS
// routing. This card puts the result in one place so an administrator never has to work it out from four screens.

import { useEffect, useState } from "react";
import Link from "next/link";
import { Check, Minus, TriangleAlert } from "lucide-react";
import { api } from "@/lib/api";
import type { NetworkOverview, OverviewNetwork } from "@/lib/api/network-overview";
import { Badge } from "@/components/ui/badge";
import { SectionCard } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/misc";
import { cn } from "@/lib/utils";
import { serviceStateOf, topologyWords } from "@/lib/network-state";
import { CELL_WORDS, METHOD_LABEL, effectiveMethods, type NetworkOverride, type SiteFacts } from "@/lib/sign-in-effective";

type Loaded = { networks: OverviewNetwork[]; overrides: Record<string, NetworkOverride> };

export function NetworkSignInOverview({ facts }: { facts: SiteFacts }) {
  const [data, setData] = useState<Loaded | null | undefined>(undefined);

  useEffect(() => {
    let live = true;
    Promise.all([
      api.get<NetworkOverview>("/network/overview"),
      api.get<{ overrides: Record<string, NetworkOverride> }>("/network/sign-in-overrides"),
    ]).then(([o, ov]) => {
      if (live) setData({ networks: (o?.networks ?? []).filter((n) => serviceStateOf(n) === "serving"), overrides: ov?.overrides ?? {} });
    }).catch(() => { if (live) setData(null); });
    return () => { live = false; };
  }, []);

  if (data === null) return null;
  return (
    <SectionCard
      title="What each client network offers"
      description="The switches above apply to the whole site. A network can narrow them, and room sign-in follows the network's PMS routing. This is the result for every network that is serving clients."
    >
      {data === undefined ? (
        <Skeleton className="h-24 w-full" />
      ) : data.networks.length === 0 ? (
        <p className="text-sm text-muted-foreground">
          No client network is serving clients. <Link href="/network" className="font-medium text-primary underline-offset-2 hover:underline">Set one up under Client networks</Link>.
        </p>
      ) : (
        <ul className="space-y-3">
          {data.networks.map((n) => {
            const { cells, lead } = effectiveMethods(facts, data.overrides[n.id], n.room_sign_in);
            const topo = topologyWords({ network_type: n.network_type, vlan_id: n.vlan_id, parent_interface: n.parent_interface });
            const offered = cells.filter((c) => c.state === "offered");
            return (
              <li key={n.id} className="rounded-lg border border-border p-3">
                <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
                  <Link href={`/network/${n.id}`} className="font-medium text-foreground underline-offset-2 hover:text-primary hover:underline">{n.name}</Link>
                  <Badge tone="neutral" size="sm" title={topo.detail}>{topo.label}</Badge>
                  <span className="text-sm text-muted-foreground">
                    {offered.length === 0 ? "No sign-in method is offered" : `${offered.length} method${offered.length === 1 ? "" : "s"} offered`}
                    {lead ? ` · leads with ${METHOD_LABEL[lead]}` : ""}
                  </span>
                  <Link href={`/network/${n.id}`} className="ms-auto text-sm text-primary underline-offset-2 hover:underline">Change for this network</Link>
                </div>
                <ul className="mt-2 flex flex-wrap gap-1.5" aria-label={`Sign-in methods on ${n.name}`}>
                  {cells.map((c) => (
                    <li key={c.key}>
                      <span
                        title={`${c.label}: ${CELL_WORDS[c.state].label}. ${c.why}`}
                        className={cn(
                          "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs",
                          c.state === "offered" && "border-success/40 bg-success-subtle text-success-subtle-foreground",
                          c.state === "needs_setup" && "border-warning/40 bg-warning-subtle text-warning-subtle-foreground",
                          (c.state === "off_here" || c.state === "off_site") && "border-border text-muted-foreground line-through decoration-muted-foreground/60",
                        )}
                      >
                        {c.state === "offered" ? <Check className="size-3" aria-hidden /> : c.state === "needs_setup" ? <TriangleAlert className="size-3" aria-hidden /> : <Minus className="size-3" aria-hidden />}
                        <span>{c.label}</span>
                        <span className="sr-only"> — {CELL_WORDS[c.state].label}. {c.why}</span>
                      </span>
                    </li>
                  ))}
                </ul>
                <ul className="mt-2 space-y-0.5 text-xs text-muted-foreground">
                  {cells.filter((c) => c.state === "off_here" || c.state === "needs_setup").map((c) => (
                    <li key={c.key}><strong className="font-medium text-foreground">{c.label}:</strong> {c.why}</li>
                  ))}
                </ul>
              </li>
            );
          })}
        </ul>
      )}
      <dl className="mt-4 grid gap-x-6 gap-y-1 border-t border-border pt-3 text-xs text-muted-foreground sm:grid-cols-2">
        <div><dt className="inline font-medium text-foreground">Site-wide: </dt><dd className="inline">the switches on this page.</dd></div>
        <div><dt className="inline font-medium text-foreground">Network-specific: </dt><dd className="inline">methods switched off for one network, on its Client networks page.</dd></div>
        <div><dt className="inline font-medium text-foreground">PMS-specific: </dt><dd className="inline">room sign-in and which PMS answers it, under <Link href="/pms-routing" className="underline underline-offset-2">PMS routing</Link>.</dd></div>
        <div><dt className="inline font-medium text-foreground">Package eligibility: </dt><dd className="inline">which package a signed-in client gets, under <Link href="/internet-packages" className="underline underline-offset-2">Internet packages</Link>. It never hides a method.</dd></div>
      </dl>
    </SectionCard>
  );
}
