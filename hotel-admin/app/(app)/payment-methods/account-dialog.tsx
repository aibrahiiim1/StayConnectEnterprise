"use client";

// ADD / EDIT A CARD PAYMENT PROVIDER ACCOUNT.
//
// CREDENTIALS ARE WRITE-ONLY, AND THIS DIALOG IS BUILT SO IT CANNOT LEAK ONE. edged never returns a stored
// credential — not a secret key, not a public one — only the NAMES of the keys that are set
// (`credential_keys_set`). So every credential input opens EMPTY, on add and on edit alike, and its placeholder
// says whether a value is already stored ("Set — leave empty to keep") or not ("Not set"). A secret is typed
// into a password field and never echoed.
//
// ONLY WHAT THE OPERATOR TYPED IS SENT. An empty credential field means "keep what is stored", so it is left out
// of the request entirely; sending "" would read to the server as "clear this key". The same rule covers the
// Paymob region, which is stored as the credential key `region`.
//
// LIVE is offered only where the provider says `live_allowed`. Where it does not, the option is present but
// disabled with the reason beside it — an operator who cannot find LIVE at all assumes the console is broken,
// and the server refuses a LIVE save here regardless (409 live_not_authorised).
//
// The reason and the password are the step-up the server requires on every account change.

import { useEffect, useMemo, useState } from "react";
import { api } from "@/lib/api";
import {
  AccountSaveBody, PaymentAccount, PaymentProvider, saveErrorMessage,
} from "@/lib/payment-admin";
import { DialogForm } from "@/components/ui/dialog";
import { Field, Input, Select } from "@/components/ui/input";

type Draft = {
  provider: string;
  region: string;
  merchant_account_ref: string;
  display_name: string;
  currency: string;
  mode: string;
  status: string;
  is_default: boolean;
  credentials: Record<string, string>;
  reason: string;
  password: string;
};

const REGION_KEY = "region";

function initialDraft(account: PaymentAccount | null, providers: PaymentProvider[]): Draft {
  return {
    provider: account?.provider ?? providers[0]?.id ?? "",
    region: "",
    merchant_account_ref: account?.merchant_account_ref ?? "",
    display_name: account?.display_name ?? "",
    currency: account?.currency ?? "",
    mode: account?.mode ?? "TEST",
    status: account?.status ?? "ACTIVE",
    is_default: account?.is_default ?? false,
    credentials: {},
    reason: "",
    password: "",
  };
}

export function AccountDialog({
  open, onOpenChange, account, providers, onSaved,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** null adds a new account. */
  account: PaymentAccount | null;
  providers: PaymentProvider[];
  onSaved: (account: PaymentAccount | null) => void;
}) {
  const [d, setD] = useState<Draft>(() => initialDraft(account, providers));
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Reset on every open, and clear on close: a typed secret or password must not survive in state after the
  // dialog that collected it has gone.
  useEffect(() => {
    setD(initialDraft(open ? account : null, providers));
    setError(null);
  }, [open, account, providers]);

  const provider = useMemo(() => providers.find((p) => p.id === d.provider), [providers, d.provider]);
  const keysSet = useMemo(
    () => new Set(account && account.provider === d.provider ? account.credential_keys_set ?? [] : []),
    [account, d.provider],
  );
  const credentialKeys = (provider?.credential_keys ?? []).filter((k) => k.key !== REGION_KEY);
  const regions = provider?.regions ?? [];
  const liveAllowed = provider?.live_allowed ?? false;

  const set = <K extends keyof Draft>(k: K, v: Draft[K]) => setD((prev) => ({ ...prev, [k]: v }));
  const setCred = (k: string, v: string) => setD((prev) => ({ ...prev, credentials: { ...prev.credentials, [k]: v } }));

  // What still has to be filled before the server can accept it. A required key already stored may be left
  // empty (it is kept); one that is not stored must be typed.
  const missing: string[] = [];
  if (!d.provider) missing.push("a provider");
  if (!d.merchant_account_ref.trim()) missing.push("the merchant account reference");
  if (!d.display_name.trim()) missing.push("a display name");
  if (!/^[A-Za-z]{3}$/.test(d.currency.trim())) missing.push("a three-letter currency");
  if (regions.length > 0 && !d.region && !keysSet.has(REGION_KEY)) missing.push("a region");
  for (const k of credentialKeys) {
    if (k.required && !keysSet.has(k.key) && !(d.credentials[k.key] ?? "").trim()) missing.push(k.label);
  }
  if (d.reason.trim().length < 4) missing.push("a reason");
  if (!d.password) missing.push("your password");

  async function submit() {
    if (missing.length > 0) return;
    const credentials: Record<string, string> = {};
    for (const k of credentialKeys) {
      const v = d.credentials[k.key] ?? "";
      // Secrets are sent exactly as typed; a visible identifier is trimmed.
      const value = k.secret ? v : v.trim();
      if (value !== "") credentials[k.key] = value;
    }
    if (regions.length > 0 && d.region) credentials[REGION_KEY] = d.region;
    const body: AccountSaveBody = {
      ...(account ? { id: account.id } : {}),
      provider: d.provider,
      merchant_account_ref: d.merchant_account_ref.trim(),
      display_name: d.display_name.trim(),
      currency: d.currency.trim().toUpperCase(),
      mode: d.mode,
      status: d.status,
      is_default: d.is_default,
      credentials,
      reason: d.reason.trim(),
      password: d.password,
    };
    setBusy(true); setError(null);
    try {
      const saved = await api.post<PaymentAccount | { account?: PaymentAccount }>("/payment-providers/accounts", body);
      const acct = saved && typeof saved === "object" && "account" in saved ? saved.account ?? null : (saved as PaymentAccount | null);
      onSaved(acct);
    } catch (e) {
      setError(saveErrorMessage(e));
      set("password", "");
    } finally {
      setBusy(false);
    }
  }

  const statusOptions = ["ACTIVE", "DISABLED"].includes(d.status) ? ["ACTIVE", "DISABLED"] : ["ACTIVE", "DISABLED", d.status];

  return (
    <DialogForm
      open={open}
      onOpenChange={onOpenChange}
      title={account ? `Edit ${account.display_name || "provider account"}` : "Add provider account"}
      description="The merchant account at the card payment provider that Client Portal payments are taken through."
      size="lg"
      submitLabel={account ? "Save account" : "Add account"}
      busyLabel="Saving…"
      busy={busy}
      error={error}
      disabled={missing.length > 0}
      onSubmit={submit}
    >
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Provider" required>
          <Select
            value={d.provider}
            // An existing account cannot change provider: its credentials belong to the provider they were issued by.
            disabled={!!account}
            onChange={(e) => setD((prev) => ({ ...prev, provider: e.target.value, region: "", credentials: {}, mode: "TEST" }))}
          >
            {providers.map((p) => <option key={p.id} value={p.id}>{p.label}</option>)}
          </Select>
        </Field>
        {regions.length > 0 && (
          <Field
            label="Region"
            required={!keysSet.has(REGION_KEY)}
            hint={keysSet.has(REGION_KEY) ? "Set — leave unchanged to keep the stored region." : "The provider region this merchant account belongs to."}
          >
            <Select value={d.region} onChange={(e) => set("region", e.target.value)}>
              <option value="">{keysSet.has(REGION_KEY) ? "Keep the stored region" : "Choose a region…"}</option>
              {regions.map((r) => <option key={r} value={r}>{r}</option>)}
            </Select>
          </Field>
        )}
        <Field label="Merchant account reference" required hint="The account or merchant identifier the provider gave you.">
          <Input value={d.merchant_account_ref} maxLength={200} autoComplete="off" onChange={(e) => set("merchant_account_ref", e.target.value)} />
        </Field>
        <Field label="Display name" required hint="How this account is named in the console.">
          <Input value={d.display_name} maxLength={200} onChange={(e) => set("display_name", e.target.value)} />
        </Field>
        <Field label="Currency" required hint="Three letters, as in USD or EGP.">
          <Input
            value={d.currency}
            maxLength={3}
            autoComplete="off"
            className="uppercase"
            onChange={(e) => set("currency", e.target.value.replace(/[^A-Za-z]/g, "").toUpperCase())}
          />
        </Field>
        <Field
          label="Mode"
          required
          hint={liveAllowed ? "TEST takes no real money. LIVE charges real cards." : "LIVE mode is not authorised on this appliance."}
        >
          <Select value={d.mode} onChange={(e) => set("mode", e.target.value)}>
            <option value="TEST">TEST</option>
            <option value="LIVE" disabled={!liveAllowed}>LIVE{liveAllowed ? "" : " — not authorised"}</option>
          </Select>
        </Field>
        <Field label="Status" required hint="A disabled account takes no new payments.">
          <Select value={d.status} onChange={(e) => set("status", e.target.value)}>
            {statusOptions.map((s) => (
              <option key={s} value={s}>{s === "ACTIVE" ? "Active" : s === "DISABLED" ? "Disabled" : s}</option>
            ))}
          </Select>
        </Field>
        <div className="flex items-center sm:pt-7">
          <label className="flex cursor-pointer items-center gap-2.5 text-sm">
            <input
              type="checkbox"
              className="size-4 accent-primary"
              checked={d.is_default}
              onChange={(e) => set("is_default", e.target.checked)}
            />
            Default account for card payments
          </label>
        </div>
      </div>

      {credentialKeys.length > 0 && (
        <fieldset className="space-y-3 rounded-md border border-border px-4 pb-4 pt-2">
          <legend className="px-1 text-label">Credentials</legend>
          <p className="text-xs text-muted-foreground">
            Stored values are never shown. Leave a field empty to keep what is stored; type a value to replace it.
          </p>
          <div className="grid gap-4 sm:grid-cols-2">
            {credentialKeys.map((k) => {
              const stored = keysSet.has(k.key);
              return (
                <Field key={k.key} label={k.label} required={k.required && !stored}>
                  <Input
                    type={k.secret ? "password" : "text"}
                    autoComplete={k.secret ? "new-password" : "off"}
                    spellCheck={false}
                    value={d.credentials[k.key] ?? ""}
                    placeholder={stored ? "Set — leave empty to keep" : "Not set"}
                    onChange={(e) => setCred(k.key, e.target.value)}
                    className="font-mono"
                  />
                </Field>
              );
            })}
          </div>
        </fieldset>
      )}

      <Field label="Reason" required hint={`Recorded in the change history. ${d.reason.length}/500`}>
        <Input value={d.reason} maxLength={500} onChange={(e) => set("reason", e.target.value)} />
      </Field>
      <Field label="Confirm your password" required>
        <Input type="password" autoComplete="current-password" value={d.password} onChange={(e) => set("password", e.target.value)} />
      </Field>
      {missing.length > 0 && (
        <p className="text-xs text-muted-foreground" aria-live="polite">Still needed: {missing.join(", ")}.</p>
      )}
    </DialogForm>
  );
}
