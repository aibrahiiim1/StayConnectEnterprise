"use client";

// Keep appliance health visible while operators configure services that depend on it.

import { useCallback, useEffect, useRef, useState } from "react";
import Link from "next/link";
import { api, EdgeHealth } from "@/lib/api";
import { Tooltip } from "@/components/ui/tooltip";
import { StatusDot } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import { usePoll } from "@/lib/use-poll";

export function ApplianceStatus({ className }: { className?: string }) {
  const [health, setHealth] = useState<EdgeHealth | null>(null);
  const [failed, setFailed] = useState(false);

  const alive = useRef(true);
  const load = useCallback(async () => {
    try {
      const h = await api.get<EdgeHealth>("/health");
      if (alive.current) { setHealth(h); setFailed(false); }
    } catch {
      if (alive.current) setFailed(true);
    }
  }, []);
  useEffect(() => {
    alive.current = true;
    void load();
    return () => { alive.current = false; };
  }, [load]);
  usePoll(() => load(), 30_000);

  if (!health && !failed) {
    return <span className={cn("h-8 w-8 animate-pulse rounded-full bg-surface sm:w-24", className)} />;
  }

  // The worst component status wins so an unreachable database cannot appear healthy.
  const down: string[] = [];
  if (failed) down.push("The admin service is not answering");
  if (health && !health.db) down.push("Site database unreachable");
  if (health && !health.scd) down.push("Session controller unreachable");

  const degraded = health?.status === "degraded";

  const tone = down.length > 0 ? "err" : degraded ? "warn" : "ok";
  const word = down.length > 0 ? "Attention" : degraded ? "Degraded" : "Healthy";

  const worth: string[] = [];
  if (down.length === 0 && health?.status === "degraded") worth.push("The admin service reports itself degraded");

  return (
    <Tooltip
      side="bottom"
      align="end"
      content={
        <div className="space-y-1.5">
          <div className="font-semibold">
            {down.length > 0
              ? "This appliance needs attention"
              : degraded
                ? "Running, with something worth checking"
                : "Everything this appliance needs is running"}
          </div>
          {down.length > 0 && (
            <ul className="list-disc space-y-0.5 ps-4">
              {down.map((d) => <li key={d}>{d}</li>)}
            </ul>
          )}
          {worth.length > 0 && (
            <ul className="list-disc space-y-0.5 ps-4">
              {worth.map((d) => <li key={d}>{d}</li>)}
            </ul>
          )}
          {down.length === 0 && (
            <div className="space-y-0.5">
              <div>Site database: reachable</div>
              <div>Session controller: reachable</div>
            </div>
          )}
          {health?.version && (
            <div className="text-muted-foreground">Admin service {health.version}</div>
          )}
          <div className="text-muted-foreground">Open Diagnostics for the full check list.</div>
        </div>
      }
    >
      <Link
        href="/health"
        aria-label={`Appliance health: ${word}. Open Diagnostics.`}
        className={cn(
          // Keep the status dot visible on narrow screens; its accessible name retains the word.
          "inline-flex h-8 min-w-8 items-center justify-center gap-1.5 rounded-full border border-border bg-card px-2.5 text-xs font-semibold",
          "transition-colors hover:bg-accent focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
          tone === "err" && "border-destructive/30 text-destructive-subtle-foreground",
          tone === "warn" && "border-warning/30 text-warning-subtle-foreground",
          className,
        )}
      >
        <StatusDot tone={tone} />
        <span className="hidden sm:inline">{word}</span>
      </Link>
    </Tooltip>
  );
}
