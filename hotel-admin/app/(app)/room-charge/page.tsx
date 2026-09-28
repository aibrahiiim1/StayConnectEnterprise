"use client";

// HOTEL → ROOM CHARGE — which PMS interface a client's Internet package may be charged to, and whether room
// charge can work at all on this appliance.
//
// ROOM CHARGE IS A FINANCIAL ONBOARDING, NOT A SWITCH. Before OneGate posts a single charge to a folio, a site
// administrator records how the PMS identifies folios (the folio identity strategy), the currency the PMS posts
// in, and a written attestation of what they observed. That record is revisioned: the approval carries the
// revision it was made against (`expected_revision_id`), and a stale one is refused with 409 revision_conflict
// so two administrators cannot approve different facts about the same interface without one of them seeing it.
//
// ONLY FIAS, AND ONLY A SITE ADMINISTRATOR. Room charge posting is implemented for Protel FIAS only, so only a
// FIAS interface is offered the approval. The approval is a site administrator's decision — edged refuses it
// from any other role — so every other role sees the same table with no button and a sentence saying whose
// decision it is.
//
// POSTING MAY STILL BE WITHHELD. An appliance whose room-charge posting is not authorised (readiness
// PMS_POSTING_NOT_AUTHORISED on the room_charge module) can be configured here in full, but room charge is not
// offered to clients and nothing is posted to the PMS. The page says so at the top, because an operator who has
// just approved an interface will otherwise go looking for the charge.
//
// The currency exponent is not asked: FIAS posts in minor units with two decimal places, and edged fixes it.

import { useCallback, useEffect, useState } from "react";
import Link from "next/link";
import { api, ApiError, Whoami } from "@/lib/api";
import { canRead } from "@/lib/roles";
import {
  FOLIO_STRATEGIES, FolioStrategy, OnboardingInterface, OnboardingResp, SiteModules, saveErrorMessage, strategyLabel,
} from "@/lib/payment-admin";
import { formatDate } from "@/lib/utils";
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DialogForm } from "@/components/ui/dialog";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Field, Input, Textarea } from "@/components/ui/input";
import { PageHeader, PageShell } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { SkeletonRows } from "@/components/ui/misc";
import { NotAvailable, ReadOnlyNotice } from "@/components/ui/patterns";
import { Table, TBody, TD, TH, THead, TR, TableWrap } from "@/components/ui/table";
import { useToast } from "@/components/ui/toast";
import { ArrowUpRight, BedDouble, CheckCircle2 } from "lucide-react";
import { CodeList, CodeText } from "../payment-methods/codes";

const FIAS = "protel-fias";
const ATTESTATION_MIN = 20;
const ATTESTATION_MAX = 2000;
const REASON_MIN = 4;
const REASON_MAX = 500;

export default function RoomChargePage() {
  const toast = useToast();
  const [roles, setRoles] = useState<string[] | null>(null);
  useEffect(() => {
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => setRoles([]));
  }, []);
  const readable = roles === null ? false : canRead("pms-financial-onboarding", roles);
  const isSiteAdmin = roles?.includes("site_admin") ?? false;

  const [data, setData] = useState<OnboardingResp | null>(null);
  const [readiness, setReadiness] = useState<string[]>([]);
  const [err, setErr] = useState<unknown>(null);
  const [approving, setApproving] = useState<OnboardingInterface | null>(null);

  const load = useCallback(async () => {
    try {
      const r = await api.get<OnboardingResp>("/pms-financial-onboarding");
      setData({ interfaces: r?.interfaces ?? [], strategies: r?.strategies ?? [] });
    } catch (e) {
      setErr(e);
      setData({ interfaces: [], strategies: [] });
    }
    // Module readiness is advisory on this page: the table is still correct without it.
    try {
      const m = await api.get<SiteModules>("/modules");
      setReadiness(m?.modules?.room_charge?.readiness ?? []);
    } catch { /* readiness unknown; the note is simply not shown */ }
  }, []);
  useEffect(() => { if (readable) load(); }, [readable, load]);

  const postingWithheld = readiness.includes("PMS_POSTING_NOT_AUTHORISED");
  const otherReadiness = readiness.filter((c) => c !== "PMS_POSTING_NOT_AUTHORISED");

  const header = (
    <PageHeader
      icon={<BedDouble />}
      eyebrow="Hotel"
      title="Room charge"
      description="Approve a PMS interface for room charge, so a verified guest can charge an Internet package to their room."
      help={
        <>
          <HelpSection title="What approval records">
            <HelpList
              items={[
                <><strong>Folio identity</strong> — how the PMS numbers folios, so each charge lands on the right bill.</>,
                <><strong>Base currency</strong> — the currency the PMS posts in. Packages charged to a room are priced in it.</>,
                <><strong>Attestation</strong> — what you observed about how this PMS identifies folios. It is kept with the approval.</>,
              ]}
            />
          </HelpSection>
          <HelpSection title="Who can approve">
            <p>
              Only a site administrator, and only for a FIAS interface. Everyone else sees the approval and its state.
            </p>
          </HelpSection>
          <HelpSection title="Where clients see it">
            <p>
              Room charge is one of the payment methods under Internet offering → Payment methods. Once an interface is
              approved and posting is authorised on this appliance, a guest who signs in with their room can choose it.
            </p>
          </HelpSection>
        </>
      }
    />
  );

  if (roles === null || (readable && data === null)) {
    return (
      <PageShell>
        {header}
        <Card><SkeletonRows rows={3} cols={5} /></Card>
      </PageShell>
    );
  }

  if (!readable) {
    return (
      <PageShell>
        {header}
        <NotAvailable title="Not available to your role" reason="Your role cannot see the room charge approval for this site's PMS interfaces." />
      </PageShell>
    );
  }

  const interfaces = data?.interfaces ?? [];

  return (
    <PageShell>
      {header}
      {!isSiteAdmin && (
        <ReadOnlyNotice>Approving a PMS interface for room charge is a site administrator decision.</ReadOnlyNotice>
      )}
      {postingWithheld && (
        <Callout tone="warning" title="Room charge is not offered on this appliance">
          Posting to the PMS is not authorised on this appliance. Room charge can be configured here, but it will not be
          offered to clients and nothing is posted to the PMS.
        </Callout>
      )}
      {otherReadiness.length > 0 && (
        <Callout tone="info" title="Room charge is not ready">
          <CodeList codes={otherReadiness} />
        </Callout>
      )}
      <ErrorBanner err={err} className="mb-0" />

      <Card>
        <CardHeader className="items-start">
          <div className="min-w-0 space-y-1">
            <CardTitle>PMS interfaces</CardTitle>
            <CardDescription>
              Room charge is supported on FIAS interfaces.{" "}
              <Link href="/payment-methods" className="inline-flex items-center gap-0.5 text-primary underline-offset-4 hover:underline">
                Payment methods <ArrowUpRight className="size-3.5" aria-hidden />
              </Link>
            </CardDescription>
          </div>
        </CardHeader>
        <TableWrap>
          {interfaces.length === 0 ? (
            <p className="px-5 py-4 text-sm text-muted-foreground">No PMS interface exists yet. Add one under Hotel → PMS connection.</p>
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Interface</TH>
                  <TH>Room charge</TH>
                  <TH>Currency</TH>
                  <TH>Folio identity</TH>
                  <TH>Approved</TH>
                  {isSiteAdmin && <TH className="text-right">Action</TH>}
                </TR>
              </THead>
              <TBody>
                {interfaces.map((i) => (
                  <TR key={i.pms_interface_id}>
                    <TD>
                      <div className="font-medium">{i.display_label || i.pms_interface_id}</div>
                      <div className="text-xs text-muted-foreground">
                        {i.connector_kind === FIAS ? "Protel FIAS" : i.connector_kind} · {i.lifecycle_state}
                      </div>
                    </TD>
                    <TD>
                      {i.ready ? (
                        <Badge tone="ok" dot>Ready</Badge>
                      ) : (
                        <div className="space-y-1">
                          <Badge tone={i.connector_kind === FIAS ? "warn" : "default"}>Not ready</Badge>
                          {i.reason && <div className="text-xs text-muted-foreground"><CodeText code={i.reason} /></div>}
                        </div>
                      )}
                    </TD>
                    <TD className="font-mono">{i.financial_base_currency ?? "—"}</TD>
                    <TD>{strategyLabel(i.folio_identity_strategy)}</TD>
                    <TD className="text-xs">
                      {i.approved_at ? (
                        <>
                          <div className="whitespace-nowrap">{formatDate(i.approved_at)}</div>
                          {i.approved_by && <div className="break-all text-muted-foreground">{i.approved_by}</div>}
                        </>
                      ) : "—"}
                    </TD>
                    {isSiteAdmin && (
                      <TD className="text-right">
                        {i.connector_kind === FIAS ? (
                          <Button
                            size="xs"
                            variant={i.ready ? "secondary" : "primary"}
                            onClick={() => setApproving(i)}
                            aria-label={`${i.ready ? "Update room charge approval for" : "Approve for room charge:"} ${i.display_label || i.pms_interface_id}`}
                          >
                            <CheckCircle2 /> {i.ready ? "Update approval" : "Approve for room charge"}
                          </Button>
                        ) : (
                          <span className="text-xs text-muted-foreground">FIAS only</span>
                        )}
                      </TD>
                    )}
                  </TR>
                ))}
              </TBody>
            </Table>
          )}
        </TableWrap>
      </Card>

      {isSiteAdmin && (
        <ApproveDialog
          iface={approving}
          strategies={(data?.strategies ?? []).filter((s): s is FolioStrategy => s in FOLIO_STRATEGIES)}
          onClose={() => setApproving(null)}
          onApproved={async () => {
            setApproving(null);
            toast.success("Approved for room charge");
            await load();
          }}
          onConflict={async (message) => {
            setApproving(null);
            setErr(message);
            await load();
          }}
        />
      )}
    </PageShell>
  );
}

function ApproveDialog({
  iface, strategies, onClose, onApproved, onConflict,
}: {
  iface: OnboardingInterface | null;
  strategies: FolioStrategy[];
  onClose: () => void;
  onApproved: () => Promise<void>;
  onConflict: (message: string) => Promise<void>;
}) {
  const open = iface !== null;
  const [strategy, setStrategy] = useState<string>("");
  const [currency, setCurrency] = useState("");
  const [attestation, setAttestation] = useState("");
  const [reason, setReason] = useState("");
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Prefill what is already recorded; clear everything, the password above all, on close.
  useEffect(() => {
    setStrategy(iface?.folio_identity_strategy && iface.folio_identity_strategy in FOLIO_STRATEGIES ? iface.folio_identity_strategy : "");
    setCurrency(iface?.financial_base_currency ?? "");
    setAttestation("");
    setReason("");
    setPassword("");
    setError(null);
  }, [iface]);

  const attLen = attestation.trim().length;
  const ready =
    strategy !== "" &&
    /^[A-Z]{3}$/.test(currency) &&
    attLen >= ATTESTATION_MIN && attLen <= ATTESTATION_MAX &&
    reason.trim().length >= REASON_MIN && reason.trim().length <= REASON_MAX &&
    password !== "";

  async function submit() {
    if (!iface || !ready) return;
    setBusy(true); setError(null);
    try {
      await api.post<{ revision_id: string }>(`/pms-financial-onboarding/${encodeURIComponent(iface.pms_interface_id)}`, {
        expected_revision_id: iface.current_revision_id,
        folio_identity_strategy: strategy,
        currency,
        attestation: attestation.trim(),
        reason: reason.trim(),
        password,
      });
      await onApproved();
    } catch (e) {
      if (e instanceof ApiError && e.body?.error === "revision_conflict") {
        await onConflict(saveErrorMessage(e));
        return;
      }
      setError(saveErrorMessage(e));
      setPassword("");
    } finally {
      setBusy(false);
    }
  }

  const list = strategies.length > 0 ? strategies : (Object.keys(FOLIO_STRATEGIES) as FolioStrategy[]);

  return (
    <DialogForm
      open={open}
      onOpenChange={(v) => { if (!v) onClose(); }}
      title={`Approve ${iface?.display_label || "interface"} for room charge`}
      description="What you record here decides how charges are posted to guest folios through this interface."
      size="lg"
      submitLabel="Approve for room charge"
      busyLabel="Approving…"
      busy={busy}
      error={error}
      disabled={!ready}
      onSubmit={submit}
    >
      <fieldset className="space-y-2">
        <legend className="mb-1.5 text-label">Folio identity strategy <span className="text-destructive">*</span></legend>
        {list.map((s) => (
          <label
            key={s}
            className="flex cursor-pointer items-start gap-3 rounded-md border border-border px-3.5 py-2.5 has-[:checked]:border-primary has-[:checked]:bg-primary-subtle/40"
          >
            <input
              type="radio"
              name="folio-strategy"
              value={s}
              checked={strategy === s}
              onChange={() => setStrategy(s)}
              className="mt-1 size-4 accent-primary"
            />
            <span className="min-w-0">
              <span className="block text-sm font-medium">{FOLIO_STRATEGIES[s].label}</span>
              <span className="block text-xs text-muted-foreground">{FOLIO_STRATEGIES[s].explanation}</span>
            </span>
          </label>
        ))}
      </fieldset>
      <Field label="Base currency" required hint="Three letters, the currency the PMS posts in. Two decimal places are fixed for FIAS.">
        <Input
          value={currency}
          maxLength={3}
          autoComplete="off"
          className="w-28 font-mono uppercase"
          onChange={(e) => setCurrency(e.target.value.replace(/[^A-Za-z]/g, "").toUpperCase())}
        />
      </Field>
      <Field
        label="Attestation"
        required
        hint={`What you observed about how this PMS identifies folios. ${attLen}/${ATTESTATION_MAX}, at least ${ATTESTATION_MIN}.`}
      >
        <Textarea value={attestation} maxLength={ATTESTATION_MAX} rows={4} onChange={(e) => setAttestation(e.target.value)} />
      </Field>
      <Field label="Reason" required hint={`Recorded in the activity log. ${reason.length}/${REASON_MAX}`}>
        <Input value={reason} maxLength={REASON_MAX} onChange={(e) => setReason(e.target.value)} />
      </Field>
      <Field label="Confirm your password" required>
        <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
      </Field>
    </DialogForm>
  );
}
