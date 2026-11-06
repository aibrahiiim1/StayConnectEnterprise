"use client";

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { api, ListResp, SocialOAuthProvider, SocialSetupStep } from "@/lib/api";
import { PROVIDER_CONSOLE_HELP, PROVIDER_LABELS, PortalCallbackURL, keepAvailable, offeredWords } from "@/lib/portal-https";
import { DEFAULT_SCOPES, extraScopes } from "@/lib/social-scopes";
import { PageShell, PageHeader } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { Card } from "@/components/ui/card";
import { DescriptionList } from "@/components/ui/data";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { RowActions } from "@/components/ui/row-actions";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Field, Select, Textarea, QuietHints } from "@/components/ui/input";
import { Disclosure, InfoHint } from "@/components/ui/quiet";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { DialogForm, ConfirmDialog } from "@/components/ui/dialog";
import { Switch, SkeletonRows } from "@/components/ui/misc";
import { CopyButton, ReadOnlyNotice } from "@/components/ui/patterns";
import { Plus, KeyRound, AlertTriangle, RefreshCw, FileKey, Pencil } from "lucide-react";
import { canWrite } from "@/lib/roles";
import { useToast } from "@/components/ui/toast";
import { cn, formatRelative } from "@/lib/utils";
import { useOperatorRoles } from "@/lib/whoami-context";

const PROVIDERS = ["google", "apple", "facebook", "microsoft"] as const;
const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";
const APPLE_ID_RE = /^[A-Z0-9]{10}$/;

type SocialList = ListResp<SocialOAuthProvider> & {
  callback_urls?: PortalCallbackURL[];
  https_active?: boolean;
  canonical_hostname?: string;
};

type FormState = {
  provider: string;
  display_name: string;
  client_id: string;
  client_secret: string;
  callback_hosts: string[];
  scopes: string;
  tenant: string;
  team_id: string;
  key_id: string;
  enabled: boolean;
};

const EMPTY: FormState = {
  provider: "google", display_name: "", client_id: "", client_secret: "",
  callback_hosts: [], scopes: "", tenant: "", team_id: "", key_id: "", enabled: true,
};

const SECRET_LABEL: Record<string, string> = {
  google: "Client secret", facebook: "App secret", microsoft: "Client secret", apple: "Private key (.p8)",
};
const ID_LABEL: Record<string, string> = {
  google: "Client ID", facebook: "App ID", microsoft: "Application (client) ID", apple: "Services ID",
};

function providerExtras(f: FormState): Record<string, string> {
  if (f.provider === "microsoft") return { tenant: f.tenant.trim() };
  if (f.provider === "apple") return { team_id: f.team_id.trim(), key_id: f.key_id.trim() };
  return {};
}

const STEP_STATE: Record<SocialSetupStep["state"], { mark: string; label: string; className: string }> = {
  ok: { mark: "✓", label: "Done", className: "text-success" },
  todo: { mark: "!", label: "To do", className: "text-warning" },
  manual: { mark: "?", label: "Only Google can confirm this", className: "text-muted-foreground" },
};

function GoogleSetupChecklist({ steps }: { steps: SocialSetupStep[] }) {
  return (
    <fieldset className="space-y-2" aria-label="Google setup checklist">
      <legend className="flex items-center gap-1 text-label">
        Google setup
        <InfoHint>
          Your Site owns its Google Cloud project, its OAuth client and its portal domain; this appliance signs your clients in
          itself and nothing about them goes through OneGate Central. Items marked “?” cannot be checked from here.
        </InfoHint>
      </legend>
      <ul className="space-y-2">
        {steps.map((s) => {
          const st = STEP_STATE[s.state];
          return (
            <li key={s.id} className="flex gap-2 rounded-md border border-border px-3 py-2 text-sm">
              <span role="img" className={`mt-0.5 w-4 shrink-0 text-center font-semibold ${st.className}`} aria-label={st.label} title={st.label}>{st.mark}</span>
              <div className="min-w-0">
                <div className="font-medium">{s.title}</div>
                {s.detail && <div className="break-words text-xs text-muted-foreground">{s.detail}</div>}
                {s.href && s.state === "todo" && (
                  <Link className="text-xs underline" href={s.href}>Open Portal HTTPS</Link>
                )}
              </div>
            </li>
          );
        })}
      </ul>
    </fieldset>
  );
}

function statusBadge(p: SocialOAuthProvider) {
  if (p.offered) return <StatusBadge tone="ok">Offered</StatusBadge>;
  return <StatusBadge tone={p.enabled ? "warn" : "default"}>{p.enabled ? "Not offered" : "Switched off"}</StatusBadge>;
}

export default function SocialProvidersPage() {
  const toast = useToast();
  const [rows, setRows] = useState<SocialOAuthProvider[] | null>(null);
  const [callbacks, setCallbacks] = useState<PortalCallbackURL[]>([]);
  const [httpsActive, setHttpsActive] = useState<boolean | null>(null);
  const roles = useOperatorRoles();
  const [err, setErr] = useState<unknown>(null);
  const [loading, setLoading] = useState(false);
  const [busy, setBusy] = useState(false);
  const [pw, setPw] = useState("");
  const [formErr, setFormErr] = useState<unknown>(null);
  const [fileErr, setFileErr] = useState<string | null>(null);

  const [mode, setMode] = useState<"closed" | "new" | "edit">("closed");
  const [editing, setEditing] = useState<SocialOAuthProvider | null>(null);
  const [viewing, setViewing] = useState<SocialOAuthProvider | null>(null);
  const [f, setF] = useState<FormState>(EMPTY);
  const [initial, setInitial] = useState<FormState>(EMPTY);
  const [deleting, setDeleting] = useState<SocialOAuthProvider | null>(null);

  const writable = roles !== null && canWrite("social-providers", roles);
  const set = <K extends keyof FormState>(k: K, v: FormState[K]) => setF((p) => ({ ...p, [k]: v }));
  const nameOf = (p: SocialOAuthProvider) => p.display_name || PROVIDER_LABELS[p.provider] || p.provider;

  async function load() {
    setLoading(true);
    try {
      const r = await api.get<SocialList>("/social-providers");
      setRows(r.data ?? []);
      setCallbacks(r.callback_urls ?? []);
      setHttpsActive(typeof r.https_active === "boolean" ? r.https_active : null);
      setErr(null);
    } catch (e) {
      setErr(e);
    } finally {
      setLoading(false);
    }
  }
  useEffect(() => {
    void load();
  }, []);

  function begin(next: FormState) {
    setF(next); setInitial(next); setFormErr(null); setFileErr(null); setPw("");
  }
  function openNew() {
    begin(EMPTY); setEditing(null); setMode("new");
  }
  function openEdit(p: SocialOAuthProvider) {
    begin({
      provider: p.provider,
      display_name: p.display_name ?? "",
      client_id: p.client_id,
      client_secret: "",
      callback_hosts: keepAvailable(p.callback_hosts ?? [], callbacks),
      scopes: p.scopes ?? "",
      tenant: p.tenant ?? "",
      team_id: p.team_id ?? "",
      key_id: p.key_id ?? "",
      enabled: p.enabled,
    });
    setViewing(null);
    setEditing(p); setMode("edit");
  }

  function changeProvider(provider: string) {
    setF((p) => ({ ...EMPTY, provider, display_name: p.display_name, enabled: p.enabled }));
    setFileErr(null);
  }

  function toggleHost(host: string, on: boolean) {
    setF((p) => ({
      ...p,
      callback_hosts: on ? Array.from(new Set([...p.callback_hosts, host])) : p.callback_hosts.filter((h) => h !== host),
    }));
  }

  async function readKeyFile(file: File | undefined) {
    setFileErr(null);
    if (!file) return;
    if (file.size > 64 * 1024) { setFileErr("That file is too large to be a .p8 key."); return; }
    const text = (await file.text()).trim();
    if (!text.includes("BEGIN") || !text.includes("PRIVATE KEY")) {
      setFileErr("That file is not a .p8 private key. Choose the AuthKey_….p8 file Apple gave you.");
      return;
    }
    set("client_secret", text);
  }

  async function onSubmit() {
    setBusy(true); setFormErr(null);
    try {
      if (mode === "new") {
        await api.post("/social-providers", {
          provider: f.provider,
          display_name: f.display_name.trim() || undefined,
          client_id: f.client_id.trim(),
          client_secret: f.client_secret,
          callback_hosts: f.callback_hosts,
          scopes: f.scopes.trim() || undefined,
          ...providerExtras(f),
          enabled: f.enabled,
          password: pw,
        });
      } else if (editing) {
        const body: Record<string, unknown> = {
          display_name: f.display_name,
          client_id: f.client_id.trim() || undefined,
          callback_hosts: f.callback_hosts,
          scopes: f.scopes,
          ...providerExtras(f),
          enabled: f.enabled,
          password: pw,
        };
        if (f.client_secret) body.client_secret = f.client_secret;
        await api.patch(`/social-providers/${editing.id}`, body);
      }
      toast.success(mode === "new" ? "Provider added" : "Provider saved");
      setMode("closed"); setEditing(null); setPw("");
      await load();
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  async function onDelete({ password }: { password: string }) {
    if (!deleting) return;
    setBusy(true); setFormErr(null);
    try {
      await api.del(`/social-providers/${deleting.id}`, { password });
      setDeleting(null);
      toast.success("Provider removed");
      await load();
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  const isApple = f.provider === "apple";
  const teamErr = isApple && f.team_id.trim() !== "" && !APPLE_ID_RE.test(f.team_id.trim())
    ? "The Team ID is 10 uppercase letters and digits, for example A1B2C3D4E5." : undefined;
  const keyErr = isApple && f.key_id.trim() !== "" && !APPLE_ID_RE.test(f.key_id.trim())
    ? "The Key ID is the 10 uppercase letters and digits shown next to the key in your Apple developer account." : undefined;
  const secretLooksWrong = isApple && f.client_secret.trim() !== "" &&
    !(f.client_secret.includes("BEGIN") && f.client_secret.includes("PRIVATE KEY"));

  const missing = useMemo(() => {
    const out: string[] = [];
    const idLabel = ID_LABEL[f.provider] ?? "Client ID";
    const secretLabel = SECRET_LABEL[f.provider] ?? "Client secret";
    if (mode === "new" && f.client_id.trim() === "") out.push(`the ${idLabel}`);
    if (mode === "new" && f.client_secret === "") out.push(`the ${secretLabel}`);
    if (isApple && (mode === "new" || f.team_id.trim() !== "") && !APPLE_ID_RE.test(f.team_id.trim())) out.push("a valid Team ID");
    if (isApple && (mode === "new" || f.key_id.trim() !== "") && !APPLE_ID_RE.test(f.key_id.trim())) out.push("a valid Key ID");
    if (pw === "") out.push("your password");
    return out;
  }, [f, mode, pw, isApple]);
  const canSubmit = missing.length === 0;
  const disabledReason = missing.length
    ? `Enter ${missing.length === 1 ? missing[0] : `${missing.slice(0, -1).join(", ")} and ${missing[missing.length - 1]}`} to save.`
    : undefined;
  const dirty = useMemo(() => JSON.stringify(f) !== JSON.stringify(initial), [f, initial]);
  const providerName = PROVIDER_LABELS[f.provider] ?? f.provider;
  const loadFailed = rows === null && err !== null;
  const extra = extraScopes(f.provider, f.scopes);

  return (
    <PageShell>
      <PageHeader
        title="Identity providers"
        description="Let clients sign in with an account they already have."
        help={
          <>
            <HelpSection title="What a provider entry is">
              <p>
                Each entry is an OAuth application you register and own with that provider (for example
                Google). The values on it come from that registered application.
              </p>
            </HelpSection>
            <HelpSection title="Setting one up">
              <HelpList
                items={[
                  <>Register the <strong>callback URL</strong> shown in the form in your app, then tick it. The portal offers the provider only over HTTPS, at a host name you ticked.</>,
                  <>The <strong>client secret</strong> is stored write-only and is never shown again. When editing, leave it blank to keep the one already stored.</>,
                  <><strong>Google</strong> needs your portal domain (set it up under Portal HTTPS); it does not accept the OneGate host name.</>,
                  <><strong>Apple</strong> needs the Services ID, the Team ID, the Key ID and the contents of the .p8 private key; the key is stored write-only.</>,
                  <><strong>Microsoft</strong> accepts a directory (tenant) ID to admit only that organisation, or <code>common</code>, <code>organizations</code> or <code>consumers</code>.</>,
                  <>The provider of an entry cannot be changed; remove it and add it again instead.</>,
                  <>Whether clients are actually offered a provider is switched on in <strong>Sign-in methods</strong>.</>,
                ]}
              />
            </HelpSection>
          </>
        }
        actions={writable && <Button onClick={openNew}><Plus /> Add provider</Button>}
      />

      {roles !== null && !writable && <ReadOnlyNotice>Your role can see the identity providers but not change them.</ReadOnlyNotice>}
      {rows !== null && <ErrorBanner err={err} className="mb-0" />}

      {httpsActive === false && (
        <Callout tone="warning" title="Social sign-in is not offered right now">
          The Client Portal is on the HTTP fallback, and providers accept HTTPS callbacks only. See{" "}
          <Link className="underline" href="/portal-https">Portal HTTPS</Link>.
        </Callout>
      )}

      <Card className="overflow-hidden">
          {rows === null ? (
            loadFailed ? (
              <EmptyState
                icon={<AlertTriangle />}
                title="Identity providers could not be loaded"
                hint="The appliance did not return its identity providers. Nothing has been changed."
                action={
                  <Button variant="secondary" disabled={loading} onClick={() => void load()}>
                    <RefreshCw className={cn(loading && "animate-spin motion-reduce:animate-none")} /> Try again
                  </Button>
                }
              />
            ) : <SkeletonRows rows={3} cols={5} />
          ) : rows.length === 0 ? (
            <EmptyState
              size="sm"
              icon={<KeyRound />}
              title="No identity provider is configured"
              hint="Clients can still sign in with the other methods switched on in Sign-in methods."
              action={writable ? <Button onClick={openNew}><Plus /> Add a provider</Button> : undefined}
            />
          ) : (
            <Table label="Identity providers">
              <THead>
                <TR><TH>Provider</TH><TH className="hidden md:table-cell">Client ID</TH><TH>Status</TH><TH className="hidden sm:table-cell">Last used</TH><TH><span className="sr-only">Actions</span></TH></TR>
              </THead>
              <TBody>
                {rows.map((p) => {
                  const name = nameOf(p);
                  return (
                    <TR key={p.id} onClick={() => (writable ? openEdit(p) : setViewing(p))}>
                      <TD nowrap className="font-medium">{name}</TD>
                      <TD truncate title={p.client_id} className="hidden max-w-xs font-mono text-xs md:table-cell">{p.client_id}</TD>
                      <TD className="max-w-sm">
                        <div className="flex flex-col items-start gap-1">
                          {statusBadge(p)}
                          {!p.offered && <span className="text-xs text-muted-foreground">{offeredWords(p.offered_reason, p.provider)}</span>}
                        </div>
                      </TD>
                      <TD nowrap muted className="hidden sm:table-cell">
                        {p.last_success_at ? formatRelative(p.last_success_at) : "Never"}
                      </TD>
                      <TD className="w-0">
                        <RowActions
                          subject={name}
                          primary={writable
                            ? { label: "Edit", onSelect: () => openEdit(p) }
                            : { label: "Details", onSelect: () => setViewing(p) }}
                          items={writable ? [
                            { label: "Details", onSelect: () => setViewing(p) },
                            { label: "Remove…", danger: true, onSelect: () => { setFormErr(null); setDeleting(p); } },
                          ] : undefined}
                        />
                      </TD>
                    </TR>
                  );
                })}
              </TBody>
            </Table>
          )}
      </Card>

      <DetailsDrawer
        open={viewing !== null}
        onOpenChange={(v) => !v && setViewing(null)}
        eyebrow="Identity provider"
        title={viewing ? nameOf(viewing) : ""}
        badges={viewing ? statusBadge(viewing) : undefined}
        actions={writable && viewing ? <Button onClick={() => openEdit(viewing)}><Pencil /> Edit</Button> : undefined}
      >
        {viewing && (
          <>
            {!viewing.offered && (
              <p className="text-sm text-muted-foreground">{offeredWords(viewing.offered_reason, viewing.provider)}</p>
            )}
            <DetailsSection title="Application">
              <DescriptionList items={[
                { label: "Provider", value: PROVIDER_LABELS[viewing.provider] ?? viewing.provider },
                { label: ID_LABEL[viewing.provider] ?? "Client ID", value: <span className="break-all font-mono text-xs">{viewing.client_id}</span> },
                { label: SECRET_LABEL[viewing.provider] ?? "Client secret", value: viewing.secret_set ? "Stored (never shown)" : "Not stored" },
                ...(viewing.provider === "microsoft" ? [{ label: "Directory (tenant)", value: viewing.tenant || "common" }] : []),
                ...(viewing.provider === "apple" ? [
                  { label: "Team ID", value: <span className="font-mono text-xs">{viewing.team_id || "—"}</span> },
                  { label: "Key ID", value: <span className="font-mono text-xs">{viewing.key_id || "—"}</span> },
                ] : []),
                { label: "Scopes", value: viewing.scopes || "Only what sign-in needs" },
                { label: "Last used", value: viewing.last_success_at ? formatRelative(viewing.last_success_at) : "Never" },
              ]} />
            </DetailsSection>
            <DetailsSection title="Callback URLs registered">
              {(viewing.callback_hosts ?? []).length === 0 ? (
                <p className="text-sm text-muted-foreground">None ticked, so clients are not offered this provider.</p>
              ) : (
                <ul className="space-y-1 font-mono text-xs" dir="ltr">
                  {(viewing.callback_hosts ?? []).map((h) => (
                    <li key={h} className="break-all">{callbacks.find((c) => c.hostname === h)?.url ?? h}</li>
                  ))}
                </ul>
              )}
            </DetailsSection>
            {viewing.provider === "google" && viewing.setup && viewing.setup.length > 0 && (
              <DetailsSection title="Setup">
                <GoogleSetupChecklist steps={viewing.setup} />
              </DetailsSection>
            )}
          </>
        )}
      </DetailsDrawer>

      <DialogForm
        open={mode !== "closed"}
        onOpenChange={(v) => { if (!v) { setMode("closed"); setEditing(null); } }}
        title={mode === "edit" ? `Edit ${editing?.display_name || PROVIDER_LABELS[editing?.provider ?? ""] || "provider"}` : "Add an identity provider"}
        description="From the OAuth application your Site registered with the provider."
        size="lg"
        submitLabel={mode === "edit" ? "Save changes" : "Add provider"}
        busy={busy}
        error={formErr}
        disabled={!canSubmit}
        disabledReason={disabledReason}
        dirty={dirty}
        onSubmit={onSubmit}
      >
        <QuietHints>
        <div className="grid gap-4 sm:grid-cols-2">
          {mode === "new" ? (
            <Field label="Provider" hint="Changing it clears the values entered for the previous provider.">
              <Select value={f.provider} onChange={(e) => changeProvider(e.target.value)}>
                {PROVIDERS.map((p) => <option key={p} value={p}>{PROVIDER_LABELS[p]}</option>)}
              </Select>
            </Field>
          ) : (
            <Field label="Provider" hint="The provider cannot be changed; remove and re-add instead.">
              <Input value={providerName} readOnly disabled />
            </Field>
          )}
          <Field label="Name on the portal" hint="Leave empty to use the provider's own name.">
            <Input value={f.display_name} onChange={(e) => set("display_name", e.target.value)} placeholder="Optional" />
          </Field>
          <Field label={ID_LABEL[f.provider] ?? "Client ID"} required>
            <Input value={f.client_id} onChange={(e) => set("client_id", e.target.value)} required />
          </Field>
          {isApple ? (
            <div className="space-y-1.5 sm:col-span-2">
              <Field
                label={SECRET_LABEL.apple}
                required={mode === "new"}
                error={secretLooksWrong ? "Paste the whole .p8 file, including the BEGIN and END lines." : fileErr ?? undefined}
                hint={mode === "edit" ? "Leave blank to keep the key already stored." : "Paste the whole file or choose it below. Stored write-only — it is never shown again."}
              >
                <Textarea
                  value={f.client_secret}
                  onChange={(e) => set("client_secret", e.target.value)}
                  required={mode === "new"}
                  rows={5}
                  dir="ltr"
                  spellCheck={false}
                  autoComplete="off"
                  className="font-mono text-xs"
                  placeholder={mode === "edit" ? "Unchanged" : "-----BEGIN PRIVATE KEY-----"}
                />
              </Field>
              <label className="inline-flex cursor-pointer items-center gap-1.5 text-xs font-medium text-primary underline-offset-4 hover:underline">
                <FileKey className="size-3.5" aria-hidden />
                Choose .p8 file
                <input
                  type="file"
                  accept=".p8,application/pkcs8,text/plain"
                  className="sr-only"
                  onChange={(e) => { void readKeyFile(e.target.files?.[0]); e.target.value = ""; }}
                />
              </label>
            </div>
          ) : (
            <Field
              label={SECRET_LABEL[f.provider] ?? "Client secret"}
              required={mode === "new"}
              hint={mode === "edit" ? "Leave blank to keep the secret already stored." : "Stored write-only — it is never shown again."}
            >
              <Input
                type="password"
                autoComplete="off"
                value={f.client_secret}
                onChange={(e) => set("client_secret", e.target.value)}
                required={mode === "new"}
                placeholder={mode === "edit" ? "Unchanged" : ""}
              />
            </Field>
          )}
          {f.provider === "microsoft" && (
            <Field label="Directory (tenant)" hint="Leave empty for common (any Microsoft account).">
              <Input value={f.tenant} onChange={(e) => set("tenant", e.target.value)} placeholder="common" />
            </Field>
          )}
          {isApple && (
            <>
              <Field label="Team ID" required hint="10 characters, from the Apple Developer account." error={teamErr}>
                <Input value={f.team_id} onChange={(e) => set("team_id", e.target.value.toUpperCase())} dir="ltr" maxLength={10} />
              </Field>
              <Field label="Key ID" required hint="The ID of the Sign in with Apple key." error={keyErr}>
                <Input value={f.key_id} onChange={(e) => set("key_id", e.target.value.toUpperCase())} dir="ltr" maxLength={10} />
              </Field>
            </>
          )}
        </div>

        {f.provider === "google" && editing?.setup && editing.setup.length > 0 && <GoogleSetupChecklist steps={editing.setup} />}

        <fieldset className="space-y-2">
          <legend className="flex items-center gap-1 text-label">
            Callback URLs
            <InfoHint>Register each URL in your {providerName} app, then tick it. The portal offers the provider only over HTTPS, at a ticked host name.</InfoHint>
          </legend>
          <Disclosure title={`How to register it with ${providerName}`} className="bg-surface/40">
            <p className="text-xs text-muted-foreground">{PROVIDER_CONSOLE_HELP[f.provider]}</p>
          </Disclosure>
          {callbacks.length === 0 ? (
            <Callout tone="warning">
              This appliance has no portal host name yet, so there is no callback URL to register. See{" "}
              <Link className="underline" href="/portal-https">Portal HTTPS</Link>.
            </Callout>
          ) : (
            <>
              <ul className="space-y-2">
                {callbacks.map((c) => {
                  const id = `cb-${c.hostname}`;
                  const googleOnOneGate = f.provider === "google" && (c.kind === "ONEGATE" || c.kind === "SHARED");
                  const checked = f.callback_hosts.includes(c.hostname);
                  return (
                    <li key={c.hostname} className="rounded-md border border-border px-3 py-2">
                      <div className="flex flex-wrap items-center gap-2">
                        <span className="break-all font-mono text-xs">{c.url}</span>
                        <CopyButton value={c.url} variant="ghost" />
                        {c.canonical && <Badge tone="ok">Current portal address</Badge>}
                      </div>
                      <label htmlFor={id} className={cn("mt-1 flex items-center gap-2 text-sm", googleOnOneGate && !checked && "text-muted-foreground")}>
                        <input
                          id={id}
                          type="checkbox"
                          className="size-4 accent-primary"
                          checked={checked}
                          disabled={googleOnOneGate && !checked}
                          onChange={(e) => toggleHost(c.hostname, e.target.checked)}
                        />
                        I registered this callback URL in my {providerName} app
                      </label>
                      {googleOnOneGate && (
                        <p className="mt-1 text-xs text-warning-subtle-foreground">Google does not accept this host name; use your portal domain.</p>
                      )}
                    </li>
                  );
                })}
              </ul>
              {f.callback_hosts.length === 0 && (
                <p className={cn("text-xs text-warning-subtle-foreground", FADE)} role="status">
                  No callback URL is ticked, so clients will not be offered this provider. You can still save and tick one later.
                </p>
              )}
            </>
          )}
        </fieldset>

        <div className="flex items-center justify-between rounded-md border border-border bg-surface/50 px-3.5 py-2.5">
          <div className="flex items-center gap-1">
            <span className="text-sm font-medium">Offer this on the sign-in page</span>
            <InfoHint>Turn it off to finish setting it up without clients seeing it.</InfoHint>
          </div>
          <Switch checked={f.enabled} onCheckedChange={(v) => set("enabled", v)} label="Offer this provider" />
        </div>

        <Disclosure title="Advanced: scopes" defaultOpen={f.scopes.trim() !== ""}>
          <Field label="Scopes" hint="Leave empty to ask only for what sign-in needs. Space separated.">
            <Input value={f.scopes} onChange={(e) => set("scopes", e.target.value)} placeholder={DEFAULT_SCOPES[f.provider] ?? ""} />
          </Field>
        </Disclosure>
        {extra.length > 0 && (
          <Callout tone="warning" title="This asks guests for more than sign-in needs" className={FADE}>
            Guests will be asked to share {extra.map((x) => `"${x}"`).join(", ")} as
            well as their email. Sign-in keeps only the email and the account id, so this data is not used. Under
            data-protection law you must have a reason to collect it and tell clients in your privacy notice.
            You can still save.
          </Callout>
        )}
        <Field label="Your password" required hint="Confirms the change. It is not stored.">
          <Input type="password" autoComplete="current-password" value={pw} onChange={(e) => setPw(e.target.value)} />
        </Field>
        </QuietHints>
      </DialogForm>

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(v) => !v && setDeleting(null)}
        title="Remove this identity provider?"
        description={
          deleting
            ? `Clients will no longer be offered ${nameOf(deleting)} on the sign-in page. The stored client secret is deleted with it, so re-adding means registering it again.`
            : undefined
        }
        confirmLabel="Remove"
        confirmVariant="danger"
        busy={busy}
        error={formErr}
        requirePassword strictPassword
        onConfirm={onDelete}
      />
    </PageShell>
  );
}
