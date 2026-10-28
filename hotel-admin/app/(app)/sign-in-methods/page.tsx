"use client";

// Each switch saves immediately (no page-level Save) and a refused change re-reads server state.
// No PMS interface selector here: the PMS is decided by the guest network, in Network routing.

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { api, ListResp } from "@/lib/api";
import type { PortalHTTPSDoc } from "@/lib/portal-https";
import { typedSignInOn } from "@/lib/sign-in-methods";
import { canRead, canWrite } from "@/lib/roles";
import { GuestSignInProtectionCard } from "@/components/guest-signin-protection";
import { NetworkPauseSettingsCard, NetworkPausesNotice } from "@/components/guest-signin-network-pause";
import { CodeRequestsCard } from "@/components/code-requests";
import { EmailAccountsSettingsCard } from "@/components/email-accounts-settings";
import { roomSignInImpaired, useRoomSignInReadiness } from "@/components/room-sign-in-readiness";
import { Card, CardBody, CardDescription, CardHeader, CardTitle, SectionCard } from "@/components/ui/card";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Select } from "@/components/ui/input";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { PageHeader, PageShell } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { Skeleton, Switch } from "@/components/ui/misc";
import { ReadOnlyNotice, SettingField } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { cn } from "@/lib/utils";
import { moduleLicensed, useCapabilities } from "@/lib/capabilities";
import { NetworkSignInOverview } from "@/components/network/effective-signin";
import type { SiteFacts } from "@/lib/sign-in-effective";
import { senderReady, type NotifyProvider } from "@/lib/sign-in-readiness";
import { Ticket, Hotel, KeyRound, Mail, MessageSquare, Users, ArrowUpRight, PackageOpen, Smartphone } from "lucide-react";
import { useOperatorRoles } from "@/lib/whoami-context";

// Only the keys this screen owns are typed; the server's per-key merge preserves the rest.
type Method = { enabled?: boolean };
type PMSMethod = { enabled?: boolean; mode?: string; provider?: string; template_id?: string };
type PrimaryMethod = "" | "pms" | "email_account" | "sms" | "whatsapp" | "guest_account" | "voucher";
type PortalJourney = { primary_method?: PrimaryMethod; remember_device_days?: number };
type AuthMethods = {
  voucher?: Method;
  guest_account?: Method;
  // Access is held by an anonymous access subject, never by the device's MAC address.
  open?: Method;
  email_account?: Method;
  sms?: Method;
  whatsapp?: Method;
  social?: Record<string, Method>;
  pms?: PMSMethod;
  portal?: PortalJourney;
};

// Remember-device days; 0 = always ask.
const REMEMBER_DAYS_DEFAULT = 30;
const REMEMBER_DAYS_MIN = 0;
const REMEMBER_DAYS_MAX = 365;

const PRIMARY_LABELS: Record<Exclude<PrimaryMethod, "">, string> = {
  pms: "Room sign-in", email_account: "Email account", sms: "SMS code", whatsapp: "WhatsApp code",
  guest_account: "Client account", voucher: "Voucher code",
};

type SocialProvider = { provider: string; enabled: boolean };

const SOCIAL_LABELS: Record<string, string> = { google: "Google", apple: "Apple", facebook: "Facebook", microsoft: "Microsoft" };

export default function SignInMethodsPage() {
  const toast = useToast();
  // Read-only until the role loads; edged enforces the real gate.
  const roles = useOperatorRoles();
  const writable = roles === null ? false : canWrite("auth-methods", roles);
  const mayChangeProtection = roles === null ? false : canWrite("guest-signin-protection", roles);

  // Optional methods show only when their module is licensed; unknown module state hides them.
  const caps = useCapabilities();
  const hospitality = moduleLicensed(caps, "hospitality");
  // The email_otp module gates "Email accounts".
  const emailMod = moduleLicensed(caps, "email_otp");
  const smsMod = moduleLicensed(caps, "sms_otp");
  const whatsappMod = moduleLicensed(caps, "whatsapp_otp");
  const socialMod = moduleLicensed(caps, "social_login");
  const cardMod = moduleLicensed(caps, "card_payment");

  const [cfg, setCfg] = useState<AuthMethods | null>(null);
  const [notify, setNotify] = useState<NotifyProvider[]>([]);
  const [social, setSocial] = useState<SocialProvider[]>([]);
  // null: unknown, nothing is shown.
  const [httpsActive, setHttpsActive] = useState<boolean | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const { readiness: pmsReadiness } = useRoomSignInReadiness(hospitality);

  const load = useCallback(async () => {
    setErr(null);
    try {
      const m = await api.get<AuthMethods>("/auth-methods");
      setCfg(m ?? {});
    } catch (e) {
      setErr(e);
      setCfg({});
    }
    // Readiness is advisory (failures must not block managing methods) and is requested only for licensed modules and readable roles.
    if ((emailMod || smsMod || whatsappMod) && roles !== null && canRead("notification-providers", roles)) {
      try {
        const n = await api.get<ListResp<NotifyProvider>>("/notification-providers");
        setNotify(n.data ?? []);
      } catch { /* readiness unknown; rendered as such */ }
    }
    if (roles !== null && canRead("portal-https", roles)) {
      try {
        const d = await api.get<PortalHTTPSDoc>("/portal-https");
        setHttpsActive(typeof d?.state?.https_active === "boolean" ? d.state.https_active : null);
      } catch { /* unknown; no warning rather than a wrong one */ }
    }
    if (socialMod && roles !== null && canRead("social-providers", roles)) {
      try {
        const s = await api.get<ListResp<SocialProvider>>("/social-providers");
        setSocial(s.data ?? []);
      } catch { /* readiness unknown; rendered as such */ }
    }
  }, [emailMod, smsMod, whatsappMod, socialMod, roles]);
  useEffect(() => { load(); }, [load]);

  // Sends only the changed key; the server merges per top-level key, so unrendered config is never deleted.
  const save = useCallback(async (patch: Partial<AuthMethods>, label: string) => {
    setBusy(label); setErr(null);
    try {
      const updated = await api.put<AuthMethods>("/auth-methods", patch);
      setCfg(updated ?? {});
      toast.success(`${label} saved`, "Clients see the change the next time the sign-in page loads.");
    } catch (e) {
      setErr(e);
      await load();
    } finally { setBusy(null); }
  }, [load, toast]);

  const emailReady = useMemo(() => senderReady(notify, "email"), [notify]);
  const smsReady = useMemo(() => senderReady(notify, "sms"), [notify]);
  const whatsappReady = useMemo(() => senderReady(notify, "whatsapp"), [notify]);
  const socialReady = useMemo(() => social.filter((p) => p.enabled).map((p) => p.provider), [social]);
  // A provider switched ON stays listed even if its app is gone or disabled, so it can be turned OFF.
  const socialListed = useMemo(() => {
    const on = Object.entries(cfg?.social ?? {}).filter(([, m]) => m?.enabled).map(([p]) => p);
    return Array.from(new Set([...socialReady, ...on]));
  }, [socialReady, cfg]);

  const header = (
    <PageHeader
      title="Sign-in methods"
      description="How clients sign in, for the whole site. Each switch applies immediately; clients already online stay connected. A network can narrow these, as shown further down."
      help={
        <>
          <HelpSection title="What this page sets">
            <p>
              How clients prove who they are on the portal, for the whole site. Each switch applies immediately — the next
              client to open the sign-in page sees it, on every network that has not switched that method off for itself.
              Turning a method off does not disconnect clients already online. <em>What each client network offers</em>
              shows the result per network.
            </p>
          </HelpSection>
          <HelpSection title="The methods">
            <HelpList
              items={[
                <><strong>Voucher code</strong> — the client types a code from a printed or emailed voucher. Vouchers are managed under Vouchers.</>,
                <><strong>Client account</strong> — a username and password issued to the client, managed under Client accounts.</>,
                <><strong>Choose a package without signing in</strong> — the client chooses a package with no credential. Only packages the client can actually get are listed. A client who returns later resumes the same access with the recovery code shown after they connect.</>,
                ...(hospitality ? [<><strong>Room sign-in</strong> — the client enters their room number and one detail from their booking. OneGate checks it against the property management system for the network they are on; the client never chooses a system, and no booking details are shown back to them. Which detail is asked for is set in Room sign-in, and which system a network uses in PMS routing — both under Hotel.</>] : []),
                ...(emailMod ? [<><strong>Email accounts</strong> — the client registers with their email address and a password, and signs in with them on any device. They are online straight away and confirm the address from an email within the time set under <em>Email accounts: verification and limits</em>. Verification and password-reset emails need an email sender under Delivery.</>] : []),
                ...(smsMod ? [<><strong>SMS code</strong> — the client receives a one-time code by text message. Available once a text-message sender exists and is switched on under Delivery.</>] : []),
                ...(whatsappMod ? [<><strong>WhatsApp code</strong> — the client receives a one-time code on WhatsApp. Available once a WhatsApp sender exists and is switched on under Delivery.</>] : []),
                ...(socialMod ? [<><strong>Identity providers</strong> — the client signs in with an existing Google, Apple, Facebook or Microsoft account. Each provider is offered individually, because each needs its own credentials; providers are set up under Identity providers.</>] : []),
              ]}
            />
            <p className="text-muted-foreground">Methods that belong to a module this site is not licensed for are not shown.</p>
          </HelpSection>
          <HelpSection title="The client journey">
            <p>
              The portal leads with one <strong>primary method</strong>; identity providers appear as quick sign-in
              buttons above it, and every other method is offered under &ldquo;Or sign in with&rdquo;. A device that
              verified a code or an identity provider is <strong>remembered</strong> for the number of days set here,
              and reconnects with one tap. Knowing an email address is never enough: the device must have proved
              the code or the provider before.
            </p>
          </HelpSection>
          {hospitality && (
          <HelpSection title="Client sign-in protection">
            <p>
              After too many incorrect sign-in details from the same device, that device is asked to wait before it
              can try again. The card at the bottom decides how strict that is; it is always on.
            </p>
          </HelpSection>
          )}
        </>
      }
    />
  );

  if (cfg === null) {
    return (
      <PageShell>
        {header}
        <div className="space-y-3" aria-busy="true">
          <span className="sr-only">Loading sign-in methods</span>
          {[0, 1, 2, 3].map((i) => <Skeleton key={i} className="h-16" />)}
        </div>
      </PageShell>
    );
  }

  const pms = cfg.pms ?? {};
  const facts: SiteFacts = {
    cfg,
    modules: { hospitality, email_otp: emailMod, sms_otp: smsMod, whatsapp_otp: whatsappMod, social_login: socialMod },
    senders: {
      email: emailMod && roles !== null && canRead("notification-providers", roles) ? emailReady : null,
      sms: smsMod && roles !== null && canRead("notification-providers", roles) ? smsReady : null,
      whatsapp: whatsappMod && roles !== null && canRead("notification-providers", roles) ? whatsappReady : null,
    },
    socialProviders: socialMod && roles !== null && canRead("social-providers", roles) ? socialReady : null,
  };

  return (
    <PageShell>
      {header}

      {roles !== null && !writable && (
        <ReadOnlyNotice>Your role can see which sign-in methods are offered but not change them.</ReadOnlyNotice>
      )}
      <ErrorBanner err={err} className="mb-0" />

      {/* A running network pause is shown where staff manage sign-in (GDPR audit A4). */}
      {hospitality && roles !== null && canRead("guest-signin-restrictions", roles) && (
        <NetworkPausesNotice canAct={canWrite("guest-signin-restrictions", roles)} />
      )}

      {httpsActive === false && typedSignInOn(cfg) && (
        <Callout tone="warning" title="Sign-ins are not encrypted yet">
          The client sign-in page is still served over plain HTTP, because the portal has no HTTPS certificate yet. Until
          it has, what clients type to sign in (room and name, passwords, codes) crosses the client network unencrypted,
          and another device on that network could read it. Keep client isolation on for client networks, and finish{" "}
          <Link href="/portal-https" className="underline">Portal HTTPS</Link> before clients rely on these methods.
        </Callout>
      )}

      <SectionCard title="Methods" description="Methods of modules this site is not licensed for are not shown." flush>
        <ul className="divide-y divide-border">
          <MethodRow
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
          <MethodRow
            icon={<KeyRound />}
            title="Client account"
            description="A username and password issued to the client."
            enabled={!!cfg.guest_account?.enabled}
            busy={busy === "Client account"}
            writable={writable}
            onToggle={(v) => save({ guest_account: { ...(cfg.guest_account ?? {}), enabled: v } }, "Client account")}
            manageHref="/guest-accounts"
            manageLabel="Client accounts"
          />
          <MethodRow
            icon={<PackageOpen />}
            title="Choose a package without signing in"
            description={cardMod
              ? "The client picks a Free package, or pays by card, with no credential. A recovery code resumes it later."
              : "The client picks a Free package with no credential. A recovery code resumes it later."}
            enabled={!!cfg.open?.enabled}
            busy={busy === "Open package selection"}
            writable={writable}
            onToggle={(v) => save({ open: { ...(cfg.open ?? {}), enabled: v } }, "Open package selection")}
            manageHref="/payment-methods"
            manageLabel="Payment methods"
          />
          {hospitality && (
            <MethodRow
              icon={<Hotel />}
              title="Room sign-in"
              description="Room number and one detail from the reservation, checked against the PMS."
              enabled={!!pms.enabled}
              busy={busy === "Room sign-in"}
              writable={writable}
              onToggle={(v) => save({ pms: { ...pms, enabled: v, mode: pms.mode || "room_lastname" } }, "Room sign-in")}
              warning={pms.enabled && roomSignInImpaired(pmsReadiness)
                ? pmsReadiness.state === "down"
                  ? "Not working on any network that offers it — see PMS routing."
                  : "Not working on some client networks — see PMS routing."
                : undefined}
              manageHref="/room-sign-in"
              manageLabel="Room sign-in settings — under Hotel"
            />
          )}
          {emailMod && (
            <MethodRow
              icon={<Mail />}
              title="Email accounts"
              description="The client registers with their email address and a password, then signs in with them."
              enabled={!!cfg.email_account?.enabled}
              busy={busy === "Email accounts"}
              writable={writable}
              onToggle={(v) => save({ email_account: { ...(cfg.email_account ?? {}), enabled: v } }, "Email accounts")}
              // A missing sender warns but does not block the switch.
              warning={emailReady ? undefined
                : "No email sender is switched on under Delivery, so verification and password-reset emails cannot be sent."}
              manageHref="/notifications"
              manageLabel="Delivery"
            />
          )}
          {smsMod && (
            <MethodRow
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
              manageLabel="Delivery"
            />
          )}
          {whatsappMod && (
            <MethodRow
              icon={<Smartphone />}
              title="WhatsApp code"
              description="The client receives a one-time code on WhatsApp for their phone number."
              enabled={!!cfg.whatsapp?.enabled}
              busy={busy === "WhatsApp code"}
              writable={writable}
              onToggle={(v) => save({ whatsapp: { ...(cfg.whatsapp ?? {}), enabled: v } }, "WhatsApp code")}
              ready={whatsappReady}
              notReadyReason="Not available until a WhatsApp sender exists and is switched on, so codes cannot be sent yet."
              manageHref="/notifications"
              manageLabel="Delivery"
            />
          )}
          {socialMod && (
            <li className="flex flex-col gap-3 px-5 py-4 sm:flex-row sm:items-start">
              <MethodIcon icon={<Users />} />
              <div className="min-w-0 flex-1 space-y-2.5">
                <div>
                  <div className="text-emphasis">Identity providers</div>
                  <p className="text-sm text-muted-foreground">The client signs in with an existing account such as Google.</p>
                </div>
                {socialListed.length === 0 ? (
                  <div className="flex flex-wrap items-center gap-2 text-sm">
                    <Badge tone="default">Not available</Badge>
                    <span className="text-muted-foreground">No identity provider is configured.</span>
                    <Link href="/social-providers" className="inline-flex items-center gap-0.5 text-primary underline underline-offset-2 hover:decoration-2">
                      Set one up in Identity providers <ArrowUpRight className="size-3.5" aria-hidden />
                    </Link>
                  </div>
                ) : (
                  <ul className="grid gap-2 sm:grid-cols-2">
                    {socialListed.map((p) => {
                      const on = !!cfg.social?.[p]?.enabled;
                      const ready = socialReady.includes(p);
                      const name = SOCIAL_LABELS[p] ?? p;
                      return (
                        <li key={p} className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-border px-3.5 py-2.5">
                          {writable ? (
                            <label className="flex cursor-pointer items-center gap-2.5 text-sm">
                              <input
                                type="checkbox"
                                className="size-4 accent-primary"
                                checked={on}
                                disabled={busy === `Social (${p})` || (!ready && !on)}
                                onChange={(e) =>
                                  save({ social: { ...(cfg.social ?? {}), [p]: { ...(cfg.social?.[p] ?? {}), enabled: e.target.checked } } },
                                    `Social (${p})`)}
                              />
                              <span className="capitalize">{name}</span>
                            </label>
                          ) : (
                            <span className="text-sm capitalize">{name}</span>
                          )}
                          {ready
                            ? <StatusBadge tone={on ? "ok" : "default"}>{on ? "Offered" : "Not offered"}</StatusBadge>
                            : <Badge tone="warn">Not set up — switch it off or set it up under Identity providers</Badge>}
                        </li>
                      );
                    })}
                  </ul>
                )}
              </div>
            </li>
          )}
        </ul>
      </SectionCard>

      <NetworkSignInOverview facts={facts} />

      {emailMod && <EmailAccountsSettingsCard canEdit={writable} />}

      {(smsMod || whatsappMod) && roles !== null && canRead("code-requests", roles) && <CodeRequestsCard roles={roles} />}

      <ClientJourneyCard
        cfg={cfg}
        writable={writable}
        busy={busy === "Client journey"}
        hospitality={hospitality}
        onSave={(portal) => save({ portal }, "Client journey")}
      />

      {/* Sign-in protection counts wrong ROOM details (Room sign-in only), so it needs Hospitality. */}
      {hospitality && <GuestSignInProtectionCard canWrite={mayChangeProtection} />}
      {hospitality && <NetworkPauseSettingsCard canWrite={mayChangeProtection} />}
    </PageShell>
  );
}

// Explicit Save, unlike the switches: saving per keystroke would write "3", "30", "300" for a typed day count.
function ClientJourneyCard({ cfg, writable, busy, hospitality, onSave }: {
  cfg: AuthMethods; writable: boolean; busy: boolean; hospitality: boolean;
  onSave: (portal: PortalJourney) => void;
}) {
  const stored = cfg.portal ?? {};
  const storedPrimary: PrimaryMethod = stored.primary_method ?? "";
  const storedDays = typeof stored.remember_device_days === "number" ? stored.remember_device_days : REMEMBER_DAYS_DEFAULT;
  const [primary, setPrimary] = useState<PrimaryMethod>(storedPrimary);
  const [days, setDays] = useState(String(storedDays));
  useEffect(() => { setPrimary(storedPrimary); setDays(String(storedDays)); }, [storedPrimary, storedDays]);

  // Only switched-on methods can lead; a stored choice since switched off stays listed so the fallback is explained.
  const enabledMethods = useMemo(() => {
    const on: Exclude<PrimaryMethod, "">[] = [];
    if (hospitality && cfg.pms?.enabled) on.push("pms");
    if (cfg.email_account?.enabled) on.push("email_account");
    if (cfg.sms?.enabled) on.push("sms");
    if (cfg.whatsapp?.enabled) on.push("whatsapp");
    if (cfg.guest_account?.enabled) on.push("guest_account");
    if (cfg.voucher?.enabled) on.push("voucher");
    return on;
  }, [cfg, hospitality]);
  const storedOff = storedPrimary !== "" && !enabledMethods.includes(storedPrimary);

  const n = Number(days);
  const daysValid = days.trim() !== "" && Number.isInteger(n) && n >= REMEMBER_DAYS_MIN && n <= REMEMBER_DAYS_MAX;
  const dirty = primary !== storedPrimary || (daysValid && n !== storedDays);

  return (
    <Card>
      <CardHeader>
        <div className="min-w-0">
          <CardTitle>Client journey</CardTitle>
          <CardDescription>What the client sees first, and whether a verified device is asked for a code again.</CardDescription>
        </div>
      </CardHeader>
      <CardBody>
        <form
          className="grid gap-4 md:grid-cols-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (!daysValid || !writable) return;
            // The whole `portal` key is replaced, so spread the stored value to keep other fields.
            onSave({ ...stored, primary_method: primary, remember_device_days: n });
          }}
        >
          <Field
            label="Primary method"
            hint="Shown first on the portal. Identity providers appear above it; other methods under 'Or sign in with'."
          >
            <Select value={primary} disabled={!writable || busy} onChange={(e) => setPrimary(e.target.value as PrimaryMethod)}>
              <option value="">Automatic</option>
              {enabledMethods.map((m) => <option key={m} value={m}>{PRIMARY_LABELS[m]}</option>)}
              {storedOff && <option value={storedPrimary}>{PRIMARY_LABELS[storedPrimary]} (switched off)</option>}
            </Select>
          </Field>
          <SettingField
            label="Remember devices"
            value={days}
            onChange={setDays}
            unit="days"
            min={REMEMBER_DAYS_MIN}
            max={REMEMBER_DAYS_MAX}
            defaultValue={REMEMBER_DAYS_DEFAULT}
            readOnly={!writable || busy}
            explanation="How long a device that verified a code or identity provider reconnects without another code. Only over the portal's HTTPS address. 0 = always ask."
          />
          {storedOff && (
            <p className="text-xs text-warning-subtle-foreground md:col-span-2" role="status">
              {PRIMARY_LABELS[storedPrimary]} is the stored primary method but is switched off, so the portal uses the automatic order until it is switched on or another method is chosen.
            </p>
          )}
          {writable && (
            <div className="flex items-center justify-end gap-3 border-t border-border pt-4 md:col-span-2">
              {dirty && !busy && <span className="text-xs text-muted-foreground">Not saved yet.</span>}
              <Button type="submit" disabled={!dirty || !daysValid || busy}>{busy ? "Saving…" : "Save journey"}</Button>
            </div>
          )}
        </form>
      </CardBody>
    </Card>
  );
}

function MethodIcon({ icon }: { icon: React.ReactNode }) {
  return (
    <span className="flex size-9 shrink-0 items-center justify-center rounded-lg border border-border bg-surface text-muted-foreground [&_svg]:size-4" aria-hidden>
      {icon}
    </span>
  );
}

// A method whose provider is not ready is shown unavailable with its switch disabled; not ready blocks switching
// on, never off.
function MethodRow({
  icon, title, description, enabled, busy, writable, onToggle, ready = true, notReadyReason, warning, manageHref, manageLabel,
}: {
  icon: React.ReactNode; title: string; description: string;
  enabled: boolean; busy: boolean; writable: boolean; onToggle: (v: boolean) => void;
  ready?: boolean; notReadyReason?: string;
  /** Does not block the switch. */
  warning?: string;
  manageHref?: string; manageLabel?: string;
}) {
  return (
    <li className={cn("flex items-start gap-3 px-5 py-4", !ready && "bg-surface/50")}>
      <MethodIcon icon={icon} />
      <div className="min-w-0 flex-1 space-y-1">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <span className="text-emphasis">{title}</span>
          <StatusBadge tone={enabled ? "ok" : "default"}>{enabled ? "On" : "Off"}</StatusBadge>
          {!ready && <Badge tone="warn">Not available</Badge>}
        </div>
        <p className="text-sm text-muted-foreground">{description}</p>
        {!ready && notReadyReason && <p className="text-xs text-warning-subtle-foreground">{notReadyReason}</p>}
        {ready && warning && <p className="text-xs text-warning-subtle-foreground" role="status">{warning}</p>}
        {manageHref && (
          <Link
            href={manageHref}
            className="inline-flex w-fit items-center gap-0.5 text-xs font-medium text-primary underline-offset-4 hover:underline"
          >
            {manageLabel} <ArrowUpRight className="size-3.5" aria-hidden />
          </Link>
        )}
      </div>
      {writable && (
        <div className="flex shrink-0 items-center gap-2 pt-1">
          {busy && <span className="text-xs text-muted-foreground" aria-live="polite">Saving…</span>}
          <Switch checked={enabled} disabled={busy || (!ready && !enabled)} onCheckedChange={onToggle} label={title} />
        </div>
      )}
    </li>
  );
}
