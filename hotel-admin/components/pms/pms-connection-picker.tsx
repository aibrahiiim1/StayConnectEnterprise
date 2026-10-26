"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { Checkbox, Input } from "@/components/ui/input";

const split = (v: string) => v.split(",").map((x) => x.trim()).filter(Boolean);

type Conn = { id: string; display_label: string; lifecycle_state: string; published?: boolean };

/** PMS connections by name, for a rule that names connections. A stored id whose connection is gone or retired stays listed so it can be unticked. If the connections cannot be read, ids can still be typed. */
export function PmsConnectionPicker({ value, onChange, label, testId }: {
  value: string;
  onChange: (v: string) => void;
  label: string;
  testId?: string;
}) {
  const [conns, setConns] = useState<Conn[] | null | undefined>(undefined);
  useEffect(() => {
    let live = true;
    api.get<{ interfaces?: Conn[] }>("/pms-interfaces?include=retired")
      .then((r) => { if (live) setConns(r?.interfaces ?? []); })
      .catch(() => { if (live) setConns(null); });
    return () => { live = false; };
  }, []);

  if (conns === null) {
    return <Input data-testid={testId} aria-label={label} placeholder="connection id, connection id" value={value} onChange={(e) => onChange(e.target.value)} />;
  }
  if (conns === undefined) return <p className="text-sm text-muted-foreground">Loading PMS connections…</p>;

  const chosen = split(value);
  const known = new Set(conns.map((c) => c.id));
  const rows = [
    ...conns
      .filter((c) => c.published || c.lifecycle_state !== "AUTH_DISABLED" || chosen.includes(c.id))
      .map((c) => ({ id: c.id, name: c.lifecycle_state === "DECOMMISSIONED" ? `${c.display_label} (retired)` : c.display_label || "Unnamed connection" })),
    ...chosen.filter((id) => !known.has(id)).map((id) => ({ id, name: "A connection that no longer exists" })),
  ];
  const toggle = (id: string, on: boolean) => onChange((on ? Array.from(new Set([...chosen, id])) : chosen.filter((x) => x !== id)).join(","));

  return (
    <div className="space-y-1.5" role="group" aria-label={label} data-testid={testId}>
      {rows.length === 0 && <p className="text-sm text-muted-foreground">There are no PMS connections yet.</p>}
      {rows.map((c) => (
        <Checkbox key={c.id} label={c.name} checked={chosen.includes(c.id)} onChange={(e) => toggle(c.id, e.target.checked)} />
      ))}
    </div>
  );
}
