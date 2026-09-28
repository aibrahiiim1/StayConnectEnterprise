"use client";

// PAYMENT METHODS — how a client may acquire an Internet package at this site, and the Card payment setup.
//
// FOUR METHODS, AND ONLY FOUR. Free (a package priced at zero), Voucher (a code the property issues), Card
// payment (through a provider's hosted payment page) and Room charge (posted to the guest's folio in the PMS).
// There is no cash method: money that changes hands at a desk is sold as a voucher, which is the auditable
// instrument for it. There is no refund button either: a refund or chargeback the provider reports is recorded
// in Settlements, and OneGate never originates one.
//
// FREE AND VOUCHER ARE CORE; CARD AND ROOM CHARGE ARE MODULES. The two core methods are always there, so their
// cards only explain themselves. Card payment and Room charge each come from a site module (GET /modules), and
// the card says which of the four things stands between the method and a client: it is not licensed, it is
// switched off for this site, it is not ready (with each readiness reason in words), or it is ready. The words
// for the codes are shared with Hotel → Room charge (lib/payment-admin.ts) so the same code never reads two
// ways.
//
// THE CARD CONFIGURATION APPEARS ONLY ONCE IT CAN BE USED. While Card payment is not manageable — not licensed
// for this site — the accounts, domains and timings are not rendered at all: every one of those requests would
// be refused, and a form that 403s on save is worse than a sentence saying when it will appear.
//
// ROLES FAIL CLOSED WHILE LOADING. Nothing is shown until whoami answers, and a role without read access on
// payment-providers is told so rather than shown an empty page. edged enforces the real gate either way.

import { useEffect, useState } from "react";
import Link from "next/link";
import { api, Whoami } from "@/lib/api";
import { canRead, canWrite } from "@/lib/roles";
import { SiteModule, SiteModules } from "@/lib/payment-admin";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { ErrorBanner } from "@/components/ui/error-banner";
import { PageHeader, PageShell } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { Skeleton } from "@/components/ui/misc";
import { NotAvailable, ReadOnlyNotice } from "@/components/ui/patterns";
import { cn } from "@/lib/utils";
import { ArrowUpRight, BedDouble, CreditCard, Gift, Ticket, Wallet } from "lucide-react";
import { CardPaymentConfig } from "./card-config";
import { CodeList } from "./codes";

type Tone = "ok" | "warn" | "err" | "default" | "info";

type MethodState = {
  tone: Tone;
  label: string;
  /** What to show under the badge: readiness or module reasons, and where to go about it. */
  detail?: React.ReactNode;
};

/** The single state a module-backed method is in, in the order an operator has to fix them. */
function moduleState(m: SiteModule | undefined): MethodState {
  if (!m) {
    return { tone: "default", label: "Not available", detail: <p className="text-xs text-muted-foreground">This appliance does not report this method.</p> };
  }
  if (!m.licensed) {
    return { tone: "default", label: "Not licensed", detail: <p className="text-xs text-muted-foreground">Not included in this site&rsquo;s licence.</p> };
  }
  if (!m.enabled) {
    return {
      tone: "default",
      label: "Switched off",
      detail: (
        <p className="text-xs text-muted-foreground">
          Licensed but switched off for this site.{" "}
          <Link href="/modules" className="inline-flex items-center gap-0.5 text-primary underline-offset-4 hover:underline">
            Modules <ArrowUpRight className="size-3.5" aria-hidden />
          </Link>
        </p>
      ),
    };
  }
  if (!m.ready) {
    return {
      tone: "warn",
      label: "Not ready",
      detail: <CodeList codes={m.readiness ?? []} className="text-xs text-warning-subtle-foreground" />,
    };
  }
  if (!m.effective) {
    return {
      tone: "warn",
      label: "Not in effect",
      detail: <CodeList codes={m.reasons ?? []} className="text-xs text-warning-subtle-foreground" />,
    };
  }
  return { tone: "ok", label: "Ready" };
}

export default function PaymentMethodsPage() {
  const [roles, setRoles] = useState<string[] | null>(null);
  useEffect(() => {
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => setRoles([]));
  }, []);
  const readable = roles === null ? false : canRead("payment-providers", roles);
  const writable = roles === null ? false : canWrite("payment-providers", roles);

  const [mods, setMods] = useState<SiteModules | null>(null);
  const [err, setErr] = useState<unknown>(null);
  useEffect(() => {
    if (!readable) return;
    api.get<SiteModules>("/modules")
      .then((m) => setMods(m ?? { site_type: null, modules: {} }))
      .catch((e) => { setErr(e); setMods({ site_type: null, modules: {} }); });
  }, [readable]);

  const header = (
    <PageHeader
      icon={<Wallet />}
      eyebrow="Internet offering"
      title="Payment methods"
      description="How clients may acquire Internet packages at this site."
      help={
        <>
          <HelpSection title="The methods">
            <HelpList
              items={[
                <><strong>Free</strong> — a package priced at zero. The client simply chooses it.</>,
                <><strong>Voucher</strong> — the client types a code the property issued. Codes are printed and managed under Vouchers.</>,
                ...(mods?.modules?.card_payment?.licensed ? [<><strong>Card payment</strong> — the client pays on the payment provider&rsquo;s own page. OneGate never sees the card number.</>] : []),
                ...(mods?.modules?.room_charge?.licensed ? [<><strong>Room charge</strong> — a verified hotel guest charges the package to their room, posted to their folio in the PMS.</>] : []),
              ]}
            />
          </HelpSection>
          {mods?.modules?.card_payment?.licensed && (
            <>
          <HelpSection title="Card payment setup">
            <p>
              Add the merchant account your provider gave you, test the connection, and choose the timings. Stored
              credentials are never shown again — leave a field empty to keep what is stored. Every change asks for a
              reason and your password and is kept in the change history.
            </p>
          </HelpSection>
          <HelpSection title="Refunds and chargebacks">
            <p>
              OneGate does not start refunds. When the provider reports a refund or a chargeback, it is recorded in
              Settlements.
            </p>
          </HelpSection>
            </>
          )}
        </>
      }
    />
  );

  if (roles === null || (readable && mods === null)) {
    return (
      <PageShell>
        {header}
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4" aria-busy="true">
          <span className="sr-only">Loading payment methods</span>
          {[0, 1, 2, 3].map((i) => <Skeleton key={i} className="h-36" />)}
        </div>
      </PageShell>
    );
  }

  if (!readable) {
    return (
      <PageShell>
        {header}
        <NotAvailable title="Not available to your role" reason="Your role cannot see how payments are configured at this site." />
      </PageShell>
    );
  }

  const card = mods?.modules?.card_payment;
  const room = mods?.modules?.room_charge;
  // A method this site is not licensed for is not presented at all: a café does not look like it charges rooms,
  // and a site without Card payment does not look like it takes cards. Settlements keep any history.
  const cardLicensed = !!card?.licensed && !!card?.deployed;
  const roomLicensed = !!room?.licensed && !!room?.deployed;
  const cardState = moduleState(card);
  const roomState = moduleState(room);

  return (
    <PageShell>
      {header}
      {!writable && <ReadOnlyNotice>Your role can see how payments are configured but not change it.</ReadOnlyNotice>}
      <ErrorBanner err={err} className="mb-0" />

      <section aria-label="Payment methods" className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <MethodCard
          icon={<Gift />}
          title="Free"
          state={{ tone: "ok", label: "Always available" }}
          description="A package priced at 0 is free: the client chooses it and is online, with no payment step."
          link={{ href: "/internet-packages", label: "Internet packages" }}
        />
        <MethodCard
          icon={<Ticket />}
          title="Voucher"
          state={{ tone: "ok", label: "Always available" }}
          description="The client types a code the property issued, on a printed card or by email."
          link={{ href: "/vouchers", label: "Vouchers" }}
        />
        {cardLicensed && (
          <MethodCard
            icon={<CreditCard />}
            title="Card payment"
            state={cardState}
            description="The client pays on the payment provider's hosted page."
          />
        )}
        {roomLicensed && (
          <MethodCard
            icon={<BedDouble />}
            title="Room charge"
            state={roomState}
            description="A verified hotel guest charges the package to their room, posted to the folio in the PMS."
            link={{ href: "/room-charge", label: "Room charge — under Hotel" }}
          />
        )}
      </section>

      {cardLicensed && (
        <section aria-labelledby="card-config-heading" className="space-y-3">
          <h2 id="card-config-heading" className="text-headline">Card payment configuration</h2>
          <CardPaymentConfig writable={writable} />
        </section>
      )}
    </PageShell>
  );
}

function MethodCard({
  icon, title, state, description, link,
}: {
  icon: React.ReactNode;
  title: string;
  state: MethodState;
  description: string;
  link?: { href: string; label: string };
}) {
  return (
    <Card className={cn("flex flex-col", state.tone === "default" && "bg-surface/60")} data-testid={`method-${title}`}>
      <CardHeader className="items-start border-b-0 pb-2">
        <div className="min-w-0 space-y-1.5">
          <CardTitle className="flex items-center gap-2 [&_svg]:size-4">
            <span className="text-muted-foreground" aria-hidden>{icon}</span>
            {title}
          </CardTitle>
          <Badge tone={state.tone} dot={state.tone === "ok"}>{state.label}</Badge>
        </div>
      </CardHeader>
      <CardBody className="flex flex-1 flex-col gap-2 pt-0">
        <CardDescription>{description}</CardDescription>
        {state.detail}
        {link && (
          <Link
            href={link.href}
            className="mt-auto inline-flex w-fit items-center gap-0.5 pt-1 text-xs text-primary underline-offset-4 hover:underline"
          >
            {link.label} <ArrowUpRight className="size-3.5" aria-hidden />
          </Link>
        )}
      </CardBody>
    </Card>
  );
}
