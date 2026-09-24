"use client";

// ADD A PMS CONNECTION — choose the system, describe it, give it its credential, review, create.
//
// What the wizard does, in order, and nothing more:
//   1. creates the connection (it starts with room sign-in paused — it cannot connect to anything yet);
//   2. saves its configuration as a DRAFT;
//   3. for a provider that signs in with a key, stores that credential (encrypted; needs the operator's password).
// It does NOT publish and does NOT activate. Both change what happens to guests, both are separate confirmed
// actions, and the last screen says so and points at them.
//
// If a step fails, what already succeeded is kept and "Try again" resumes from the failed step — pressing it
// never creates a second connection.

import { useEffect, useMemo, useState } from "react";
import { api } from "@/lib/api";
import {
  Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Field, Input } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { KeyValueGrid, OptionCard, Stepper } from "@/components/ui/data";
import { useToast } from "@/components/ui/toast";
import {
  type FormValues, type PmsProvider,
  PROTEL_KIND, TRANSPORT_KIND_WORDS, credentialSecret, fieldsFor, initialValues, needsCredential,
  pmsErrorText, providerRevisionBody, validateAll, verificationWords,
} from "@/lib/api/pms-connections";
import { ProviderFieldsForm } from "./provider-fields";
import { ProviderTile } from "./connection-card";

type Progress = { id?: string; revisionId?: string; revisionNo?: number; secretStored?: boolean };

export function AddConnectionWizard({
  open,
  onOpenChange,
  providers,
  catalogueAvailable,
  onChanged,
  onOpenConnection,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  providers: PmsProvider[];
  catalogueAvailable: boolean;
  onChanged: () => void | Promise<void>;
  /** Opens the new connection's sheet, on its configuration, to publish and activate. */
  onOpenConnection: (id: string) => void;
}) {
  const toast = useToast();
  const [kind, setKind] = useState<string>("");
  const [step, setStep] = useState(0);
  const [name, setName] = useState("");
  const [timezone, setTimezone] = useState("Africa/Cairo");
  const [values, setValues] = useState<FormValues>({});
  const [creds, setCreds] = useState<Record<string, string>>({});
  const [secretReason] = useState("INITIAL_COMMISSIONING");
  const [password, setPassword] = useState("");
  const [showErrors, setShowErrors] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [progress, setProgress] = useState<Progress>({});
  const [done, setDone] = useState(false);

  const provider = providers.find((p) => p.kind === kind) ?? null;
  const fields = useMemo(() => (provider ? fieldsFor(provider) : []), [provider]);
  const withCreds = needsCredential(provider);
  const steps = withCreds
    ? ["Provider", "Connection", "Credentials", "Review"]
    : ["Provider", "Connection", "Review"];
  const reviewStep = steps.length - 1;

  // A fresh wizard every time it opens. A half-finished connection from last time is already on the list.
  useEffect(() => {
    if (!open) return;
    const only = providers.length === 1 ? providers[0].kind : "";
    setKind(only);
    setStep(0);
    setName("");
    setTimezone("Africa/Cairo");
    setValues(only ? initialValues(fieldsFor(providers[0])) : {});
    setCreds({});
    setPassword("");
    setShowErrors(false);
    setErr(null);
    setProgress({});
    setDone(false);
  }, [open, providers]);

  function choose(k: string) {
    setKind(k);
    const p = providers.find((x) => x.kind === k);
    setValues(p ? initialValues(fieldsFor(p)) : {});
    setCreds({});
  }

  const fieldErrors = useMemo(() => validateAll(fields, values), [fields, values]);
  const nameError = !name.trim() ? "Required." : name.trim().length > 120 ? "At most 120 characters." : null;
  const tzError = !timezone.trim() ? "Required." : null;
  const credErrors: Record<string, string> = {};
  for (const f of provider?.credential?.fields ?? []) {
    if (f.required && !(creds[f.key] ?? "").trim()) credErrors[f.key] = "Required.";
  }

  const stepValid = (s: number): boolean => {
    if (s === 0) return !!provider;
    if (s === 1) return !nameError && !tzError && Object.keys(fieldErrors).length === 0;
    if (withCreds && s === 2) return Object.keys(credErrors).length === 0;
    return true;
  };

  function next() {
    if (!stepValid(step)) { setShowErrors(true); return; }
    setShowErrors(false);
    setStep((s) => Math.min(s + 1, reviewStep));
  }

  async function create() {
    if (!provider) return;
    if (withCreds && !progress.secretStored && password === "") { setShowErrors(true); return; }
    setBusy(true); setErr(null);
    let p = { ...progress };
    try {
      if (!p.id) {
        const r = await api.post<{ id: string }>("/pms-interfaces", {
          connector_kind: provider.kind, display_label: name.trim(),
        });
        p = { ...p, id: r.id };
        setProgress(p);
      }
      if (!p.revisionId) {
        const r = await api.post<{ revision_id: string; revision_no: number }>(
          `/pms-interfaces/${p.id}/revisions`, providerRevisionBody(provider, values, timezone),
        );
        p = { ...p, revisionId: r.revision_id, revisionNo: r.revision_no };
        setProgress(p);
      }
      if (withCreds && !p.secretStored) {
        await api.post(`/pms-interfaces/${p.id}/secret`, {
          secret: credentialSecret(provider, creds), reason_code: secretReason, password,
        });
        p = { ...p, secretStored: true };
        setProgress(p);
      }
      setPassword("");
      setDone(true);
      toast.success("Connection created", "Its configuration is saved as a draft. Publish and activate it next.");
      await onChanged();
    } catch (e) {
      setErr(e);
      // Whatever did succeed is on the list now; show it rather than waiting for the operator to close this.
      if (p.id) void onChanged();
    } finally {
      setBusy(false);
    }
  }

  const failedStepWords = !progress.id ? null
    : !progress.revisionId ? "The connection was created, but its configuration was not saved."
      : withCreds && !progress.secretStored ? "The connection and its configuration were saved, but the credential was not stored."
        : null;

  return (
    <Dialog open={open} onOpenChange={(v) => !busy && onOpenChange(v)}>
      <DialogContent size="lg">
        <DialogHeader>
          <DialogTitle>{done ? "Connection created" : "Add a PMS connection"}</DialogTitle>
          <DialogDescription>
            {done
              ? "One more step before guests can use it: publish its configuration and activate it."
              : "Connect the appliance to the hotel's property management system so guests can sign in with their room number."}
          </DialogDescription>
          {!done && <Stepper className="pt-3" steps={steps} current={step} onStep={progress.revisionId ? undefined : (i) => !busy && setStep(i)} />}
        </DialogHeader>

        <DialogBody className="space-y-4">
          {done ? (
            <DoneView provider={provider!} name={name.trim()} progress={progress} withCreds={withCreds} />
          ) : (
            <>
              <ErrorBanner err={err ? `${failedStepWords ? failedStepWords + " " : ""}${pmsErrorText(err)}` : null} />

              {step === 0 && (
                <div className="space-y-3">
                  {!catalogueAvailable && (
                    <Callout tone="neutral" title="Only Protel is offered">
                      The list of supported property management systems could not be loaded from this appliance,
                      so only the connection every appliance supports is shown.
                    </Callout>
                  )}
                  <div role="radiogroup" aria-label="Property management system" className="grid gap-3 sm:grid-cols-2">
                    {providers.map((p) => {
                      const v = verificationWords(p.verification);
                      return (
                        <OptionCard
                          key={p.kind}
                          name="pms-provider"
                          value={p.kind}
                          checked={kind === p.kind}
                          onChange={choose}
                          icon={<ProviderTile label={p.label} bare />}
                          title={p.label}
                          badge={<Badge tone={v.tone} dot>{v.label}</Badge>}
                          description={[p.vendor, p.integration, p.transport ? TRANSPORT_KIND_WORDS[p.transport] : null]
                            .filter(Boolean).join(" · ")}
                        >
                          {p.verification_note && (
                            <p className="pl-12 text-xs text-muted-foreground">{p.verification_note}</p>
                          )}
                        </OptionCard>
                      );
                    })}
                  </div>
                  {showErrors && !provider && <p className="text-sm text-destructive">Choose a system to continue.</p>}
                </div>
              )}

              {step === 1 && provider && (
                <div className="space-y-4">
                  {(provider.setup_steps?.length ?? 0) > 0 && (
                    <Callout tone="info" title={`Before you start at ${provider.vendor || "the PMS"}`}>
                      <ol className="list-decimal space-y-0.5 pl-4">
                        {provider.setup_steps!.map((s, i) => <li key={i}>{s}</li>)}
                      </ol>
                      {provider.docs_url && (
                        <a href={provider.docs_url} target="_blank" rel="noreferrer"
                          className="mt-1 inline-block font-medium underline underline-offset-2">
                          Provider documentation
                        </a>
                      )}
                    </Callout>
                  )}
                  <div className="grid gap-4 sm:grid-cols-2">
                    <Field label="Name" required hint="How this connection appears throughout the admin."
                      error={showErrors ? nameError : undefined}>
                      <Input value={name} maxLength={120} placeholder="Front office PMS"
                        onChange={(e) => setName(e.target.value)} />
                    </Field>
                    <Field label="PMS time zone" required hint="Time zone name, for example Europe/Berlin. Arrivals and departures are read in it."
                      error={showErrors ? tzError : undefined}>
                      <Input value={timezone} placeholder="Africa/Cairo" spellCheck={false}
                        onChange={(e) => setTimezone(e.target.value)} />
                    </Field>
                  </div>
                  <ProviderFieldsForm
                    fields={fields}
                    values={values}
                    errors={showErrors ? fieldErrors : {}}
                    onChange={(k, v) => setValues((s) => ({ ...s, [k]: v }))}
                  />
                  {provider.kind === PROTEL_KIND && (
                    <Callout tone="neutral" title="Fixed for this connection">
                      <ul className="list-disc space-y-0.5 pl-4 text-xs">
                        <li>Read-only — StayConnect never writes to the PMS.</li>
                        <li>No credential required — the link needs none.</li>
                        <li>
                          Charging a room through the PMS stays off until the property&rsquo;s folio behaviour has
                          been determined.
                        </li>
                      </ul>
                    </Callout>
                  )}
                </div>
              )}

              {withCreds && step === 2 && provider && (
                <div className="space-y-4">
                  <Callout tone="neutral">
                    Stored encrypted on the appliance and never shown again — not here, not to any other operator.
                    You can replace it later; you cannot read it back.
                  </Callout>
                  <div className="grid gap-4 sm:grid-cols-2">
                    {(provider.credential?.fields ?? []).map((f) => (
                      <Field key={f.key} label={f.label} required={f.required} hint={f.help}
                        error={showErrors ? credErrors[f.key] : undefined}>
                        <Input
                          type={f.secret === false ? "text" : "password"}
                          autoComplete={f.secret === false ? "off" : "new-password"}
                          spellCheck={false}
                          value={creds[f.key] ?? ""}
                          onChange={(e) => setCreds((c) => ({ ...c, [f.key]: e.target.value }))}
                        />
                      </Field>
                    ))}
                  </div>
                </div>
              )}

              {step === reviewStep && provider && (
                <div className="space-y-4">
                  <KeyValueGrid
                    items={[
                      { label: "System", value: provider.label },
                      { label: "Name", value: name.trim() },
                      { label: "PMS time zone", value: timezone.trim() },
                      ...fields
                        .filter((f) => !f.advanced)
                        .map((f) => ({
                          label: f.label,
                          value: typeof values[f.key] === "boolean"
                            ? (values[f.key] ? "Yes" : "No")
                            : (String(values[f.key] ?? "").trim() || "—"),
                        })),
                      ...(withCreds
                        ? (provider.credential?.fields ?? []).map((f) => ({
                          label: f.label,
                          value: (creds[f.key] ?? "").trim()
                            ? (f.secret === false ? creds[f.key] : "Entered — hidden")
                            : "—",
                        }))
                        : []),
                    ]}
                  />
                  <Callout tone="info" title="What happens when you press Create">
                    <ol className="list-decimal space-y-0.5 pl-4">
                      <li>The connection is added with room sign-in paused. It does not contact the PMS yet.</li>
                      <li>These settings are saved as a draft configuration.</li>
                      {withCreds && <li>The credential is stored, encrypted.</li>}
                    </ol>
                    <p className="mt-1">
                      Publishing the configuration and activating the connection are separate steps you confirm
                      afterwards.
                    </p>
                  </Callout>
                  {withCreds && !progress.secretStored && (
                    <Field label="Confirm your password" required
                      hint="Storing a PMS credential needs your own admin password."
                      error={showErrors && password === "" ? "Required." : undefined}>
                      <Input type="password" autoComplete="current-password" value={password}
                        onChange={(e) => setPassword(e.target.value)} />
                    </Field>
                  )}
                </div>
              )}
            </>
          )}
        </DialogBody>

        <DialogFooter>
          {done ? (
            <>
              <Button variant="ghost" onClick={() => onOpenChange(false)}>Close</Button>
              <Button onClick={() => { onOpenChange(false); if (progress.id) onOpenConnection(progress.id); }}>
                Publish and activate
              </Button>
            </>
          ) : (
            <>
              {step > 0 && !progress.id && (
                <Button variant="ghost" className="mr-auto" disabled={busy} onClick={() => setStep((s) => s - 1)}>
                  Back
                </Button>
              )}
              <Button variant="ghost" disabled={busy} onClick={() => onOpenChange(false)}>Cancel</Button>
              {step < reviewStep ? (
                <Button onClick={next} disabled={busy}>Next</Button>
              ) : (
                <Button onClick={() => void create()} disabled={busy}>
                  {busy ? "Creating…" : progress.id ? "Try again" : "Create connection"}
                </Button>
              )}
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}

function DoneView({
  provider, name, progress, withCreds,
}: { provider: PmsProvider; name: string; progress: Progress; withCreds: boolean }) {
  return (
    <div className="space-y-4">
      <Callout tone="success" title={`${name} was added`}>
        Its configuration is saved as draft version {progress.revisionNo ?? 1}
        {withCreds ? " and its credential is stored" : ""}. Room sign-in is paused, and the appliance has not
        contacted the PMS.
      </Callout>
      <div className="space-y-2 text-sm">
        <p className="font-medium">Next: publish and activate</p>
        <ol className="list-decimal space-y-1 pl-5 text-muted-foreground">
          <li>Publish the draft configuration — this makes it the one the connection uses.</li>
          <li>Activate the connection — the appliance connects to {provider.label} and loads the guest list.</li>
          <li>Point at least one guest network at it on the network routing screen.</li>
        </ol>
        <p className="text-muted-foreground">Both steps ask for your password.</p>
      </div>
    </div>
  );
}
