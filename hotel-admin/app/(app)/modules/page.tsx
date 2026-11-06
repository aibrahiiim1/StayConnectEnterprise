"use client";

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { AlertTriangle, ArrowUpRight, RefreshCw } from "lucide-react";
import { api } from "@/lib/api";
import { PageHeader, PageShell } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { Card, CardBody, CardDescription, CardHeader, CardTitle, SectionCard } from "@/components/ui/card";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { Button, LinkButton } from "@/components/ui/button";
import { Callout } from "@/components/ui/error-banner";
import { EmptyState } from "@/components/ui/empty-state";
import { Skeleton } from "@/components/ui/misc";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { ConfirmDialog } from "@/components/ui/dialog";
import { useToast } from "@/components/ui/toast";
import { useCapabilities } from "@/lib/capabilities";
import { moduleReasonWords, READINESS_WORDS, SITE_TYPE_LABELS, switchError, type ModuleReport, type ModuleState } from "@/lib/modules";
import { canRead, canWrite } from "@/lib/roles";
import { REASON_MAX_CHARS } from "@/lib/reason";
import { cn, formatDate, formatRelative } from "@/lib/utils";
import { useOperatorRoles } from "@/lib/whoami-context";

const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";
const LINK = "inline-flex items-center gap-0.5 text-xs text-primary underline-offset-4 hover:underline";
const CHANGES_SHOWN = 10;

const CONFIGURE: Record<string, { href: string; label: string }> = {
  hospitality: { href: "/pms-interfaces", label: "PMS connection" },
  paid_access: { href: "/internet-packages", label: "Internet packages" },
  card_payment: { href: "/payment-methods", label: "Payment methods" },
  room_charge: { href: "/room-charge", label: "Room charge" },
  sms_otp: { href: "/notifications", label: "Delivery" },
  email_otp: { href: "/notifications", label: "Delivery" },
  whatsapp_otp: { href: "/notifications", label: "Delivery" },
  social_login: { href: "/social-providers", label: "Identity providers" },
};

const IDENTITY_MODULES = new Set(["sms_otp", "email_otp", "whatsapp_otp", "social_login"]);

const LICENCE_STATE_WORDS: Record<string, string> = {
  Active: "Active", GracePeriod: "Grace period", Restricted: "Restricted", Expired: "Expired",
  Suspended: "Suspended", Revoked: "Revoked", unlicensed: "Not licensed",
};

const ORDER = ["hospitality", "paid_access", "card_payment", "room_charge", "sms_otp", "email_otp", "whatsapp_otp", "social_login", "white_label", "ha"];

const RECORDS: Record<string, { href: string; label: string; surface: string }[]> = {
  hospitality: [
    { href: "/stays", label: "Stays", surface: "pms-stays" },
    { href: "/stay-events", label: "PMS activity", surface: "pms-events" },
    { href: "/pms-resolutions", label: "Guest sign-in checks", surface: "pms-resolutions" },
    { href: "/guest-signin-attempts", label: "Guest sign-in attempts", surface: "guest-signin-attempts" },
  ],
  room_charge: [
    { href: "/financial-review", label: "Manual review", surface: "financial-review" },
    { href: "/financial-settlements", label: "Settlements", surface: "financial-review" },
    { href: "/financial-recovery", label: "Recovery", surface: "financial-review" },
  ],
};

type Change = {
  module: string;
  changed_at: string;
  changed_by: string;
  reason: string;
  old_enabled: boolean | null;
  new_enabled: boolean;
};

const recognised = (code: string) => <span title={code}>Not recognised</span>;

export default function ModulesPage() {
  const toast = useToast();
  const roles = useOperatorRoles();
  const caps = useCapabilities();
  const mayToggle = roles !== null && canWrite("modules", roles);
  const mayReadOperators = roles !== null && canRead("operators", roles);

  const [report, setReport] = useState<ModuleReport | null>(null);
  const [err, setErr] = useState(false);
  const [loading, setLoading] = useState(false);
  const [pending, setPending] = useState<{ mod: ModuleState; enable: boolean } | null>(null);
  const [busy, setBusy] = useState(false);
  const [dialogErr, setDialogErr] = useState<string | null>(null);
  const [changes, setChanges] = useState<Change[] | null>(null);
  const [changesErr, setChangesErr] = useState(false);
  const [names, setNames] = useState<Map<string, string> | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setReport(await api.get<ModuleReport>("/modules"));
      setErr(false);
    } catch {
      setErr(true);
    } finally {
      setLoading(false);
    }
  }, []);
  const loadChanges = useCallback(async () => {
    try {
      const r = await api.get<{ changes?: Change[] }>("/modules/changes");
      setChanges(r.changes ?? []);
      setChangesErr(false);
    } catch {
      setChangesErr(true);
    }
  }, []);
  useEffect(() => { void load(); void loadChanges(); }, [load, loadChanges]);
  useEffect(() => {
    if (!mayReadOperators) return;
    api.get<{ data: { id: string; display_name?: string; email?: string }[] }>("/operators")
      .then((r) => setNames(new Map((r.data ?? []).map((o) => [o.id, o.display_name || o.email || o.id]))))
      .catch(() => {});
  }, [mayReadOperators]);

  const confirm = async ({ reason, password }: { reason: string; password: string }) => {
    if (!pending) return;
    setBusy(true); setDialogErr(null);
    try {
      const next = await api.put<ModuleReport>(`/modules/${pending.mod.id}`, { enabled: pending.enable, reason, password });
      let fresh: ModuleReport | null = next && (next as ModuleReport).modules ? next as ModuleReport : null;
      if (fresh) setReport(fresh);
      else {
        try {
          fresh = await api.get<ModuleReport>("/modules");
          setReport(fresh);
          setErr(false);
        } catch {
          setErr(true);
        }
      }
      const now = fresh?.modules[pending.mod.id];
      const verb = pending.enable ? "switched on" : "switched off";
      if (pending.enable && now && !now.ready) {
        toast.success(`${pending.mod.label} ${verb}`, "Clients are offered it once it is ready.");
      } else {
        toast.success(`${pending.mod.label} ${verb}`);
      }
      setPending(null);
      void loadChanges();
    } catch (e) {
      setDialogErr(switchError(e));
    } finally { setBusy(false); }
  };

  const siteType = report?.site_type ? SITE_TYPE_LABELS[report.site_type] ?? null : "Not set";
  const licenceState = report ? LICENCE_STATE_WORDS[report.license_state] ?? null : null;
  const header = (
    <PageHeader
      title="Modules"
      description="What the licence allows here, what is switched on, and why anything is not in use."
      meta={report && (
        <>
          <Badge tone="default">Site type: {siteType ?? recognised(report.site_type ?? "")}</Badge>
          <StatusBadge tone={report.license_state === "Active" ? "ok" : "warn"}>
            Licence: {licenceState ?? recognised(report.license_state ?? "")}
          </StatusBadge>
        </>
      )}
      actions={
        <LinkButton href="/appliance" variant="secondary">
          Appliance &amp; licence <ArrowUpRight aria-hidden />
        </LinkButton>
      }
      help={
        <>
          <HelpSection title="Four separate questions">
            <HelpList
              items={[
                <><strong>Available</strong> — this appliance can run the module.</>,
                <><strong>Licensed</strong> — the licence from your OneGate vendor authorises it for this site. Ask your vendor to change the licence.</>,
                <><strong>Switched on</strong> — the site administrator has chosen to use it here.</>,
                <><strong>Ready</strong> — it can serve a client now, for example a card provider account exists or a PMS connection is approved for room charge.</>,
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
        {err ? (
          <Card role="alert" className={FADE}>
            <EmptyState
              size="sm"
              icon={<AlertTriangle />}
              title="Modules could not be loaded"
              hint="Check the appliance is reachable, then try again. Nothing has been changed."
              action={
                <Button size="sm" variant="secondary" onClick={() => void load()} disabled={loading}>
                  <RefreshCw className={cn(loading && "animate-spin motion-reduce:animate-none")} aria-hidden /> Try again
                </Button>
              }
            />
          </Card>
        ) : (
          <div className="grid gap-4 md:grid-cols-2" aria-busy="true">
            <span className="sr-only">Loading modules</span>
            {[0, 1, 2, 3].map((i) => <Skeleton key={i} className="h-32" />)}
          </div>
        )}
      </PageShell>
    );
  }

  const ids = [...ORDER, ...Object.keys(report.modules).filter((id) => !ORDER.includes(id))];
  const mods = ids.map((id) => report.modules[id]).filter(Boolean) as ModuleState[];
  const who = (id: string) => (id && names?.get(id)) || "An operator";
  return (
    <PageShell>
      {header}
      {roles !== null && !mayToggle && (
        <ReadOnlyNotice>Your role can see the modules but not switch them on or off.</ReadOnlyNotice>
      )}
      {err && (
        <div className={FADE}>
          <Callout tone="warning">Modules could not be refreshed. What is shown may be out of date.</Callout>
        </div>
      )}

      <div className="grid gap-4 md:grid-cols-2 2xl:grid-cols-3">
        {mods.map((m) => (
          <ModuleCard
            key={m.id}
            mod={m}
            all={report.modules}
            surfaces={caps?.surfaces ?? []}
            mayToggle={mayToggle}
            onToggle={(enable) => { setDialogErr(null); setPending({ mod: m, enable }); }}
          />
        ))}
      </div>

      <SectionCard
        title="Recent changes"
        description="Who switched a module on or off here, when and why."
        actions={<Link href="/audit" className={LINK}>Activity <ArrowUpRight className="size-3.5" aria-hidden /></Link>}
      >
        {changesErr ? (
          <div className={cn("flex flex-wrap items-center gap-3 text-sm text-muted-foreground", FADE)} role="alert">
            Recent changes could not be loaded.
            <Button size="sm" variant="secondary" onClick={() => void loadChanges()}>
              <RefreshCw aria-hidden /> Try again
            </Button>
          </div>
        ) : changes === null ? (
          <Skeleton className="h-16" />
        ) : changes.length === 0 ? (
          <p className="text-sm text-muted-foreground">No module has been switched on or off at this site.</p>
        ) : (
          <ul className={cn("divide-y divide-border", FADE)} aria-label="Recent module changes">
            {changes.slice(0, CHANGES_SHOWN).map((c, i) => (
              <li key={`${c.module}-${c.changed_at}-${i}`} className="space-y-0.5 py-2.5 first:pt-0 last:pb-0">
                <div className="flex flex-wrap items-baseline justify-between gap-x-3 text-sm">
                  <span>
                    <strong className="font-medium">{report.modules[c.module]?.label ?? c.module}</strong>{" "}
                    {c.new_enabled ? "switched on" : "switched off"} by {who(c.changed_by)}
                  </span>
                  <time dateTime={c.changed_at} title={formatDate(c.changed_at)} className="text-xs text-muted-foreground tabular">
                    {formatRelative(c.changed_at)}
                  </time>
                </div>
                {c.reason && <p className="text-xs text-muted-foreground">Reason: {c.reason}</p>}
              </li>
            ))}
          </ul>
        )}
        {changes && changes.length > CHANGES_SHOWN && (
          <p className="mt-3 text-xs text-muted-foreground">Showing the latest {CHANGES_SHOWN}. Activity lists every change.</p>
        )}
      </SectionCard>

      <ConfirmDialog
        open={pending !== null}
        onOpenChange={(o) => { if (!o && !busy) setPending(null); }}
        title={pending ? `${pending.enable ? "Switch on" : "Switch off"} ${pending.mod.label}?` : ""}
        description={pending?.enable
          ? "Clients are offered it once it is also ready."
          : "New client use stops at once. Clients already online stay connected; configuration and history stay available."}
        confirmLabel={pending?.enable ? "Switch on" : "Switch off"}
        confirmVariant={pending?.enable ? "primary" : "danger"}
        busy={busy}
        error={dialogErr}
        requireReason
        reasonMaxLength={REASON_MAX_CHARS}
        requirePassword
        onConfirm={confirm}
      />
    </PageShell>
  );
}

function ModuleCard({
  mod, all, surfaces, mayToggle, onToggle,
}: {
  mod: ModuleState; all: Record<string, ModuleState>; surfaces: string[]; mayToggle: boolean; onToggle: (enable: boolean) => void;
}) {
  const conf = CONFIGURE[mod.id];
  const records = !mod.licensed ? (RECORDS[mod.id] ?? []).filter((r) => surfaces.includes(r.surface)) : [];
  const requires = (mod.requires ?? []).map((r) => all[r]?.label ?? r);
  const reasons = (mod.reasons ?? []).filter((r) => r !== "NOT_READY" && (mod.licensed || r !== "DISABLED_BY_SITE"));
  const tone = mod.effective ? "ok" : mod.licensed && mod.enabled ? "warn" : "default";
  const badge = mod.effective ? "In use" : !mod.licensed ? "Not licensed" : mod.enabled ? "Not in use" : "Switched off";
  return (
    <Card className="flex flex-col">
      <CardHeader className="items-start">
        <div className="min-w-0 space-y-1">
          <CardTitle className="flex flex-wrap items-center gap-2">
            {mod.label}
            <StatusBadge tone={tone}>{badge}</StatusBadge>
          </CardTitle>
          {requires.length > 0 && <CardDescription>Needs {requires.join(" and ")}.</CardDescription>}
        </div>
      </CardHeader>
      <CardBody className="flex flex-1 flex-col gap-3">
        <dl className="grid grid-cols-2 gap-x-4 gap-y-2 rounded-md bg-surface px-3 py-2.5 text-sm sm:grid-cols-4">
          <Gate label="Available" on={mod.deployed} />
          <Gate label="Licensed" on={mod.licensed} />
          <Gate label="Switched on" on={mod.licensed ? mod.enabled : "Needs a licence"}
            note={mod.licensed && !mod.switchable ? (IDENTITY_MODULES.has(mod.id) ? "Set on Sign-in methods" : "No local switch") : undefined} />
          <Gate label="Ready" on={!mod.licensed ? "Needs a licence" : !mod.deployed ? "Not available" : mod.ready} />
        </dl>
        {reasons.length > 0 && (
          <ul className="list-disc space-y-0.5 ps-5 text-xs text-muted-foreground">
            {reasons.map((r) => <li key={r}>{moduleReasonWords(mod.id, r) ?? recognised(r)}</li>)}
          </ul>
        )}
        {!mod.ready && (mod.readiness ?? []).length > 0 && (
          <ul className="list-disc space-y-0.5 ps-5 text-xs text-warning-subtle-foreground">
            {(mod.readiness ?? []).map((r) => <li key={r}>{READINESS_WORDS[r] ?? recognised(r)}</li>)}
          </ul>
        )}
        <div className="mt-auto flex flex-wrap items-center gap-2 pt-1">
          {mod.switchable && mayToggle && mod.licensed && (
            <Button size="sm" variant="secondary" onClick={() => onToggle(!mod.enabled)}>
              {mod.enabled ? "Switch off" : "Switch on"}
            </Button>
          )}
          {IDENTITY_MODULES.has(mod.id) && mod.licensed && (
            <Link href="/sign-in-methods" className={LINK}>
              Sign-in methods <ArrowUpRight className="size-3.5" aria-hidden />
            </Link>
          )}
          {conf && mod.licensed && mod.manageable && (
            <Link href={conf.href} className={LINK}>
              {conf.label} <ArrowUpRight className="size-3.5" aria-hidden />
            </Link>
          )}
        </div>
        {records.length > 0 && (
          <div className="border-t border-border pt-2 text-xs" data-testid={`records-${mod.id}`}>
            <span className="text-muted-foreground">Records kept here: </span>
            {records.map((r, i) => (
              <span key={r.href}>
                {i > 0 && <span className="text-muted-foreground"> · </span>}
                <Link href={r.href} className="text-primary underline-offset-4 hover:underline">{r.label}</Link>
              </span>
            ))}
          </div>
        )}
      </CardBody>
    </Card>
  );
}

function Gate({ label, on, note }: { label: string; on: boolean | string; note?: string }) {
  return (
    <div>
      <dt className="text-xs text-muted-foreground">{label}</dt>
      <dd className={on === true ? "font-medium text-foreground" : "text-muted-foreground"}>
        {typeof on === "string" ? on : on ? "Yes" : "No"}
        {note ? <span className="block text-xs text-muted-foreground">{note}</span> : null}
      </dd>
    </div>
  );
}
