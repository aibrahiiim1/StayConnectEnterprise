"use client";

// THE APPLIANCE'S WRITE DIALOGS. Each is one §6 call wrapped in withStepUp (every one is marked SU), and each
// says in plain words what will happen before it happens.

import Link from "next/link";
import { useEffect, useState } from "react";
import { api, itemsOf, withStepUp, type ApplianceRow, type Items, type Site } from "@/lib/api";
import { DialogForm } from "@/components/ui/dialog";
import { Field, Input, Select } from "@/components/ui/input";
import { Callout } from "@/components/ui/error-banner";
import { useToast } from "@/components/ui/toast";
import {
  DEFAULT_TERMS, LicenseTermsFields, termsBody, termsProblem, withSuggestedModules, type TermsDraft,
} from "@/components/license-terms";
import {
  PlacementFields, emptyPlacement, placementBody, placementProblem, placementSiteType, type Placement,
} from "@/components/placement-fields";
import { useModuleCatalog } from "@/lib/modules";

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <fieldset className="space-y-3">
      <legend className="mb-3 text-emphasis">{title}</legend>
      {children}
    </fieldset>
  );
}

/** The server's operator sentence for 409 holds_other_customer_data, shown before the call when the outcome is known. */
const HOLDS_OTHER_CUSTOMER =
  "This appliance still holds another customer's data. Factory-reset it before activating it for a different customer.";

/** Activate a waiting appliance: where it goes and what its license allows, in one step. */
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
  // An appliance that still holds a customer's data is activated for that customer or not at all (the server
  // refuses any other with 409 holds_other_customer_data). Preselect it and lock the choice.
  const heldId = appliance.holds_customer_id ?? "";
  const heldName = appliance.holds_customer_name ?? "";
  const heldGone = !!heldId && !heldName; // the customer it holds is no longer in Central
  const [placement, setPlacement] = useState<Placement>(emptyPlacement(heldGone ? "" : heldId));
  const [terms, setTerms] = useState<TermsDraft>(DEFAULT_TERMS);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const { catalog } = useModuleCatalog();
  const siteType = placementSiteType(placement);

  useEffect(() => {
    if (open) {
      setPlacement(emptyPlacement(heldGone ? "" : heldId));
      setTerms(DEFAULT_TERMS);
      setErr(null);
    }
  }, [open, heldId, heldGone]);

  // A new license starts from the site type's suggested modules until the operator changes them by hand.
  useEffect(() => {
    setTerms((t) => withSuggestedModules(t, catalog, siteType));
  }, [catalog, siteType]);

  async function submit() {
    if (heldGone) {
      setErr(HOLDS_OTHER_CUSTOMER);
      return;
    }
    const problem = placementProblem(placement) ?? termsProblem(terms);
    if (problem) {
      setErr(problem);
      return;
    }
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
        />
      </Section>
      <Section title="License">
        <LicenseTermsFields value={terms} onChange={setTerms} siteType={siteType} />
      </Section>
    </DialogForm>
  );
}

/** Issue, renew or change a license. Always a new signed version that replaces the current one. */
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

  useEffect(() => {
    if (!open) return;
    setTerms({
      ...DEFAULT_TERMS,
      maxGuests: String(current?.max_concurrent_online_guests ?? DEFAULT_TERMS.maxGuests),
      // A change keeps the current license's modules unless the operator edits them. Only a first license
      // starts from the site type's suggestion.
      modules: hasLicense ? [...(current?.modules ?? [])] : [],
      modulesTouched: hasLicense,
    });
    setReason("");
    setErr(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  useEffect(() => {
    if (open && !hasLicense) setTerms((t) => withSuggestedModules(t, catalog, appliance.site_type));
  }, [open, hasLicense, catalog, appliance.site_type]);

  async function submit() {
    const problem = termsProblem(terms) ?? (reason.trim() ? null : "Enter a reason; it is recorded in the audit log.");
    if (problem) {
      setErr(problem);
      return;
    }
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
      <LicenseTermsFields value={terms} onChange={setTerms} siteType={appliance.site_type} />
      <Field label="Reason" required hint="Recorded in the audit log.">
        <Input value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} placeholder="e.g. annual renewal" />
      </Field>
    </DialogForm>
  );
}

/**
 * Move an activated appliance to another site of the SAME customer. Another customer is not a move: the server
 * refuses it (409) and the console does not offer it. That path is Retire → factory reset → activate for the new
 * customer.
 */
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

  useEffect(() => {
    if (!open) return;
    setSiteId("");
    setReason("");
    setErr(null);
    setSites(null);
    if (!customerId) {
      setSites([]);
      return;
    }
    api.get<Items<Site>>(`/cloud/v1/customers/${customerId}/sites`)
      // Only the customer's other active sites: the current one is where it already is, and an archived site
      // takes no appliance.
      .then((r) => setSites(itemsOf(r).filter((s) => s.status !== "archived" && s.id !== appliance.site_id)))
      .catch((e) => { setSites([]); setErr(e); });
  }, [open, customerId, appliance.site_id]);

  async function submit() {
    const problem =
      (siteId ? null : "Choose the site it moves to.") ??
      (reason.trim() ? null : "Enter a reason; it is recorded in the audit log.");
    if (problem) {
      setErr(problem);
      return;
    }
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
      // A refusal (409) or licensing being unavailable (503, nothing changed) carries the server's own
      // sentence; the dialog shows it as it is.
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
      >
        <Select value={siteId} disabled={sites === null || noOtherSite} onChange={(e) => setSiteId(e.target.value)}>
          <option value="">{sites === null ? "Loading…" : "Choose a site…"}</option>
          {(sites ?? []).map((s) => (
            <option key={s.id} value={s.id}>{s.name}</option>
          ))}
        </Select>
      </Field>
      <Callout tone="info">Its license is re-issued for the new site with the same terms.</Callout>
      <p className="text-caption text-muted-foreground">
        To give it to another customer, retire it, factory-reset it and activate it for that customer.
      </p>
      <Field label="Reason" required hint="Recorded in the audit log.">
        <Input value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} />
      </Field>
    </DialogForm>
  );
}
