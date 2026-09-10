"use client";

// SOCIAL LOGIN — the OAuth apps a guest can use instead of a room number or a voucher.
//
// The Add and Edit forms were two cards that appeared above the table, and the Edit one could be open while the
// Add one was too. Both are dialogs now, for the reason that applies everywhere in this admin: on a list with
// several providers the form opened below the fold, so the click appeared to do nothing.
//
// One behaviour is preserved exactly and is worth naming, because it is easy to lose in a rewrite: on Edit, a
// BLANK client secret means "keep the one you have". The field is therefore not pre-filled (there is nothing to
// pre-fill it with — the secret is write-only) and an empty value is omitted from the request rather than sent as
// an empty string, which would erase it.

import { useEffect, useState } from "react";
import { api, ListResp, Whoami, SocialOAuthProvider } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Field, Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { DialogForm, ConfirmDialog } from "@/components/ui/dialog";
import { Switch, SkeletonRows } from "@/components/ui/misc";
import { Plus, KeyRound } from "lucide-react";
import { canWrite } from "@/lib/roles";
import { formatRelative } from "@/lib/utils";

const PROVIDERS = ["google", "apple", "facebook", "microsoft"] as const;
const PROVIDER_LABELS: Record<string, string> = {
  google: "Google",
  apple: "Apple",
  facebook: "Facebook",
  microsoft: "Microsoft",
};

type FormState = {
  provider: string;
  display_name: string;
  client_id: string;
  client_secret: string;
  redirect_uri: string;
  scopes: string;
  enabled: boolean;
};

const EMPTY: FormState = {
  provider: "google", display_name: "", client_id: "", client_secret: "",
  redirect_uri: "", scopes: "", enabled: true,
};

export default function SocialProvidersPage() {
  const [rows, setRows] = useState<SocialOAuthProvider[] | null>(null);
  const [roles, setRoles] = useState<string[]>([]);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [formErr, setFormErr] = useState<unknown>(null);

  const [mode, setMode] = useState<"closed" | "new" | "edit">("closed");
  const [editing, setEditing] = useState<SocialOAuthProvider | null>(null);
  const [f, setF] = useState<FormState>(EMPTY);
  const [deleting, setDeleting] = useState<SocialOAuthProvider | null>(null);

  const writable = canWrite("social-providers", roles);
  const set = <K extends keyof FormState>(k: K, v: FormState[K]) => setF((p) => ({ ...p, [k]: v }));

  async function load() {
    try { setRows((await api.get<ListResp<SocialOAuthProvider>>("/social-providers")).data); }
    catch (e) { setErr(e); }
  }
  useEffect(() => {
    load();
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => {});
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
      redirect_uri: p.redirect_uri,
      scopes: p.scopes ?? "",
      enabled: p.enabled,
    });
    setEditing(p); setFormErr(null); setMode("edit");
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
          redirect_uri: f.redirect_uri.trim(),
          scopes: f.scopes.trim() || undefined,
          enabled: f.enabled,
        });
      } else if (editing) {
        const body: Record<string, unknown> = {
          display_name: f.display_name,
          client_id: f.client_id.trim() || undefined,
          redirect_uri: f.redirect_uri.trim() || undefined,
          scopes: f.scopes,
          enabled: f.enabled,
        };
        if (f.client_secret) body.client_secret = f.client_secret; // blank keeps the existing secret
        await api.patch(`/social-providers/${editing.id}`, body);
      }
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
      await load();
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  const canSubmit =
    mode === "new"
      ? f.client_id.trim() !== "" && f.client_secret !== "" && f.redirect_uri.trim() !== ""
      : true;

  return (
    <PageShell>
      <PageHeader
        eyebrow="Guest portal"
        title="Social login"
        description="Let guests sign in with an account they already have. Each entry is an OAuth application you register with that provider; the client secret is stored write-only and is never shown again."
        actions={writable && <Button onClick={openNew}><Plus /> Add provider</Button>}
      />

      <ErrorBanner err={err} />

      <Card>
        <CardBody className="p-0">
          {rows === null ? (
            <SkeletonRows rows={3} cols={5} />
          ) : rows.length === 0 ? (
            <EmptyState
              icon={<KeyRound />}
              title="No social login is configured"
              hint="Guests can still sign in with a room number, a voucher or an account."
              action={writable ? <Button onClick={openNew}><Plus /> Add a provider</Button> : undefined}
            />
          ) : (
            <Table>
              <THead>
                <TR><TH>Provider</TH><TH>Client ID</TH><TH>Redirect URI</TH><TH>Last used</TH><TH>Offered</TH><TH /></TR>
              </THead>
              <tbody>
                {rows.map((p) => (
                  <TR key={p.id}>
                    <TD className="font-medium">{p.display_name || PROVIDER_LABELS[p.provider] || p.provider}</TD>
                    <TD className="max-w-xs truncate font-mono text-xs" title={p.client_id}>{p.client_id}</TD>
                    <TD className="max-w-xs truncate font-mono text-xs" title={p.redirect_uri}>{p.redirect_uri}</TD>
                    <TD className="text-sm text-muted-foreground">
                      {p.last_success_at ? formatRelative(p.last_success_at) : "Never"}
                    </TD>
                    <TD>
                      {p.enabled
                        ? <Badge tone="ok" dot>On the portal</Badge>
                        : <Badge tone="default">Hidden</Badge>}
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
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      <DialogForm
        open={mode !== "closed"}
        onOpenChange={(v) => { if (!v) { setMode("closed"); setEditing(null); } }}
        title={mode === "edit" ? `Edit ${editing?.display_name || PROVIDER_LABELS[editing?.provider ?? ""] || "provider"}` : "Add a social login provider"}
        description="These values come from the OAuth application you registered with the provider."
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
              <div className="flex h-9 items-center rounded-md border border-border bg-surface px-3 text-sm">
                {PROVIDER_LABELS[f.provider] ?? f.provider}
              </div>
            </Field>
          )}
          <Field label="Name on the portal" hint="Leave empty to use the provider's own name.">
            <Input value={f.display_name} onChange={(e) => set("display_name", e.target.value)} placeholder="Optional" />
          </Field>
          <Field label="Client ID" required>
            <Input value={f.client_id} onChange={(e) => set("client_id", e.target.value)} required />
          </Field>
          <Field
            label="Client secret"
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
          <Field label="Redirect URI" required hint="Must match the one registered with the provider exactly.">
            <Input
              value={f.redirect_uri}
              onChange={(e) => set("redirect_uri", e.target.value)}
              required
              placeholder="https://portal.hotel.local/callback"
            />
          </Field>
          <Field label="Scopes" hint="Space separated.">
            <Input value={f.scopes} onChange={(e) => set("scopes", e.target.value)} placeholder="openid email profile" />
          </Field>
        </div>
        <div className="flex items-center justify-between rounded-md border border-border bg-surface/50 px-3.5 py-2.5">
          <div>
            <div className="text-sm font-medium">Offer this on the sign-in page</div>
            <div className="text-xs text-muted-foreground">
              Turn it off to finish setting it up without guests seeing it.
            </div>
          </div>
          <Switch checked={f.enabled} onCheckedChange={(v) => set("enabled", v)} label="Offer this provider" />
        </div>
      </DialogForm>

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(v) => !v && setDeleting(null)}
        title="Remove this social login?"
        description={
          deleting
            ? `Guests will no longer be offered ${deleting.display_name || PROVIDER_LABELS[deleting.provider] || deleting.provider} on the sign-in page. The stored client secret is deleted with it, so re-adding means registering it again.`
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
