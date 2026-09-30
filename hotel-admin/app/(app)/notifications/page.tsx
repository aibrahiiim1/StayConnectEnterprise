"use client";

// EMAIL, SMS & WHATSAPP — how the appliance sends a guest a one-time code. WhatsApp is its own channel with its
// own services, never a kind of SMS. A channel is offered for a new sender only when its sign-in module is
// licensed here; an existing sender stays listed so it can be edited or removed.
//
// Same treatment as the other provider screens: Add and Edit are dialogs instead of cards stacked above the
// table, the native confirm() on delete is a real confirmation that says what stops working, and a blank API key
// on Edit still means "keep the one already stored" rather than "erase it".
//
// The one substantive addition: the Health column used to read "ok", "error" or "idle". Those are states of the
// integration; what an operator needs is whether codes are actually reaching guests. An "idle" provider that has
// never sent anything is not healthy — it is untested — and that is now what it says.

import { useEffect, useState } from "react";
import { api, ListResp, Whoami, NotificationProvider } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Field, Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { DialogForm, ConfirmDialog } from "@/components/ui/dialog";
import { Switch, SkeletonRows } from "@/components/ui/misc";
import { Plus, Send, MessageSquare } from "lucide-react";
import { canWrite } from "@/lib/roles";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { formatRelative } from "@/lib/utils";
import { moduleLicensed, useCapabilities } from "@/lib/capabilities";

type Channel = "email" | "sms" | "whatsapp";

const KINDS: Record<Channel, string[]> = {
  email: ["stub", "sendgrid", "ses"],
  sms: ["stub", "twilio"],
  whatsapp: ["stub", "meta_whatsapp", "twilio_whatsapp"],
};

const KIND_LABELS: Record<string, string> = {
  stub: "Test only (nothing is sent)",
  sendgrid: "SendGrid",
  ses: "Amazon SES",
  twilio: "Twilio",
  meta_whatsapp: "Meta WhatsApp Cloud API",
  twilio_whatsapp: "Twilio WhatsApp",
};

const CHANNEL_LABELS: Record<Channel, string> = { email: "Email", sms: "Text message", whatsapp: "WhatsApp" };
const CHANNEL_MODULE: Record<Channel, string> = { email: "email_otp", sms: "sms_otp", whatsapp: "whatsapp_otp" };

function health(n: NotificationProvider): { tone: "ok" | "err" | "default"; label: string; detail?: string } {
  if (n.last_error_at && (!n.last_success_at || n.last_error_at > n.last_success_at)) {
    return { tone: "err", label: "Failing", detail: n.last_error ?? undefined };
  }
  if (n.last_success_at) return { tone: "ok", label: "Sending" };
  // NOT "idle". A sender that has never delivered anything is untested, and the difference matters the first time
  // a guest cannot receive their code.
  return { tone: "default", label: "Never used" };
}

type FormState = {
  channel: Channel;
  kind: string;
  display_name: string;
  api_key: string;
  api_user: string;
  from_address: string;
  from_name: string;
  template_name: string;
  language: string;
  content_sid: string;
  enabled: boolean;
};

const EMPTY: FormState = {
  channel: "email", kind: "sendgrid", display_name: "", api_key: "", api_user: "",
  from_address: "", from_name: "", template_name: "", language: "", content_sid: "", enabled: true,
};

// The non-secret WhatsApp template settings, sent only for a WhatsApp sender (an empty value removes one).
function extraOf(f: FormState): Record<string, string> | undefined {
  if (f.channel !== "whatsapp") return undefined;
  return { template_name: f.template_name.trim(), language: f.language.trim(), content_sid: f.content_sid.trim() };
}

export default function NotificationsPage() {
  const toast = useToast();
  const [rows, setRows] = useState<NotificationProvider[] | null>(null);
  const [roles, setRoles] = useState<string[] | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [formErr, setFormErr] = useState<unknown>(null);

  const [mode, setMode] = useState<"closed" | "new" | "edit">("closed");
  const [editing, setEditing] = useState<NotificationProvider | null>(null);
  const [f, setF] = useState<FormState>(EMPTY);
  const [deleting, setDeleting] = useState<NotificationProvider | null>(null);

  const writable = roles !== null && canWrite("notification-providers", roles);
  const caps = useCapabilities();
  // Channels a NEW sender may use: those whose sign-in module is licensed here.
  const channels = (Object.keys(KINDS) as Channel[]).filter((c) => moduleLicensed(caps, CHANNEL_MODULE[c]));
  const set = <K extends keyof FormState>(k: K, v: FormState[K]) => setF((p) => ({ ...p, [k]: v }));

  async function load() {
    try { setRows((await api.get<ListResp<NotificationProvider>>("/notification-providers")).data); }
    catch (e) { setErr(e); }
  }
  useEffect(() => {
    load();
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => setRoles([]));
  }, []);

  function openNew() {
    const first = channels[0] ?? "email";
    setF({ ...EMPTY, channel: first, kind: KINDS[first][1] ?? KINDS[first][0] });
    setEditing(null); setFormErr(null); setMode("new");
  }
  function openEdit(n: NotificationProvider) {
    setF({
      channel: n.channel,
      kind: n.kind,
      display_name: n.display_name ?? "",
      api_key: "", // write-only: blank means keep
      api_user: n.api_user ?? "",
      from_address: n.from_address ?? "",
      from_name: n.from_name ?? "",
      template_name: n.extra?.template_name ?? "",
      language: n.extra?.language ?? "",
      content_sid: n.extra?.content_sid ?? "",
      enabled: n.enabled,
    });
    setEditing(n); setFormErr(null); setMode("edit");
  }

  async function onSubmit() {
    setBusy(true); setFormErr(null);
    try {
      if (mode === "new") {
        await api.post("/notification-providers", {
          channel: f.channel,
          kind: f.kind,
          display_name: f.display_name.trim() || undefined,
          api_key: f.api_key || undefined,
          api_user: f.api_user.trim() || undefined,
          from_address: f.from_address.trim() || undefined,
          from_name: f.from_name.trim() || undefined,
          extra: extraOf(f),
          enabled: f.enabled,
        });
      } else if (editing) {
        const body: Record<string, unknown> = {
          display_name: f.display_name,
          api_user: f.api_user,
          from_address: f.from_address,
          from_name: f.from_name,
          enabled: f.enabled,
        };
        const extra = extraOf(f);
        if (extra) body.extra = extra;
        if (f.api_key) body.api_key = f.api_key; // blank keeps the existing secret
        await api.patch(`/notification-providers/${editing.id}`, body);
      }
      toast.success(mode === "new" ? "Sender added" : "Sender saved");
      setMode("closed"); setEditing(null);
      await load();
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  async function onDelete() {
    if (!deleting) return;
    setBusy(true); setFormErr(null);
    try {
      await api.del(`/notification-providers/${deleting.id}`);
      setDeleting(null);
      toast.success("Sender removed");
      await load();
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  // Changing channel invalidates the kind, so it follows rather than being left pointing at an SMS provider for
  // an email channel.
  function setChannel(channel: Channel) {
    setF((p) => ({ ...p, channel, kind: KINDS[channel][1] ?? KINDS[channel][0] }));
  }

  return (
    <PageShell>
      <PageHeader
        icon={<MessageSquare />}
        eyebrow="Client Portal"
        title={channels.includes("whatsapp") ? "Email, SMS & WhatsApp" : "Email & SMS"}
        description="Without a working sender, sign-in methods that need a code cannot be used."
        help={
          <>
            <HelpSection title="What a sender does">
              <p>
                How the appliance delivers one-time sign-in codes to clients, by email, text message or WhatsApp. Without a
                working sender, any sign-in method that needs a code cannot be used. Room numbers, vouchers and
                accounts do not need one.
              </p>
            </HelpSection>
            <HelpSection title="Setting one up">
              <HelpList
                items={[
                  <>Credentials come from the sending service&apos;s own console.</>,
                  <>The key is stored write-only and is never shown again. When editing, leave it blank to keep the one already stored.</>,
                  <>The channel and service of a sender cannot be changed; remove it and add it again instead.</>,
                  <>Whether clients are offered email, SMS or WhatsApp codes is switched on in <strong>Sign-in methods</strong>.</>,
                  <>A WhatsApp sender needs an approved <strong>authentication template</strong> from Meta (template name and language) or Twilio (content SID); codes are sent only through that template.</>,
                ]}
              />
            </HelpSection>
          </>
        }
        actions={writable && channels.length > 0 && <Button onClick={openNew}><Plus /> Add sender</Button>}
      />

      {roles !== null && !writable && <ReadOnlyNotice>Your role can see the senders but not change them.</ReadOnlyNotice>}
      <ErrorBanner err={err} className="mb-0" />

      <Card>
        <CardBody className="p-0">
          {rows === null ? (
            <SkeletonRows rows={3} cols={5} />
          ) : rows.length === 0 ? (
            <EmptyState
              icon={<Send />}
              title="No sender is configured"
              hint="Clients cannot be sent an emailed or texted code until one is. Room numbers, vouchers and accounts do not need this."
              action={writable && channels.length > 0 ? <Button onClick={openNew}><Plus /> Add a sender</Button> : undefined}
            />
          ) : (
            <Table>
              <THead>
                <TR><TH>Sender</TH><TH>Service</TH><TH>Sends as</TH><TH>Delivery</TH><TH>Offered</TH><TH /></TR>
              </THead>
              <TBody>
                {rows.map((n) => {
                  const h = health(n);
                  return (
                    <TR key={n.id}>
                      <TD>
                        <div className="font-medium">{n.display_name || CHANNEL_LABELS[n.channel] || n.channel}</div>
                        <div className="text-xs text-muted-foreground">
                          {CHANNEL_LABELS[n.channel] ?? n.channel}
                        </div>
                      </TD>
                      <TD className="text-sm">{KIND_LABELS[n.kind] ?? n.kind}</TD>
                      <TD className="text-xs text-muted-foreground">
                        {n.from_address || n.api_user || "—"}
                        {n.from_name && <div>{n.from_name}</div>}
                      </TD>
                      <TD>
                        <Badge tone={h.tone} dot>{h.label}</Badge>
                        {n.last_success_at && (
                          <div className="mt-0.5 text-2xs text-muted-foreground">
                            Last {formatRelative(n.last_success_at)}
                          </div>
                        )}
                        {h.detail && (
                          <div className="mt-0.5 max-w-xs truncate text-2xs text-destructive" title={h.detail}>
                            {h.detail}
                          </div>
                        )}
                      </TD>
                      <TD>
                        {n.enabled ? <Badge tone="ok">In use</Badge> : <Badge tone="default">Off</Badge>}
                      </TD>
                      <TD className="whitespace-nowrap text-right">
                        {writable && <Button size="sm" variant="ghost" onClick={() => openEdit(n)}>Edit</Button>}
                        {writable && (
                          <Button size="sm" variant="ghost" onClick={() => { setFormErr(null); setDeleting(n); }}>
                            Remove
                          </Button>
                        )}
                      </TD>
                    </TR>
                  );
                })}
              </TBody>
            </Table>
          )}
        </CardBody>
      </Card>

      <DialogForm
        open={mode !== "closed"}
        onOpenChange={(v) => { if (!v) { setMode("closed"); setEditing(null); } }}
        title={mode === "edit" ? "Edit sender" : "Add a sender"}
        description="Credentials come from the sending service's own console. The key is stored write-only and is never shown again."
        submitLabel={mode === "edit" ? "Save changes" : "Add sender"}
        busy={busy}
        error={formErr}
        onSubmit={onSubmit}
      >
        <div className="grid gap-4 sm:grid-cols-2">
          {mode === "new" ? (
            <>
              <Field label="Channel">
                <Select value={f.channel} onChange={(e) => setChannel(e.target.value as Channel)}>
                  {channels.map((c) => <option key={c} value={c}>{CHANNEL_LABELS[c]}</option>)}
                </Select>
              </Field>
              <Field label="Service">
                <Select value={f.kind} onChange={(e) => set("kind", e.target.value)}>
                  {KINDS[f.channel].map((k) => <option key={k} value={k}>{KIND_LABELS[k] ?? k}</option>)}
                </Select>
              </Field>
            </>
          ) : (
            <Field label="Channel and service" hint="These cannot be changed; remove and re-add instead.">
              <div className="flex h-9 items-center rounded-md border border-border bg-surface px-3 text-sm">
                {CHANNEL_LABELS[f.channel] ?? f.channel} · {KIND_LABELS[f.kind] ?? f.kind}
              </div>
            </Field>
          )}
          <Field label="Name" hint="How this sender is labelled in this admin.">
            <Input value={f.display_name} onChange={(e) => set("display_name", e.target.value)} placeholder="Optional" />
          </Field>
          <Field
            label={f.kind === "meta_whatsapp" ? "Access token" : f.kind === "twilio_whatsapp" || f.kind === "twilio" ? "Auth token" : "API key"}
            hint={mode === "edit" ? "Leave blank to keep the one already stored." : "From the service's console. Stored write-only."}
          >
            <Input
              type="password"
              autoComplete="off"
              value={f.api_key}
              onChange={(e) => set("api_key", e.target.value)}
              placeholder={mode === "edit" ? "Unchanged" : ""}
            />
          </Field>
          <Field
            label={f.kind === "meta_whatsapp" ? "Phone number ID" : f.channel === "email" ? "API user" : "Account SID"}
            hint={f.kind === "meta_whatsapp" ? "The WhatsApp Business phone number ID (digits). Not a secret."
              : f.channel === "email" ? "Only some services need this." : "Twilio's account SID. Not a secret."}
          >
            <Input value={f.api_user} onChange={(e) => set("api_user", e.target.value)} placeholder="Optional" />
          </Field>
          {f.channel === "email" && (
            <>
              <Field label="From address" hint="Clients see this as the sender.">
                <Input
                  type="email"
                  value={f.from_address}
                  onChange={(e) => set("from_address", e.target.value)}
                  placeholder="noreply@example.com"
                />
              </Field>
              <Field label="From name">
                <Input value={f.from_name} onChange={(e) => set("from_name", e.target.value)} placeholder="Wi-Fi Access" />
              </Field>
            </>
          )}
          {f.kind === "twilio_whatsapp" && (
            <Field label="WhatsApp sender number" hint="The approved WhatsApp sender, with the country code.">
              <Input value={f.from_address} onChange={(e) => set("from_address", e.target.value)} placeholder="+14155238886" dir="ltr" />
            </Field>
          )}
          {f.kind === "meta_whatsapp" && (
            <>
              <Field label="Template name" hint="The approved authentication template that carries the code.">
                <Input value={f.template_name} onChange={(e) => set("template_name", e.target.value)} placeholder="sign_in_code" />
              </Field>
              <Field label="Template language" hint="The template's language code, for example en or en_US.">
                <Input value={f.language} onChange={(e) => set("language", e.target.value)} placeholder="en" />
              </Field>
            </>
          )}
          {f.kind === "twilio_whatsapp" && (
            <Field label="Content SID" hint="The approved authentication template (starts with HX).">
              <Input value={f.content_sid} onChange={(e) => set("content_sid", e.target.value)} placeholder="HX…" />
            </Field>
          )}
        </div>
        <div className="flex items-center justify-between rounded-md border border-border bg-surface/50 px-3.5 py-2.5">
          <div>
            <div className="text-sm font-medium">Use this sender</div>
            <div className="text-xs text-muted-foreground">
              Turn it off to finish configuring it before any client code goes through it.
            </div>
          </div>
          <Switch checked={f.enabled} onCheckedChange={(v) => set("enabled", v)} label="Use this sender" />
        </div>
      </DialogForm>

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(v) => !v && setDeleting(null)}
        title="Remove this sender?"
        description={
          deleting
            ? `Codes will no longer be sent by ${deleting.display_name || (CHANNEL_LABELS[deleting.channel] ?? deleting.channel).toLowerCase()}. Any sign-in method that depends on it will stop working for clients until another sender is configured.`
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
