"use client";

// THE APPLIANCE'S WRITE DIALOGS. Each is one §6 call wrapped in withStepUp (every one is marked SU), and each
// says in plain words what will happen before it happens.

import { useEffect, useState } from "react";
import { api, withStepUp, type ApplianceRow } from "@/lib/api";
import { DialogForm } from "@/components/ui/dialog";
import { Field, Input } from "@/components/ui/input";
import { Callout } from "@/components/ui/error-banner";
import { useToast } from "@/components/ui/toast";
import { DEFAULT_TERMS, LicenseTermsFields, termsBody, termsProblem, type TermsDraft } from "@/components/license-terms";
import {
  PlacementFields, emptyPlacement, placementBody, placementProblem, type Placement,
} from "@/components/placement-fields";

function Section({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <fieldset className="space-y-3">
      <legend className="mb-3 text-emphasis">{title}</legend>
      {children}
    </fieldset>
  );
}

/** Activate a waiting appliance: where it goes and what its license allows, in one step. */
export function ActivateDialog({
  appliance,
  open,
  onOpenChange,
  onDone,
}: {
  appliance: Pick<ApplianceRow, "id" | "serial">;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onDone: () => void;
}) {
  const toast = useToast();
  const [placement, setPlacement] = useState<Placement>(emptyPlacement());
  const [terms, setTerms] = useState<TermsDraft>(DEFAULT_TERMS);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);

  useEffect(() => {
    if (open) {
      setPlacement(emptyPlacement());
      setTerms(DEFAULT_TERMS);
      setErr(null);
    }
  }, [open]);

  async function submit() {
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
      <Section title="Where it is installed">
        <PlacementFields value={placement} onChange={setPlacement} />
      </Section>
      <Section title="License">
        <LicenseTermsFields value={terms} onChange={setTerms} />
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
  appliance: Pick<ApplianceRow, "id" | "serial" | "license">;
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

  useEffect(() => {
    if (!open) return;
    setTerms({
      ...DEFAULT_TERMS,
      maxGuests: String(current?.max_concurrent_online_guests ?? DEFAULT_TERMS.maxGuests),
    });
    setReason("");
    setErr(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

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
      <LicenseTermsFields value={terms} onChange={setTerms} />
      <Field label="Reason" required hint="Recorded in the audit log.">
        <Input value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} placeholder="e.g. annual renewal" />
      </Field>
    </DialogForm>
  );
}

/** Move an activated appliance to another site or customer. */
export function MoveDialog({
  appliance,
  open,
  onOpenChange,
  onDone,
}: {
  appliance: Pick<ApplianceRow, "id" | "serial" | "customer_id" | "site_id">;
  open: boolean;
  onOpenChange: (v: boolean) => void;
  onDone: () => void;
}) {
  const toast = useToast();
  const [placement, setPlacement] = useState<Placement>(emptyPlacement());
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);

  useEffect(() => {
    if (open) {
      setPlacement(emptyPlacement(appliance.customer_id ?? "", ""));
      setReason("");
      setErr(null);
    }
  }, [open, appliance.customer_id]);

  const crossCustomer = !!placement.customerId && placement.customerId !== appliance.customer_id;

  async function submit() {
    const problem =
      placementProblem(placement) ??
      (placement.siteId === appliance.site_id ? "Choose a different site." : null) ??
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
          customer_id: placement.customerId,
          site_id: placement.siteId,
          reason: reason.trim(),
        }),
      );
      toast.success(`${appliance.serial} moved`);
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
      title={`Move ${appliance.serial}`}
      description="Assign this appliance to another site."
      size="md"
      submitLabel="Move appliance"
      busyLabel="Moving…"
      busy={busy}
      error={err}
      onSubmit={submit}
    >
      <PlacementFields value={placement} onChange={setPlacement} allowNew={false} />
      {crossCustomer && (
        <Callout tone="warning" title="Moving to another customer">
          Its license is revoked and the appliance erases the previous customer&apos;s data. Set a new license after
          the move.
        </Callout>
      )}
      <Field label="Reason" required hint="Recorded in the audit log.">
        <Input value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} />
      </Field>
    </DialogForm>
  );
}
