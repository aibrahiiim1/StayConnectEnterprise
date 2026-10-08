"use client";

// ROOM SIGN-IN — what a guest types besides their room number, configured in the Hotel module
// (docs/PRODUCT_TERMINOLOGY.md, "The Hotel module"). Its meaning depends on the PMS stay record, so it lives
// here; the on/off switch for the method stays with the other methods in Client access → Sign-in methods, and
// this screen only reports that state.
//
// THE CHOICES ARE EXACTLY THE SERVER CONTRACT. edged accepts room_any, room_lastname, room_firstname and
// room_reservation (data-plane/cmd/edged/resources_site.go, pmsSignInModes), and "either" only when it is
// already stored. Every option is room number + ONE detail; nothing here offers two details at once, because
// no mode asks for that.
//
// "either" is not offered. It is still honoured if already stored, but it means last-name-or-reservation
// decided by a guess at the shape of what the guest typed, so a surname containing a digit is submitted as a
// reservation number and fails. "Any one of" is not the same thing: the guest fills in ONE box and the SERVER
// compares that value against all three fields, instead of the browser guessing which one was meant.
//
// A CHOICE SAVES IMMEDIATELY, as a merge of the "pms" key only (the server merges per top-level key), and it
// does not switch the method on or off: choosing what guests will type is not the same decision as offering
// room sign-in at all.

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { DoorClosed, ArrowUpRight } from "lucide-react";
import { api, Whoami } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { RoomSignInReadinessCallout, useRoomSignInReadiness } from "@/components/room-sign-in-readiness";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { PageHeader, PageShell } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { OptionCard } from "@/components/ui/data";
import { Skeleton } from "@/components/ui/misc";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";

type PMSMethod = { enabled?: boolean; mode?: string; provider?: string; template_id?: string };
type AuthMethods = { pms?: PMSMethod; [key: string]: unknown };

/** The supported credential choices, one per mode edged accepts. Order: the recommended one first. */
// Not exported: a Next page module may export only its page and route config.
const ROOM_SIGN_IN_OPTIONS: { value: string; label: string; hint: string }[] = [
  {
    value: "room_any",
    label: "Room number + any one of: first name, surname or reservation number (recommended)",
    hint:
      "The guest types their room number, then one more detail in a single box — their first name, their " +
      "surname or their reservation number. They are not asked which one. If the value matches more than one " +
      "guest in that room, sign-in is refused rather than guessing between them.",
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
// What the portal and resolver fall back to when nothing is stored.
const DEFAULT_MODE = "room_lastname";

export default function RoomSignInPage() {
  const toast = useToast();
  // The role is read once and FAILS CLOSED while it loads: an editable choice that appears and then turns
  // read-only is worse than one that arrives read-only and unlocks. edged enforces the real gate either way.
  const [roles, setRoles] = useState<string[] | null>(null);
  useEffect(() => {
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => setRoles([]));
  }, []);
  const writable = roles === null ? false : canWrite("auth-methods", roles);

  const [cfg, setCfg] = useState<AuthMethods | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const { readiness } = useRoomSignInReadiness();

  const load = useCallback(async () => {
    setErr(null);
    try {
      const m = await api.get<AuthMethods>("/auth-methods");
      setCfg(m ?? {});
    } catch (e) {
      setErr(e);
      setCfg({});
    }
  }, []);
  useEffect(() => { load(); }, [load]);

  const pms: PMSMethod = cfg?.pms ?? {};
  const mode = pms.mode || DEFAULT_MODE;
  const modeIsLegacy = mode === LEGACY_EITHER;

  const chooseMode = useCallback(async (v: string) => {
    setBusy(true); setErr(null);
    try {
      const updated = await api.put<AuthMethods>("/auth-methods", { pms: { ...pms, mode: v } });
      setCfg(updated ?? {});
      toast.success("Room sign-in saved", "Guests see the change the next time the sign-in page loads.");
    } catch (e) {
      setErr(e);
      await load(); // never leave a choice showing a state the server did not accept
    } finally { setBusy(false); }
  }, [pms, load, toast]);

  const header = (
    <PageHeader
      icon={<DoorClosed />}
      eyebrow="Hotel"
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
              A choice applies immediately — the next guest to open the sign-in page sees it. It does not switch
              room sign-in on or off; that switch is in Client access → Sign-in methods, with the other methods.
            </p>
          </HelpSection>
          <HelpSection title="The choices">
            <HelpList
              items={[
                <><strong>Any one of</strong> — one box that accepts the first name, the surname or the reservation number. The guest is never asked which; the value is compared against all three. Recommended, because a guest does not have to guess what the hotel wants.</>,
                <><strong>Surname</strong>, <strong>First name</strong> or <strong>Reservation number</strong> — only that detail is accepted.</>,
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
        <div className="grid gap-4" aria-busy="true">
          <span className="sr-only">Loading room sign-in</span>
          {[0, 1].map((i) => <Skeleton key={i} className="h-32" />)}
        </div>
      </PageShell>
    );
  }

  return (
    <PageShell>
      {header}

      {roles !== null && !writable && (
        <ReadOnlyNotice>Your role can see how room sign-in is set up but not change it.</ReadOnlyNotice>
      )}
      <ErrorBanner err={err} className="mb-0" />

      {/* Same rule as before the move: an outage is only worth announcing while the method is offered. */}
      {pms.enabled && <RoomSignInReadinessCallout readiness={readiness} />}

      <Card>
        <CardHeader className="items-start">
          <div className="min-w-0 space-y-1.5">
            <CardTitle>Offered on the Client Portal</CardTitle>
            <CardDescription>
              {pms.enabled
                ? "Guests can sign in with their room number."
                : "Room sign-in is switched off. The choice below is kept and applies once it is switched on."}
            </CardDescription>
          </div>
          <Badge tone={pms.enabled ? "ok" : "default"} dot={!!pms.enabled}>{pms.enabled ? "On" : "Off"}</Badge>
        </CardHeader>
        <CardBody>
          <Link
            href="/sign-in-methods"
            className="inline-flex items-center gap-0.5 text-sm text-primary underline-offset-4 hover:underline"
          >
            Switch it on or off in Client access → Sign-in methods <ArrowUpRight className="size-3.5" aria-hidden />
          </Link>
        </CardBody>
      </Card>

      <Card>
        <CardHeader>
          <div className="min-w-0 space-y-1.5">
            <CardTitle>What the guest types</CardTitle>
            <CardDescription>The room number, plus one detail from their reservation.</CardDescription>
          </div>
          {busy && <span className="text-xs text-muted-foreground" aria-live="polite">Saving…</span>}
        </CardHeader>
        <CardBody>
          <fieldset className="space-y-3">
            <legend className="sr-only">What the guest types, besides the room number</legend>
            {modeIsLegacy && (
              // Shown rather than silently migrated: changing what a stored configuration does is the operator's
              // decision, not this screen's.
              <Callout tone="warning">
                This site currently uses an older setting that accepts a surname or a reservation number and
                guesses which one was typed, so some surnames are rejected. Choosing one of the options below
                replaces it.
              </Callout>
            )}
            <div className="grid gap-2 sm:grid-cols-2">
              {ROOM_SIGN_IN_OPTIONS.map((m) => (
                <OptionCard
                  key={m.value}
                  name="room-sign-in-mode"
                  value={m.value}
                  checked={mode === m.value}
                  disabled={!writable || busy}
                  onChange={chooseMode}
                  title={m.label}
                  description={m.hint}
                />
              ))}
            </div>
            <p className="text-xs text-muted-foreground">
              Which property management system a guest is checked against is decided by their network, in{" "}
              <Link href="/pms-routing" className="text-primary underline underline-offset-2 hover:decoration-2">PMS routing</Link>.
            </p>
          </fieldset>
        </CardBody>
      </Card>
    </PageShell>
  );
}
