"use client";

// SOCIAL LOGIN — the OAuth apps a guest can use instead of a room number or a voucher. Each hotel registers
// and owns its own apps (docs/architecture/ONEGATE_HTTPS_PORTAL_AND_SOCIAL_LOGIN.md §5).
//
// The Add and Edit forms were two cards that appeared above the table, and the Edit one could be open while the
// Add one was too. Both are dialogs now, for the reason that applies everywhere in this admin: on a list with
// several providers the form opened below the fold, so the click appeared to do nothing.
//
// One behaviour is preserved exactly and is worth naming, because it is easy to lose in a rewrite: on Edit, a
// BLANK client secret means "keep the one you have". The field is therefore not pre-filled (there is nothing to
// pre-fill it with — the secret is write-only) and an empty value is omitted from the request rather than sent as
// an empty string, which would erase it.
//
// THERE IS NO REDIRECT URI FIELD ANY MORE. The callback is https://<portal host name>/auth/social/callback, one
// per host name the appliance has (its OneGate host name and, once set up, the hotel portal domain). The page
// shows each one with a copy button and asks the hotel to tick the ones it registered in its app; a provider is
// offered only while HTTPS is active and the portal's current host name is ticked.

import { useEffect, useState } from "react";
import Link from "next/link";
import { api, ListResp, Whoami, SocialOAuthProvider } from "@/lib/api";
import { PROVIDER_CONSOLE_HELP, PROVIDER_LABELS, PortalCallbackURL, keepAvailable, offeredWords } from "@/lib/portal-https";
import { PageShell, PageHeader } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Field, Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { DialogForm, ConfirmDialog } from "@/components/ui/dialog";
import { Switch, SkeletonRows } from "@/components/ui/misc";
import { CopyButton, ReadOnlyNotice } from "@/components/ui/patterns";
import { Plus, KeyRound, AtSign } from "lucide-react";
import { canWrite } from "@/lib/roles";
import { useToast } from "@/components/ui/toast";
import { formatRelative } from "@/lib/utils";

const PROVIDERS = ["google", "apple", "facebook", "microsoft"] as const;

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

// What each provider calls its two values, so the form uses the words on the provider's own console.
const SECRET_LABEL: Record<string, string> = {
  google: "Client secret", facebook: "App secret", microsoft: "Client secret", apple: "Private key (.p8)",
};
const ID_LABEL: Record<string, string> = {
  google: "Client ID", facebook: "App ID", microsoft: "Application (client) ID", apple: "Services ID",
};

// Provider-specific, non-secret settings: only the ones this provider uses are sent.
function providerExtras(f: FormState): Record<string, string> {
  if (f.provider === "microsoft") return { tenant: f.tenant.trim() };
  if (f.provider === "apple") return { team_id: f.team_id.trim(), key_id: f.key_id.trim() };
  return {};
}

export default function SocialProvidersPage() {
  const toast = useToast();
  const [rows, setRows] = useState<SocialOAuthProvider[] | null>(null);
  const [callbacks, setCallbacks] = useState<PortalCallbackURL[]>([]);
  const [httpsActive, setHttpsActive] = useState<boolean | null>(null);
  const [roles, setRoles] = useState<string[] | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [formErr, setFormErr] = useState<unknown>(null);

  const [mode, setMode] = useState<"closed" | "new" | "edit">("closed");
  const [editing, setEditing] = useState<SocialOAuthProvider | null>(null);
  const [f, setF] = useState<FormState>(EMPTY);
  const [deleting, setDeleting] = useState<SocialOAuthProvider | null>(null);

  const writable = roles !== null && canWrite("social-providers", roles);
  const set = <K extends keyof FormState>(k: K, v: FormState[K]) => setF((p) => ({ ...p, [k]: v }));

  async function load() {
    try {
      const r = await api.get<SocialList>("/social-providers");
      setRows(r.data);
      setCallbacks(r.callback_urls ?? []);
      setHttpsActive(typeof r.https_active === "boolean" ? r.https_active : null);
    } catch (e) { setErr(e); }
  }
  useEffect(() => {
    load();
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => setRoles([]));
  }, []);

  function openNew() {
    setF(EMPTY); setEditing(null); setFormErr(null); setMode("new");
  }
  function openEdit(p: SocialOAuthProvider) {
    setF({
      provider: p.provider,
      display_name: p.display_name ?? "",
      client_id: p.client_id,
      // Deliberately blank. The secret cannot be read back, and a placeholder value here would be sent on save.
      client_secret: "",
      callback_hosts: keepAvailable(p.callback_hosts ?? [], callbacks),
      scopes: p.scopes ?? "",
      tenant: p.tenant ?? "",
      team_id: p.team_id ?? "",
      key_id: p.key_id ?? "",
      enabled: p.enabled,
    });
    setEditing(p); setFormErr(null); setMode("edit");
  }

  function toggleHost(host: string, on: boolean) {
    setF((p) => ({
      ...p,
      callback_hosts: on ? Array.from(new Set([...p.callback_hosts, host])) : p.callback_hosts.filter((h) => h !== host),
    }));
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
        });
      } else if (editing) {
        const body: Record<string, unknown> = {
          display_name: f.display_name,
          client_id: f.client_id.trim() || undefined,
          callback_hosts: f.callback_hosts,
          scopes: f.scopes,
          ...providerExtras(f),
          enabled: f.enabled,
        };
        if (f.client_secret) body.client_secret = f.client_secret; // blank keeps the existing secret
        await api.patch(`/social-providers/${editing.id}`, body);
      }
      toast.success(mode === "new" ? "Provider added" : "Provider saved");
      setMode("closed"); setEditing(null);
      await load();
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  async function onDelete() {
    if (!deleting) return;
    setBusy(true); setFormErr(null);
    try {
      await api.del(`/social-providers/${deleting.id}`);
      setDeleting(null);
      toast.success("Provider removed");
      await load();
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  const canSubmit =
    mode === "new"
      ? f.client_id.trim() !== "" && f.client_secret !== "" &&
        (f.provider !== "apple" || (f.team_id.trim() !== "" && f.key_id.trim() !== ""))
      : true;
  const providerName = PROVIDER_LABELS[f.provider] ?? f.provider;

  return (
    <PageShell>
      <PageHeader
        icon={<AtSign />}
        eyebrow="Client access"
        title="Identity providers"
        description="Let clients sign in with an account they already have."
        help={
          <>
            <HelpSection title="What a provider entry is">
              <p>
                Each entry is an OAuth application your hotel registers and owns with that provider (for example
                Google). The values on it come from that registered application.
              </p>
            </HelpSection>
            <HelpSection title="Setting one up">
              <HelpList
                items={[
                  <>Register the <strong>callback URL</strong> shown in the form in your app, then tick it. The portal offers the provider only over HTTPS, at a host name you ticked.</>,
                  <>The <strong>client secret</strong> is stored write-only and is never shown again. When editing, leave it blank to keep the one already stored.</>,
                  <><strong>Google</strong> needs your hotel portal domain (set it up under Portal HTTPS); it does not accept the OneGate host name.</>,
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

      {roles !== null && !writable && <ReadOnlyNotice>Your role can see the social login providers but not change them.</ReadOnlyNotice>}
      <ErrorBanner err={err} className="mb-0" />

      {httpsActive === false && (
        <Callout tone="warning" title="Social sign-in is not offered right now">
          The Client Portal is on the HTTP fallback, and providers accept HTTPS callbacks only. See{" "}
          <Link className="underline" href="/portal-https">Portal HTTPS</Link>.
        </Callout>
      )}

      <Card>
        <CardBody className="p-0">
          {rows === null ? (
            <SkeletonRows rows={3} cols={5} />
          ) : rows.length === 0 ? (
            <EmptyState
              icon={<KeyRound />}
              title="No identity provider is configured"
              hint="Clients can still sign in with a room number, a voucher or an account."
              action={writable ? <Button onClick={openNew}><Plus /> Add a provider</Button> : undefined}
            />
          ) : (
            <Table>
              <THead>
                <TR><TH>Provider</TH><TH>Client ID</TH><TH>Status</TH><TH>Last used</TH><TH /></TR>
              </THead>
              <TBody>
                {rows.map((p) => (
                  <TR key={p.id}>
                    <TD className="font-medium">{p.display_name || PROVIDER_LABELS[p.provider] || p.provider}</TD>
                    <TD className="max-w-xs truncate font-mono text-xs" title={p.client_id}>{p.client_id}</TD>
                    <TD className="max-w-sm">
                      <div className="flex flex-col gap-1">
                        {p.offered
                          ? <Badge tone="ok" dot>Offered</Badge>
                          : <Badge tone={p.enabled ? "warn" : "default"}>{p.enabled ? "Not offered" : "Hidden"}</Badge>}
                        {!p.offered && <span className="text-xs text-muted-foreground">{offeredWords(p.offered_reason, p.provider)}</span>}
                      </div>
                    </TD>
                    <TD className="text-sm text-muted-foreground">
                      {p.last_success_at ? formatRelative(p.last_success_at) : "Never"}
                    </TD>
                    <TD className="whitespace-nowrap text-right">
                      {writable && <Button size="sm" variant="ghost" onClick={() => openEdit(p)}>Edit</Button>}
                      {writable && (
                        <Button size="sm" variant="ghost" onClick={() => { setFormErr(null); setDeleting(p); }}>
                          Remove
                        </Button>
                      )}
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          )}
        </CardBody>
      </Card>

      <DialogForm
        open={mode !== "closed"}
        onOpenChange={(v) => { if (!v) { setMode("closed"); setEditing(null); } }}
        title={mode === "edit" ? `Edit ${editing?.display_name || PROVIDER_LABELS[editing?.provider ?? ""] || "provider"}` : "Add an identity provider"}
        description="These values come from the OAuth application your hotel registered with the provider."
        size="lg"
        submitLabel={mode === "edit" ? "Save changes" : "Add provider"}
        busy={busy}
        error={formErr}
        disabled={!canSubmit}
        onSubmit={onSubmit}
      >
        <div className="grid gap-4 sm:grid-cols-2">
          {mode === "new" ? (
            <Field label="Provider">
              <Select value={f.provider} onChange={(e) => set("provider", e.target.value)}>
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
          <Field label="Scopes" hint="Space separated.">
            <Input value={f.scopes} onChange={(e) => set("scopes", e.target.value)} placeholder="openid email profile" />
          </Field>
          {f.provider === "microsoft" && (
            <Field label="Directory (tenant)" hint="Leave empty for common (any Microsoft account).">
              <Input value={f.tenant} onChange={(e) => set("tenant", e.target.value)} placeholder="common" />
            </Field>
          )}
          {f.provider === "apple" && (
            <>
              <Field label="Team ID" required hint="10 characters, from the Apple Developer account.">
                <Input value={f.team_id} onChange={(e) => set("team_id", e.target.value)} />
              </Field>
              <Field label="Key ID" required hint="The ID of the Sign in with Apple key.">
                <Input value={f.key_id} onChange={(e) => set("key_id", e.target.value)} />
              </Field>
            </>
          )}
        </div>

        <fieldset className="space-y-2">
          <legend className="text-sm font-medium">Callback URLs</legend>
          <p className="text-xs text-muted-foreground">{PROVIDER_CONSOLE_HELP[f.provider]}</p>
          {callbacks.length === 0 ? (
            <Callout tone="warning">
              This appliance has no portal host name yet, so there is no callback URL to register. See{" "}
              <Link className="underline" href="/portal-https">Portal HTTPS</Link>.
            </Callout>
          ) : (
            <ul className="space-y-2">
              {callbacks.map((c) => {
                const id = `cb-${c.hostname}`;
                const googleOnOneGate = f.provider === "google" && c.kind === "ONEGATE";
                return (
                  <li key={c.hostname} className="rounded-md border border-border px-3 py-2">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="break-all font-mono text-xs">{c.url}</span>
                      <CopyButton value={c.url} variant="ghost" />
                      {c.canonical && <Badge tone="ok">Current portal address</Badge>}
                    </div>
                    <label htmlFor={id} className="mt-1 flex items-center gap-2 text-sm">
                      <input
                        id={id}
                        type="checkbox"
                        className="size-4 accent-primary"
                        checked={f.callback_hosts.includes(c.hostname)}
                        onChange={(e) => toggleHost(c.hostname, e.target.checked)}
                      />
                      I registered this callback URL in my {providerName} app
                    </label>
                    {googleOnOneGate && (
                      <p className="mt-1 text-xs text-muted-foreground">Google does not accept this host name; use your hotel portal domain.</p>
                    )}
                  </li>
                );
              })}
            </ul>
          )}
        </fieldset>

        <div className="flex items-center justify-between rounded-md border border-border bg-surface/50 px-3.5 py-2.5">
          <div>
            <div className="text-sm font-medium">Offer this on the sign-in page</div>
            <div className="text-xs text-muted-foreground">
              Turn it off to finish setting it up without clients seeing it.
            </div>
          </div>
          <Switch checked={f.enabled} onCheckedChange={(v) => set("enabled", v)} label="Offer this provider" />
        </div>
      </DialogForm>

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(v) => !v && setDeleting(null)}
        title="Remove this identity provider?"
        description={
          deleting
            ? `Clients will no longer be offered ${deleting.display_name || PROVIDER_LABELS[deleting.provider] || deleting.provider} on the sign-in page. The stored client secret is deleted with it, so re-adding means registering it again.`
            : undefined
        }
        confirmLabel="Remove"
        confirmVariant="danger"
        busy={busy}
        error={formErr}
        onConfirm={onDelete}
      />
    </PageShell>
  );
}
