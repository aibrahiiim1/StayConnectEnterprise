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
import { api, ListResp, Whoami } from "@/lib/api";
import { canWrite } from "@/lib/roles";
import { GuestSignInProtectionCard } from "@/components/guest-signin-protection";
import { roomSignInImpaired, useRoomSignInReadiness } from "@/components/room-sign-in-readiness";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { ErrorBanner } from "@/components/ui/error-banner";
import { PageHeader, PageShell } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { Skeleton, Switch } from "@/components/ui/misc";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { cn } from "@/lib/utils";
import { Ticket, Hotel, KeyRound, Mail, MessageSquare, Users, ArrowUpRight, LogIn, PackageOpen } from "lucide-react";

// The auth_methods document. Only the keys this screen owns are typed; everything else is preserved
// untouched by the server's merge, so an unknown future method cannot be deleted by saving here.
type Method = { enabled?: boolean };
type PMSMethod = { enabled?: boolean; mode?: string; provider?: string; template_id?: string };
type AuthMethods = {
  voucher?: Method;
  guest_account?: Method;
  // Open package selection: the client picks an Internet package without any credential (Free or Card
  // payment). The access is held by an anonymous access subject, never by the device's MAC address.
  open?: Method;
  email?: Method;
  sms?: Method;
  social?: Record<string, Method>;
  pms?: PMSMethod;
};

type NotifyProvider = { channel: string; kind: string; enabled: boolean };
type SocialProvider = { provider: string; enabled: boolean };

// WHAT THE GUEST TYPES BESIDES THE ROOM NUMBER IS NOT SET HERE. It depends on the PMS stay record, so it is
// configured in the Hotel module, on Room sign-in (/room-sign-in). This card keeps only the method switch, so
// the choice of methods stays in one place and the credential choice in another, with no duplicated setting.

const SOCIAL_LABELS: Record<string, string> = { google: "Google", apple: "Apple", facebook: "Facebook", microsoft: "Microsoft" };

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
  // Whether Room sign-in can serve a guest right now. Only a one-line warning is shown here — the detail is on
  // Hotel → Room sign-in — but an operator switching the method on during an outage must not miss it.
  const { readiness: pmsReadiness } = useRoomSignInReadiness();

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
  }, []);
  useEffect(() => { load(); }, [load]);

  // A PATCH carrying only the key being changed. The server merges per top-level key, so this screen can
  // never delete configuration it does not render.
  const save = useCallback(async (patch: Partial<AuthMethods>, label: string) => {
    setBusy(label); setErr(null);
    try {
      const updated = await api.put<AuthMethods>("/auth-methods", patch);
      setCfg(updated ?? {});
      toast.success(`${label} saved`, "Clients see the change the next time the sign-in page loads.");
    } catch (e) {
      setErr(e);
      await load(); // never leave a toggle showing a state the server did not accept
    } finally { setBusy(null); }
  }, [load, toast]);

  const emailReady = useMemo(() => notify.some((p) => p.channel === "email" && p.enabled), [notify]);
  const smsReady = useMemo(() => notify.some((p) => p.channel === "sms" && p.enabled), [notify]);
  const socialReady = useMemo(() => social.filter((p) => p.enabled).map((p) => p.provider), [social]);

  const header = (
    <PageHeader
      icon={<LogIn />}
      eyebrow="Client Portal"
      title="Sign-in methods"
      description="Each switch applies immediately. Turning a method off does not disconnect clients already online."
      help={
        <>
          <HelpSection title="What this page sets">
            <p>
              How clients prove who they are on the portal. Each switch applies immediately — the next client to open
              the sign-in page sees it. Turning a method off does not disconnect clients already online.
            </p>
          </HelpSection>
          <HelpSection title="The methods">
            <HelpList
              items={[
                <><strong>Voucher code</strong> — the client types a code from a printed or emailed voucher. Vouchers are managed under Vouchers.</>,
                <><strong>Client account</strong> — a username and password issued to the client, managed under Client accounts.</>,
                <><strong>Choose a package without signing in</strong> — for a café, office or venue: the client chooses a Free package, or one paid by card, without any credential. Only packages the client can actually get are listed. A client who returns later resumes the same access with the recovery code shown after they connect.</>,
                <><strong>Room sign-in</strong> — the client enters their room number and one detail from their booking. OneGate checks it against the property management system for the network they are on; the client never chooses a system, and no booking details are shown back to them. Which detail is asked for is set in Room sign-in, and which system a network uses in PMS routing — both under Hotel.</>,
                <><strong>Email code</strong> and <strong>SMS code</strong> — the client receives a one-time code. Each is available only once a sender exists and is switched on under Email &amp; SMS.</>,
                <><strong>Social login</strong> — the client signs in with an existing account such as Google. Each provider is offered individually, because each needs its own credentials; providers are set up under Social login.</>,
              ]}
            />
          </HelpSection>
          <HelpSection title="Client sign-in protection">
            <p>
              After too many incorrect sign-in details from the same device, that device is asked to wait before it
              can try again. The card at the bottom decides how strict that is; it is always on.
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
        <div className="grid gap-4 md:grid-cols-2" aria-busy="true">
          <span className="sr-only">Loading sign-in methods</span>
          {[0, 1, 2, 3].map((i) => <Skeleton key={i} className="h-32" />)}
        </div>
      </PageShell>
    );
  }

  const pms = cfg.pms ?? {};

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
          description="The client types a code from a printed or emailed voucher."
          enabled={!!cfg.voucher?.enabled}
          busy={busy === "Voucher"}
          writable={writable}
          onToggle={(v) => save({ voucher: { ...(cfg.voucher ?? {}), enabled: v } }, "Voucher")}
          manageHref="/vouchers"
          manageLabel="Vouchers"
        />

        <MethodCard
          icon={<KeyRound />}
          title="Client account"
          description="A username and password issued to the client, managed under Client accounts."
          enabled={!!cfg.guest_account?.enabled}
          busy={busy === "Client account"}
          writable={writable}
          onToggle={(v) => save({ guest_account: { ...(cfg.guest_account ?? {}), enabled: v } }, "Client account")}
          manageHref="/guest-accounts"
          manageLabel="Client accounts"
        />

        <MethodCard
          icon={<PackageOpen />}
          title="Choose a package without signing in"
          description="The client picks a Free package, or pays by card, with no code, account or room. A returning client resumes with a recovery code shown on screen."
          enabled={!!cfg.open?.enabled}
          busy={busy === "Open package selection"}
          writable={writable}
          onToggle={(v) => save({ open: { ...(cfg.open ?? {}), enabled: v } }, "Open package selection")}
          manageHref="/payment-methods"
          manageLabel="Payment methods"
        />

        {/* Room sign-in spans the row so the grid of single methods below it stays even. */}
        <Card className="flex flex-col md:col-span-2">
          <CardHeader className="items-start border-b-0 pb-2">
            <MethodTitle icon={<Hotel />} title="Room sign-in" enabled={!!pms.enabled} />
            <MethodSwitch
              label="Room sign-in"
              enabled={!!pms.enabled}
              busy={busy === "Room sign-in"}
              writable={writable}
              onChange={(v) => save({ pms: { ...pms, enabled: v, mode: pms.mode || "room_lastname" } }, "Room sign-in")}
            />
          </CardHeader>
          <CardBody className="flex flex-1 flex-col gap-2 pt-0">
            <p className="text-sm text-muted-foreground">
              The client enters their room number and one detail from their reservation, checked against the PMS.
            </p>
            {pms.enabled && roomSignInImpaired(pmsReadiness) && (
              // One line, not the full callout: which networks are affected and why is on the Hotel screen.
              <p className="text-xs text-warning-subtle-foreground" role="status">
                {pmsReadiness.state === "down"
                  ? "Not working at the moment — the PMS is not available."
                  : "Not working on some client networks — the PMS is not available for them."}
              </p>
            )}
            <Link
              href="/room-sign-in"
              className="mt-auto inline-flex w-fit items-center gap-0.5 pt-1 text-xs text-primary underline-offset-4 hover:underline"
            >
              Room sign-in settings — under Hotel <ArrowUpRight className="size-3.5" aria-hidden />
            </Link>
          </CardBody>
        </Card>

        <MethodCard
          icon={<Mail />}
          title="Email code"
          description="The client receives a one-time code by email."
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
          description="The client receives a one-time code by text message."
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
                The client signs in with an existing account such as Google.
              </CardDescription>
            </div>
          </CardHeader>
          <CardBody>
            {socialReady.length === 0 ? (
              <div className="flex flex-wrap items-center gap-2 text-sm">
                <Badge tone="default">Not available</Badge>
                <span className="text-muted-foreground">No social provider is configured.</span>
                <Link href="/social-providers" className="inline-flex items-center gap-0.5 text-primary underline underline-offset-2 hover:decoration-2">
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
