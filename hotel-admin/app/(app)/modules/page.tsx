"use client";

// MODULES — what this site's licence authorises, what the site has switched on, and why anything is not in use.
//
// FOUR QUESTIONS, NEVER ONE (docs/architecture/ONEGATE_MODULES_AND_ACQUISITION.md):
//
//   deployed    can this appliance run it at all (a property of the installed build and its configuration)
//   licensed    does the signed licence from OneGate Central authorise it for this site
//   switched on has the site administrator chosen to use it here
//   ready       can it serve a client right now (a provider account, an approved PMS interface, ...)
//
// Only all four together offer something to a client. The screen shows each separately because each has a
// different owner: the vendor for the licence, the site administrator for the switch, and the configuration
// pages for readiness. Collapsing them into one "enabled" word is how an operator ends up asking the vendor
// for a licence they already have.
//
// THE SITE TYPE IS SHOWN, NOT USED. It comes from Central with the site assignment and describes the property
// (a hotel, a café, ...). It suggests which modules fit, and it never switches, grants or hides one.
//
// SWITCHING A MODULE IS A SITE ADMINISTRATOR DECISION with password step-up and a reason. Turning one off stops
// new client use at once; configuration, history, reconciliation and recovery stay available.

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { Blocks, ArrowUpRight } from "lucide-react";
import { api, Whoami } from "@/lib/api";
import { PageHeader, PageShell } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ErrorBanner } from "@/components/ui/error-banner";
import { Skeleton } from "@/components/ui/misc";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { ConfirmDialog } from "@/components/ui/dialog";
import { useToast } from "@/components/ui/toast";
import { MODULE_REASON_WORDS, READINESS_WORDS, SITE_TYPE_LABELS, type ModuleReport, type ModuleState } from "@/lib/modules";

// Where each module is configured, so "not ready" always comes with the place to fix it.
const CONFIGURE: Record<string, { href: string; label: string }> = {
  hospitality: { href: "/pms-interfaces", label: "PMS connection" },
  paid_access: { href: "/internet-packages", label: "Internet packages" },
  card_payment: { href: "/payment-methods", label: "Payment methods" },
  room_charge: { href: "/room-charge", label: "Room charge" },
  sms_otp: { href: "/notifications", label: "Email & SMS" },
  email_otp: { href: "/notifications", label: "Email & SMS" },
  social_login: { href: "/social-providers", label: "Social login" },
};

// Display order: hospitality, then the paid-access chain, then identity, then platform.
const LICENCE_STATE_WORDS: Record<string, string> = {
  Active: "Active", GracePeriod: "Grace period", Restricted: "Restricted", Expired: "Expired",
  Suspended: "Suspended", Revoked: "Revoked", unlicensed: "Not licensed",
};

const ORDER = ["hospitality", "paid_access", "card_payment", "room_charge", "sms_otp", "email_otp", "social_login", "white_label", "ha"];

export default function ModulesPage() {
  const toast = useToast();
  const [roles, setRoles] = useState<string[] | null>(null);
  useEffect(() => {
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => setRoles([]));
  }, []);
  // Switching is site_admin only on the server; the button is offered to the same audience.
  const mayToggle = roles !== null && (roles.includes("site_admin") || roles.includes("tenant_admin"));

  const [report, setReport] = useState<ModuleReport | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [pending, setPending] = useState<{ mod: ModuleState; enable: boolean } | null>(null);
  const [busy, setBusy] = useState(false);
  const [dialogErr, setDialogErr] = useState<unknown>(null);

  const load = useCallback(async () => {
    try {
      setReport(await api.get<ModuleReport>("/modules"));
      setErr(null);
    } catch (e) {
      setErr(e);
    }
  }, []);
  useEffect(() => { load(); }, [load]);

  const confirm = async ({ reason, password }: { reason: string; password: string }) => {
    if (!pending) return;
    setBusy(true); setDialogErr(null);
    try {
      const next = await api.put<ModuleReport>(`/modules/${pending.mod.id}`, { enabled: pending.enable, reason, password });
      if (next && (next as ModuleReport).modules) setReport(next as ModuleReport); else await load();
      toast.success(`${pending.mod.label} ${pending.enable ? "switched on" : "switched off"}`);
      setPending(null);
    } catch (e) {
      setDialogErr(e);
    } finally { setBusy(false); }
  };

  const header = (
    <PageHeader
      icon={<Blocks />}
      eyebrow="System"
      title="Modules"
      description="What the licence authorises for this site, what is switched on here, and why anything is not in use."
      help={
        <>
          <HelpSection title="Four separate questions">
            <HelpList
              items={[
                <><strong>Available</strong> — this appliance can run the module.</>,
                <><strong>Licensed</strong> — the licence from your OneGate vendor authorises it for this site. Ask your vendor to change the licence.</>,
                <><strong>Switched on</strong> — the site administrator has chosen to use it here.</>,
                <><strong>Ready</strong> — it can serve a client now, for example a card provider account exists or a PMS interface is approved for room charge.</>,
              ]}
            />
            <p>Clients are offered a module only when all four are true.</p>
          </HelpSection>
          <HelpSection title="Switching a module off">
            <p>
              New client use stops at once. Clients already online are not disconnected, and its configuration, history,
              reconciliation and recovery screens stay available.
            </p>
          </HelpSection>
          <HelpSection title="Site type">
            <p>
              Set by your OneGate vendor. It describes the property and suggests modules; it never switches a module on
              or off.
            </p>
          </HelpSection>
        </>
      }
    />
  );

  if (!report) {
    return (
      <PageShell>
        {header}
        <ErrorBanner err={err} />
        {!err && (
          <div className="grid gap-4 md:grid-cols-2" aria-busy="true">
            <span className="sr-only">Loading modules</span>
            {[0, 1, 2, 3].map((i) => <Skeleton key={i} className="h-32" />)}
          </div>
        )}
      </PageShell>
    );
  }

  const mods = ORDER.map((id) => report.modules[id]).filter(Boolean) as ModuleState[];
  const siteType = report.site_type ? SITE_TYPE_LABELS[report.site_type] ?? `Other (${report.site_type})` : "Not set";

  return (
    <PageShell>
      {header}
      {roles !== null && !mayToggle && (
        <ReadOnlyNotice>Only a site administrator can switch a module on or off.</ReadOnlyNotice>
      )}
      <ErrorBanner err={err} className="mb-0" />

      <Card>
        <CardBody className="flex flex-wrap items-center gap-x-6 gap-y-2 text-sm">
          <span><span className="text-muted-foreground">Site type</span> <strong>{siteType}</strong></span>
          <span>
            <span className="text-muted-foreground">Licence</span>{" "}
            <Badge tone={report.license_state === "Active" ? "ok" : "warn"}>{LICENCE_STATE_WORDS[report.license_state] ?? report.license_state ?? "Unknown"}</Badge>
          </span>
          <Link href="/appliance" className="inline-flex items-center gap-0.5 text-primary underline-offset-4 hover:underline">
            Appliance &amp; licence <ArrowUpRight className="size-3.5" aria-hidden />
          </Link>
        </CardBody>
      </Card>

      <div className="grid gap-4 md:grid-cols-2">
        {mods.map((m) => (
          <ModuleCard
            key={m.id}
            mod={m}
            all={report.modules}
            mayToggle={mayToggle}
            onToggle={(enable) => { setDialogErr(null); setPending({ mod: m, enable }); }}
          />
        ))}
      </div>

      <ConfirmDialog
        open={pending !== null}
        onOpenChange={(o) => { if (!o) setPending(null); }}
        title={pending ? `${pending.enable ? "Switch on" : "Switch off"} ${pending.mod.label}` : ""}
        description={pending?.enable
          ? "Clients are offered it once it is also ready."
          : "New client use stops at once. Clients already online stay connected; configuration and history stay available."}
        confirmLabel={pending?.enable ? "Switch on" : "Switch off"}
        confirmVariant={pending?.enable ? "primary" : "danger"}
        busy={busy}
        error={dialogErr}
        requireReason
        requirePassword
        onConfirm={confirm}
      />
    </PageShell>
  );
}

function ModuleCard({
  mod, all, mayToggle, onToggle,
}: {
  mod: ModuleState; all: Record<string, ModuleState>; mayToggle: boolean; onToggle: (enable: boolean) => void;
}) {
  const conf = CONFIGURE[mod.id];
  const requires = (mod.requires ?? []).map((r) => all[r]?.label ?? r);
  const reasons = (mod.reasons ?? []).filter((r) => r !== "NOT_READY");
  return (
    <Card className="flex flex-col">
      <CardHeader className="items-start">
        <div className="min-w-0 space-y-1">
          <CardTitle className="flex flex-wrap items-center gap-2">
            {mod.label}
            <Badge tone={mod.effective ? "ok" : mod.licensed ? "warn" : "default"} dot>
              {mod.effective ? "In use" : mod.licensed ? "Not in use" : "Not licensed"}
            </Badge>
          </CardTitle>
          {requires.length > 0 && <CardDescription>Needs {requires.join(" and ")}.</CardDescription>}
        </div>
      </CardHeader>
      <CardBody className="flex flex-1 flex-col gap-3">
        <dl className="grid grid-cols-2 gap-x-4 gap-y-1 text-sm sm:grid-cols-4">
          <Gate label="Available" on={mod.deployed} />
          <Gate label="Licensed" on={mod.licensed} />
          <Gate label="Switched on" on={mod.switchable ? mod.enabled : mod.licensed} note={mod.switchable ? undefined : "No local switch"} />
          <Gate label="Ready" on={mod.ready} />
        </dl>
        {reasons.length > 0 && (
          <ul className="list-disc space-y-0.5 ps-5 text-xs text-muted-foreground">
            {reasons.map((r) => <li key={r}>{MODULE_REASON_WORDS[r] ?? <code>{r}</code>}</li>)}
          </ul>
        )}
        {!mod.ready && (mod.readiness ?? []).length > 0 && (
          <ul className="list-disc space-y-0.5 ps-5 text-xs text-warning-subtle-foreground">
            {(mod.readiness ?? []).map((r) => <li key={r}>{READINESS_WORDS[r] ?? <code>{r}</code>}</li>)}
          </ul>
        )}
        <div className="mt-auto flex flex-wrap items-center gap-2 pt-1">
          {mod.switchable && mayToggle && mod.licensed && (
            <Button size="sm" variant={mod.enabled ? "outline" : "primary"} onClick={() => onToggle(!mod.enabled)}>
              {mod.enabled ? "Switch off" : "Switch on"}
            </Button>
          )}
          {conf && mod.manageable && (
            <Link href={conf.href} className="inline-flex items-center gap-0.5 text-xs text-primary underline-offset-4 hover:underline">
              {conf.label} <ArrowUpRight className="size-3.5" aria-hidden />
            </Link>
          )}
        </div>
      </CardBody>
    </Card>
  );
}

function Gate({ label, on, note }: { label: string; on: boolean; note?: string }) {
  return (
    <div>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className={on ? "font-medium" : "text-muted-foreground"}>{on ? "Yes" : "No"}{note ? <span className="block text-xs text-muted-foreground">{note}</span> : null}</dd>
    </div>
  );
}
