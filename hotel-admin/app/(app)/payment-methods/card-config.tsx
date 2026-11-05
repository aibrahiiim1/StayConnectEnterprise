"use client";

// Rendered only once the Card payment module is manageable. Every change takes a reason and the operator's
// password and lands in the change history. Nothing here moves money: "Test connection" charges nothing.

import { useCallback, useEffect, useMemo, useState } from "react";
import { api, ApiError } from "@/lib/api";
import {
  AccountsResp, CardSettings, PaymentAccount, PaymentChange, PaymentProvider, ProvidersResp, TestResult,
  codeWords, saveErrorMessage,
} from "@/lib/payment-admin";
import { cn, formatDate } from "@/lib/utils";
import { Card, CardBody, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { DescriptionList } from "@/components/ui/data";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { EmptyState } from "@/components/ui/empty-state";
import { HistoryList } from "@/components/ui/history";
import { RowActions } from "@/components/ui/row-actions";
import { Button } from "@/components/ui/button";
import { ConfirmDialog } from "@/components/ui/dialog";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { Field, Input, QuietHints } from "@/components/ui/input";
import { QuietDescription, InfoHint } from "@/components/ui/quiet";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { SkeletonRows } from "@/components/ui/misc";
import { SettingField } from "@/components/ui/patterns";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { useToast } from "@/components/ui/toast";
import { AccountDialog } from "./account-dialog";
import { cardAccepts, defaultCardAccount } from "@/lib/card-currency";
import { CodeList, CodeText } from "./codes";
import { AlertTriangle, CreditCard, Plus, PlugZap, RefreshCw, Trash2 } from "lucide-react";

const MAX_EXTRA_DOMAINS = 20;

/**
 * Why a typed domain is refused, or null. The server validates again. Names only: no IP, path or wildcard
 * (the garden opens the addresses a name resolves to, and a wildcard names no host).
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

function LoadFailed({ title, err, loading, onRetry }: { title: string; err: unknown; loading: boolean; onRetry: () => void }) {
  return (
    <EmptyState
      size="sm"
      icon={<AlertTriangle />}
      title={title}
      hint={err instanceof Error ? err.message : undefined}
      action={<Button size="sm" variant="secondary" disabled={loading} onClick={onRetry}><RefreshCw /> Try again</Button>}
    />
  );
}

type Failed = { providers?: unknown; accounts?: unknown; settings?: unknown; changes?: unknown };

const changeKey = (c: PaymentChange) => `${c.changed_at}|${c.kind}`;

const nowTime = () => new Date().toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });

type PackageCurrencyRow = {
  package_id: string; code: string; name?: string | null; active?: boolean; current_revision_id?: string | null;
  price_minor?: number | null; currency?: string | null;
};

/**
 * Names the live card-priced packages that will not be offered by card, by scd's rule (lib/card-currency):
 * only the default active account settles, in its own currency or one its provider converts. The package list
 * carries no `settlement_methods`, so each live priced package's methods are read from its current configuration.
 */
export function CurrencyMismatchNotice({ accounts }: { accounts: AccountsResp | null }) {
  const [pkgs, setPkgs] = useState<{ name: string; currency: string }[] | null>(null);
  useEffect(() => {
    let live = true;
    (async () => {
      try {
        const r = await api.get<{ data?: PackageCurrencyRow[] } | PackageCurrencyRow[]>("/commercial-packages");
        const rows = (Array.isArray(r) ? r : r?.data ?? [])
          .filter((p) => p.active !== false && !!p.current_revision_id && (p.price_minor ?? 0) > 0);
        const byCard = await Promise.all(rows.map(async (p) => {
          try {
            const cur = await api.get<{ settlement_methods?: string[] | null; currency?: string | null }>(`/commercial-packages/${p.package_id}/current`);
            return (cur?.settlement_methods ?? []).includes("ONLINE_PAYMENT")
              ? { name: p.name || p.code, currency: ((cur?.currency ?? p.currency) ?? "").trim().toUpperCase() }
              : null;
          } catch { return null; }
        }));
        if (live) setPkgs(byCard.filter((x): x is { name: string; currency: string } => x !== null));
      } catch { if (live) setPkgs([]); }
    })();
    return () => { live = false; };
  }, []);
  const acct = useMemo(() => (accounts ? defaultCardAccount(accounts.accounts ?? []) : null), [accounts]);
  if (!acct || !pkgs) return null;
  const withheld = acct === "none" ? pkgs : pkgs.filter((p) => !cardAccepts(acct, p.currency));
  if (withheld.length === 0) return null;
  return (
    <p className="mt-3 rounded-md border border-warning/40 bg-warning-subtle px-3.5 py-2.5 text-xs text-warning-subtle-foreground animate-in fade-in-0 slide-in-from-top-1 duration-base ease-onegate motion-reduce:animate-none" role="status" data-testid="card-currency-mismatch">
      <strong>Not offered by card:</strong>{" "}
      {withheld.map((p) => `${p.name} (${p.currency})`).join(", ")}.{" "}
      {acct === "none"
        ? "There is no default active account, and card checkouts use only the default account."
        : acct.provider_converts
          ? `Card checkouts use the default account, which settles ${acct.currency}; its provider does not convert these currencies for it.`
          : `Card checkouts use the default account, which settles ${acct.currency} and is not set to let the provider convert.`}{" "}
      These packages are withheld from the card option until their price currency or the default account matches.
    </p>
  );
}

function summariseDetail(detail: unknown): string {
  if (detail === null || detail === undefined || detail === "") return "—";
  if (typeof detail === "string") return detail;
  if (typeof detail === "boolean") return detail ? "Yes" : "No";
  if (Array.isArray(detail)) return detail.length === 0 ? "None" : detail.map((v) => summariseDetail(v)).join(", ");
  if (typeof detail === "object") {
    return Object.entries(detail as Record<string, unknown>)
      .map(([k, v]) => {
        const words = k.replace(/_/g, " ");
        return `${words.charAt(0).toUpperCase()}${words.slice(1)}: ${summariseDetail(v)}`;
      })
      .join(" · ");
  }
  return String(detail);
}

type ConfigTab = "methods" | "accounts" | "settings" | "history";

/**
 * The page's tabs: the methods list, then the card payment Accounts, Settings (payment domains and timings) and
 * Change history, each on its own tab so the page is never a wall of cards. Data is read once and refreshed after a save.
 */
export function CardPaymentConfig({ writable, methods }: { writable: boolean; methods?: React.ReactNode }) {
  const [tab, setTab] = useState<ConfigTab>(methods ? "methods" : "accounts");
  const toast = useToast();
  const [providers, setProviders] = useState<ProvidersResp | null>(null);
  const [accounts, setAccounts] = useState<AccountsResp | null>(null);
  const [settings, setSettings] = useState<CardSettings | null>(null);
  const [changes, setChanges] = useState<PaymentChange[] | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [failed, setFailed] = useState<Failed>({});
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    setErr(null);
    setLoading(true);
    const [p, a, s, c] = await Promise.allSettled([
      api.get<ProvidersResp>("/payment-providers/providers"),
      api.get<AccountsResp>("/payment-providers/accounts"),
      api.get<CardSettings>("/payment-providers/settings"),
      api.get<{ changes: PaymentChange[] }>("/payment-providers/changes"),
    ]);
    const f: Failed = {};
    if (p.status === "fulfilled") setProviders(p.value); else f.providers = p.reason;
    if (a.status === "fulfilled") setAccounts(a.value); else f.accounts = a.reason;
    if (s.status === "fulfilled") setSettings(s.value); else f.settings = s.reason;
    if (c.status === "fulfilled") setChanges(c.value.changes ?? []); else f.changes = c.reason;
    setFailed(f);
    setLoading(false);
  }, []);
  useEffect(() => { load(); }, [load]);

  const reloadHistory = useCallback(async () => {
    try {
      const c = await api.get<{ changes: PaymentChange[] }>("/payment-providers/changes");
      setChanges(c.changes ?? []);
      setFailed((prev) => ({ ...prev, changes: undefined }));
    } catch { return; }
  }, []);
  const retry = () => void load();

  return (
    <QuietHints>
    <Tabs value={tab} onValueChange={(v) => setTab(v as ConfigTab)}>
      <TabsList>
        {methods && <TabsTrigger value="methods">Methods</TabsTrigger>}
        <TabsTrigger value="accounts">Accounts</TabsTrigger>
        <TabsTrigger value="settings">Settings</TabsTrigger>
        <TabsTrigger value="history">Change history</TabsTrigger>
      </TabsList>
      <ErrorBanner err={err} className="mb-0 mt-4" />
      {methods && <TabsContent value="methods" className="mt-4">{methods}</TabsContent>}
      <TabsContent value="accounts" className="mt-4 space-y-4">
      {providers?.engine_unready && (
        <Callout tone={providers.engine_unready === "PROVIDER_CALLS_NOT_AUTHORISED" ? "info" : "warning"} title={providers.engine_unready === "PROVIDER_CALLS_NOT_AUTHORISED" ? "Card payment can be configured here, not run" : "The card payment engine is not ready on this appliance"}>
          <CodeText code={providers.engine_unready} />
        </Callout>
      )}
      <AccountsCard
        providers={providers?.providers ?? null}
        accounts={accounts}
        writable={writable}
        failed={failed.accounts ?? failed.providers}
        loading={loading}
        onRetry={retry}
        onChanged={async () => {
          try { setAccounts(await api.get<AccountsResp>("/payment-providers/accounts")); } catch (e) { setErr(e); }
          await reloadHistory();
        }}
        toastSuccess={toast.success}
      />
      </TabsContent>
      <TabsContent value="settings" forceMount hidden={tab !== "settings"} className="mt-4 space-y-4">
      <DomainsCard
        providers={providers?.providers ?? null}
        extra={accounts?.extra_domains ?? null}
        writable={writable}
        failed={failed.providers ?? failed.accounts}
        loading={loading}
        onRetry={retry}
        onSaved={async (domains) => {
          setAccounts((prev) => (prev ? { ...prev, extra_domains: domains } : prev));
          toast.success("Payment domains saved");
          await reloadHistory();
        }}
      />
      <SettingsCard
        settings={settings}
        writable={writable}
        failed={failed.settings}
        loading={loading}
        onRetry={retry}
        onSaved={async (next) => {
          setSettings(next);
          toast.success("Card payment timings saved");
          await reloadHistory();
        }}
      />
      </TabsContent>
      <TabsContent value="history" className="mt-4">
        <HistoryCard changes={changes} failed={failed.changes} loading={loading} onRetry={retry} />
      </TabsContent>
    </Tabs>
    </QuietHints>
  );
}

function AccountsCard({
  providers, accounts, writable, failed, loading, onRetry, onChanged, toastSuccess,
}: {
  providers: PaymentProvider[] | null;
  accounts: AccountsResp | null;
  writable: boolean;
  failed: unknown;
  loading: boolean;
  onRetry: () => void;
  onChanged: () => Promise<void>;
  toastSuccess: (t: string, d?: string) => void;
}) {
  const [editing, setEditing] = useState<PaymentAccount | null>(null);
  const [dialogOpen, setDialogOpen] = useState(false);
  const [testing, setTesting] = useState<string | null>(null);
  const [results, setResults] = useState<Record<string, TestResult & { at: string }>>({});
  const [converting, setConverting] = useState<PaymentAccount | null>(null);

  const providerLabel = (id: string) => providers?.find((p) => p.id === id)?.label ?? id;

  async function test(a: PaymentAccount) {
    setTesting(a.id);
    try {
      const r = await api.post<TestResult>(`/payment-providers/accounts/${encodeURIComponent(a.id)}/test`, {});
      setResults((prev) => ({ ...prev, [a.id]: { ...r, at: nowTime() } }));
    } catch (e) {
      const body = e instanceof ApiError ? e.body : null;
      setResults((prev) => ({
        ...prev,
        [a.id]: { ok: false, error: body?.error ?? "request_failed", message: saveErrorMessage(e), at: nowTime() },
      }));
    } finally {
      setTesting(null);
    }
  }

  return (
    <Card className="overflow-hidden">
      <CardHeader>
        <div className="min-w-0">
          <CardTitle className="flex items-center gap-1">Provider accounts
            <QuietDescription>The default account takes every new card payment.</QuietDescription></CardTitle>
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
      {(accounts === null || providers === null) && failed ? (
          <LoadFailed title="The provider accounts could not be loaded" err={failed} loading={loading} onRetry={onRetry} />
        ) : accounts === null ? (
          <SkeletonRows rows={2} cols={5} />
        ) : accounts.accounts.length === 0 ? (
          <EmptyState size="sm" icon={<CreditCard />} title="No provider account yet"
            hint="Card payment cannot be offered to clients until one is added and active." />
        ) : (
          <Table label="Provider accounts">
            <THead>
              <TR>
                <TH>Account</TH>
                <TH className="hidden md:table-cell">Provider</TH>
                <TH>Currency</TH>
                <TH>Other currencies</TH>
                <TH className="hidden md:table-cell">Mode</TH>
                <TH>Status</TH>
                <TH className="hidden md:table-cell">Credentials</TH>
                <TH><span className="sr-only">Actions</span></TH>
              </TR>
            </THead>
            <TBody>
              {accounts.accounts.map((a) => {
                const r = results[a.id];
                return (
                  <TR key={a.id}>
                    <TD nowrap className="max-w-[16rem]">
                      <div className="flex items-center gap-1.5">
                        <span className="min-w-0 truncate font-medium" title={a.display_name || undefined}>{a.display_name || "—"}</span>
                        {a.is_default && <Badge tone="accent">Default</Badge>}
                      </div>
                      <div className="truncate font-mono text-xs text-muted-foreground" title={a.merchant_account_ref}>{a.merchant_account_ref}</div>
                      {r && (
                        <div key={r.at} className="mt-1 whitespace-normal text-xs animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none" role="status">
                          {r.ok ? (
                            <span className="text-success-subtle-foreground">Connection accepted</span>
                          ) : r.error === "provider_calls_not_authorised" ? (
                            <span className="text-muted-foreground">Not tested: {r.message}</span>
                          ) : (
                            <span className="text-destructive">
                              Connection failed{r.message ? `: ${r.message}` : r.error && codeWords(r.error) ? `: ${codeWords(r.error)}` : "."}
                            </span>
                          )}
                          <span className="text-muted-foreground"> · tested at {r.at}</span>
                        </div>
                      )}
                    </TD>
                    <TD nowrap className="hidden md:table-cell">{providerLabel(a.provider)}</TD>
                    <TD nowrap className="font-mono">{a.currency}</TD>
                    <TD className="min-w-[12rem]">
                      {a.conversion_supported ? (
                        <div className="flex flex-col items-start gap-1">
                          <Badge tone={a.provider_converts ? "ok" : "default"}>{a.provider_converts ? "Provider converts" : "Not converted"}</Badge>
                          {/* Only the default account takes card checkouts; the server refuses conversion elsewhere. */}
                          {writable && (
                            <Button size="xs" variant="ghost" className="text-foreground" disabled={!a.is_default} onClick={() => setConverting(a)}
                              aria-describedby={!a.is_default ? `convert-default-only-${a.id}` : undefined}>
                              {a.provider_converts ? "Stop converting" : "Let the provider convert"}
                            </Button>
                          )}
                          {!a.is_default && (
                            <span id={`convert-default-only-${a.id}`} className="text-xs text-muted-foreground" data-testid={`convert-default-only-${a.id}`}>
                              Applies only to the default account, which every card checkout uses.
                            </span>
                          )}
                        </div>
                      ) : (
                        <span className="text-xs text-muted-foreground">Settles {a.currency} only</span>
                      )}
                    </TD>
                    <TD nowrap className="hidden md:table-cell"><Badge tone={a.mode === "LIVE" ? "warn" : "info"}>{a.mode}</Badge></TD>
                    <TD nowrap>
                      <StatusBadge tone={a.status === "ACTIVE" ? "ok" : "default"}>
                        {a.status === "ACTIVE" ? "Active" : a.status === "DISABLED" ? "Disabled" : a.status}
                      </StatusBadge>
                    </TD>
                    <TD className="hidden md:table-cell">
                      <Badge tone={a.has_credentials ? "ok" : "warn"}>{a.has_credentials ? "Stored" : "Not set"}</Badge>
                    </TD>
                    <TD className="w-0">
                      <RowActions
                        subject={a.display_name || a.merchant_account_ref}
                        primary={writable ? { label: "Edit", onSelect: () => { setEditing(a); setDialogOpen(true); } } : undefined}
                        items={[{
                          label: testing === a.id ? "Testing…" : "Test connection",
                          icon: <PlugZap />,
                          disabled: testing !== null || !a.has_credentials,
                          reason: !a.has_credentials ? "Add the account's credentials first." : undefined,
                          onSelect: () => void test(a),
                        }]}
                      />
                    </TD>
                  </TR>
                );
              })}
            </TBody>
          </Table>
        )}
      <div className="empty:hidden [&>p]:m-4"><CurrencyMismatchNotice accounts={accounts} /></div>
      <ConversionDialog account={converting} providerName={converting ? providerLabel(converting.provider) : ""}
        onClose={() => setConverting(null)} onSaved={async () => {
          setConverting(null);
          toastSuccess("Saved", "Card currencies follow the account now.");
          await onChanged();
        }} />
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

function DomainsCard({
  providers, extra, writable, failed, loading, onRetry, onSaved,
}: {
  providers: PaymentProvider[] | null;
  extra: string[] | null;
  writable: boolean;
  failed: unknown;
  loading: boolean;
  onRetry: () => void;
  onSaved: (domains: string[]) => Promise<void>;
}) {
  const [draft, setDraft] = useState<string[]>([]);
  const [adding, setAdding] = useState("");
  const [addErr, setAddErr] = useState<string | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [saveErr, setSaveErr] = useState<string | null>(null);
  const [added, setAdded] = useState<string | null>(null);

  useEffect(() => { if (extra) setDraft(extra); }, [extra]);
  const dirty = useMemo(() => JSON.stringify(draft) !== JSON.stringify(extra ?? []), [draft, extra]);

  function add() {
    const d = adding.trim().toLowerCase();
    const problem = domainProblem(d);
    if (problem) { setAddErr(problem); return; }
    if (draft.includes(d)) { setAddErr("That domain is already on the list."); return; }
    if (draft.length >= MAX_EXTRA_DOMAINS) { setAddErr(`At most ${MAX_EXTRA_DOMAINS} domains.`); return; }
    setDraft([...draft, d]); setAdding(""); setAddErr(null); setAdded(d);
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
      <CardHeader>
        <div className="min-w-0">
          <CardTitle className="flex items-center gap-1">Hosted payment domains
            <QuietDescription>Reachable before sign-in only while Card payment is in use.</QuietDescription></CardTitle>
        </div>
      </CardHeader>
      <CardBody className="space-y-5">
        {(providers === null || extra === null) && failed ? (
          <LoadFailed title="The payment domains could not be loaded" err={failed} loading={loading} onRetry={onRetry} />
        ) : (
        <>
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
          <div className="flex items-center gap-1 text-label">
            Additional domains
            <InfoHint>
              Only when your provider account uses a payment page on another domain. Domain names only — no IP
              addresses and no wildcards: list each host name. Up to {MAX_EXTRA_DOMAINS}.
            </InfoHint>
          </div>
          {extra === null ? (
            <SkeletonRows rows={1} cols={2} />
          ) : draft.length === 0 ? (
            <p className="text-sm text-muted-foreground">No additional domains.</p>
          ) : (
            <ul className="space-y-1.5">
              {draft.map((d) => (
                <li key={d} className={cn("flex items-center justify-between gap-2 rounded-md border border-border px-3 py-1.5", d === added && "animate-in fade-in-0 zoom-in-95 duration-base ease-onegate motion-reduce:animate-none")}>
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
                <Button type="submit" variant="secondary" className="sm:mt-7" disabled={draft.length >= MAX_EXTRA_DOMAINS}
                  aria-describedby={draft.length >= MAX_EXTRA_DOMAINS ? "domains-full" : undefined}>
                  <Plus /> Add
                </Button>
              </form>
              {draft.length >= MAX_EXTRA_DOMAINS && (
                <p id="domains-full" className="text-xs text-muted-foreground">
                  The list holds at most {MAX_EXTRA_DOMAINS} domains. Remove one to add another.
                </p>
              )}
              <div className="flex flex-wrap gap-2">
                <Button disabled={!dirty} onClick={() => { setSaveErr(null); setConfirming(true); }}>Save domains</Button>
                {dirty && <Button variant="ghost" onClick={() => setDraft(extra)}>Discard changes</Button>}
              </div>
            </div>
          )}
        </div>
        </>
        )}
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
        requirePassword strictPassword
        onConfirm={save}
      />
    </Card>
  );
}

function SettingsCard({
  settings, writable, failed, loading, onRetry, onSaved,
}: {
  settings: CardSettings | null;
  writable: boolean;
  failed: unknown;
  loading: boolean;
  onRetry: () => void;
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
        {failed ? (
          <LoadFailed title="The card payment timings could not be loaded" err={failed} loading={loading} onRetry={onRetry} />
        ) : (
          <SkeletonRows rows={2} cols={2} />
        )}
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
      <CardHeader>
        <div className="min-w-0">
          <CardTitle className="flex items-center gap-1">Card payment timings
            <QuietDescription>
              {settings.settings.is_default
                ? "Using the defaults. Nobody has changed these for this site."
                : settings.settings.updated_at
                  ? `Last changed ${formatDate(settings.settings.updated_at)}.`
                  : "Set for this site."}
            </QuietDescription></CardTitle>
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
      </CardBody>
      {writable && (
        <CardFooter className="justify-end">
          <Button disabled={!dirty || !valid} onClick={() => { setSaveErr(null); setConfirming(true); }}>
            Save timings
          </Button>
        </CardFooter>
      )}
      <ConfirmDialog
        open={confirming}
        onOpenChange={setConfirming}
        title="Save card payment timings"
        description={`Checkout expiry ${expiry} minutes, reconciliation grace ${grace} minutes. Applies to payments started after the change.`}
        confirmLabel="Save timings"
        busy={busy}
        error={saveErr}
        requireReason
        requirePassword strictPassword
        onConfirm={save}
      />
    </Card>
  );
}

const HISTORY_SHOWN = 8;

function HistoryCard({ changes, failed, loading, onRetry }: { changes: PaymentChange[] | null; failed: unknown; loading: boolean; onRetry: () => void }) {
  const [open, setOpen] = useState<string | null>(null);
  const [all, setAll] = useState(false);
  const shown = changes ? (all ? changes : changes.slice(0, HISTORY_SHOWN)) : [];
  const sel = open !== null && changes ? changes.find((c) => changeKey(c) === open) ?? null : null;
  return (
    <Card className="overflow-hidden">
      <CardHeader>
        <div className="min-w-0">
          <CardTitle className="flex items-center gap-1">Change history
            <QuietDescription>Accounts, payment domains and timings: who changed what, and why.</QuietDescription></CardTitle>
        </div>
        {changes && changes.length > HISTORY_SHOWN && (
          <Button size="sm" variant="ghost" onClick={() => setAll((v) => !v)}>
            {all ? "Show latest" : `View all ${changes.length}`}
          </Button>
        )}
      </CardHeader>
      {changes === null && failed ? (
        <LoadFailed title="The change history could not be loaded" err={failed} loading={loading} onRetry={onRetry} />
      ) : changes === null ? (
        <SkeletonRows rows={3} cols={4} />
      ) : (
        <HistoryList
          label="Change history"
          emptyLabel="No changes recorded yet."
          onSelect={(it) => setOpen(it.id)}
          selectedId={open}
          items={shown.map((c) => ({
            id: changeKey(c),
            title: KIND_LABEL[c.kind] ?? c.kind,
            actor: c.changed_by || undefined,
            at: c.changed_at,
            summary: c.reason || undefined,
            tone: "info" as const,
          }))}
        />
      )}
      <DetailsDrawer
        open={sel !== null}
        onOpenChange={(v) => !v && setOpen(null)}
        eyebrow="Payment configuration change"
        title={sel ? KIND_LABEL[sel.kind] ?? sel.kind : ""}
      >
        {sel && (
          <DetailsSection title="Record">
            <DescriptionList
              items={[
                { label: "When", value: formatDate(sel.changed_at) },
                { label: "Changed by", value: sel.changed_by || "—" },
                { label: "Reason", value: sel.reason || "—" },
                { label: "Detail", value: <span className="text-xs">{summariseDetail(sel.detail)}</span> },
              ]}
            />
          </DetailsSection>
        )}
      </DetailsDrawer>
    </Card>
  );
}

/**
 * Marks an account provider-converts: the client pays the package's own price and currency, the provider
 * settles at its own rate, and OneGate only records the provider's report (it never sets or computes a rate).
 */
function ConversionDialog({ account, providerName, onClose, onSaved }: { account: PaymentAccount | null; providerName: string; onClose: () => void; onSaved: () => void | Promise<void> }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => { setError(null); }, [account]);
  const turningOn = !!account && !account.provider_converts;
  return (
    <ConfirmDialog
      open={account !== null}
      onOpenChange={(v) => !v && !busy && onClose()}
      title={turningOn ? "Let the provider convert other currencies?" : "Stop converting other currencies?"}
      description={turningOn
        ? `Packages priced in another currency will be offered by card. The client pays the package price in that currency; ${providerName} settles it into ${account?.currency ?? ""} at its own exchange rate and fee, and the settlement it reports is recorded with each payment.`
        : "Card will be offered only for packages priced in the account currency again. Payments already made are unaffected."}
      confirmLabel={turningOn ? "Let the provider convert" : "Stop converting"}
      busy={busy}
      error={error}
      requireReason
      reasonStrict
      reasonMinLength={4}
      requirePassword
      strictPassword
      onConfirm={async ({ reason, password }) => {
        if (!account) return;
        setBusy(true); setError(null);
        try {
          await api.post(`/payment-providers/accounts/${account.id}/conversion`, { provider_converts: turningOn, reason, password });
          await onSaved();
        } catch (e) { setError(saveErrorMessage(e)); } finally { setBusy(false); }
      }}
    />
  );
}
