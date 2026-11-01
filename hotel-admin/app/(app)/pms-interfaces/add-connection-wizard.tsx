"use client";

// Commissions a connection in one confirmed action: it creates the connection (paused), saves the configuration,
// stores the credential if the provider needs one, then puts that configuration live and activates the connection.
// Pressing Create is the confirmation, with the administrator's password, so there is no hidden second step.
// A draft that is not completed stays paused: the connection is activated only after every earlier step succeeded.
// After a failure, "Try again" resumes from the failed step and never creates a second connection. Each step uses the
// same audited endpoint as the manual path, so validation, duplicate-source protection and the lifecycle rules apply
// unchanged.

import { useEffect, useMemo, useState } from "react";
import { api } from "@/lib/api";
import {
  Dialog, DialogBody, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle,
} from "@/components/ui/dialog";
import { Button } from "@/components/ui/button";
import { Field, FieldGrid, Input } from "@/components/ui/input";
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

type Progress = {
  id?: string; revisionId?: string; revisionNo?: number; secretStored?: boolean; published?: boolean; activated?: boolean;
};

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

  // A half-finished connection from last time is already on the list.
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
    if (password === "") { setShowErrors(true); return; }
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
      if (!p.published) {
        await api.post(`/pms-interfaces/${p.id}/publish`, {
          revision_id: p.revisionId, expected_revision_id: "", reason_code: "INITIAL_COMMISSIONING", password,
        });
        p = { ...p, published: true };
        setProgress(p);
      }
      if (!p.activated) {
        await api.post(`/pms-interfaces/${p.id}/lifecycle`, { state: "ACTIVE", reason_code: "COMMISSIONING", password });
        p = { ...p, activated: true };
        setProgress(p);
      }
      setPassword("");
      setDone(true);
      toast.success("Connection activated", "The appliance is connecting to the PMS and will load the guest list.");
      await onChanged();
    } catch (e) {
      setErr(e);
      if (p.id) void onChanged();
    } finally {
      setBusy(false);
    }
  }

  const failedStepWords = !progress.id ? null
    : !progress.revisionId ? "The connection was created, but its configuration was not saved."
      : withCreds && !progress.secretStored ? "The connection and its configuration were saved, but the credential was not stored."
        : !progress.published ? "The connection is saved but not live yet: its configuration could not be put live, so it stays paused."
          : !progress.activated ? "The configuration is live, but the connection could not be activated, so it stays paused."
            : null;

  return (
    <Dialog open={open} onOpenChange={(v) => !busy && onOpenChange(v)}>
      <DialogContent size="lg">
        <DialogHeader>
          <DialogTitle>{done ? "Connection activated" : "Add a PMS connection"}</DialogTitle>
          <DialogDescription>
            {done
              ? "It is live and connecting. Point a client network at it on the PMS routing screen so Clients can use it."
              : "Link the appliance to the Site's PMS so Clients can sign in with their room number."}
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
                            <p className="ps-12 text-xs text-muted-foreground">{p.verification_note}</p>
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
                      <ol className="list-decimal space-y-0.5 ps-4">
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
                  <FieldGrid cols={2}>
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
                  </FieldGrid>
                  <ProviderFieldsForm
                    fields={fields}
                    values={values}
                    errors={showErrors ? fieldErrors : {}}
                    onChange={(k, v) => setValues((s) => ({ ...s, [k]: v }))}
                  />
                  {provider.kind === PROTEL_KIND && (
                    <Callout tone="neutral" title="Fixed for this connection">
                      <ul className="list-disc space-y-0.5 ps-4 text-xs">
                        <li>OneGate reads the guest list from the PMS. The only thing it ever sends is a room charge, and only once room charge is approved for this connection.</li>
                        <li>No credential required — the link needs none.</li>
                        <li>
                          Charging a room through the PMS stays off until a site administrator approves the
                          connection for room charge under Hotel → Room charge.
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
                  <FieldGrid cols={2}>
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
                  </FieldGrid>
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
                    <ol className="list-decimal space-y-0.5 ps-4">
                      <li>The connection is added and these settings are saved.</li>
                      {withCreds && <li>The credential is stored, encrypted.</li>}
                      <li>The settings are put live and the connection is activated: the appliance connects to the PMS and loads the guest list.</li>
                    </ol>
                    <p className="mt-1">
                      If a step fails the connection stays paused and you can try again from that step. Room charge stays
                      off; this connection only reads the guest list.
                    </p>
                  </Callout>
                  {!progress.activated && (
                    <Field label="Confirm your password" required
                      hint="Commissioning a connection needs your own admin password."
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
                Open the connection
              </Button>
            </>
          ) : (
            <>
              {step > 0 && !progress.id && (
                <Button variant="ghost" className="me-auto" disabled={busy} onClick={() => setStep((s) => s - 1)}>
                  Back
                </Button>
              )}
              <Button variant="ghost" disabled={busy} onClick={() => onOpenChange(false)}>Cancel</Button>
              {step < reviewStep ? (
                <Button onClick={next} disabled={busy}>Next</Button>
              ) : (
                <Button onClick={() => void create()} disabled={busy}>
                  {busy ? "Working…" : progress.id ? "Try again" : "Create and activate"}
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
      <Callout tone="success" title={`${name} is live`}>
        Version {progress.revisionNo ?? 1} is the configuration in use
        {withCreds ? " and its credential is stored" : ""}. The appliance is connecting to {provider.label} and will
        load the guest list; this page shows whether it connected.
      </Callout>
      <div className="space-y-2 text-sm">
        <p className="font-medium">Next</p>
        <ol className="list-decimal space-y-1 ps-5 text-muted-foreground">
          <li>Check the connection status: Connected, then In sync once the guest list has loaded.</li>
          <li>Point at least one client network at it on the PMS routing screen. It decides which property&rsquo;s guests are checked on which network.</li>
        </ol>
      </div>
    </div>
  );
}
