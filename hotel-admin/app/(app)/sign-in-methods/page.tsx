"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import { api, ListResp } from "@/lib/api";
import type { PortalHTTPSDoc } from "@/lib/portal-https";
import { typedSignInOn } from "@/lib/sign-in-methods";
import { canRead, canWrite } from "@/lib/roles";
import { GuestSignInProtectionCard } from "@/components/guest-signin-protection";
import { NetworkPauseSettingsCard, NetworkPausesNotice } from "@/components/guest-signin-network-pause";
import { SIGNIN_SETTING_GROUPS, SigninSettingsGroup } from "@/components/devices/signin-limits-card";
import { CodeRequestsCard } from "@/components/code-requests";
import { EmailAccountsSettingsCard } from "@/components/email-accounts-settings";
import { roomSignInImpaired, useRoomSignInReadiness } from "@/components/room-sign-in-readiness";
import { Card, CardBody, CardDescription, CardHeader, CardTitle, SectionCard } from "@/components/ui/card";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, QuietHints, Select } from "@/components/ui/input";
import { InfoHint } from "@/components/ui/quiet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Callout } from "@/components/ui/error-banner";
import { EmptyState } from "@/components/ui/empty-state";
import { ConfirmDialog } from "@/components/ui/dialog";
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
import { Ticket, Hotel, KeyRound, Mail, MessageSquare, Users, ArrowUpRight, PackageOpen, Smartphone, AlertTriangle, RefreshCw } from "lucide-react";
import { useOperatorRoles } from "@/lib/whoami-context";

type Method = { enabled?: boolean };
type PMSMethod = { enabled?: boolean; mode?: string; provider?: string; template_id?: string };
type PrimaryMethod = "" | "pms" | "email_account" | "sms" | "whatsapp" | "guest_account" | "voucher";
type PortalJourney = { primary_method?: PrimaryMethod; remember_device_days?: number };
type AuthMethods = {
  voucher?: Method;
  guest_account?: Method;
  open?: Method;
  email_account?: Method;
  sms?: Method;
  whatsapp?: Method;
  social?: Record<string, Method>;
  pms?: PMSMethod;
  portal?: PortalJourney;
};

const REMEMBER_DAYS_DEFAULT = 30;
const REMEMBER_DAYS_MIN = 0;
const REMEMBER_DAYS_MAX = 365;

const PRIMARY_LABELS: Record<Exclude<PrimaryMethod, "">, string> = {
  pms: "Room sign-in", email_account: "Email account", sms: "SMS code", whatsapp: "WhatsApp code",
  guest_account: "Client account", voucher: "Voucher code",
};

type SocialProvider = { provider: string; enabled: boolean };

const SOCIAL_LABELS: Record<string, string> = { google: "Google", apple: "Apple", facebook: "Facebook", microsoft: "Microsoft" };

type ReadState = "unknown" | "known" | "failed";

function readable(e: unknown, fallback: string): string {
  const m = e instanceof Error ? e.message : typeof e === "string" ? e : "";
  return m && /\s/.test(m) && !/[_{}]/.test(m) ? m : fallback;
}

type SignInTab = "methods" | "networks" | "email" | "codes" | "protection";
const SIGN_IN_TABS: readonly SignInTab[] = ["methods", "networks", "email", "codes", "protection"];

function tabFromHash(): SignInTab {
  if (typeof window === "undefined") return "methods";
  const h = window.location.hash.replace(/^#/, "") as SignInTab;
  return SIGN_IN_TABS.includes(h) ? h : "methods";
}

export default function SignInMethodsPage() {
  const toast = useToast();
  const roles = useOperatorRoles();
  const writable = roles === null ? false : canWrite("auth-methods", roles);
  const mayChangeProtection = roles === null ? false : canWrite("guest-signin-protection", roles);

  const caps = useCapabilities();
  const hospitality = moduleLicensed(caps, "hospitality");
  const emailMod = moduleLicensed(caps, "email_otp");
  const smsMod = moduleLicensed(caps, "sms_otp");
  const whatsappMod = moduleLicensed(caps, "whatsapp_otp");
  const socialMod = moduleLicensed(caps, "social_login");
  const cardMod = moduleLicensed(caps, "card_payment");

  const [cfg, setCfg] = useState<AuthMethods | null>(null);
  const [loadErr, setLoadErr] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);
  const [notify, setNotify] = useState<NotifyProvider[]>([]);
  const [notifyState, setNotifyState] = useState<ReadState>("unknown");
  const [social, setSocial] = useState<SocialProvider[]>([]);
  const [socialState, setSocialState] = useState<ReadState>("unknown");
  const [httpsActive, setHttpsActive] = useState<boolean | null>(null);
  const [saveErr, setSaveErr] = useState<{ label: string; message: string } | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [pendingOff, setPendingOff] = useState<{ label: string; title: string; patch: Partial<AuthMethods>; last: boolean } | null>(null);
  const [journeyDirty, setJourneyDirty] = useState(false);
  const [journeyKey, setJourneyKey] = useState(0);
  const [askTab, setAskTab] = useState<SignInTab | null>(null);
  const keepRef = useRef<HTMLButtonElement>(null);
  const [tab, setTab] = useState<SignInTab>("methods");
  useEffect(() => {
    setTab(tabFromHash());
    const onHash = () => setTab(tabFromHash());
    window.addEventListener("hashchange", onHash);
    return () => window.removeEventListener("hashchange", onHash);
  }, []);
  const goTab = useCallback((t: SignInTab) => {
    setTab(t);
    window.history.replaceState(null, "", t === "methods" ? window.location.pathname + window.location.search : `#${t}`);
  }, []);
  const selectTab = useCallback((t: SignInTab) => {
    if (journeyDirty && t !== "methods") { setAskTab(t); return; }
    goTab(t);
  }, [journeyDirty, goTab]);
  useEffect(() => { if (askTab) keepRef.current?.focus(); }, [askTab]);
  const { readiness: pmsReadiness } = useRoomSignInReadiness(hospitality);

  const notifyReadable = (emailMod || smsMod || whatsappMod) && roles !== null && canRead("notification-providers", roles);
  const socialReadable = socialMod && roles !== null && canRead("social-providers", roles);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const m = await api.get<AuthMethods>("/auth-methods");
      setCfg(m ?? {});
      setLoadErr(null);
    } catch (e) {
      setLoadErr(e);
    } finally {
      setLoading(false);
    }
    if (notifyReadable) {
      try {
        const n = await api.get<ListResp<NotifyProvider>>("/notification-providers");
        setNotify(n.data ?? []);
        setNotifyState("known");
      } catch {
        setNotifyState("failed");
      }
    }
    if (roles !== null && canRead("portal-https", roles)) {
      try {
        const d = await api.get<PortalHTTPSDoc>("/portal-https");
        setHttpsActive(typeof d?.state?.https_active === "boolean" ? d.state.https_active : null);
      } catch {
        setHttpsActive(null);
      }
    }
    if (socialReadable) {
      try {
        const s = await api.get<ListResp<SocialProvider>>("/social-providers");
        setSocial(s.data ?? []);
        setSocialState("known");
      } catch {
        setSocialState("failed");
      }
    }
  }, [notifyReadable, socialReadable, roles]);
  useEffect(() => { void load(); }, [load]);

  const save = useCallback(async (patch: Partial<AuthMethods>, label: string) => {
    setBusy(label); setSaveErr(null);
    try {
      const updated = await api.put<AuthMethods>("/auth-methods", patch);
      setCfg(updated ?? {});
      toast.success(`${label} saved`, "Clients see the change the next time the sign-in page loads.");
    } catch (e) {
      setSaveErr({ label, message: `Not saved. ${readable(e, "The appliance did not accept the change; try again.")}` });
      await load();
    } finally { setBusy(null); }
  }, [load, toast]);

  const senderKnown = notifyReadable && notifyState === "known";
  const socialKnown = socialReadable && socialState === "known";
  const emailReady = useMemo(() => senderReady(notify, "email"), [notify]);
  const smsReady = useMemo(() => senderReady(notify, "sms"), [notify]);
  const whatsappReady = useMemo(() => senderReady(notify, "whatsapp"), [notify]);
  const socialReady = useMemo(() => (socialKnown ? social.filter((p) => p.enabled).map((p) => p.provider) : []), [social, socialKnown]);
  const socialListed = useMemo(() => {
    const on = Object.entries(cfg?.social ?? {}).filter(([, m]) => m?.enabled).map(([p]) => p);
    return Array.from(new Set([...socialReady, ...on]));
  }, [socialReady, cfg]);

  const header = (
    <PageHeader
      title="Sign-in methods"
      description="How clients sign in, for the whole site. A network can narrow these: see By network."
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
              can try again. The Protection tab decides how strict that is; it is always on.
            </p>
          </HelpSection>
          )}
        </>
      }
    />
  );

  if (cfg === null) {
    if (loadErr) {
      return (
        <PageShell>
          {header}
          <EmptyState
            icon={<AlertTriangle />}
            title="Sign-in methods could not be loaded"
            hint="The appliance did not return its sign-in settings, so none are shown rather than showing every method as off. Nothing has been changed."
            action={
              <Button variant="secondary" disabled={loading} onClick={() => void load()}>
                <RefreshCw className={cn(loading && "animate-spin motion-reduce:animate-none")} /> Try again
              </Button>
            }
          />
        </PageShell>
      );
    }
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
      email: emailMod && senderKnown ? emailReady : null,
      sms: smsMod && senderKnown ? smsReady : null,
      whatsapp: whatsappMod && senderKnown ? whatsappReady : null,
    },
    socialProviders: socialKnown ? socialReady : null,
  };

  const locked = busy !== null;
  const socialOn = socialMod ? Object.values(cfg.social ?? {}).filter((m) => m?.enabled).length : 0;
  const onCount = [
    cfg.voucher?.enabled,
    cfg.guest_account?.enabled,
    cfg.open?.enabled,
    hospitality && pms.enabled,
    emailMod && cfg.email_account?.enabled,
    smsMod && cfg.sms?.enabled,
    whatsappMod && cfg.whatsapp?.enabled,
  ].filter(Boolean).length + socialOn;
  const primaryNow = cfg.portal?.primary_method ?? "";
  const rowError = (label: string) => (saveErr?.label === label ? saveErr.message : undefined);
  const toggle = (label: string, title: string, on: boolean, patch: Partial<AuthMethods>, primaryKey?: PrimaryMethod) => {
    if (on) { void save(patch, label); return; }
    const last = onCount <= 1;
    const primary = !!primaryKey && primaryNow === primaryKey;
    if (last || primary) setPendingOff({ label, title, patch, last });
    else void save(patch, label);
  };
  const senderWarning = (ready: boolean, what: string, missing: string) =>
    notifyState === "failed" && notifyReadable
      ? `The ${what} sender under Delivery could not be checked, so whether it works is unknown.`
      : senderKnown && !ready ? missing : undefined;

  const showCodes = roles !== null && (canRead("auth-methods", roles) || ((smsMod || whatsappMod) && canRead("code-requests", roles)));
  const showProtection = hospitality || (roles !== null && canRead("auth-methods", roles));
  const tabs: { value: SignInTab; label: string }[] = [
    { value: "methods", label: "Methods" },
    { value: "networks", label: "By network" },
    ...(emailMod ? [{ value: "email" as const, label: "Email accounts" }] : []),
    ...(showCodes ? [{ value: "codes" as const, label: "One-time codes" }] : []),
    ...(showProtection ? [{ value: "protection" as const, label: "Protection" }] : []),
  ];
  const activeTab = tabs.some((t) => t.value === tab) ? tab : "methods";

  return (
    <PageShell>
      {header}

      {roles !== null && !writable && (
        <ReadOnlyNotice>Your role can see which sign-in methods are offered but not change them.</ReadOnlyNotice>
      )}
      {loadErr !== null && (
        <Callout tone="warning" title="The sign-in settings could not be re-read">
          What is shown is the last answer from the appliance and may be out of date.{" "}
          <button type="button" className="underline underline-offset-2" disabled={loading} onClick={() => void load()}>Try again</button>
        </Callout>
      )}

      {onCount === 0 && (
        <Callout tone="warning" title="No sign-in method is on">
          New clients cannot connect until a method is switched on below. Clients already online stay connected.
        </Callout>
      )}

      {hospitality && roles !== null && canRead("guest-signin-restrictions", roles) && (
        <NetworkPausesNotice canAct={canWrite("guest-signin-restrictions", roles)} />
      )}

      {httpsActive === false && typedSignInOn(cfg) && (
        <Callout tone="warning" title="Sign-ins are not encrypted yet">
          The sign-in page is still plain HTTP, so what clients type crosses the client network unencrypted. Keep client
          isolation on, and finish <Link href="/portal-https" className="underline">Portal HTTPS</Link> before clients rely on
          these methods.
        </Callout>
      )}

      {askTab && (
        <div className={cn("flex flex-wrap items-center gap-3 rounded-md border border-border bg-surface px-4 py-3", "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none")}>
          <p role="status" className="me-auto text-sm">Discard the changes you made to the client journey?</p>
          <Button ref={keepRef} type="button" variant="secondary" onClick={() => setAskTab(null)}>Keep editing</Button>
          <Button
            type="button"
            variant="danger"
            onClick={() => {
              const t = askTab;
              setAskTab(null);
              setJourneyDirty(false);
              setJourneyKey((k) => k + 1);
              goTab(t);
            }}
          >
            Discard changes
          </Button>
        </div>
      )}

      <Tabs value={activeTab} onValueChange={(v) => selectTab(v as SignInTab)}>
        <TabsList aria-label="Sign-in methods sections">
          {tabs.map((t) => <TabsTrigger key={t.value} value={t.value}>{t.label}</TabsTrigger>)}
        </TabsList>

        <TabsContent value="methods" className="mt-5 space-y-5">
          <SectionCard title="Methods" description="Each switch applies immediately; clients already online stay connected. Methods of modules this site is not licensed for are not shown." flush>
            <ul className="divide-y divide-border">
              <MethodRow
                icon={<Ticket />}
                title="Voucher code"
                description="The client types a code from a printed or emailed voucher."
                enabled={!!cfg.voucher?.enabled}
                busy={busy === "Voucher"}
                locked={locked}
                error={rowError("Voucher")}
                writable={writable}
                onToggle={(v) => toggle("Voucher", "Voucher code", v, { voucher: { ...(cfg.voucher ?? {}), enabled: v } }, "voucher")}
                manageHref="/vouchers"
                manageLabel="Vouchers"
              />
              <MethodRow
                icon={<KeyRound />}
                title="Client account"
                description="A username and password issued to the client."
                enabled={!!cfg.guest_account?.enabled}
                busy={busy === "Client account"}
                locked={locked}
                error={rowError("Client account")}
                writable={writable}
                onToggle={(v) => toggle("Client account", "Client account", v, { guest_account: { ...(cfg.guest_account ?? {}), enabled: v } }, "guest_account")}
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
                locked={locked}
                error={rowError("Open package selection")}
                writable={writable}
                onToggle={(v) => toggle("Open package selection", "Choose a package without signing in", v, { open: { ...(cfg.open ?? {}), enabled: v } })}
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
                  locked={locked}
                  error={rowError("Room sign-in")}
                  writable={writable}
                  onToggle={(v) => toggle("Room sign-in", "Room sign-in", v, { pms: { ...pms, enabled: v, mode: pms.mode || "room_lastname" } }, "pms")}
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
                  locked={locked}
                  error={rowError("Email accounts")}
                  writable={writable}
                  onToggle={(v) => toggle("Email accounts", "Email accounts", v, { email_account: { ...(cfg.email_account ?? {}), enabled: v } }, "email_account")}
                  warning={senderWarning(emailReady, "email",
                    "No email sender is switched on under Delivery, so verification and password-reset emails cannot be sent.")}
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
                  locked={locked}
                  error={rowError("SMS code")}
                  writable={writable}
                  onToggle={(v) => toggle("SMS code", "SMS code", v, { sms: { ...(cfg.sms ?? {}), enabled: v } }, "sms")}
                  ready={senderKnown ? smsReady : true}
                  warning={senderWarning(true, "text-message", "")}
                  notReadyReason="Not available until a text-message sender is switched on under Delivery."
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
                  locked={locked}
                  error={rowError("WhatsApp code")}
                  writable={writable}
                  onToggle={(v) => toggle("WhatsApp code", "WhatsApp code", v, { whatsapp: { ...(cfg.whatsapp ?? {}), enabled: v } }, "whatsapp")}
                  ready={senderKnown ? whatsappReady : true}
                  warning={senderWarning(true, "WhatsApp", "")}
                  notReadyReason="Not available until a WhatsApp sender is switched on under Delivery."
                  manageHref="/notifications"
                  manageLabel="Delivery"
                />
              )}
              {socialMod && (
                <li className="flex flex-col gap-3 px-5 py-3.5 sm:flex-row sm:items-start">
                  <MethodIcon icon={<Users />} />
                  <div className="min-w-0 flex-1 space-y-2">
                    <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
                      <span className="text-emphasis">Identity providers</span>
                      <InfoHint>The client signs in with an existing account such as Google. Each provider is offered on its own.</InfoHint>
                      {socialListed.length > 0 && (
                        <Link href="/social-providers" className="inline-flex items-center gap-0.5 text-xs font-medium text-primary underline-offset-4 hover:underline">
                          Identity providers <ArrowUpRight className="size-3.5" aria-hidden />
                        </Link>
                      )}
                    </div>
                    {socialReadable && socialState === "failed" && (
                      <p className={cn("text-xs text-warning-subtle-foreground", "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none")} role="status">
                        Identity providers could not be checked, so whether each one works is unknown.
                      </p>
                    )}
                    {socialListed.length === 0 ? (
                      socialState === "failed" ? null :
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
                          const ready = socialKnown ? socialReady.includes(p) : true;
                          const name = SOCIAL_LABELS[p] ?? p;
                          const label = `Social (${p})`;
                          const error = rowError(label);
                          return (
                            <li key={p} className="space-y-1.5 rounded-md border border-border px-3.5 py-2.5">
                              <div className="flex flex-wrap items-center justify-between gap-2">
                                <div className="flex items-center gap-2">
                                  <span className="text-sm capitalize">{name}</span>
                                  {ready
                                    ? <StatusBadge tone={on ? "ok" : "default"}>{on ? "Offered" : "Not offered"}</StatusBadge>
                                    : <Badge tone="warn">Not set up</Badge>}
                                </div>
                                {writable && (
                                  <div className="flex items-center gap-2">
                                    {busy === label && <span className="text-xs text-muted-foreground" aria-live="polite">Saving…</span>}
                                    <Switch
                                      checked={on}
                                      disabled={locked || (!ready && !on)}
                                      label={name}
                                      onCheckedChange={(v) =>
                                        toggle(label, name, v, { social: { ...(cfg.social ?? {}), [p]: { ...(cfg.social?.[p] ?? {}), enabled: v } } })}
                                    />
                                  </div>
                                )}
                              </div>
                              {!ready && (
                                <p className="text-xs text-warning-subtle-foreground">Switch it off, or set it up under Identity providers.</p>
                              )}
                              {error && <p className={cn("text-xs text-destructive", "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none")} role="alert">{error}</p>}
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

          <ClientJourneyCard
            key={journeyKey}
            cfg={cfg}
            writable={writable}
            busy={busy === "Client journey"}
            locked={locked}
            error={rowError("Client journey")}
            httpsActive={httpsActive}
            hospitality={hospitality}
            onDirtyChange={setJourneyDirty}
            onSave={(portal) => save({ portal }, "Client journey")}
          />
        </TabsContent>

        <TabsContent value="networks" className="mt-5">
          <NetworkSignInOverview facts={facts} />
        </TabsContent>

        {emailMod && (
          <TabsContent value="email" className="mt-5">
            <EmailAccountsSettingsCard canEdit={writable} />
          </TabsContent>
        )}

        {showCodes && (
          <TabsContent value="codes" className="mt-5 space-y-5">
            {roles !== null && canRead("auth-methods", roles) && (
              <SigninSettingsGroup title="One-time codes" keys={SIGNIN_SETTING_GROUPS.oneTimeCodes} canEdit={writable}
                hint="How long a code sent by email, text or WhatsApp lasts, how many wrong guesses it allows, and how many codes may be requested per hour." />
            )}
            {(smsMod || whatsappMod) && roles !== null && canRead("code-requests", roles) && <CodeRequestsCard roles={roles} />}
          </TabsContent>
        )}

        {showProtection && (
          <TabsContent value="protection" className="mt-5 space-y-5">
            <QuietHints>
              {hospitality && <GuestSignInProtectionCard canWrite={mayChangeProtection} />}
              {hospitality && <NetworkPauseSettingsCard canWrite={mayChangeProtection} />}
            </QuietHints>
            {roles !== null && canRead("auth-methods", roles) && (
              <SigninSettingsGroup title="Sign-in protection" keys={SIGNIN_SETTING_GROUPS.protection} canEdit={writable}
                hint="The most sign-ins the whole appliance accepts per minute, and how long recent room guests are exempt from a room sign-in pause." />
            )}
          </TabsContent>
        )}
      </Tabs>

      <ConfirmDialog
        open={pendingOff !== null}
        onOpenChange={(o) => { if (!o) setPendingOff(null); }}
        title={`Switch off ${pendingOff?.title ?? ""}?`}
        description={pendingOff?.last
          ? "It is the only sign-in method that is on. New clients will have no way to sign in until another method is switched on. Clients already online stay connected."
          : "It is the primary method on the portal. The portal will use the automatic order until it is switched on again or another primary method is chosen."}
        confirmLabel="Switch off"
        confirmVariant="danger"
        onConfirm={() => {
          const p = pendingOff;
          setPendingOff(null);
          if (p) void save(p.patch, p.label);
        }}
      />
    </PageShell>
  );
}

function ClientJourneyCard({ cfg, writable, busy, locked, error, httpsActive, hospitality, onDirtyChange, onSave }: {
  cfg: AuthMethods; writable: boolean; busy: boolean; locked: boolean; error?: string; httpsActive: boolean | null;
  hospitality: boolean; onDirtyChange: (dirty: boolean) => void;
  onSave: (portal: PortalJourney) => void;
}) {
  const stored = cfg.portal ?? {};
  const storedPrimary: PrimaryMethod = stored.primary_method ?? "";
  const storedDays = typeof stored.remember_device_days === "number" ? stored.remember_device_days : REMEMBER_DAYS_DEFAULT;
  const [primary, setPrimary] = useState<PrimaryMethod>(storedPrimary);
  const [days, setDays] = useState(String(storedDays));
  useEffect(() => { setPrimary(storedPrimary); setDays(String(storedDays)); }, [storedPrimary, storedDays]);

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
  const dirty = primary !== storedPrimary || days.trim() !== String(storedDays);
  const canSave = primary !== storedPrimary || (daysValid && n !== storedDays);
  const daysError = writable && days.trim() !== "" && Number.isFinite(n) && !Number.isInteger(n)
    ? "Enter a whole number of days from 0 to 365."
    : writable && days.trim() === "" ? "Enter a whole number of days from 0 to 365." : undefined;
  useEffect(() => { onDirtyChange(dirty); }, [dirty, onDirtyChange]);
  useEffect(() => () => onDirtyChange(false), [onDirtyChange]);

  return (
    <Card>
      <CardHeader>
        <div className="min-w-0">
          <CardTitle>Client journey</CardTitle>
          <CardDescription>What the client sees first, and whether a verified device is asked for a code again.</CardDescription>
        </div>
      </CardHeader>
      <CardBody>
        <QuietHints>
        <form
          className="grid gap-4 md:grid-cols-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (!daysValid || !writable || locked) return;
            onSave({ ...stored, primary_method: primary, remember_device_days: n });
          }}
        >
          <Field
            label="Primary method"
            hint="Shown first on the portal. Identity providers appear above it; other methods under 'Or sign in with'."
          >
            <Select value={primary} disabled={!writable || locked} onChange={(e) => setPrimary(e.target.value as PrimaryMethod)}>
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
            readOnly={!writable || locked}
            error={daysError}
            explanation="How long a device that verified a code or identity provider reconnects without another code. Only over the portal's HTTPS address. 0 = always ask."
          />
          {httpsActive === false && (
            <p className="text-xs text-warning-subtle-foreground md:col-span-2" role="status">
              The portal has no HTTPS address yet, so no device is remembered until{" "}
              <Link href="/portal-https" className="underline underline-offset-2">Portal HTTPS setup</Link> is finished.
            </p>
          )}
          {storedOff && (
            <p className="text-xs text-warning-subtle-foreground md:col-span-2" role="status">
              {PRIMARY_LABELS[storedPrimary]} is the stored primary method but is switched off, so the portal uses the automatic order until it is switched on or another method is chosen.
            </p>
          )}
          {writable && (
            <div className="flex flex-wrap items-center justify-end gap-3 border-t border-border pt-4 md:col-span-2">
              {error && <p className={cn("me-auto text-xs text-destructive", "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none")} role="alert">{error}</p>}
              {dirty && !busy && !error && <span className="text-xs text-muted-foreground">Not saved yet.</span>}
              <Button type="submit" disabled={!canSave || !daysValid || locked}>{busy ? "Saving…" : "Save journey"}</Button>
            </div>
          )}
        </form>
        </QuietHints>
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

function MethodRow({
  icon, title, description, enabled, busy, locked, error, writable, onToggle, ready = true, notReadyReason, warning, manageHref, manageLabel,
}: {
  icon: React.ReactNode; title: string; description: string;
  enabled: boolean; busy: boolean; locked: boolean; error?: string; writable: boolean; onToggle: (v: boolean) => void;
  ready?: boolean; notReadyReason?: string;
  warning?: string;
  manageHref?: string; manageLabel?: string;
}) {
  return (
    <li className={cn("flex items-start gap-3 px-5 py-3.5", !ready && "bg-surface/50")}>
      <MethodIcon icon={icon} />
      <div className="min-w-0 flex-1 space-y-1">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <span className="text-emphasis">{title}</span>
          <InfoHint>{description}</InfoHint>
          <StatusBadge tone={enabled ? "ok" : "default"}>{enabled ? "On" : "Off"}</StatusBadge>
          {!ready && <Badge tone="warn">Not available</Badge>}
          {manageHref && (
            <Link
              href={manageHref}
              className="inline-flex w-fit items-center gap-0.5 text-xs font-medium text-primary underline-offset-4 hover:underline sm:ms-1"
            >
              {manageLabel} <ArrowUpRight className="size-3.5" aria-hidden />
            </Link>
          )}
        </div>
        {!ready && notReadyReason && <p className={cn("text-xs text-warning-subtle-foreground", "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none")}>{notReadyReason}</p>}
        {ready && warning && <p className={cn("text-xs text-warning-subtle-foreground", "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none")} role="status">{warning}</p>}
        {error && <p className={cn("text-xs text-destructive", "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none")} role="alert">{error}</p>}
      </div>
      {writable && (
        <div className="flex shrink-0 items-center gap-2 pt-1">
          {busy && <span className="text-xs text-muted-foreground" aria-live="polite">Saving…</span>}
          <Switch checked={enabled} disabled={locked || (!ready && !enabled)} onCheckedChange={onToggle} label={title} />
        </div>
      )}
    </li>
  );
}
