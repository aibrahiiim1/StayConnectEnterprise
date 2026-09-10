"use client";

// THE APPLIANCE PILL — health where the operator already is.
//
// `GET /health` was reachable from exactly two screens. Everything the admin does depends on the site database
// and the session controller being up, so an operator configuring packages while scd is down was working
// against a surface that could not take effect, with no indication anywhere on the page.
//
// The pill is deliberately terse — a dot and a word — and the tooltip carries the detail, including the
// outbox figures in sentences rather than as two bare integers.

import { useEffect, useState } from "react";
import Link from "next/link";
import { api, EdgeHealth } from "@/lib/api";
import { Tooltip } from "@/components/ui/tooltip";
import { StatusDot } from "@/components/ui/badge";
import { cn } from "@/lib/utils";
import { describeOutbox } from "@/lib/health-words";

export function ApplianceStatus({ className }: { className?: string }) {
  const [health, setHealth] = useState<EdgeHealth | null>(null);
  const [failed, setFailed] = useState(false);

  useEffect(() => {
    let alive = true;
    const load = async () => {
      try {
        const h = await api.get<EdgeHealth>("/health");
        if (alive) { setHealth(h); setFailed(false); }
      } catch {
        if (alive) setFailed(true);
      }
    };
    void load();
    const iv = setInterval(load, 30_000);
    return () => { alive = false; clearInterval(iv); };
  }, []);

  if (!health && !failed) {
    return <span className={cn("hidden h-6 w-24 animate-pulse rounded-md bg-surface sm:block", className)} />;
  }

  // THE WORST FACT WINS. A pill that says "ok" because the top-level status field says so, while the database
  // is unreachable, is worse than no pill: it actively tells the operator not to look.
  const down: string[] = [];
  if (failed) down.push("The admin service is not answering");
  if (health && !health.db) down.push("Site database unreachable");
  if (health && !health.scd) down.push("Session controller unreachable");

  const outbox = health?.sync_outbox;
  const outboxWords = describeOutbox(outbox);
  const degraded = health?.status === "degraded" || outboxWords.tone === "warn" || outboxWords.tone === "err";

  const tone = down.length > 0 ? "err" : degraded ? "warn" : "ok";
  const word = down.length > 0 ? "Attention" : degraded ? "Degraded" : "Healthy";

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
            <ul className="list-disc space-y-0.5 pl-4">
              {down.map((d) => <li key={d}>{d}</li>)}
            </ul>
          )}
          {down.length === 0 && (
            <div className="space-y-0.5">
              <div>Site database: reachable</div>
              <div>Session controller: reachable</div>
            </div>
          )}
          <div className="border-t border-border pt-1.5">{outboxWords.summary}</div>
          {health?.version && (
            <div className="text-muted-foreground">Admin service {health.version}</div>
          )}
          <div className="text-muted-foreground">Open Diagnostics for the full check list.</div>
        </div>
      }
    >
      <Link
        href="/health"
        className={cn(
          "hidden items-center gap-1.5 rounded-md border border-border bg-card px-2 py-1 text-xs font-medium",
          "transition-colors hover:bg-surface sm:inline-flex",
          tone === "err" && "border-destructive/30 text-destructive-subtle-foreground",
          tone === "warn" && "border-warning/30 text-warning-subtle-foreground",
          className,
        )}
      >
        <StatusDot tone={tone} />
        <span>{word}</span>
      </Link>
    </Tooltip>
  );
}
