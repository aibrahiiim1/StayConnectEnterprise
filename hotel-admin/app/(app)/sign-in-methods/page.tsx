"use client";

// SIGN-IN METHODS — which ways a guest may prove who they are on the portal.
//
// This surface was missing entirely. `/edge/v1/auth-methods` and its RBAC resource existed, the portal read
// the result on every render, and there was no screen: enabling PMS room sign-in meant editing a JSON column
// by hand. An operator could commission a PMS end to end and still have no way to offer it to a guest.
//
// THESE ARE RUNTIME SETTINGS. The portal fetches /api/auth-methods when the landing page renders, so a change
// saved here takes effect on the next guest page load — no rebuild, no redeploy, no environment variable.
// That is the whole point of the screen: which methods a hotel offers is an operating decision that changes
// with the season, not a deployment gate.
//
// EACH SWITCH SAVES IMMEDIATELY. There is no page-level Save: a switch that looks flipped but is not yet
// applied is the one thing this screen must never show, so every change is a PATCH of that one key and a
// refused change re-reads the server's state.
//
// A ROLE THAT CANNOT CHANGE THEM sees each method's state as a word, and no switch at all. The server refuses
// the write regardless; offering a control that 403s on click is what this replaces.
//
// WHAT IS DELIBERATELY NOT HERE: any PMS interface, provider or connector selector. Which PMS a guest is
// resolved against is decided by the guest network they are on, in Network routing. Putting it here would
// give an operator two places to decide one thing, and would imply the guest picks a PMS — which they never
// do.

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { api, ListResp, PmsInterface, PmsInterfaceHealth, PmsGuestNetworkRoute, Whoami } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { GuestSignInProtectionCard } from "@/components/guest-signin-protection";
import { roomSignInReadiness } from "@/lib/pms-availability";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { PageHeader, PageShell } from "@/components/ui/page";
import { OptionCard } from "@/components/ui/data";
import { Skeleton, Switch } from "@/components/ui/misc";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { cn } from "@/lib/utils";
import { Ticket, Hotel, KeyRound, Mail, MessageSquare, Users, ArrowUpRight, LogIn } from "lucide-react";

// The auth_methods document. Only the keys this screen owns are typed; everything else is preserved
// untouched by the server's merge, so an unknown future method cannot be deleted by saving here.
type Method = { enabled?: boolean };
type PMSMethod = { enabled?: boolean; mode?: string; provider?: string; template_id?: string };
type AuthMethods = {
  voucher?: Method;
  guest_account?: Method;
  email?: Method;
  sms?: Method;
  social?: Record<string, Method>;
  pms?: PMSMethod;
};

type NotifyProvider = { channel: string; kind: string; enabled: boolean };
type SocialProvider = { provider: string; enabled: boolean };

// PMS verification values. Each maps to a wire mode the resolver genuinely matches against a field the PMS
// populated — there is no fuzzy matching and nothing is inferred.
//
// "either" is absent on purpose. It is still honoured if already stored, but it means
// last-name-or-reservation decided by a guess at the shape of what the guest typed, so a surname containing
// a digit is submitted as a reservation number and fails. An operator picking a mode here chooses one
// explicit identifier instead — or "Any of the three", which is not the same thing: the guest still fills in
// ONE box, and the SERVER compares that value against all three fields rather than the browser guessing which
// one was meant.
const PMS_MODES: { value: string; label: string; hint: string }[] = [
  {
    value: "room_any",
    label: "Any of the three (recommended)",
    hint:
      "Room number plus one box that accepts the first name, the surname, or the reservation number. " +
      "The guest is not asked which one they are entering. If the value matches more than one guest in " +
      "that room, sign-in is refused rather than guessing between them.",
  },
  { value: "room_lastname", label: "Last name (surname)", hint: "Room number plus the surname on the reservation." },
  { value: "room_firstname", label: "First name", hint: "Room number plus the first name on the reservation." },
  { value: "room_reservation", label: "Reservation number", hint: "Room number plus the reservation / confirmation number." },
];
const LEGACY_EITHER = "either";

const SOCIAL_LABELS: Record<string, string> = { google: "Google", apple: "Apple", facebook: "Facebook", microsoft: "Microsoft" };

// The reasons read as sentence fragments so they can be listed after a network name; the single-outage copy
// puts one at the start of a sentence instead.
const capitalise = (t: string) => t.charAt(0).toUpperCase() + t.slice(1);

export default function SignInMethodsPage() {
  const toast = useToast();
  // The role is read once and FAILS CLOSED while it loads: an editable field that appears and then turns
  // read-only is worse than one that arrives read-only and unlocks. edged enforces the real gate either way.
  const [roles, setRoles] = useState<string[] | null>(null);
  useEffect(() => {
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => setRoles([]));
  }, []);
  const writable = roles === null ? false : canWrite("auth-methods", roles);
  const mayChangeProtection = roles === null ? false : canWrite("guest-signin-protection", roles);

  const [cfg, setCfg] = useState<AuthMethods | null>(null);
  const [notify, setNotify] = useState<NotifyProvider[]>([]);
  const [social, setSocial] = useState<SocialProvider[]>([]);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [pmsIfaces, setPmsIfaces] = useState<PmsInterface[] | null>(null);
  const [pmsHealth, setPmsHealth] = useState<PmsInterfaceHealth[] | null>(null);
  const [pmsRoutes, setPmsRoutes] = useState<PmsGuestNetworkRoute[] | null>(null);

  const load = useCallback(async () => {
    setErr(null);
    try {
      const m = await api.get<AuthMethods>("/auth-methods");
      setCfg(m ?? {});
    } catch (e) {
      setErr(e);
      setCfg({});
    }
    // Provider readiness is advisory: a failure here must not stop the methods themselves being managed.
    try {
      const n = await api.get<ListResp<NotifyProvider>>("/notification-providers");
      setNotify(n.data ?? []);
    } catch { /* readiness unknown; rendered as such */ }
    try {
      const s = await api.get<ListResp<SocialProvider>>("/social-providers");
      setSocial(s.data ?? []);
    } catch { /* readiness unknown; rendered as such */ }
    // Whether Room sign-in can actually serve a guest right now, which is not the same question as whether it
    // is switched on. Guest authentication requires a live PMS feed, so an interface that is disconnected or
    // still loading its guest list refuses every guest — with the uniform message, which looks exactly like a
    // wrong surname. Without this the screen would show a correctly configured, enabled method while the front
    // desk fields complaints.
    try {
      const [list, routing] = await Promise.all([
        api.get<ListResp<PmsInterface>>("/pms-interfaces"),
        api.get<{ routes: PmsGuestNetworkRoute[] }>("/pms-routing"),
      ]);
      const ifaces = list.data ?? [];
      // Health is read for ACTIVE interfaces only: the others cannot serve a guest whatever their axes say,
      // and asking is a request per interface.
      const healths = await Promise.all(
        ifaces.filter((i) => i.lifecycle_state === "ACTIVE").map((i) =>
          api.get<{ health: PmsInterfaceHealth }>(`/pms-interfaces/${i.id}/health`)
            .then((h) => h.health)
            .catch(() => null)),
      );
      setPmsIfaces(ifaces);
      setPmsHealth(healths.filter(Boolean) as PmsInterfaceHealth[]);
      setPmsRoutes(routing.routes ?? []);
    } catch { /* readiness unknown; the notice is simply not shown */ }
  }, []);
  useEffect(() => { load(); }, [load]);

  // A PATCH carrying only the key being changed. The server merges per top-level key, so this screen can
  // never delete configuration it does not render.
  const save = useCallback(async (patch: Partial<AuthMethods>, label: string) => {
    setBusy(label); setErr(null);
    try {
      const updated = await api.put<AuthMethods>("/auth-methods", patch);
      setCfg(updated ?? {});
      toast.success(`${label} saved`, "Guests see the change the next time the sign-in page loads.");
    } catch (e) {
      setErr(e);
      await load(); // never leave a toggle showing a state the server did not accept
    } finally { setBusy(null); }
  }, [load, toast]);

  const emailReady = useMemo(() => notify.some((p) => p.channel === "email" && p.enabled), [notify]);
  const smsReady = useMemo(() => notify.some((p) => p.channel === "sms" && p.enabled), [notify]);
  const socialReady = useMemo(() => social.filter((p) => p.enabled).map((p) => p.provider), [social]);
  // Declared with the other readiness values and ABOVE the loading early-return: a hook after a conditional
  // return is called on some renders and not others, which React rejects outright.
  const pmsReadiness = useMemo(
    () => roomSignInReadiness(pmsIfaces, pmsHealth, pmsRoutes),
    [pmsIfaces, pmsHealth, pmsRoutes]);

  const header = (
    <PageHeader
      icon={<LogIn />}
      eyebrow="Guest portal"
      title="Sign-in methods"
      description="How guests prove who they are on the portal. Each switch applies immediately — the next guest to open the sign-in page sees it. Turning a method off does not disconnect guests already online."
    />
  );

  if (cfg === null) {
    return (
      <PageShell>
        {header}
        <div className="grid gap-4 md:grid-cols-2" aria-busy="true">
          <span className="sr-only">Loading sign-in methods</span>
          {[0, 1, 2, 3].map((i) => <Skeleton key={i} className="h-32" />)}
        </div>
      </PageShell>
    );
  }

  const pms = cfg.pms ?? {};
  const pmsMode = pms.mode || "room_lastname";
  const modeIsLegacy = pmsMode === LEGACY_EITHER;

  return (
    <PageShell>
      {header}

      {roles !== null && !writable && (
        <ReadOnlyNotice>Your role can see which sign-in methods are offered but not change them.</ReadOnlyNotice>
      )}
      <ErrorBanner err={err} className="mb-0" />

      <div className="grid gap-4 md:grid-cols-2">
        <MethodCard
          icon={<Ticket />}
          title="Voucher code"
          description="The guest types a code from a printed or emailed voucher."
          enabled={!!cfg.voucher?.enabled}
          busy={busy === "Voucher"}
          writable={writable}
          onToggle={(v) => save({ voucher: { ...(cfg.voucher ?? {}), enabled: v } }, "Voucher")}
          manageHref="/vouchers"
          manageLabel="Vouchers"
        />

        <MethodCard
          icon={<KeyRound />}
          title="Guest account"
          description="A username and password issued to the guest, managed under Guest accounts."
          enabled={!!cfg.guest_account?.enabled}
          busy={busy === "Guest account"}
          writable={writable}
          onToggle={(v) => save({ guest_account: { ...(cfg.guest_account ?? {}), enabled: v } }, "Guest account")}
          manageHref="/guest-accounts"
          manageLabel="Guest accounts"
        />

        {/* Room sign-in spans the row: it carries the readiness warning and the "what the guest types" choice. */}
        <Card className="md:col-span-2">
          <CardHeader className="items-start">
            <MethodTitle
              icon={<Hotel />}
              title="Room sign-in (from the PMS)"
              enabled={!!pms.enabled}
            />
            <MethodSwitch
              label="Room sign-in"
              enabled={!!pms.enabled}
              busy={busy === "Room sign-in"}
              writable={writable}
              onChange={(v) => save({ pms: { ...pms, enabled: v, mode: pms.mode || "room_lastname" } }, "Room sign-in")}
            />
          </CardHeader>
          <CardBody className="space-y-4">
            <p className="max-w-2xl text-sm text-muted-foreground">
              The guest enters their room number and one detail from their booking. OneGate checks it
              against the property management system for the network they are on — the guest never chooses a
              system, and no booking details are shown back to them.
            </p>

            {pms.enabled && (pmsReadiness.state === "down" || pmsReadiness.state === "partial") && (
              // WHY THIS IS SEPARATE FROM THE SWITCH. The method is switched on and correctly configured; what
              // is missing is the live PMS feed it depends on, and there is nothing on this screen to fix. An
              // operator otherwise sees a healthy-looking feature while guests are refused with the uniform
              // failure message — which reads as a wrong surname, so the front desk starts re-checking spellings
              // instead of the interface.
              //
              // PARTIAL IS ITS OWN CASE. When one guest network is affected and another is fine, "Room sign-in
              // is not working" would be false for half the property, and staying silent would be false for the
              // other half. The networks are named so the operator knows which guests are affected.
              <Callout
                tone="warning"
                title={pmsReadiness.state === "down"
                  ? "Room sign-in is not working at the moment"
                  : "Room sign-in is not working on some guest networks"}
              >
                {pmsReadiness.state === "down" ? (
                  <p>
                    {capitalise(pmsReadiness.reason)}. Guests cannot sign in with their room number until the
                    property management system is connected to OneGate again; they can still use any other
                    method switched on here. Nothing here needs changing — this setting is kept as it is and
                    starts working again on its own once the connection returns.
                  </p>
                ) : (
                  <>
                    <p>
                      Guests on the networks below cannot sign in with their room number. Everywhere else is
                      working normally. Nothing here needs changing — each one starts working again on its own
                      once its property management system is connected.
                    </p>
                    <ul className="mt-1 list-disc space-y-0.5 ps-4">
                      {pmsReadiness.affected.map((a) => (
                        <li key={`${a.guestNetwork}-${a.pmsInterface}`}>
                          <span className="font-medium">{a.guestNetwork}</span> (via {a.pmsInterface}) —{" "}
                          {a.reason}
                        </li>
                      ))}
                    </ul>
                  </>
                )}
                {pmsReadiness.unchecked.length > 0 && (
                  // Neutral, and never counted as an outage. A health read that failed is absence of evidence,
                  // and one of those networks may be perfectly fine.
                  <p className="mt-1">Readiness could not be checked for {pmsReadiness.unchecked.join(", ")}.</p>
                )}
                <p className="mt-1">
                  <Link href="/pms-interfaces" className="inline-flex items-center gap-0.5 font-medium underline">
                    Check the PMS connection <ArrowUpRight className="size-3.5" aria-hidden />
                  </Link>
                </p>
              </Callout>
            )}

            {pms.enabled && (
              <div className="border-t border-border pt-4">
              <fieldset className="space-y-3">
                <legend className="mb-3 text-label">
                  What the guest types, besides the room number
                </legend>
                {modeIsLegacy && (
                  // Shown rather than silently migrated: changing what a stored configuration does is the
                  // operator's decision, not this screen's.
                  <Callout tone="warning">
                    This site currently uses an older setting that accepts a last name or a reservation number
                    and guesses which one was typed, so some surnames are rejected. Choosing one of the
                    options below replaces it.
                  </Callout>
                )}
                <div className="grid gap-2 sm:grid-cols-2">
                  {PMS_MODES.map((m) => (
                    <OptionCard
                      key={m.value}
                      name="pms-mode"
                      value={m.value}
                      checked={pmsMode === m.value}
                      disabled={!writable || busy === "Room sign-in mode"}
                      onChange={(v) => save({ pms: { ...pms, enabled: true, mode: v } }, "Room sign-in mode")}
                      title={m.label}
                      description={m.hint}
                    />
                  ))}
                </div>
                <p className="text-xs text-muted-foreground">
                  Which property management system a guest is checked against is decided by their network, in{" "}
                  <Link href="/pms-routing" className="text-primary underline-offset-4 hover:underline">Network routing</Link>.
                </p>
              </fieldset>
              </div>
            )}
          </CardBody>
        </Card>

        <MethodCard
          icon={<Mail />}
          title="Email code"
          description="The guest receives a one-time code by email."
          enabled={!!cfg.email?.enabled}
          busy={busy === "Email code"}
          writable={writable}
          onToggle={(v) => save({ email: { ...(cfg.email ?? {}), enabled: v } }, "Email code")}
          ready={emailReady}
          notReadyReason="Not available until an email sender exists and is switched on, so codes cannot be sent yet."
          manageHref="/notifications"
          manageLabel="Email & SMS"
        />

        <MethodCard
          icon={<MessageSquare />}
          title="SMS code"
          description="The guest receives a one-time code by text message."
          enabled={!!cfg.sms?.enabled}
          busy={busy === "SMS code"}
          writable={writable}
          onToggle={(v) => save({ sms: { ...(cfg.sms ?? {}), enabled: v } }, "SMS code")}
          ready={smsReady}
          notReadyReason="Not available until a text-message sender exists and is switched on, so codes cannot be sent yet."
          manageHref="/notifications"
          manageLabel="Email & SMS"
        />

        <Card className="md:col-span-2">
          <CardHeader className="items-start">
            <div className="min-w-0 space-y-1">
              <CardTitle className="flex items-center gap-2 [&_svg]:size-4"><Users aria-hidden /> Social login</CardTitle>
              <CardDescription>
                The guest signs in with an existing account such as Google. Each provider is offered
                individually, because each needs its own credentials.
              </CardDescription>
            </div>
          </CardHeader>
          <CardBody>
            {socialReady.length === 0 ? (
              <div className="flex flex-wrap items-center gap-2 text-sm">
                <Badge tone="default">Not available</Badge>
                <span className="text-muted-foreground">No social provider is configured.</span>
                <Link href="/social-providers" className="inline-flex items-center gap-0.5 text-primary underline-offset-4 hover:underline">
                  Set one up in Social login <ArrowUpRight className="size-3.5" aria-hidden />
                </Link>
              </div>
            ) : (
              <ul className="grid gap-2 sm:grid-cols-2">
                {socialReady.map((p) => {
                  const on = !!cfg.social?.[p]?.enabled;
                  const name = SOCIAL_LABELS[p] ?? p;
                  return (
                    <li key={p} className="flex items-center justify-between gap-3 rounded-md border border-border px-3.5 py-2.5">
                      {writable ? (
                        <label className="flex cursor-pointer items-center gap-2.5 text-sm">
                          <input
                            type="checkbox"
                            className="size-4 accent-primary"
                            checked={on}
                            disabled={busy === `Social (${p})`}
                            onChange={(e) =>
                              save({ social: { ...(cfg.social ?? {}), [p]: { ...(cfg.social?.[p] ?? {}), enabled: e.target.checked } } },
                                `Social (${p})`)}
                          />
                          <span className="capitalize">{name}</span>
                        </label>
                      ) : (
                        <span className="text-sm capitalize">{name}</span>
                      )}
                      <Badge tone={on ? "ok" : "default"} dot={on}>{on ? "Offered" : "Not offered"}</Badge>
                    </li>
                  );
                })}
              </ul>
            )}
          </CardBody>
        </Card>
      </div>

      {/* The numbers are about guests signing in, and this is the screen an operator is already on when they
          decide that five attempts is too few for their property. */}
      <GuestSignInProtectionCard canWrite={mayChangeProtection} />
    </PageShell>
  );
}

function MethodTitle({ icon, title, enabled, extra }: {
  icon: React.ReactNode; title: string; enabled: boolean; extra?: React.ReactNode;
}) {
  return (
    <div className="min-w-0 space-y-1.5">
      <CardTitle className="flex items-center gap-2 [&_svg]:size-4">
        <span className="text-muted-foreground" aria-hidden>{icon}</span>
        {title}
      </CardTitle>
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge tone={enabled ? "ok" : "default"} dot={enabled}>{enabled ? "On" : "Off"}</Badge>
        {extra}
      </div>
    </div>
  );
}

/** The switch, or nothing for a role that cannot change it (the On/Off badge beside the title still says the state). */
function MethodSwitch({ label, enabled, busy, writable, disabled, onChange }: {
  label: string; enabled: boolean; busy: boolean; writable: boolean; disabled?: boolean; onChange: (v: boolean) => void;
}) {
  if (!writable) return null;
  return (
    <div className="flex shrink-0 items-center gap-2">
      {busy && <span className="text-xs text-muted-foreground" aria-live="polite">Saving…</span>}
      <Switch
        checked={enabled}
        disabled={busy || disabled}
        onCheckedChange={onChange}
        label={label}
      />
    </div>
  );
}

// MethodCard is one switchable method. A method whose provider is not ready is shown as unavailable and its
// switch is disabled — presenting a working switch for something that cannot deliver a code is how an
// operator turns a method on and only finds out at the front desk.
function MethodCard({
  icon, title, description, enabled, busy, writable, onToggle, ready = true, notReadyReason, manageHref, manageLabel,
}: {
  icon: React.ReactNode; title: string; description: string;
  enabled: boolean; busy: boolean; writable: boolean; onToggle: (v: boolean) => void;
  ready?: boolean; notReadyReason?: string; manageHref?: string; manageLabel?: string;
}) {
  return (
    <Card className={cn("flex flex-col", !ready && "bg-surface/60")}>
      <CardHeader className="items-start border-b-0 pb-2">
        <MethodTitle
          icon={icon}
          title={title}
          enabled={enabled}
          extra={!ready ? <Badge tone="warn">Not available</Badge> : undefined}
        />
        <MethodSwitch label={title} enabled={enabled} busy={busy} writable={writable} disabled={!ready} onChange={onToggle} />
      </CardHeader>
      <CardBody className="flex flex-1 flex-col gap-2 pt-0">
        <p className="text-sm text-muted-foreground">{description}</p>
        {!ready && notReadyReason && <p className="text-xs text-warning-subtle-foreground">{notReadyReason}</p>}
        {manageHref && (
          <Link
            href={manageHref}
            className="mt-auto inline-flex w-fit items-center gap-0.5 pt-1 text-xs text-primary underline-offset-4 hover:underline"
          >
            {manageLabel} <ArrowUpRight className="size-3.5" aria-hidden />
          </Link>
        )}
      </CardBody>
    </Card>
  );
}
