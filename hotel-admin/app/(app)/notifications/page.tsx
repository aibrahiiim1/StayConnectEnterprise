"use client";

// EMAIL & SMS — how the appliance sends a guest a one-time code.
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
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Field, Select } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { DialogForm, ConfirmDialog } from "@/components/ui/dialog";
import { Switch, SkeletonRows } from "@/components/ui/misc";
import { Plus, Send } from "lucide-react";
import { canWrite } from "@/lib/roles";
import { formatRelative } from "@/lib/utils";

const KINDS: Record<string, string[]> = {
  email: ["stub", "sendgrid", "ses"],
  sms: ["stub", "twilio"],
};

const KIND_LABELS: Record<string, string> = {
  stub: "Test only (nothing is sent)",
  sendgrid: "SendGrid",
  ses: "Amazon SES",
  twilio: "Twilio",
};

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
  channel: "email" | "sms";
  kind: string;
  display_name: string;
  api_key: string;
  api_user: string;
  from_address: string;
  from_name: string;
  enabled: boolean;
};

const EMPTY: FormState = {
  channel: "email", kind: "sendgrid", display_name: "", api_key: "", api_user: "",
  from_address: "", from_name: "", enabled: true,
};

export default function NotificationsPage() {
  const [rows, setRows] = useState<NotificationProvider[] | null>(null);
  const [roles, setRoles] = useState<string[]>([]);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const [formErr, setFormErr] = useState<unknown>(null);

  const [mode, setMode] = useState<"closed" | "new" | "edit">("closed");
  const [editing, setEditing] = useState<NotificationProvider | null>(null);
  const [f, setF] = useState<FormState>(EMPTY);
  const [deleting, setDeleting] = useState<NotificationProvider | null>(null);

  const writable = canWrite("notification-providers", roles);
  const set = <K extends keyof FormState>(k: K, v: FormState[K]) => setF((p) => ({ ...p, [k]: v }));

  async function load() {
    try { setRows((await api.get<ListResp<NotificationProvider>>("/notification-providers")).data); }
    catch (e) { setErr(e); }
  }
  useEffect(() => {
    load();
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => {});
  }, []);

  function openNew() { setF(EMPTY); setEditing(null); setFormErr(null); setMode("new"); }
  function openEdit(n: NotificationProvider) {
    setF({
      channel: n.channel,
      kind: n.kind,
      display_name: n.display_name ?? "",
      api_key: "", // write-only: blank means keep
      api_user: n.api_user ?? "",
      from_address: n.from_address ?? "",
      from_name: n.from_name ?? "",
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
        if (f.api_key) body.api_key = f.api_key; // blank keeps the existing secret
        await api.patch(`/notification-providers/${editing.id}`, body);
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
      await api.del(`/notification-providers/${deleting.id}`);
      setDeleting(null);
      await load();
    } catch (e) { setFormErr(e); }
    finally { setBusy(false); }
  }

  // Changing channel invalidates the kind, so it follows rather than being left pointing at an SMS provider for
  // an email channel.
  function setChannel(channel: "email" | "sms") {
    setF((p) => ({ ...p, channel, kind: KINDS[channel][channel === "email" ? 1 : 1] ?? KINDS[channel][0] }));
  }

  return (
    <PageShell>
      <PageHeader
        eyebrow="Guest portal"
        title="Email & SMS"
        description="How the appliance delivers one-time sign-in codes to guests. Without a working sender, any sign-in method that needs a code cannot be used."
        actions={writable && <Button onClick={openNew}><Plus /> Add sender</Button>}
      />

      <ErrorBanner err={err} />

      <Card>
        <CardBody className="p-0">
          {rows === null ? (
            <SkeletonRows rows={3} cols={5} />
          ) : rows.length === 0 ? (
            <EmptyState
              icon={<Send />}
              title="No sender is configured"
              hint="Guests cannot be sent an emailed or texted code until one is. Room numbers, vouchers and accounts do not need this."
              action={writable ? <Button onClick={openNew}><Plus /> Add a sender</Button> : undefined}
            />
          ) : (
            <Table>
              <THead>
                <TR><TH>Sender</TH><TH>Service</TH><TH>Sends as</TH><TH>Delivery</TH><TH>Offered</TH><TH /></TR>
              </THead>
              <tbody>
                {rows.map((n) => {
                  const h = health(n);
                  return (
                    <TR key={n.id}>
                      <TD>
                        <div className="font-medium">{n.display_name || (n.channel === "email" ? "Email" : "SMS")}</div>
                        <div className="text-xs text-muted-foreground">
                          {n.channel === "email" ? "Email" : "Text message"}
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
              </tbody>
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
                <Select value={f.channel} onChange={(e) => setChannel(e.target.value as "email" | "sms")}>
                  <option value="email">Email</option>
                  <option value="sms">Text message</option>
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
                {f.channel === "email" ? "Email" : "Text message"} · {KIND_LABELS[f.kind] ?? f.kind}
              </div>
            </Field>
          )}
          <Field label="Name" hint="How this sender is labelled in this admin.">
            <Input value={f.display_name} onChange={(e) => set("display_name", e.target.value)} placeholder="Optional" />
          </Field>
          <Field
            label="API key"
            hint={mode === "edit" ? "Leave blank to keep the key already stored." : "From the service's console."}
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
            label={f.channel === "sms" ? "Account SID" : "API user"}
            hint={f.channel === "sms" ? "Twilio's account SID. Not a secret." : "Only some services need this."}
          >
            <Input value={f.api_user} onChange={(e) => set("api_user", e.target.value)} placeholder="Optional" />
          </Field>
          {f.channel === "email" && (
            <>
              <Field label="From address" hint="Guests see this as the sender.">
                <Input
                  type="email"
                  value={f.from_address}
                  onChange={(e) => set("from_address", e.target.value)}
                  placeholder="noreply@hotel.com"
                />
              </Field>
              <Field label="From name">
                <Input value={f.from_name} onChange={(e) => set("from_name", e.target.value)} placeholder="Hotel Wi-Fi" />
              </Field>
            </>
          )}
        </div>
        <div className="flex items-center justify-between rounded-md border border-border bg-surface/50 px-3.5 py-2.5">
          <div>
            <div className="text-sm font-medium">Use this sender</div>
            <div className="text-xs text-muted-foreground">
              Turn it off to finish configuring it before any guest code goes through it.
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
            ? `Codes will no longer be sent by ${deleting.display_name || (deleting.channel === "email" ? "email" : "text message")}. Any sign-in method that depends on it will stop working for guests until another sender is configured.`
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
