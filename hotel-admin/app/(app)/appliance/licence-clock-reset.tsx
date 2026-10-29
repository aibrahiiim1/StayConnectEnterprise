"use client";

// "The clock was wrong" (AD-17): the appliance remembers the latest time it has seen so a licence cannot be
// extended by setting the clock back. If the clock was far ahead once, that mark stays ahead and the licence looks
// expired. This asks OneGate Central for its time and moves the mark to it. The appliance refuses while its own
// clock still disagrees with Central; the refusal is shown as it is worded. Shown only with licence write.

import { useState } from "react";
import { api } from "@/lib/api";
import { SectionCard } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/dialog";
import { Callout } from "@/components/ui/error-banner";
import { REASON_MAX_CHARS, REASON_MIN_CHARS } from "@/lib/reason";

type ClockResetResult = { status?: string; previous_high_water?: string; high_water?: string; state?: string };

function stamp(s?: string): string {
  if (!s) return "—";
  const d = new Date(s);
  return Number.isNaN(d.getTime()) ? s : d.toLocaleString();
}

export function LicenceClockReset({ onDone }: { onDone?: () => void }) {
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<unknown>(null);
  const [result, setResult] = useState<ClockResetResult | null>(null);

  async function run({ reason }: { reason: string }) {
    setBusy(true);
    setError(null);
    try {
      const r = await api.post<ClockResetResult>("/license/clock-reset", { reason });
      setResult(r);
      setOpen(false);
      onDone?.();
    } catch (e) {
      // The refusal text (for example "the local clock still disagrees with Central") is the answer; keep the dialog open.
      setError(e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <SectionCard
      title="Licence clock"
      description="Use only if the appliance's clock was wrong and the licence now shows as not yet valid or expired."
      bodyClassName="space-y-3"
    >
      <p className="text-caption text-muted-foreground">
        To stop a licence being extended by turning the clock back, the appliance remembers the latest time it has
        seen. After a clock that was set too far ahead, that remembered time stays ahead. Correct the appliance&apos;s
        clock first; then this asks OneGate Central for its time and moves the remembered time to it. It is refused
        while the clock still disagrees with Central, and it is recorded with your reason.
      </p>
      {result && (
        <Callout tone="success" title="Licence clock reset">
          Remembered time moved from <strong>{stamp(result.previous_high_water)}</strong> to{" "}
          <strong>{stamp(result.high_water)}</strong>.
          {result.state ? <> Licence state now: <strong>{result.state}</strong>.</> : null}
        </Callout>
      )}
      <Button variant="secondary" size="sm" onClick={() => { setError(null); setOpen(true); }}>
        Reset licence clock…
      </Button>
      <ConfirmDialog
        open={open}
        onOpenChange={(v) => !v && !busy && setOpen(false)}
        title="Reset the licence clock?"
        description="The appliance asks OneGate Central for the current time and moves its remembered time to it."
        consequences={[
          "Do this only after the appliance's own clock is correct.",
          "It is refused if the appliance's clock still disagrees with Central.",
        ]}
        confirmLabel="Reset licence clock"
        requireReason
        reasonMinLength={REASON_MIN_CHARS}
        reasonMaxLength={REASON_MAX_CHARS}
        reasonPlaceholder="For example: the clock was set a year ahead during setup and has been corrected"
        busy={busy}
        error={error}
        onConfirm={run}
      />
    </SectionCard>
  );
}
