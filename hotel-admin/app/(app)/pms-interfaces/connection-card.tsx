"use client";

// ONE PMS CONNECTION AT A GLANCE.
//
// The card answers, without opening anything: which system is this, is it switched on, can guests sign in
// through it right now, when was the PMS last heard from, is the guest list current, which Wi-Fi networks use
// it, what configuration is live, and is there anything that needs a person. Readiness itself is the server's
// verdict (room_auth_ready) put into words by describePmsReadiness; this card does not re-derive it.

import Link from "next/link";
import type { PmsInterfaceHealth } from "@/lib/api";
import { Card } from "@/components/ui/card";
import { Badge, StatusDot } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn, formatDate, formatRelative } from "@/lib/utils";
import { describePmsReadiness } from "@/lib/health-words";
import {
  type PmsConnection, type PmsProvider,
  LIFECYCLE_WORDS, ROOM_AUTH_WORDS, SYNC_WORDS, isSyncing, lastCommunication, safeHost, verificationWords, wordsFor,
} from "@/lib/api/pms-connections";
import { Check, Minus, X } from "lucide-react";

/** A letter tile standing in for a provider logo — no vendor artwork is shipped. */
export function ProviderTile({ label, bare, className }: { label: string; bare?: boolean; className?: string }) {
  const letter = (label.trim()[0] ?? "?").toUpperCase();
  if (bare) return <span className="text-sm font-semibold">{letter}</span>;
  return (
    <span
      aria-hidden
      className={cn(
        "inline-flex size-10 shrink-0 items-center justify-center rounded-lg border border-primary/15 bg-primary-subtle text-base font-semibold text-primary-subtle-foreground",
        className,
      )}
    >
      {letter}
    </span>
  );
}

export function When({ at, prefix }: { at?: string | null; prefix?: string }) {
  if (!at) return <span className="text-muted-foreground">Never</span>;
  return (
    <time dateTime={at} title={formatDate(at)}>
      {prefix}{formatRelative(at)}
    </time>
  );
}

export function lifecycleTone(state: string): "ok" | "warn" | "default" | "info" {
  return state === "ACTIVE" ? "ok" : state === "DRAINING" ? "warn" : "default";
}

export function providerLabel(i: PmsConnection, p: PmsProvider | null): string {
  return i.provider_label || p?.label || "Unrecognised system";
}

type Check = { label: string; state: boolean | null };

/** The four things that have to be true for room sign-in, each stated on its own. */
export function readinessChecks(h?: PmsInterfaceHealth | null): Check[] {
  const known = (v?: string) => (v ? v !== "UNKNOWN" : false);
  return [
    { label: "Connected", state: h ? h.transport_status === "CONNECTED" : null },
    { label: "Receiving updates", state: h ? (known(h.continuity_status) ? h.continuity_status === "CONTINUOUS" : false) : null },
    { label: "Guest list in sync", state: h ? h.sync_status === "IN_SYNC" : null },
    { label: "Room sign-in ready", state: h && h.room_auth_ready !== undefined ? h.room_auth_ready : null },
  ];
}

export function ChecksRow({ health }: { health?: PmsInterfaceHealth | null }) {
  return (
    <ul className="flex flex-wrap gap-1.5" aria-label="Readiness checks">
      {readinessChecks(health).map((c) => (
        <li
          key={c.label}
          className={cn(
            "inline-flex items-center gap-1 rounded-md border px-1.5 py-0.5 text-2xs font-medium",
            c.state === true && "border-success/25 bg-success-subtle text-success-subtle-foreground",
            c.state === false && "border-warning/30 bg-warning-subtle text-warning-subtle-foreground",
            c.state === null && "border-border bg-surface text-muted-foreground",
          )}
        >
          {c.state === true ? <Check className="size-3" aria-hidden /> : c.state === false
            ? <X className="size-3" aria-hidden /> : <Minus className="size-3" aria-hidden />}
          <span>{c.label}</span>
          <span className="sr-only">{c.state === true ? ": yes" : c.state === false ? ": no" : ": not known"}</span>
        </li>
      ))}
    </ul>
  );
}

export function ConnectionCard({
  iface, health, healthLoaded, provider, networks, onOpen,
}: {
  iface: PmsConnection;
  health?: PmsInterfaceHealth;
  healthLoaded: boolean;
  provider: PmsProvider | null;
  /** Names of the guest networks resolved against this connection; null when routing could not be read. */
  networks: string[] | null;
  onOpen: () => void;
}) {
  const h = health;
  const active = iface.lifecycle_state === "ACTIVE";
  const readiness = describePmsReadiness({
    transport: h?.transport_status, sync: h?.sync_status, roomAuthReady: h?.room_auth_ready, inHouse: h?.in_house_stays,
  });
  const syncing = isSyncing(h);
  const heard = lastCommunication(h);
  const host = safeHost(iface.endpoint);
  const verification = verificationWords(iface.verification ?? provider?.verification);

  const warnings: React.ReactNode[] = [];
  if (active && h && !h.room_auth_ready && h.room_auth_reason) {
    warnings.push(ROOM_AUTH_WORDS[h.room_auth_reason] ?? "Room sign-in is not available through this connection.");
  }
  if ((h?.review_events ?? 0) > 0) {
    warnings.push(
      <Link key="review" href="/stay-events" className="underline underline-offset-2">
        {h!.review_events.toLocaleString()} PMS message{h!.review_events === 1 ? " needs" : "s need"} a decision
      </Link>,
    );
  }
  if (networks && networks.length === 0 && iface.lifecycle_state !== "DECOMMISSIONED") {
    warnings.push("No guest network uses this connection, so no guest is ever checked against it.");
  }

  const tone = !active ? "default" : !healthLoaded || !h ? "default" : readiness.tone;

  return (
    <Card className="flex flex-col">
      <div className="flex items-start gap-3 p-4 pb-3">
        <ProviderTile label={providerLabel(iface, provider)} />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h3 className="truncate text-sm font-semibold">{iface.display_label || "Unnamed connection"}</h3>
            <Badge tone={lifecycleTone(iface.lifecycle_state)} dot>
              {wordsFor(LIFECYCLE_WORDS, iface.lifecycle_state, "State not recognised")}
            </Badge>
          </div>
          <p className="truncate text-xs text-muted-foreground">
            {providerLabel(iface, provider)}
            {host ? ` · ${host}` : ""}
          </p>
        </div>
      </div>

      <div className="space-y-3 px-4 pb-4">
        <div className="flex items-start gap-2">
          <StatusDot tone={tone === "ok" ? "ok" : tone === "err" ? "err" : tone === "warn" ? "warn" : "default"} className="mt-1.5" />
          <div className="min-w-0">
            <div className="text-sm font-medium">
              {!active
                ? iface.lifecycle_state === "DRAINING"
                  ? "Winding down — no new room sign-ins"
                  : iface.lifecycle_state === "DECOMMISSIONED" ? "Retired" : "Room sign-in paused"
                : !healthLoaded ? "Checking…" : !h ? "Status could not be read" : readiness.headline}
            </div>
            {active && syncing && (
              <div className="text-xs text-muted-foreground">
                Loading the guest list now — {(h?.sync_records_received ?? 0).toLocaleString()} records received
              </div>
            )}
          </div>
        </div>

        {h && <ChecksRow health={h} />}

        <dl className="grid grid-cols-2 gap-x-4 gap-y-2 text-xs">
          <div className="min-w-0">
            <dt className="text-muted-foreground">Last heard from</dt>
            <dd className="font-medium"><When at={heard} /></dd>
          </div>
          <div className="min-w-0">
            <dt className="text-muted-foreground">Guest list</dt>
            <dd className="font-medium">
              {syncing ? "Refreshing" : wordsFor(SYNC_WORDS, h?.sync_status)}
              {h?.last_complete_sync_at && (
                <span className="font-normal text-muted-foreground"> · full <When at={h.last_complete_sync_at} /></span>
              )}
            </dd>
          </div>
          <div className="min-w-0">
            <dt className="text-muted-foreground">Guests in house</dt>
            <dd className="font-medium tabular">{h ? (h.in_house_stays ?? 0).toLocaleString() : "—"}</dd>
          </div>
          <div className="min-w-0">
            <dt className="text-muted-foreground">Live configuration</dt>
            <dd className="font-medium">
              {iface.published && iface.current_revision_no ? `Version ${iface.current_revision_no}` : "None published"}
            </dd>
          </div>
          <div className="col-span-2 min-w-0">
            <dt className="text-muted-foreground">Guest networks</dt>
            <dd className="truncate font-medium" title={networks?.join(", ")}>
              {networks === null ? "—" : networks.length === 0 ? "None" : networks.join(", ")}
            </dd>
          </div>
        </dl>

        {warnings.length > 0 && (
          <ul className="space-y-1 rounded-md border border-warning/30 bg-warning-subtle px-3 py-2 text-xs text-warning-subtle-foreground">
            {warnings.map((w, i) => <li key={i}>{w}</li>)}
          </ul>
        )}
      </div>

      <div className="mt-auto flex flex-wrap items-center justify-between gap-2 border-t border-border px-4 py-2.5">
        <Badge tone={verification.tone} className="whitespace-normal">{verification.label}</Badge>
        <Button size="sm" variant="secondary" onClick={onOpen}>Manage</Button>
      </div>
    </Card>
  );
}
