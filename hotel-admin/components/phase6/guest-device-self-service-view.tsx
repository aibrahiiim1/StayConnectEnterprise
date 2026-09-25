"use client";

// GUEST DEVICES — the per-appliance device self-service setting (Phase 6, DARK).
//
// THE HARD PART OF THIS SCREEN IS NOT THE SWITCH, IT IS THE TWO STATES BEHIND IT.
//
//   The PRODUCT SETTING is this property's decision: does this hotel offer guests the ability to remove
//   their own offline devices? It lives in the appliance's own database, it is what this screen changes, and
//   it survives restarts and works with no Central Control Plane.
//
//   The RELEASE GATE is whether the capability is included in this build at all. It is not a hotel
//   decision, this screen cannot change it, and while it is off no guest can reach the feature no matter
//   what the setting says.
//
// An operator who turns the setting on, sees it confirmed, and walks away believing guests can now manage
// their devices has been misled by the product -- so the two states are shown as two separate tiles, and the
// screen says in plain words what the combination currently means. No text here may suggest that switching
// the setting on installs anything.
//
// canAct decides whether the CONTROL is offered, from the same role matrix edged enforces. A read-only
// operator sees the state and no switch; if they forged the request anyway, edged would refuse it -- the
// hiding is courtesy, the refusal is the boundary.

import { useCallback, useEffect, useState } from "react";
import { CheckCircle2, CircleSlash, PackageCheck, Smartphone } from "lucide-react";
import { api } from "@/lib/api";
import { cn } from "@/lib/utils";
import { PageHeader, PageShell } from "@/components/ui/page";
import { Card, CardBody, CardFooter } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Skeleton } from "@/components/ui/misc";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";

type Setting = {
  enabled: boolean;
  changed?: boolean;
  phase_gate_enabled: boolean;
};

function StateTile({
  label,
  value,
  on,
  testId,
  icon,
  children,
}: {
  label: string;
  value: string;
  on: boolean;
  testId: string;
  icon: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <Card className="h-full">
      <CardBody className="space-y-2">
        <div className="flex items-start justify-between gap-3">
          <div className="text-xs font-medium text-muted-foreground">{label}</div>
          <span
            className={cn(
              "inline-flex size-7 shrink-0 items-center justify-center rounded-md [&_svg]:size-4",
              on ? "bg-success-subtle text-success" : "bg-surface text-muted-foreground",
            )}
            aria-hidden
          >
            {icon}
          </span>
        </div>
        <div className="text-metric" data-testid={testId}>{value}</div>
        <p className="text-caption text-muted-foreground">{children}</p>
      </CardBody>
    </Card>
  );
}

/** `rolesKnown` is false while the route shell is still reading the operator's roles, so the read-only notice
 *  does not flash at an operator who can act. */
export function GuestDeviceSelfServiceView({ canAct, rolesKnown = true }: { canAct: boolean; rolesKnown?: boolean }) {
  const toast = useToast();
  const [state, setState] = useState<Setting | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [pending, setPending] = useState<boolean | null>(null);
  const [reason, setReason] = useState("");

  const load = useCallback(() => {
    api
      .get<Setting>("/guest-device-self-service/")
      .then((s) => {
        setState(s);
        setError(null);
      })
      .catch((e) => setError(String((e as { message?: string })?.message ?? e)));
  }, []);

  useEffect(load, [load]);

  async function save(next: boolean) {
    setBusy(true);
    setError(null);
    try {
      const res = await api.put<Setting>("/guest-device-self-service/", {
        enabled: next,
        reason: reason.trim(),
      });
      setState(res);
      if (res.changed) {
        toast.success(
          next
            ? "Saved. This property now offers guest device self-service."
            : "Saved. This property no longer offers guest device self-service.",
        );
      } else {
        toast.toast({ tone: "info", title: "No change — the setting was already in that state." });
      }
      setPending(null);
      setReason("");
    } catch (e: unknown) {
      setError(String((e as { message?: string })?.message ?? e));
    } finally {
      setBusy(false);
    }
  }

  const header = (
    <PageHeader
      eyebrow="Guests"
      title="Guest devices"
      icon={<Smartphone />}
      description="Whether a signed-in guest may remove one of their own devices that is not connected, to free its place for another. A device that is online is never removable, and a guest only ever sees their own devices."
    />
  );

  if (!state) {
    return (
      <PageShell width="narrow">
        {header}
        {error ? (
          <ErrorBanner err={error} />
        ) : (
          <div className="grid gap-4 sm:grid-cols-2" aria-busy="true">
            <Skeleton className="h-32" />
            <Skeleton className="h-32" />
            <Skeleton className="col-span-full h-12" />
          </div>
        )}
      </PageShell>
    );
  }

  const on = state.enabled;
  const deployed = state.phase_gate_enabled;

  return (
    <PageShell width="narrow">
      {header}
      {rolesKnown && !canAct && (
        <ReadOnlyNotice>
          <span data-testid="readonly-note">
            Your role can see this setting but not change it. Ask a site administrator or the hotel IT manager.
          </span>
        </ReadOnlyNotice>
      )}

      <ErrorBanner err={error} />

      {/* THE TWO STATES, SIDE BY SIDE AND NEVER MERGED INTO ONE INDICATOR. */}
      <div className="grid gap-4 sm:grid-cols-2">
        <StateTile
          label="This property offers it"
          value={on ? "On" : "Off"}
          on={on}
          testId="setting-state"
          icon={on ? <CheckCircle2 /> : <CircleSlash />}
        >
          Your setting, stored on this appliance. It applies as soon as it is saved and keeps working if the
          connection to Velonet Central is unavailable.
        </StateTile>
        <StateTile
          label="Available in this release"
          value={deployed ? "Yes" : "Not yet"}
          on={deployed}
          testId="gate-state"
          icon={<PackageCheck />}
        >
          Whether the guest feature is included in the software running on this appliance. This is not a hotel
          setting and is not changed from this screen.
        </StateTile>
      </div>

      {/* WHAT THE COMBINATION ACTUALLY MEANS RIGHT NOW, in one sentence, so nobody has to work it out. */}
      <div role="note" data-testid="effect">
        <Callout tone="neutral" icon={deployed && on ? <CheckCircle2 className="text-success" /> : <CircleSlash />}>
          {deployed
            ? on
              ? "Guests can use device self-service on this property now."
              : "Guests cannot use device self-service here, because this property has it switched off."
            : on
              ? "Guests cannot use device self-service yet: it is switched on for this property, and it is not included in the software running on this appliance. Saving this setting does not install it."
              : "Guests cannot use device self-service: it is not included in the software running on this appliance, and this property has it switched off."}
        </Callout>
      </div>

      {canAct && (
        <Card>
          {pending === null ? (
            <CardBody className="flex flex-wrap items-center justify-between gap-3">
              <div className="min-w-0">
                <div className="text-emphasis">Offer device self-service at this property</div>
                <p className="text-caption text-muted-foreground">
                  Currently {on ? "on" : "off"}. You will be asked to confirm, with an optional reason.
                </p>
              </div>
              <Button variant={on ? "secondary" : "primary"} onClick={() => setPending(!on)}>
                {on ? "Switch off" : "Switch on"}
              </Button>
            </CardBody>
          ) : (
            // The INLINE confirmation: this is one setting on its own screen, so the question stays where the
            // operator's eyes already are rather than moving into an overlay.
            <form
              onSubmit={(e) => { e.preventDefault(); void save(pending); }}
              aria-label="Confirm the change"
            >
              <CardBody className="space-y-3">
                <div className="text-emphasis">
                  {pending
                    ? "Offer guest device self-service at this property?"
                    : "Stop offering guest device self-service at this property?"}
                </div>
                <p className="text-sm text-muted-foreground">
                  {pending
                    ? "Guests will be able to remove their own devices that are not connected. Devices that are online stay put."
                    : "Guests will no longer see or be able to remove their devices. Nothing already connected is disconnected by this change."}
                </p>
                <Field label="Reason (optional)" hint="Recorded in the change history.">
                  <Input
                    type="text"
                    value={reason}
                    maxLength={500}
                    onChange={(e) => setReason(e.target.value)}
                    placeholder="Why are you making this change?"
                  />
                </Field>
              </CardBody>
              <CardFooter className="justify-end">
                <Button
                  type="button"
                  variant="ghost"
                  disabled={busy}
                  onClick={() => {
                    setPending(null);
                    setReason("");
                  }}
                >
                  Cancel
                </Button>
                <Button type="submit" disabled={busy}>
                  {busy ? "Saving…" : pending ? "Switch on" : "Switch off"}
                </Button>
              </CardFooter>
            </form>
          )}
        </Card>
      )}
    </PageShell>
  );
}
