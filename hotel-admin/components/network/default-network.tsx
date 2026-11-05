"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { ChevronRight } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { SectionCard } from "@/components/ui/card";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { useToast } from "@/components/ui/toast";
import { RoutingEditor, usePmsRouting } from "@/components/pms/routing-editor";
import { netErrorWords } from "@/components/network/shared";
import type { LoadReport } from "@/components/network/lifecycle";
import type { ArchivedNetwork, DefaultReadiness } from "@/lib/api/network-archive";

const STATE_TONE = { ok: "ok", attention: "warn", unresolved: "err" } as const;
const STATE_LABEL = { ok: "Ready", attention: "Check", unresolved: "Needs your decision" } as const;

const absent = (e: unknown) => e instanceof ApiError && e.status === 404;

export function DefaultNetworkCard({ reloadKey, onLoaded }: { reloadKey: number; onLoaded?: LoadReport }) {
  const [info, setInfo] = useState<DefaultReadiness | null>(null);
  const { canRoute, interfaces } = usePmsRouting();
  const [editing, setEditing] = useState(false);

  const load = useCallback(() => {
    api.get<DefaultReadiness>("/network/default")
      .then((r) => { setInfo(r); onLoaded?.(true); })
      .catch((e) => { setInfo(null); onLoaded?.(absent(e)); });
  }, [onLoaded]);
  useEffect(load, [load, reloadKey]);

  if (!info || !info.present) return null;
  const room = info.room_sign_in;
  const undecided = room?.state === "UNDECIDED" || room?.state === "NOT_SET";

  const items = info.items ?? [];
  const worst = items.some((i) => i.state === "unresolved") ? "unresolved" : items.some((i) => i.state === "attention") ? "attention" : "ok";
  const notReady = items.filter((i) => i.state !== "ok");
  return (
    <SectionCard title={`Default Network: ${info.name}`} description="The entry point guests meet. Each check says what was found; nothing here is changed for you.">
      <div className="flex flex-wrap items-center gap-2 text-sm">
        <Badge tone={STATE_TONE[worst]}>{worst === "ok" ? "All ready" : `${notReady.length} to check`}</Badge>
        <span className="min-w-0 flex-1 truncate text-muted-foreground">
          {worst === "ok" ? `${items.length} checks passed` : notReady.map((i) => i.label).join(", ")}
        </span>
        {canRoute && room && (
          <Button size="sm" variant={undecided ? "primary" : "secondary"} onClick={() => setEditing(true)}>
            {undecided ? "Configure room sign-in…" : "Room sign-in…"}
          </Button>
        )}
      </div>
      <details className="mt-2 text-sm">
        <summary className="cursor-pointer text-muted-foreground hover:text-foreground">Show all checks</summary>
        <ul className="mt-2 space-y-1.5">
          {items.map((i) => (
            <li key={i.id} className="flex flex-wrap items-baseline gap-2">
              <Badge tone={STATE_TONE[i.state]}>{STATE_LABEL[i.state]}</Badge>
              <strong>{i.label}</strong>
              <span className="text-muted-foreground">{i.detail}</span>
            </li>
          ))}
        </ul>
      </details>

      {(info.packages_pinned_to_other_networks ?? []).length > 0 && (
        <Callout tone="warning" title="Packages limited to other networks">
          <ul className="list-disc ps-5">
            {(info.packages_pinned_to_other_networks ?? []).map((p) => (
              <li key={p.code}>{p.code} (revision {p.revision}) is limited to {p.networks.join(", ")}.</li>
            ))}
          </ul>
          <p className="mt-2">They are not offered on the Default Network and were not widened. To offer one here, publish a new revision that includes this network under <Link className="underline" href="/internet-packages">Internet packages</Link>.</p>
        </Callout>
      )}

      <RoutingEditor open={editing} network={{ id: info.network_id, name: info.name }} interfaces={interfaces}
        onClose={() => setEditing(false)} onDone={() => load()} />
    </SectionCard>
  );
}

export function ArchivedNetworks({ writable, reloadKey, onRestored, onLoaded }: { writable: boolean; reloadKey: number; onRestored: () => void; onLoaded?: LoadReport }) {
  const toast = useToast();
  const [items, setItems] = useState<ArchivedNetwork[]>([]);
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState<string | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    api.get<{ data: ArchivedNetwork[] }>("/network/archive")
      .then((r) => { setItems(r.data ?? []); onLoaded?.(true); })
      .catch((e) => { setItems([]); onLoaded?.(absent(e)); });
  }, [reloadKey, onLoaded]);

  if (items.length === 0) return null;

  async function restore(n: ArchivedNetwork) {
    setBusy(n.id);
    setErr(null);
    try {
      await api.post(`/network/guest-networks/${n.id}/unarchive`);
      toast.success(`${n.name} is back in the main list`, "It is still switched off.");
      onRestored();
    } catch (e) {
      setErr(netErrorWords(e));
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="rounded-lg border border-border bg-card">
      <button type="button" aria-expanded={open} aria-controls="archived-networks" onClick={() => setOpen(!open)}
        className="flex w-full items-center gap-2 px-4 py-3 text-start text-sm font-medium">
        <ChevronRight
          aria-hidden
          className={
            "size-4 transition-transform duration-base ease-onegate motion-reduce:transition-none " +
            (open ? "rotate-90" : "rtl:rotate-180")
          }
        />
        Archived networks ({items.length})
      </button>
      {open && (
        <div
          id="archived-networks"
          className="border-t border-border px-4 py-3 animate-in fade-in-0 slide-in-from-top-1 duration-base ease-onegate motion-reduce:slide-in-from-top-0"
        >
          <p className="mb-3 text-sm text-muted-foreground">Switched off by a confirmed reset. They keep their history, settings and audit trail, and are not served. Nothing was deleted.</p>
          <ErrorBanner err={err} className="mb-2" />
          <ul className="space-y-2">
            {items.map((n) => (
              <li key={n.id} className="flex flex-wrap items-center justify-between gap-2 text-sm">
                <span>
                  <Link className="font-medium hover:underline" href={`/network/${n.id}`}>{n.name}</Link>{" "}
                  <span className="text-muted-foreground">
                    {n.retired_by || "Archived"}, {new Date(n.archived_at).toLocaleDateString()}
                    {n.has_history ? "; has client history" : ""}{n.has_pms_routing ? "; keeps its PMS routing" : ""}
                  </span>
                </span>
                {writable && <Button size="sm" variant="secondary" disabled={busy !== null} onClick={() => void restore(n)}>{busy === n.id ? "Restoring…" : "Restore from archive"}</Button>}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  );
}
