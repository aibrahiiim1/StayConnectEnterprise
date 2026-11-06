"use client";

import { AlertTriangle, RefreshCw } from "lucide-react";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout } from "@/components/ui/error-banner";

export const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";

export function LoadFailure({ what, onRetry, busy = false }: { what: string; onRetry: () => void; busy?: boolean }) {
  return (
    <div role="alert" className={FADE}>
      <EmptyState
        size="sm"
        icon={<AlertTriangle />}
        title={`${what} could not be loaded`}
        hint="Check the appliance is reachable, then try again. Nothing has been changed."
        action={
          <Button variant="secondary" size="sm" disabled={busy} onClick={onRetry}>
            <RefreshCw /> {busy ? "Trying again…" : "Try again"}
          </Button>
        }
      />
    </div>
  );
}

export function StaleNotice({ what }: { what: string }) {
  return (
    <div className={FADE}>
      <Callout tone="warning">{`${what} could not be refreshed. What is shown may be out of date.`}</Callout>
    </div>
  );
}
