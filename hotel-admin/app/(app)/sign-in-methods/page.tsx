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
// WHAT IS DELIBERATELY NOT HERE: any PMS interface, provider or connector selector. Which PMS a guest is
// resolved against is decided by the guest network they are on, in Network routing. Putting it here would
// give an operator two places to decide one thing, and would imply the guest picks a PMS — which they never
// do.

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { api, ApiError, ListResp, PmsInterface, PmsInterfaceHealth, PmsGuestNetworkRoute } from "@/lib/api";
import { roomSignInReadiness } from "@/lib/pms-availability";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ErrorBanner } from "@/components/ui/error-banner";
import { Ticket, Hotel, KeyRound, Mail, MessageSquare, Users, ExternalLink } from "lucide-react";

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

// The reasons read as sentence fragments so they can be listed after a network name; the single-outage copy
// puts one at the start of a sentence instead.
const capitalise = (t: string) => t.charAt(0).toUpperCase() + t.slice(1);

export default function SignInMethodsPage() {
  const [cfg, setCfg] = useState<AuthMethods | null>(null);
  const [notify, setNotify] = useState<NotifyProvider[]>([]);
  const [social, setSocial] = useState<SocialProvider[]>([]);
  const [err, setErr] = useState<unknown>(null);
  const [note, setNote] = useState<string | null>(null);
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
    setBusy(label); setErr(null); setNote(null);
    try {
      const updated = await api.put<AuthMethods>("/auth-methods", patch);
      setCfg(updated ?? {});
      setNote(`${label} saved. Guests see the change the next time the sign-in page loads.`);
    } catch (e) {
      setErr(e);
      await load(); // never leave a toggle showing a state the server did not accept
    } finally { setBusy(null); }
  }, [load]);

  const emailReady = useMemo(() => notify.some((p) => p.channel === "email" && p.enabled), [notify]);
  const smsReady = useMemo(() => notify.some((p) => p.channel === "sms" && p.enabled), [notify]);
  const socialReady = useMemo(() => social.filter((p) => p.enabled).map((p) => p.provider), [social]);
  // Declared with the other readiness values and ABOVE the loading early-return: a hook after a conditional
  // return is called on some renders and not others, which React rejects outright.
  const pmsReadiness = useMemo(
    () => roomSignInReadiness(pmsIfaces, pmsHealth, pmsRoutes),
    [pmsIfaces, pmsHealth, pmsRoutes]);

  if (cfg === null) {
    return <div className="space-y-4"><h1 className="text-lg font-semibold">Sign-in methods</h1>
      <p className="text-sm text-muted">Loading…</p></div>;
  }

  const pms = cfg.pms ?? {};
  const pmsMode = pms.mode || "room_lastname";
  const modeIsLegacy = pmsMode === LEGACY_EITHER;

  return (
    <div className="space-y-4">
      <div>
        <h1 className="text-lg font-semibold">Sign-in methods</h1>
        <p className="text-sm text-muted mt-1 max-w-2xl">
          How guests prove who they are on the portal. Changes apply immediately — the next guest to open the
          sign-in page sees them. Turning a method off does not disconnect guests already online.
        </p>
      </div>

      {err ? <ErrorBanner err={err} /> : null}
      {note && <p className="text-sm text-emerald-700" role="status">{note}</p>}

      <MethodCard
        icon={<Ticket size={16} />}
        title="Voucher code"
        description="The guest types a code from a printed or emailed voucher."
        enabled={!!cfg.voucher?.enabled}
        busy={busy === "Voucher"}
        onToggle={(v) => save({ voucher: { ...(cfg.voucher ?? {}), enabled: v } }, "Voucher")}
      />

      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2"><Hotel size={16} /> Room sign-in (from the PMS)</CardTitle>
        </CardHeader>
        <CardBody className="space-y-3">
          {pms.enabled && (pmsReadiness.state === "down" || pmsReadiness.state === "partial") && (
            // WHY THIS IS SEPARATE FROM THE TOGGLE. The method is switched on and correctly configured; what
            // is missing is the live PMS feed it depends on, and there is nothing on this screen to fix. An
            // operator otherwise sees a healthy-looking feature while guests are refused with the uniform
            // failure message — which reads as a wrong surname, so the front desk starts re-checking spellings
            // instead of the interface.
            //
            // PARTIAL IS ITS OWN CASE. When one guest network is affected and another is fine, "Room sign-in
            // is not working" would be false for half the property, and staying silent would be false for the
            // other half. The networks are named so the operator knows which guests are affected.
            <div role="status" className="rounded-md border border-amber-300 bg-amber-50 p-3">
              <div className="text-sm font-medium text-amber-900">
                {pmsReadiness.state === "down"
                  ? "Room sign-in is not working at the moment"
                  : "Room sign-in is not working on some guest networks"}
              </div>
              {pmsReadiness.state === "down" ? (
                <p className="text-xs text-amber-800 mt-1">
                  {capitalise(pmsReadiness.reason)}. Guests cannot sign in with their room number until the
                  property management system is connected to StayConnect again; they can still use any other
                  method switched on below. Nothing here needs changing — this setting is kept as it is and
                  starts working again on its own once the connection returns.
                </p>
              ) : (
                <>
                  <p className="text-xs text-amber-800 mt-1">
                    Guests on the networks below cannot sign in with their room number. Everywhere else is
                    working normally. Nothing here needs changing — each one starts working again on its own
                    once its property management system is connected.
                  </p>
                  <ul className="text-xs text-amber-800 mt-1 list-disc pl-4 space-y-0.5">
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
                <p className="text-xs text-amber-800 mt-1">
                  Readiness could not be checked for {pmsReadiness.unchecked.join(", ")}.
                </p>
              )}
              <p className="text-xs mt-1">
                <Link href="/pms-interfaces" className="underline text-amber-900">
                  Check the PMS interface <ExternalLink size={11} className="inline" />
                </Link>
              </p>
            </div>
          )}

          <div className="flex items-start justify-between gap-4">
            <p className="text-sm text-muted max-w-xl">
              The guest enters their room number and one detail from their booking. StayConnect checks it
              against the property management system for the network they are on — the guest never chooses a
              system, and no booking details are shown back to them.
            </p>
            <Toggle
              on={!!pms.enabled}
              busy={busy === "Room sign-in"}
              onChange={(v) => save({ pms: { ...pms, enabled: v, mode: pms.mode || "room_lastname" } }, "Room sign-in")}
            />
          </div>

          {pms.enabled && (
            <div className="border-t border-border pt-3">
              <div className="text-sm font-medium mb-1">What the guest is asked for, besides the room number</div>
              {modeIsLegacy && (
                // Shown rather than silently migrated: changing what a stored configuration does is the
                // operator's decision, not this screen's.
                <p className="text-xs text-amber-600 mb-2">
                  This site currently uses an older setting that accepts a last name or a reservation number
                  and guesses which one was typed, so some surnames are rejected. Choosing one of the
                  options below replaces it.
                </p>
              )}
              <div className="space-y-1.5">
                {PMS_MODES.map((m) => (
                  <label key={m.value} className="flex items-start gap-2 text-sm cursor-pointer">
                    <input
                      type="radio"
                      name="pms-mode"
                      className="mt-1"
                      checked={pmsMode === m.value}
                      disabled={busy === "Room sign-in mode"}
                      onChange={() => save({ pms: { ...pms, enabled: true, mode: m.value } }, "Room sign-in mode")}
                    />
                    <span>
                      <span className="font-medium">{m.label}</span>
                      <span className="block text-xs text-muted">{m.hint}</span>
                    </span>
                  </label>
                ))}
              </div>
              <p className="text-xs text-muted mt-2">
                Which property management system a guest is checked against is decided by their network, in{" "}
                <Link href="/pms-routing" className="underline">Network routing</Link>.
              </p>
            </div>
          )}
        </CardBody>
      </Card>

      <MethodCard
        icon={<KeyRound size={16} />}
        title="Guest account"
        description="A username and password issued to the guest, managed under Guest accounts."
        enabled={!!cfg.guest_account?.enabled}
        busy={busy === "Guest account"}
        onToggle={(v) => save({ guest_account: { ...(cfg.guest_account ?? {}), enabled: v } }, "Guest account")}
        manageHref="/guest-accounts"
        manageLabel="Guest accounts"
      />

      <MethodCard
        icon={<Mail size={16} />}
        title="Email code"
        description="The guest receives a one-time code by email."
        enabled={!!cfg.email?.enabled}
        busy={busy === "Email code"}
        onToggle={(v) => save({ email: { ...(cfg.email ?? {}), enabled: v } }, "Email code")}
        ready={emailReady}
        notReadyReason="No email provider is switched on, so codes cannot be sent."
        manageHref="/notifications"
        manageLabel="Email & SMS providers"
      />

      <MethodCard
        icon={<MessageSquare size={16} />}
        title="SMS code"
        description="The guest receives a one-time code by text message."
        enabled={!!cfg.sms?.enabled}
        busy={busy === "SMS code"}
        onToggle={(v) => save({ sms: { ...(cfg.sms ?? {}), enabled: v } }, "SMS code")}
        ready={smsReady}
        notReadyReason="No SMS provider is switched on, so codes cannot be sent."
        manageHref="/notifications"
        manageLabel="Email & SMS providers"
      />

      <Card>
        <CardHeader><CardTitle className="flex items-center gap-2"><Users size={16} /> Social login</CardTitle></CardHeader>
        <CardBody className="space-y-2">
          <p className="text-sm text-muted">
            The guest signs in with an existing account such as Google. Each provider is switched on
            individually, because each needs its own credentials.
          </p>
          {socialReady.length === 0 ? (
            <p className="text-sm">
              <Badge tone="default">Not available</Badge>{" "}
              No social provider is configured.{" "}
              <Link href="/social-providers" className="underline">Set one up <ExternalLink size={11} className="inline" /></Link>
            </p>
          ) : (
            <div className="space-y-1.5">
              {socialReady.map((p) => (
                <label key={p} className="flex items-center gap-2 text-sm cursor-pointer">
                  <input
                    type="checkbox"
                    checked={!!cfg.social?.[p]?.enabled}
                    disabled={busy === `Social (${p})`}
                    onChange={(e) =>
                      save({ social: { ...(cfg.social ?? {}), [p]: { ...(cfg.social?.[p] ?? {}), enabled: e.target.checked } } },
                        `Social (${p})`)}
                  />
                  <span className="capitalize">{p}</span>
                </label>
              ))}
            </div>
          )}
        </CardBody>
      </Card>
    </div>
  );
}

// MethodCard is one switchable method. A method whose provider is not ready is shown as unavailable and its
// switch is disabled — presenting a working toggle for something that cannot deliver a code is how an
// operator turns a method on and only finds out at the front desk.
function MethodCard({
  icon, title, description, enabled, busy, onToggle, ready = true, notReadyReason, manageHref, manageLabel,
}: {
  icon: React.ReactNode; title: string; description: string;
  enabled: boolean; busy: boolean; onToggle: (v: boolean) => void;
  ready?: boolean; notReadyReason?: string; manageHref?: string; manageLabel?: string;
}) {
  return (
    <Card>
      <CardBody className="flex items-start justify-between gap-4">
        <div>
          <div className="font-medium flex items-center gap-2">{icon}{title}
            {!ready && <Badge tone="default">Not available</Badge>}
          </div>
          <p className="text-sm text-muted mt-1">{description}</p>
          {!ready && notReadyReason && <p className="text-xs text-amber-600 mt-1">{notReadyReason}</p>}
          {manageHref && (
            <p className="text-xs mt-1">
              <Link href={manageHref} className="underline text-muted">
                {manageLabel} <ExternalLink size={11} className="inline" />
              </Link>
            </p>
          )}
        </div>
        <Toggle on={enabled} busy={busy} disabled={!ready} onChange={onToggle} />
      </CardBody>
    </Card>
  );
}

function Toggle({ on, busy, disabled, onChange }: {
  on: boolean; busy: boolean; disabled?: boolean; onChange: (v: boolean) => void;
}) {
  return (
    <Button
      variant={on ? "primary" : "ghost"}
      disabled={busy || disabled}
      aria-pressed={on}
      onClick={() => onChange(!on)}
    >
      {busy ? "Saving…" : on ? "On" : "Off"}
    </Button>
  );
}
