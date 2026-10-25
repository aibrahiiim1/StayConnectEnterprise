"use client";

// What a topology change touches before anything is applied: the clients, room sign-in routing, sign-in methods and packages of each affected network, and what the
// switch, access points and hypervisor must provide. Read-only; it describes consequences and changes nothing.

import { Callout } from "@/components/ui/error-banner";
import { POLICY_WORDS, STATE_WORDS, type NetworkConsequence } from "@/lib/api/pms-routing";
import { Badge } from "@/components/ui/badge";

export function ImpactPanel({ affected, external }: { affected?: NetworkConsequence[] | null; external?: string[] | null }) {
  const nets = affected ?? [];
  const ext = external ?? [];
  if (nets.length === 0 && ext.length === 0) return null;
  return (
    <div className="space-y-3" data-testid="impact-panel">
      {nets.length > 0 && (
        <div>
          <strong>What this affects</strong>
          <ul className="mt-1 space-y-2">
            {nets.map((n) => (
              <li key={n.network_id} className="rounded-md border border-border p-2.5 text-xs">
                <div className="flex flex-wrap items-center gap-2 text-sm">
                  <span className="font-medium">{n.name}</span>
                  <span className="text-muted-foreground">{n.active_clients} client{n.active_clients === 1 ? "" : "s"} online · {n.active_sessions} session{n.active_sessions === 1 ? "" : "s"}{n.live_room_entitlements > 0 ? ` (${n.live_room_entitlements} with room access)` : ""}</span>
                </div>
                {n.room_sign_in && (
                  <div className="mt-1 flex flex-wrap items-center gap-2">
                    <span>Room sign-in:</span>
                    <Badge tone={STATE_WORDS[n.room_sign_in.state].tone} size="sm">{STATE_WORDS[n.room_sign_in.state].label}</Badge>
                    <span className="text-muted-foreground">{POLICY_WORDS[n.room_sign_in.policy]}{n.room_sign_in.interfaces.length > 0 ? ` — ${n.room_sign_in.interfaces.join(", ")}` : ""}</span>
                  </div>
                )}
                {n.sign_in_methods_switched_off.length > 0 && <div className="mt-1 text-muted-foreground">Sign-in methods switched off here: {n.sign_in_methods_switched_off.join(", ")}</div>}
                {n.notes.length > 0 && <ul className="mt-1 list-disc ps-4 text-muted-foreground">{n.notes.map((t) => <li key={t}>{t}</li>)}</ul>}
              </li>
            ))}
          </ul>
        </div>
      )}
      {ext.length > 0 && (
        <Callout tone="info" title="Outside OneGate: switch, access points and hypervisor">
          <ul className="list-disc pl-5">{ext.map((t) => <li key={t}>{t}</li>)}</ul>
        </Callout>
      )}
    </div>
  );
}
