"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { AlertTriangle, ArrowUpRight, RefreshCw } from "lucide-react";
import { api, ApiError } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { RoomSignInReadinessCallout, useRoomSignInReadiness } from "@/components/room-sign-in-readiness";
import { Card, CardBody, SectionCard } from "@/components/ui/card";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { EmptyState } from "@/components/ui/empty-state";
import { PageHeader, PageShell } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { OptionCard } from "@/components/ui/data";
import { Skeleton } from "@/components/ui/misc";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { OneGateLoader } from "@/components/brand";
import { useOperatorRoles } from "@/lib/whoami-context";
import { cn } from "@/lib/utils";

type PMSMethod = { enabled?: boolean; mode?: string; provider?: string; template_id?: string };
type AuthMethods = { pms?: PMSMethod; [key: string]: unknown };

const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";

const ROOM_SIGN_IN_OPTIONS: { value: string; label: string; hint: string; recommended?: boolean }[] = [
  {
    value: "room_any",
    label: "Room number + first name, surname or reservation number",
    hint: "One box for any of the three. A value matching more than one guest is refused, never guessed.",
    recommended: true,
  },
  {
    value: "room_lastname",
    label: "Room number + surname",
    hint: "The guest types their room number and the surname on the reservation.",
  },
  {
    value: "room_firstname",
    label: "Room number + first name",
    hint: "The guest types their room number and the first name on the reservation.",
  },
  {
    value: "room_reservation",
    label: "Room number + reservation number",
    hint: "The guest types their room number and the reservation (confirmation) number.",
  },
];
const LEGACY_EITHER = "either";
const KNOWN = new Set([...ROOM_SIGN_IN_OPTIONS.map((o) => o.value), LEGACY_EITHER]);

function saveError(e: unknown): unknown {
  if (e instanceof ApiError && e.code === "validation") {
    return new Error("That choice could not be saved. Choose one of the options shown.");
  }
  return e;
}

export default function RoomSignInPage() {
  const roles = useOperatorRoles();
  const writable = roles === null ? false : canWrite("auth-methods", roles);

  const [cfg, setCfg] = useState<AuthMethods | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [loading, setLoading] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [saving, setSaving] = useState<string | null>(null);
  const [saved, setSaved] = useState<{ mode: string; previous: string | null } | null>(null);
  const { readiness } = useRoomSignInReadiness();

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const m = await api.get<AuthMethods>("/auth-methods");
      setCfg(m ?? {});
      setLoadFailed(false);
    } catch {
      setLoadFailed(true);
    } finally {
      setLoading(false);
    }
  }, []);
  useEffect(() => { void load(); }, [load]);

  const pms: PMSMethod = cfg?.pms ?? {};
  const stored = pms.mode ?? "";
  const mode = stored || LEGACY_EITHER;
  const unknownMode = stored !== "" && !KNOWN.has(stored);

  const save = useCallback(async (v: string, undo = false) => {
    setSaving(v); setErr(null);
    try {
      const fresh = await api.get<AuthMethods>("/auth-methods");
      const before = fresh?.pms?.mode ?? "";
      const updated = await api.put<AuthMethods>("/auth-methods", { pms: { ...(fresh?.pms ?? {}), mode: v } });
      setCfg(updated ?? {});
      setSaved(undo ? null : { mode: v, previous: KNOWN.has(before) ? before : null });
    } catch (e) {
      setErr(saveError(e));
      setSaved(null);
      await load();
    } finally { setSaving(null); }
  }, [load]);

  const header = (
    <PageHeader
      title="Room sign-in"
      description="What a guest types besides their room number when signing in to the Wi-Fi."
      help={
        <>
          <HelpSection title="What this page sets">
            <p>
              With room sign-in, the guest types their room number and one detail from their reservation. OneGate
              checks the pair against the property management system (PMS) for the network the guest is on. This
              page decides which detail that is.
            </p>
            <p>
              A choice applies immediately: the next guest to open the sign-in page sees it. It does not switch
              room sign-in on or off; that switch is in Client access → Sign-in methods, with the other methods.
            </p>
          </HelpSection>
          <HelpSection title="The choices">
            <HelpList
              items={[
                <><strong>Any one of</strong>: one box that accepts the first name, the surname or the reservation number. The guest is never asked which; the value is compared against all three. Recommended, because a guest does not have to guess what the hotel wants.</>,
                <><strong>Surname</strong>, <strong>First name</strong> or <strong>Reservation number</strong>: only that detail is accepted.</>,
              ]}
            />
            <p>Every choice is the room number plus one detail. No choice asks for two details at once.</p>
          </HelpSection>
          <HelpSection title="Which PMS is checked">
            <p>
              The guest never chooses a system, and no reservation details are shown back to them. Which PMS a
              network is checked against is set in PMS routing; if room sign-in works on one Wi-Fi network but not
              another, check PMS routing and the PMS connection first.
            </p>
          </HelpSection>
        </>
      }
    />
  );

  if (cfg === null) {
    return (
      <PageShell>
        {header}
        {loadFailed ? (
          <Card className={FADE}>
            <EmptyState
              icon={<AlertTriangle />}
              title="Room sign-in could not be loaded"
              hint="Nothing has been changed."
              action={
                <Button variant="secondary" disabled={loading} onClick={() => void load()}>
                  <RefreshCw className={cn(loading && "animate-spin motion-reduce:animate-none")} /> Try again
                </Button>
              }
            />
          </Card>
        ) : (
          <div className="grid gap-4" aria-busy="true">
            <span className="sr-only">Loading room sign-in</span>
            {[0, 1].map((i) => <Skeleton key={i} className="h-32" />)}
          </div>
        )}
      </PageShell>
    );
  }

  const savedLabel = saved ? ROOM_SIGN_IN_OPTIONS.find((o) => o.value === saved.mode)?.label : undefined;

  return (
    <PageShell>
      {header}

      {roles !== null && !writable && (
        <ReadOnlyNotice>Your role can see how room sign-in is set up but not change it.</ReadOnlyNotice>
      )}
      {err ? (
        <div key={String((err as { message?: string })?.message ?? err)} className={FADE}>
          <ErrorBanner err={err} className="mb-0" />
        </div>
      ) : null}

      {pms.enabled && <RoomSignInReadinessCallout readiness={readiness} />}

      <Card className={FADE}>
        <CardBody className="flex flex-wrap items-center justify-between gap-3 py-3">
          <div className="min-w-0 space-y-0.5">
            <div className="flex flex-wrap items-center gap-2">
              <span className="text-emphasis">Offered on the Client Portal</span>
              <StatusBadge tone={pms.enabled ? "ok" : "default"}>{pms.enabled ? "On" : "Off"}</StatusBadge>
            </div>
            <p className="text-xs text-muted-foreground">
              {pms.enabled
                ? "Guests can sign in with their room number."
                : "Room sign-in is switched off. The choice below is kept and applies once it is switched on."}
            </p>
          </div>
          <Link
            href="/sign-in-methods"
            className="inline-flex items-center gap-0.5 text-xs font-medium text-primary hover:underline"
          >
            Switch it on or off in Client access → Sign-in methods <ArrowUpRight className="size-3.5" aria-hidden />
          </Link>
        </CardBody>
      </Card>

      <SectionCard
        title="What the guest types"

        className={FADE}
      >
        <fieldset className="space-y-3">
          <legend className="sr-only">What the guest types, besides the room number</legend>
          {mode === LEGACY_EITHER && (
            <Callout tone="warning" className={FADE}>
              {stored === ""
                ? "No choice has been saved for this site, so guests get an older setting: it accepts a surname or a reservation number and guesses which one was typed, so some surnames are rejected. Choosing one of the options below replaces it."
                : "This site currently uses an older setting that accepts a surname or a reservation number and guesses which one was typed, so some surnames are rejected. Choosing one of the options below replaces it."}
            </Callout>
          )}
          {unknownMode && (
            <Callout tone="warning" className={FADE}>
              This site uses a setting this console does not recognise, so none of the options below is shown as
              selected. Choosing one replaces it.
            </Callout>
          )}
          <div className="grid gap-2 sm:grid-cols-2">
            {ROOM_SIGN_IN_OPTIONS.map((m) => (
              <OptionCard
                key={m.value}
                name="room-sign-in-mode"
                value={m.value}
                checked={mode === m.value}
                disabled={!writable || saving !== null}
                onChange={(v) => void save(v)}
                title={m.label}
                badge={m.recommended ? <Badge tone="info" size="sm">Recommended</Badge> : undefined}
                description={m.hint}
              >
                {saving === m.value && (
                  <span role="status" className={cn("flex items-center gap-1.5 ps-1 text-xs text-muted-foreground", FADE)}>
                    <OneGateLoader className="size-3.5" /> Saving
                  </span>
                )}
              </OptionCard>
            ))}
          </div>
          {saved && savedLabel && (
            <div key={saved.mode} role="status" className={cn("flex flex-wrap items-center gap-2 text-sm", FADE)}>
              <span>
                Saved: <strong className="font-medium">{savedLabel}</strong>. Guests see it the next time the sign-in
                page loads.
              </span>
              {saved.previous && saved.previous !== saved.mode && writable && (
                <Button size="sm" variant="ghost" disabled={saving !== null} onClick={() => void save(saved.previous!, true)}>
                  Undo
                </Button>
              )}
            </div>
          )}
        </fieldset>
      </SectionCard>
    </PageShell>
  );
}
