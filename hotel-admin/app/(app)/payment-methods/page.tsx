"use client";

// Exactly four methods: Free, Voucher, Card payment and Room charge. There is no cash method (desk money is
// sold as a voucher) and no refund button: provider refunds and chargebacks are recorded in Settlements and
// OneGate never originates one. Card and Room charge come from site modules (GET /modules). The card
// configuration is not rendered until Card payment is manageable, since its requests would be refused.
// Roles fail closed while loading; edged enforces the real gate either way.

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { api } from "@/lib/api";
import { canRead, canWrite } from "@/lib/roles";
import { SiteModule, SiteModules } from "@/lib/payment-admin";
import { Card } from "@/components/ui/card";
import { InfoHint } from "@/components/ui/quiet";
import { StatusBadge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Button } from "@/components/ui/button";
import { PageHeader, PageShell } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { Skeleton } from "@/components/ui/misc";
import { NotAvailable, ReadOnlyNotice } from "@/components/ui/patterns";
import { cn } from "@/lib/utils";
import { AlertTriangle, ArrowUpRight, BedDouble, CreditCard, Gift, RefreshCw, Ticket } from "lucide-react";
import { CardPaymentConfig } from "./card-config";
import { CodeList } from "./codes";
import { useOperatorRoles } from "@/lib/whoami-context";

type Tone = "ok" | "warn" | "err" | "default" | "info";

type MethodState = {
  tone: Tone;
  label: string;
  /** Why it is in this state: read as a hint. */
  detail?: React.ReactNode;
  /** What to do about it, shown in the row. */
  action?: { href: string; label: string };
};

// States are checked in the order an operator has to fix them.
function moduleState(m: SiteModule | undefined): MethodState {
  if (!m) {
    return { tone: "default", label: "Not available", detail: "This appliance does not report this method." };
  }
  if (!m.licensed) {
    return { tone: "default", label: "Not licensed", detail: "Not included in this site's licence." };
  }
  if (!m.enabled) {
    return {
      tone: "default",
      label: "Switched off",
      detail: "Licensed but switched off for this site.",
      action: { href: "/modules", label: "Modules" },
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
  const roles = useOperatorRoles();
  const readable = roles === null ? false : canRead("payment-providers", roles);
  const writable = roles === null ? false : canWrite("payment-providers", roles);

  const [mods, setMods] = useState<SiteModules | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);
  const load = useCallback(() => {
    setErr(null);
    setLoading(true);
    api.get<SiteModules>("/modules")
      .then((m) => setMods(m ?? { site_type: null, modules: {} }))
      .catch(setErr)
      .finally(() => setLoading(false));
  }, []);
  useEffect(() => {
    if (readable) load();
  }, [readable, load]);

  const header = (
    <PageHeader
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

  if (roles === null || (readable && mods === null && !err)) {
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

  if (mods === null) {
    return (
      <PageShell>
        {header}
        <EmptyState
          icon={<AlertTriangle />}
          title="The payment methods could not be loaded"
          hint={err instanceof Error ? err.message : undefined}
          action={<Button size="sm" variant="secondary" disabled={loading} onClick={load}><RefreshCw /> Try again</Button>}
        />
      </PageShell>
    );
  }

  const card = mods?.modules?.card_payment;
  const room = mods?.modules?.room_charge;
  // A method the site is not licensed for is not presented at all; Settlements keeps any history.
  const cardLicensed = !!card?.licensed && !!card?.deployed;
  const roomLicensed = !!room?.licensed && !!room?.deployed;
  const cardState = moduleState(card);
  const roomState = moduleState(room);

  return (
    <PageShell>
      {header}
      {!writable && <ReadOnlyNotice>Your role can see how payments are configured but not change it.</ReadOnlyNotice>}

      {(() => {
        const methods = (
          <Card className="overflow-hidden">
            <ul aria-label="Payment methods" className="divide-y divide-border">
              <MethodRow
                icon={<Gift />}
                title="Free"
                state={{ tone: "ok", label: "Always available" }}
                description="A package priced at 0 is free: the client chooses it and is online, with no payment step."
                link={{ href: "/internet-packages", label: "Internet packages" }}
              />
              <MethodRow
                icon={<Ticket />}
                title="Voucher"
                state={{ tone: "ok", label: "Always available" }}
                description="The client types a code the property issued, on a printed card or by email."
                link={{ href: "/vouchers", label: "Vouchers" }}
              />
              {cardLicensed && (
                <MethodRow
                  icon={<CreditCard />}
                  title="Card payment"
                  state={cardState}
                  description="The client pays on the payment provider's hosted page. OneGate never sees the card number."
                />
              )}
              {roomLicensed && (
                <MethodRow
                  icon={<BedDouble />}
                  title="Room charge"
                  state={roomState}
                  description="A verified hotel guest charges the package to their room, posted to the folio in the PMS."
                  link={{ href: "/room-charge", label: "Room charge" }}
                />
              )}
            </ul>
          </Card>
        );
        return cardLicensed ? <CardPaymentConfig writable={writable} methods={methods} /> : methods;
      })()}
    </PageShell>
  );
}

function MethodRow({
  icon, title, state, description, link,
}: {
  icon: React.ReactNode;
  title: string;
  state: MethodState;
  description: string;
  link?: { href: string; label: string };
}) {
  const go = state.action ?? link;
  return (
    <li className={cn("flex min-h-14 flex-wrap items-center gap-x-4 gap-y-2 px-5 py-3", state.tone === "default" && "bg-surface/50")}
      data-testid={`method-${title}`}>
      <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-surface text-muted-foreground [&_svg]:size-4" aria-hidden>
        {icon}
      </span>
      <div className="flex min-w-0 flex-1 items-center gap-1">
        <span className="text-sm font-medium">{title}</span>
        <InfoHint>{description}</InfoHint>
      </div>
      <div className="flex items-center gap-1.5">
        <StatusBadge tone={state.tone}>{state.label}</StatusBadge>
        {state.detail && <InfoHint>{state.detail}</InfoHint>}
      </div>
      <div className="w-36 text-end">
        {go && (
          <Link href={go.href} className="inline-flex items-center gap-0.5 text-xs text-primary underline-offset-4 hover:underline">
            {go.label} <ArrowUpRight className="size-3.5" aria-hidden />
          </Link>
        )}
      </div>
    </li>
  );
}
