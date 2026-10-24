"use client";

// Appliance writes require password step-up.

import Link from "next/link";
import { useEffect, useState } from "react";
import { api, itemsOf, withStepUp, type ApplianceRow, type Items, type Site } from "@/lib/api";
import { DialogForm } from "@/components/ui/dialog";
import { Field, Input, Select, useFieldErrors } from "@/components/ui/input";
import { Callout } from "@/components/ui/error-banner";
import { useToast } from "@/components/ui/toast";
import {
  DEFAULT_TERMS, LicenseTermsFields, termsBody, termsIssue, withSuggestedModules, type TermsDraft, type TermsField,
} from "@/components/license-terms";
import {
  PlacementFields, emptyPlacement, placementBody, placementIssue, placementSiteType, type Placement, type PlacementField,
} from "@/components/placement-fields";
import { useModuleCatalog } from "@/lib/modules";

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <fieldset className="space-y-3 border-t border-border pt-4 first-of-type:border-t-0 first-of-type:pt-0">
      <legend className="float-left mb-3 w-full text-emphasis">{title}</legend>
      <div className="clear-both">{children}</div>
    </fieldset>
  );
}

const HOLDS_OTHER_CUSTOMER =
  "This appliance still holds another customer's data. Factory-reset it before activating it for a different customer.";

export function ActivateDialog({
  appliance,
  open,
  onOpenChange,
  onDone,
}: {
  appliance: Pick<ApplianceRow, "id" | "serial" | "holds_customer_id" | "holds_customer_name">;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onDone: () => void;
}) {
  const toast = useToast();
  // Held customer data locks activation to that customer to prevent cross-customer disclosure.
  const heldId = appliance.holds_customer_id ?? "";
  const heldName = appliance.holds_customer_name ?? "";
  const heldGone = !!heldId && !heldName;
  const [placement, setPlacement] = useState<Placement>(emptyPlacement(heldGone ? "" : heldId));
  const [terms, setTerms] = useState<TermsDraft>(DEFAULT_TERMS);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const { catalog } = useModuleCatalog();
  const siteType = placementSiteType(placement);
  const fields = useFieldErrors<PlacementField | TermsField>();
  const clearFields = fields.clear;

  useEffect(() => {
    if (open) {
      setPlacement(emptyPlacement(heldGone ? "" : heldId));
      setTerms(DEFAULT_TERMS);
      setErr(null);
      clearFields();
    }
  }, [open, heldId, heldGone, clearFields]);

  // Manual module choices override site-type suggestions.
  useEffect(() => {
    setTerms((t) => withSuggestedModules(t, catalog, siteType));
  }, [catalog, siteType]);

  async function submit() {
    if (heldGone) {
      setErr(HOLDS_OTHER_CUSTOMER);
      return;
    }
    const issue = placementIssue(placement) ?? termsIssue(terms);
    if (issue) {
      setErr(null);
      fields.fail({ [issue.field]: issue.message });
      return;
    }
    fields.clear();
    setBusy(true);
    setErr(null);
    try {
      await withStepUp(() =>
        api.post(`/cloud/v1/appliances/${appliance.id}/activate`, { ...placementBody(placement), license: termsBody(terms) }),
      );
      toast.success(`${appliance.serial} activated`, "It picks up its license the next time it contacts Central.");
      onOpenChange(false);
      onDone();
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <DialogForm
      open={open}
      onOpenChange={onOpenChange}
      title={`Activate ${appliance.serial}`}
      description="Choose where this appliance is installed and what its license allows."
      size="lg"
      submitLabel="Activate"
      busyLabel="Activating…"
      busy={busy}
      error={err}
      onSubmit={submit}
    >
      {heldId && (
        <Callout tone="warning" title="This appliance still holds a customer's data">
          {heldGone ? (
            <>
              It holds the data of a customer that is no longer in Central, so it cannot be activated for anyone.
              Factory-reset it; it then registers again and can be activated for any customer.
            </>
          ) : (
            <>
              It can be activated only for <strong>{heldName}</strong>, whose data is still on it. To give it to
              another customer, factory-reset it first; it then registers again and can be activated for anyone.
            </>
          )}
        </Callout>
      )}
      <Section title="Where it is installed">
        <PlacementFields
          value={placement}
          onChange={setPlacement}
          lockedCustomer={heldId && !heldGone ? { id: heldId, name: heldName } : undefined}
          errorFor={fields.error}
          idFor={fields.id}
        />
      </Section>
      <Section title="License">
        <LicenseTermsFields value={terms} onChange={setTerms} siteType={siteType} errorFor={fields.error} idFor={fields.id} />
      </Section>
    </DialogForm>
  );
}

export function SetLicenseDialog({
  appliance,
  open,
  onOpenChange,
  onDone,
}: {
  appliance: Pick<ApplianceRow, "id" | "serial" | "license" | "site_type">;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onDone: () => void;
}) {
  const toast = useToast();
  const [terms, setTerms] = useState<TermsDraft>(DEFAULT_TERMS);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const current = appliance.license;
  const hasLicense = !!current && current.state !== "none";
  const { catalog } = useModuleCatalog();
  const fields = useFieldErrors<TermsField | "reason">();

  useEffect(() => {
    if (!open) return;
    // Preserve current terms on edits; an expired end date starts renewal from the default term.
    const until = current?.valid_until ? current.valid_until.slice(0, 10) : "";
    const untilInFuture = !!current?.valid_until && Date.parse(current.valid_until) > Date.now();
    setTerms({
      ...DEFAULT_TERMS,
      maxGuests: String(current?.max_concurrent_online_guests ?? DEFAULT_TERMS.maxGuests),
      graceDays: hasLicense && current?.grace_period_days != null ? String(current.grace_period_days) : DEFAULT_TERMS.graceDays,
      ...(hasLicense && untilInFuture ? { validMode: "date" as const, validUntil: until } : {}),
      // Only a first license uses site-type module suggestions.
      modules: hasLicense ? [...(current?.modules ?? [])] : [],
      modulesTouched: hasLicense,
    });
    setReason("");
    setErr(null);
    fields.clear();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  useEffect(() => {
    if (open && !hasLicense) setTerms((t) => withSuggestedModules(t, catalog, appliance.site_type));
  }, [open, hasLicense, catalog, appliance.site_type]);

  async function submit() {
    const issue = termsIssue(terms) ?? (reason.trim() ? null : { field: "reason" as const, message: "Enter a reason; it is recorded in the audit log." });
    if (issue) {
      setErr(null);
      fields.fail({ [issue.field]: issue.message });
      return;
    }
    fields.clear();
    setBusy(true);
    setErr(null);
    try {
      await withStepUp(() =>
        api.post(`/cloud/v1/appliances/${appliance.id}/license`, { ...termsBody(terms), reason: reason.trim() }),
      );
      toast.success(hasLicense ? "License updated" : "License issued", appliance.serial);
      onOpenChange(false);
      onDone();
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <DialogForm
      open={open}
      onOpenChange={onOpenChange}
      title={hasLicense ? "Renew or change license" : "Issue a license"}
      description={
        hasLicense
          ? "A new version replaces the current license. The appliance picks it up on its next contact."
          : `Issue a license for ${appliance.serial}.`
      }
      size="md"
      submitLabel={hasLicense ? "Save new license" : "Issue license"}
      busyLabel="Saving…"
      busy={busy}
      error={err}
      onSubmit={submit}
    >
      <LicenseTermsFields value={terms} onChange={setTerms} siteType={appliance.site_type} errorFor={fields.error} idFor={fields.id} />
      <Field label="Reason" required hint="Recorded in the audit log." error={fields.error("reason")}>
        <Input id={fields.id("reason")} value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} placeholder="e.g. annual renewal" />
      </Field>
    </DialogForm>
  );
}

/** Cross-customer moves require retirement and factory reset before activation for the new customer. */
export function MoveDialog({
  appliance,
  open,
  onOpenChange,
  onDone,
}: {
  appliance: Pick<ApplianceRow, "id" | "serial" | "customer_id" | "customer_name" | "site_id">;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onDone: () => void;
}) {
  const toast = useToast();
  const customerId = appliance.customer_id ?? "";
  const [sites, setSites] = useState<Site[] | null>(null);
  const [siteId, setSiteId] = useState("");
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const fields = useFieldErrors<"site" | "reason">();
  const clearFields = fields.clear;

  useEffect(() => {
    if (!open) return;
    setSiteId("");
    setReason("");
    setErr(null);
    clearFields();
    setSites(null);
    if (!customerId) {
      setSites([]);
      return;
    }
    api.get<Items<Site>>(`/cloud/v1/customers/${customerId}/sites`)
      .then((r) => setSites(itemsOf(r).filter((s) => s.status !== "archived" && s.id !== appliance.site_id)))
      .catch((e) => { setSites([]); setErr(e); });
  }, [open, customerId, appliance.site_id, clearFields]);

  async function submit() {
    if (!siteId || !reason.trim()) {
      setErr(null);
      fields.fail(!siteId
        ? { site: "Choose the site it moves to." }
        : { reason: "Enter a reason; it is recorded in the audit log." });
      return;
    }
    fields.clear();
    setBusy(true);
    setErr(null);
    try {
      await withStepUp(() =>
        api.post(`/cloud/v1/appliances/${appliance.id}/move`, {
          customer_id: customerId,
          site_id: siteId,
          reason: reason.trim(),
        }),
      );
      toast.success(`${appliance.serial} moved`, "It picks up its new site and license on its next contact.");
      onOpenChange(false);
      onDone();
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  }

  const customerName = appliance.customer_name ?? "this customer";
  const noOtherSite = sites !== null && sites.length === 0;

  return (
    <DialogForm
      open={open}
      onOpenChange={onOpenChange}
      title={`Move ${appliance.serial}`}
      description={<>Move it to another site of <strong>{customerName}</strong>.</>}
      size="md"
      submitLabel="Move appliance"
      busyLabel="Moving…"
      busy={busy}
      error={err}
      disabled={noOtherSite}
      disabledReason="To continue, the customer needs another active site."
      onSubmit={submit}
    >
      <Field
        label="New site"
        required
        hint={noOtherSite ? (
          <>
            {customerName} has no other active site.{" "}
            {customerId && (
              <Link href={`/customers/${customerId}?tab=sites`} className="underline underline-offset-2">
                Add one on its Sites tab.
              </Link>
            )}
          </>
        ) : undefined}
        error={fields.error("site")}
      >
        <Select id={fields.id("site")} value={siteId} disabled={sites === null || noOtherSite} onChange={(e) => setSiteId(e.target.value)}>
          <option value="">{sites === null ? "Loading…" : "Choose a site…"}</option>
          {(sites ?? []).map((s) => (
            <option key={s.id} value={s.id}>{s.name}</option>
          ))}
        </Select>
      </Field>
      <Callout tone="info">
        Its license is re-issued for the new site with the same terms. To give it to another customer, retire it,
        factory-reset it and activate it for that customer.
      </Callout>
      <Field label="Reason" required hint="Recorded in the audit log." error={fields.error("reason")}>
        <Input id={fields.id("reason")} value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} />
      </Field>
    </DialogForm>
  );
}
