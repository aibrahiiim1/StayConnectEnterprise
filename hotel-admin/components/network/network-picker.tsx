"use client";

import { useEffect, useState } from "react";
import { api, ListResp, GuestNetwork } from "@/lib/api";
import { Checkbox, Input } from "@/components/ui/input";

const split = (v: string) => v.split(",").map((x) => x.trim()).filter(Boolean);

/** Client networks by name, for a rule that names networks. Stored ids whose network is gone stay listed so they can be unticked. If the networks cannot be read, the ids can still be typed. */
export function NetworkPicker({ value, onChange, label, testId }: {
  value: string;
  onChange: (v: string) => void;
  label: string;
  testId?: string;
}) {
  const [networks, setNetworks] = useState<GuestNetwork[] | null | undefined>(undefined);
  useEffect(() => {
    let live = true;
    api.get<ListResp<GuestNetwork>>("/network/guest-networks")
      .then((r) => { if (live) setNetworks(r.data ?? []); })
      .catch(() => { if (live) setNetworks(null); });
    return () => { live = false; };
  }, []);

  if (networks === null) {
    return <Input data-testid={testId} aria-label={label} placeholder="uuid,uuid" value={value} onChange={(e) => onChange(e.target.value)} />;
  }
  if (networks === undefined) return <p className="text-sm text-muted-foreground">Loading client networks…</p>;

  const chosen = split(value);
  const known = new Set(networks.map((n) => n.id));
  const rows = [
    ...networks.map((n) => ({ id: n.id, name: n.name })),
    ...chosen.filter((id) => !known.has(id)).map((id) => ({ id, name: "A network that no longer exists" })),
  ];
  const toggle = (id: string, on: boolean) => onChange((on ? Array.from(new Set([...chosen, id])) : chosen.filter((x) => x !== id)).join(","));

  return (
    <div className="space-y-1.5" role="group" aria-label={label} data-testid={testId}>
      {rows.length === 0 && <p className="text-sm text-muted-foreground">There are no client networks yet.</p>}
      {rows.map((n) => (
        <Checkbox key={n.id} label={n.name} checked={chosen.includes(n.id)} onChange={(e) => toggle(n.id, e.target.checked)} />
      ))}
    </div>
  );
}
