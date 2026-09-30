"use client";

// CARD PAYMENT CONFIGURATION — provider accounts, the provider payment pages clients may reach before signing
// in, the two card payment timings, and the history of changes to all three.
//
// Rendered by Payment methods only once the Card payment module is MANAGEABLE (licensed for this site). Before
// that there is nothing here an operator could usefully set, and every request would 403 or 409.
//
// EVERY CHANGE IS A STEP-UP. Accounts, domains and timings each take a reason and the operator's password,
// because each one decides where clients' card payments go or how long they wait. The reason lands in the change
// history at the bottom of this section, which is also where an auditor starts.
//
// NOTHING HERE MOVES MONEY. "Test connection" asks the provider whether the stored credentials are accepted; it
// charges nothing. Refunds and chargebacks are not started from OneGate at all: when the provider reports one
// it is recorded in Settlements.

import { useCallback, useEffect, useMemo, useState } from "react";
import { api, ApiError } from "@/lib/api";
import {
  AccountsResp, CardSettings, PaymentAccount, PaymentChange, PaymentProvider, ProvidersResp, TestResult,
  saveErrorMessage,
} from "@/lib/payment-admin";
import { formatDate } from "@/lib/utils";
import { Card, CardBody, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/dialog";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { Field, Input } from "@/components/ui/input";
import { SkeletonRows } from "@/components/ui/misc";
import { SettingField } from "@/components/ui/patterns";
import { Table, TBody, TD, TH, THead, TR, TableWrap } from "@/components/ui/table";
import { useToast } from "@/components/ui/toast";
import { AccountDialog } from "./account-dialog";
import { CodeList, CodeText } from "./codes";
import { Globe, Pencil, Plus, PlugZap, Trash2 } from "lucide-react";

const MAX_EXTRA_DOMAINS = 20;

/**
 * Why a typed domain is refused, or null when it is acceptable. The server validates again; this only saves a
 * round trip. A domain name only — never an IP address, never a path — with no wildcard (the garden opens the addresses a name resolves to, and a wildcard names no host).
 */
export function domainProblem(raw: string): string | null {
  const s = raw.trim().toLowerCase();
  if (!s) return "Enter a domain name.";
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(s) || s.includes(":") || s.startsWith("[")) {
    return "IP addresses are not allowed. Enter a domain name.";
  }
  if (/[\/\s?#@]/.test(s)) return "Enter the domain name only, without https:// or a path.";
  if (s.includes("*")) return "List each payment host name; a wildcard cannot be opened before sign-in.";
  const rest = s;
  if (s.length > 253) return "That domain name is too long.";
  const labels = rest.split(".");
  if (labels.length < 2) return "Enter a full domain name, such as pay.example.com.";
  for (const l of labels) {
    if (!/^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$/.test(l)) return "That is not a valid domain name.";
  }
  if (/^\d+$/.test(labels[labels.length - 1])) return "That is not a valid domain name.";
  return null;
}

const KIND_LABEL: Record<string, string> = { account: "Provider account", domains: "Payment domains", settings: "Timings" };

function summariseDetail(detail: unknown): string {
  if (detail === null || detail === undefined || detail === "") return "—";
  if (typeof detail === "string") return detail;
  if (Array.isArray(detail)) return detail.map((v) => summariseDetail(v)).join(", ");
  if (typeof detail === "object") {
    return Object.entries(detail as Record<string, unknown>)
      .map(([k, v]) => `${k.replace(/_/g, " ")}: ${typeof v === "object" && v !== null ? summariseDetail(v) : String(v)}`)
      .join(" · ");
  }
  return String(detail);
}

export function CardPaymentConfig({ writable }: { writable: boolean }) {
  const toast = useToast();
  const [providers, setProviders] = useState<ProvidersResp | null>(null);
  const [accounts, setAccounts] = useState<AccountsResp | null>(null);
  const [settings, setSettings] = useState<CardSettings | null>(null);
  const [changes, setChanges] = useState<PaymentChange[] | null>(null);
  const [err, setErr] = useState<unknown>(null);

  const load = useCallback(async () => {
    setErr(null);
    const [p, a, s, c] = await Promise.allSettled([
      api.get<ProvidersResp>("/payment-providers/providers"),
      api.get<AccountsResp>("/payment-providers/accounts"),
      api.get<CardSettings>("/payment-providers/settings"),
      api.get<{ changes: PaymentChange[] }>("/payment-providers/changes"),
    ]);
    if (p.status === "fulfilled") setProviders(p.value); else setErr(p.reason);
    if (a.status === "fulfilled") setAccounts(a.value); else setErr(a.reason);
    if (s.status === "fulfilled") setSettings(s.value); else setErr(s.reason);
    // The history is evidence, not a control: a failure to read it must not stop the rest being managed.
    setChanges(c.status === "fulfilled" ? c.value.changes ?? [] : []);
  }, []);
  useEffect(() => { load(); }, [load]);

  const reloadHistory = useCallback(async () => {
    try {
      const c = await api.get<{ changes: PaymentChange[] }>("/payment-providers/changes");
      setChanges(c.changes ?? []);
    } catch { /* the save itself succeeded; the history refreshes on the next load */ }
  }, []);

  return (
    <div className="space-y-5">
      <ErrorBanner err={err} className="mb-0" />
      {providers?.engine_unready && (
        <Callout tone="warning" title="The card payment engine is not ready on this appliance">
          <CodeText code={providers.engine_unready} />
        </Callout>
      )}
      <AccountsCard
        providers={providers?.providers ?? null}
        accounts={accounts}
        writable={writable}
        onChanged={async () => {
          try { setAccounts(await api.get<AccountsResp>("/payment-providers/accounts")); } catch (e) { setErr(e); }
          await reloadHistory();
        }}
        toastSuccess={toast.success}
      />
      <DomainsCard
        providers={providers?.providers ?? null}
        extra={accounts?.extra_domains ?? null}
        writable={writable}
        onSaved={async (domains) => {
          setAccounts((prev) => (prev ? { ...prev, extra_domains: domains } : prev));
          toast.success("Payment domains saved");
          await reloadHistory();
        }}
      />
      <SettingsCard
        settings={settings}
        writable={writable}
        onSaved={async (next) => {
          setSettings(next);
          toast.success("Card payment timings saved");
          await reloadHistory();
        }}
      />
      <HistoryCard changes={changes} />
    </div>
  );
}

/* ------------------------------------------------------------------------------------------------------ */
/* Accounts                                                                                                */
/* ------------------------------------------------------------------------------------------------------ */

function AccountsCard({
  providers, accounts, writable, onChanged, toastSuccess,
}: {
  providers: PaymentProvider[] | null;
  accounts: AccountsResp | null;
  writable: boolean;
  onChanged: () => Promise<void>;
  toastSuccess: (t: string, d?: string) => void;
}) {
  const [editing, setEditing] = useState<PaymentAccount | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [testing, setTesting] = useState<string | null>(null);
  const [results, setResults] = useState<Record<string, TestResult>>({});

  const providerLabel = (id: string) => providers?.find((p) => p.id === id)?.label ?? id;

  async function test(a: PaymentAccount) {
    setTesting(a.id);
    try {
      const r = await api.post<TestResult>(`/payment-providers/accounts/${encodeURIComponent(a.id)}/test`, {});
      setResults((prev) => ({ ...prev, [a.id]: r }));
    } catch (e) {
      const body = e instanceof ApiError ? e.body : null;
      setResults((prev) => ({
        ...prev,
        [a.id]: { ok: false, error: body?.error ?? "request_failed", message: saveErrorMessage(e) },
      }));
    } finally {
      setTesting(null);
    }
  }

  return (
    <Card>
      <CardHeader className="items-start">
        <div className="min-w-0 space-y-1">
          <CardTitle>Provider accounts</CardTitle>
          <CardDescription>The merchant accounts card payments are taken through. The default account is used for new payments.</CardDescription>
        </div>
        {writable && (
          <Button
            size="sm"
            disabled={!providers || providers.length === 0}
            onClick={() => { setEditing(null); setDialogOpen(true); }}
          >
            <Plus /> Add account
          </Button>
        )}
      </CardHeader>
      {accounts && !accounts.ready && accounts.readiness.length > 0 && (
        <CardBody className="border-b border-border">
          <Callout tone="warning" title="Card payment is not ready">
            <CodeList codes={accounts.readiness} />
          </Callout>
        </CardBody>
      )}
      <TableWrap>
        {accounts === null ? (
          <SkeletonRows rows={2} cols={5} />
        ) : accounts.accounts.length === 0 ? (
          <CardBody>
            <p className="text-sm text-muted-foreground">
              No provider account yet. Card payment cannot be offered to clients until one is added and active.
            </p>
          </CardBody>
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>Account</TH>
                <TH>Provider</TH>
                <TH>Currency</TH>
                <TH>Mode</TH>
                <TH>Status</TH>
                <TH>Credentials</TH>
                <TH className="text-right">Actions</TH>
              </TR>
            </THead>
            <TBody>
              {accounts.accounts.map((a) => {
                const r = results[a.id];
                return (
                  <TR key={a.id}>
                    <TD>
                      <div className="font-medium">{a.display_name || "—"}</div>
                      <div className="font-mono text-xs text-muted-foreground">{a.merchant_account_ref}</div>
                      {a.is_default && <Badge tone="accent" className="mt-1">Default</Badge>}
                    </TD>
                    <TD>{providerLabel(a.provider)}</TD>
                    <TD className="font-mono">{a.currency}</TD>
                    <TD><Badge tone={a.mode === "LIVE" ? "warn" : "info"}>{a.mode}</Badge></TD>
                    <TD>
                      <Badge tone={a.status === "ACTIVE" ? "ok" : "default"} dot={a.status === "ACTIVE"}>
                        {a.status === "ACTIVE" ? "Active" : a.status === "DISABLED" ? "Disabled" : a.status}
                      </Badge>
                    </TD>
                    <TD>
                      <Badge tone={a.has_credentials ? "ok" : "warn"}>{a.has_credentials ? "Stored" : "Not set"}</Badge>
                      {r && (
                        <div className="mt-1 text-xs" role="status">
                          {r.ok ? (
                            <span className="text-success-subtle-foreground">Connection accepted</span>
                          ) : (
                            <span className="text-destructive">
                              Connection failed{r.message ? `: ${r.message}` : r.error ? ` (${r.error})` : ""}
                            </span>
                          )}
                        </div>
                      )}
                    </TD>
                    <TD>
                      <div className="flex flex-wrap justify-end gap-1.5">
                        <Button
                          size="xs"
                          variant="outline"
                          disabled={testing !== null || !a.has_credentials}
                          onClick={() => test(a)}
                          aria-label={`Test connection for ${a.display_name || a.merchant_account_ref}`}
                        >
                          <PlugZap /> {testing === a.id ? "Testing…" : "Test connection"}
                        </Button>
                        {writable && (
                          <Button
                            size="xs"
                            variant="secondary"
                            onClick={() => { setEditing(a); setDialogOpen(true); }}
                            aria-label={`Edit ${a.display_name || a.merchant_account_ref}`}
                          >
                            <Pencil /> Edit
                          </Button>
                        )}
                      </div>
                    </TD>
                  </TR>
                );
              })}
            </TBody>
          </Table>
        )}
      </TableWrap>
      {providers && (
        <AccountDialog
          open={dialogOpen}
          onOpenChange={setDialogOpen}
          account={editing}
          providers={providers}
          onSaved={async () => {
            setDialogOpen(false);
            toastSuccess(editing ? "Provider account saved" : "Provider account added");
            await onChanged();
          }}
        />
      )}
    </Card>
  );
}

/* ------------------------------------------------------------------------------------------------------ */
/* Hosted payment domains                                                                                  */
/* ------------------------------------------------------------------------------------------------------ */

function DomainsCard({
  providers, extra, writable, onSaved,
}: {
  providers: PaymentProvider[] | null;
  extra: string[] | null;
  writable: boolean;
  onSaved: (domains: string[]) => Promise<void>;
}) {
  const [draft, setDraft] = useState<string[]>([]);
  const [adding, setAdding] = useState("");
  const [addErr, setAddErr] = useState<string | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [saveErr, setSaveErr] = useState<string | null>(null);

  useEffect(() => { if (extra) setDraft(extra); }, [extra]);
  const dirty = useMemo(() => JSON.stringify(draft) !== JSON.stringify(extra ?? []), [draft, extra]);

  function add() {
    const d = adding.trim().toLowerCase();
    const problem = domainProblem(d);
    if (problem) { setAddErr(problem); return; }
    if (draft.includes(d)) { setAddErr("That domain is already on the list."); return; }
    if (draft.length >= MAX_EXTRA_DOMAINS) { setAddErr(`At most ${MAX_EXTRA_DOMAINS} domains.`); return; }
    setDraft([...draft, d]); setAdding(""); setAddErr(null);
  }

  async function save({ reason, password }: { reason: string; password: string }) {
    setBusy(true); setSaveErr(null);
    try {
      const r = await api.put<{ config_version: number; domains: string[] }>("/payment-providers/domains", {
        domains: draft, reason, password,
      });
      setConfirming(false);
      await onSaved(r.domains ?? draft);
    } catch (e) {
      setSaveErr(saveErrorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader className="items-start">
        <div className="min-w-0 space-y-1">
          <CardTitle className="flex items-center gap-2 [&_svg]:size-4"><Globe aria-hidden /> Hosted payment domains</CardTitle>
          <CardDescription>
            The provider&rsquo;s payment pages a client must reach before signing in. They are reachable before sign-in
            only while Card payment is in use.
          </CardDescription>
        </div>
      </CardHeader>
      <CardBody className="space-y-5">
        <div className="space-y-2">
          <div className="text-label">Provided by each provider</div>
          {providers === null ? (
            <SkeletonRows rows={1} cols={2} />
          ) : (
            <ul className="grid gap-2 sm:grid-cols-2">
              {providers.map((p) => (
                <li key={p.id} className="rounded-md border border-border px-3.5 py-2.5">
                  <div className="text-sm font-medium">{p.label}</div>
                  {p.hosted_domains.length === 0 ? (
                    <p className="text-xs text-muted-foreground">None</p>
                  ) : (
                    <ul className="mt-1 space-y-0.5">
                      {p.hosted_domains.map((d) => (
                        <li key={d} className="break-all font-mono text-xs text-muted-foreground">{d}</li>
                      ))}
                    </ul>
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>

        <div className="space-y-2">
          <div className="text-label">Additional domains</div>
          <p className="text-xs text-muted-foreground">
            Only when your provider account uses a payment page on another domain. Domain names only — no IP
            addresses and no wildcards: list each host name. Up to {MAX_EXTRA_DOMAINS}.
          </p>
          {extra === null ? (
            <SkeletonRows rows={1} cols={2} />
          ) : draft.length === 0 ? (
            <p className="text-sm text-muted-foreground">No additional domains.</p>
          ) : (
            <ul className="space-y-1.5">
              {draft.map((d) => (
                <li key={d} className="flex items-center justify-between gap-2 rounded-md border border-border px-3 py-1.5">
                  <span className="min-w-0 break-all font-mono text-sm">{d}</span>
                  {writable && (
                    <Button
                      size="icon-sm"
                      variant="ghost"
                      aria-label={`Remove ${d}`}
                      onClick={() => setDraft(draft.filter((x) => x !== d))}
                    >
                      <Trash2 />
                    </Button>
                  )}
                </li>
              ))}
            </ul>
          )}
          {writable && extra !== null && (
            <div className="space-y-3">
              <form
                className="flex flex-col gap-2 sm:flex-row sm:items-start"
                onSubmit={(e) => { e.preventDefault(); add(); }}
              >
                <Field label="Add a domain" className="flex-1" error={addErr}>
                  <Input
                    value={adding}
                    placeholder="pay.example.com"
                    autoComplete="off"
                    spellCheck={false}
                    onChange={(e) => { setAdding(e.target.value); setAddErr(null); }}
                    className="font-mono"
                  />
                </Field>
                <Button type="submit" variant="secondary" className="sm:mt-7" disabled={draft.length >= MAX_EXTRA_DOMAINS}>
                  <Plus /> Add
                </Button>
              </form>
              <div className="flex flex-wrap gap-2">
                <Button disabled={!dirty} onClick={() => { setSaveErr(null); setConfirming(true); }}>Save domains</Button>
                {dirty && <Button variant="ghost" onClick={() => setDraft(extra)}>Discard changes</Button>}
              </div>
            </div>
          )}
        </div>
      </CardBody>
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title="Save payment domains"
        description={`${draft.length} additional domain${draft.length === 1 ? "" : "s"} will be reachable before sign-in while Card payment is in use.`}
        confirmLabel="Save domains"
        busy={busy}
        error={saveErr}
        requireReason
        requirePassword
        onConfirm={save}
      />
    </Card>
  );
}

/* ------------------------------------------------------------------------------------------------------ */
/* Timings                                                                                                 */
/* ------------------------------------------------------------------------------------------------------ */

function SettingsCard({
  settings, writable, onSaved,
}: {
  settings: CardSettings | null;
  writable: boolean;
  onSaved: (s: CardSettings) => Promise<void>;
}) {
  const [expiry, setExpiry] = useState("");
  const [grace, setGrace] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [saveErr, setSaveErr] = useState<string | null>(null);

  useEffect(() => {
    if (!settings) return;
    setExpiry(String(settings.settings.checkout_expiry_minutes));
    setGrace(String(settings.settings.reconcile_grace_minutes));
  }, [settings]);

  if (!settings) {
    return (
      <Card>
        <CardHeader><CardTitle>Card payment timings</CardTitle></CardHeader>
        <SkeletonRows rows={2} cols={2} />
      </Card>
    );
  }

  const b = settings.bounds;
  const inRange = (v: string, bound: { min: number; max: number }) => {
    const n = Number(v);
    return v.trim() !== "" && Number.isInteger(n) && n >= bound.min && n <= bound.max;
  };
  const valid = inRange(expiry, b.checkout_expiry_minutes) && inRange(grace, b.reconcile_grace_minutes);
  const dirty =
    Number(expiry) !== settings.settings.checkout_expiry_minutes ||
    Number(grace) !== settings.settings.reconcile_grace_minutes;

  async function save({ reason, password }: { reason: string; password: string }) {
    setBusy(true); setSaveErr(null);
    try {
      const next = await api.put<CardSettings>("/payment-providers/settings", {
        checkout_expiry_minutes: Number(expiry),
        reconcile_grace_minutes: Number(grace),
        reason,
        password,
      });
      setConfirming(false);
      await onSaved(next);
    } catch (e) {
      setSaveErr(saveErrorMessage(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader className="items-start">
        <div className="min-w-0 space-y-1">
          <CardTitle>Card payment timings</CardTitle>
          <CardDescription>
            {settings.settings.is_default
              ? "Using the defaults. Nobody has changed these for this site."
              : settings.settings.updated_at
                ? `Last changed ${formatDate(settings.settings.updated_at)}.`
                : "Set for this site."}
          </CardDescription>
        </div>
      </CardHeader>
      <CardBody className="space-y-4">
        <div className="grid gap-5 sm:grid-cols-2">
          <SettingField
            label="Checkout expiry"
            value={expiry}
            onChange={setExpiry}
            unit="minutes"
            min={b.checkout_expiry_minutes.min}
            max={b.checkout_expiry_minutes.max}
            defaultValue={b.checkout_expiry_minutes.default}
            explanation="How long the provider's payment page stays valid once a client opens it."
            readOnly={!writable}
          />
          <SettingField
            label="Reconciliation grace"
            value={grace}
            onChange={setGrace}
            unit="minutes"
            min={b.reconcile_grace_minutes.min}
            max={b.reconcile_grace_minutes.max}
            defaultValue={b.reconcile_grace_minutes.default}
            explanation="How long after the page expires OneGate keeps asking the provider about a payment before sending it to manual review."
            readOnly={!writable}
          />
        </div>
        {writable && (
          <Button disabled={!dirty || !valid} onClick={() => { setSaveErr(null); setConfirming(true); }}>
            Save timings
          </Button>
        )}
      </CardBody>
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title="Save card payment timings"
        description={`Checkout expiry ${expiry} minutes, reconciliation grace ${grace} minutes. Applies to payments started after the change.`}
        confirmLabel="Save timings"
        busy={busy}
        error={saveErr}
        requireReason
        requirePassword
        onConfirm={save}
      />
    </Card>
  );
}

/* ------------------------------------------------------------------------------------------------------ */
/* History                                                                                                 */
/* ------------------------------------------------------------------------------------------------------ */

function HistoryCard({ changes }: { changes: PaymentChange[] | null }) {
  return (
    <Card>
      <CardHeader className="items-start">
        <div className="min-w-0 space-y-1">
          <CardTitle>Change history</CardTitle>
          <CardDescription>Every change to provider accounts, payment domains and timings, with who made it and why.</CardDescription>
        </div>
      </CardHeader>
      <TableWrap>
        {changes === null ? (
          <SkeletonRows rows={3} cols={4} />
        ) : changes.length === 0 ? (
          <CardBody><p className="text-sm text-muted-foreground">No changes recorded yet.</p></CardBody>
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>When</TH>
                <TH>What</TH>
                <TH>By</TH>
                <TH>Reason</TH>
                <TH>Detail</TH>
              </TR>
            </THead>
            <TBody>
              {changes.map((c, i) => (
                <TR key={`${c.changed_at}-${i}`}>
                  <TD className="whitespace-nowrap">{formatDate(c.changed_at)}</TD>
                  <TD className="whitespace-nowrap">{KIND_LABEL[c.kind] ?? c.kind}</TD>
                  <TD className="break-all">{c.changed_by || "—"}</TD>
                  <TD className="min-w-40">{c.reason || "—"}</TD>
                  <TD className="min-w-48 break-words text-xs text-muted-foreground">{summariseDetail(c.detail)}</TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      </TableWrap>
    </Card>
  );
}
