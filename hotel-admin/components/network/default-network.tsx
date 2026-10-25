"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { ChevronDown, ChevronRight } from "lucide-react";
import { api } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { SectionCard } from "@/components/ui/card";
import { DialogForm } from "@/components/ui/dialog";
import { Callout } from "@/components/ui/error-banner";
import { useToast } from "@/components/ui/toast";
import { errMsg } from "@/lib/utils";
import type { ArchivedNetwork, DefaultReadiness, PmsCandidate } from "@/lib/api/network-archive";

const STATE_TONE = { ok: "ok", attention: "warn", unresolved: "err" } as const;
const STATE_LABEL = { ok: "Ready", attention: "Check", unresolved: "Needs your decision" } as const;

/** What stands between the Default Network and being the guest-service entry point. It describes; it changes nothing by itself. */
export function DefaultNetworkCard({ writable, reloadKey }: { writable: boolean; reloadKey: number }) {
  const toast = useToast();
  const [info, setInfo] = useState<DefaultReadiness | null>(null);
  const [choose, setChoose] = useState<PmsCandidate | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);

  const load = useCallback(() => {
    api.get<DefaultReadiness>("/network/default").then(setInfo).catch(() => setInfo(null));
  }, []);
  useEffect(load, [load, reloadKey]);

  if (!info || !info.present) return null;

  async function route() {
    if (!choose || !info) return;
    setBusy(true);
    setErr(null);
    try {
      await api.put(`/pms-routing/${info.network_id}`, { pms_interface_id: choose.pms_interface_id });
      toast.success("Room sign-in is routed to the Default Network", `It now checks ${choose.label}. The routes of the archived networks were not changed.`);
      setChoose(null);
      load();
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <SectionCard title={`Default Network: ${info.name}`} description="The entry point guests meet. Each check says what was found; nothing here is changed for you.">
      <ul className="space-y-2 text-sm">
        {info.items.map((i) => (
          <li key={i.id} className="flex flex-wrap items-baseline gap-2">
            <Badge tone={STATE_TONE[i.state]}>{STATE_LABEL[i.state]}</Badge>
            <strong>{i.label}</strong>
            <span className="text-muted-foreground">{i.detail}</span>
          </li>
        ))}
      </ul>

      {info.pms_candidates.length > 0 && (
        <Callout tone="warning" title="Room sign-in needs a decision">
          <p className="mb-2">A PMS interface keeps its routes to the archived networks. None was copied here. Choosing one routes room sign-in on this network to that interface only.</p>
          <ul className="space-y-2">
            {info.pms_candidates.map((c) => (
              <li key={c.pms_interface_id} className="flex flex-wrap items-center justify-between gap-2">
                <span><strong>{c.label}</strong> — routed to {c.routed_from.join(", ")}</span>
                {writable && <Button size="sm" variant="secondary" onClick={() => { setErr(null); setChoose(c); }}>Route to this network…</Button>}
              </li>
            ))}
          </ul>
        </Callout>
      )}

      {info.packages_pinned_to_other_networks.length > 0 && (
        <Callout tone="warning" title="Packages limited to other networks">
          <ul className="list-disc pl-5">
            {info.packages_pinned_to_other_networks.map((p) => (
              <li key={p.code}>{p.code} (revision {p.revision}) is limited to {p.networks.join(", ")}.</li>
            ))}
          </ul>
          <p className="mt-2">They are not offered on the Default Network and were not widened. To offer one here, publish a new revision that includes this network under <Link className="underline" href="/internet-packages">Internet packages</Link>.</p>
        </Callout>
      )}

      <DialogForm open={choose !== null} onOpenChange={(v) => !v && setChoose(null)} title="Route room sign-in to the Default Network"
        description="Check what this does before you confirm." submitLabel="Route to the Default Network" busy={busy} error={err} onSubmit={route}>
        {choose && (
          <div className="space-y-3 text-sm">
            <p>{choose.preview}</p>
            <ul className="list-disc pl-5 text-muted-foreground">
              <li>No PMS traffic is started or stopped, and nothing is posted.</li>
              <li>Package eligibility is not changed.</li>
              <li>You can undo it under PMS routing.</li>
            </ul>
          </div>
        )}
      </DialogForm>
    </SectionCard>
  );
}

/** Networks a confirmed reset retired. Collapsed by default; the count is always visible. */
export function ArchivedNetworks({ writable, reloadKey, onRestored }: { writable: boolean; reloadKey: number; onRestored: () => void }) {
  const toast = useToast();
  const [items, setItems] = useState<ArchivedNetwork[]>([]);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    api.get<{ data: ArchivedNetwork[] }>("/network/archive").then((r) => setItems(r.data ?? [])).catch(() => setItems([]));
  }, [reloadKey]);

  if (items.length === 0) return null;

  async function restore(n: ArchivedNetwork) {
    setBusy(n.id);
    setErr(null);
    try {
      await api.post(`/network/guest-networks/${n.id}/unarchive`);
      toast.success(`${n.name} is back in the main list`, "It is still switched off.");
      onRestored();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="rounded-lg border border-border bg-card">
      <button type="button" aria-expanded={open} aria-controls="archived-networks" onClick={() => setOpen(!open)}
        className="flex w-full items-center gap-2 px-4 py-3 text-start text-sm font-medium">
        {open ? <ChevronDown className="size-4" aria-hidden /> : <ChevronRight className="size-4 rtl:rotate-180" aria-hidden />}
        Archived networks ({items.length})
      </button>
      {open && (
        <div id="archived-networks" className="border-t border-border px-4 py-3">
          <p className="mb-3 text-sm text-muted-foreground">Switched off by a confirmed reset. They keep their history, settings and audit trail, and are not served. Nothing was deleted.</p>
          {err && <p className="mb-2 text-sm text-danger">{err}</p>}
          <ul className="space-y-2">
            {items.map((n) => (
              <li key={n.id} className="flex flex-wrap items-center justify-between gap-2 text-sm">
                <span>
                  <Link className="font-medium hover:underline" href={`/network/${n.id}`}>{n.name}</Link>{" "}
                  <span className="text-muted-foreground">
                    — {n.retired_by}, {new Date(n.archived_at).toLocaleDateString()}
                    {n.has_history ? "; has client history" : ""}{n.has_pms_routing ? "; keeps its PMS routing" : ""}
                  </span>
                </span>
                {writable && <Button size="sm" variant="secondary" disabled={busy === n.id} onClick={() => void restore(n)}>Restore from archive</Button>}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
