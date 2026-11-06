"use client";

import { useCallback, useEffect, useState } from "react";
import { BellRing, Clock, MailWarning, Send } from "lucide-react";
import { api, withStepUp } from "@/lib/api";
import { usePermissions } from "@/lib/permissions";
import { SectionCard } from "@/components/ui/card";
import { Field, FieldGrid, Input, Select, useFieldErrors } from "@/components/ui/input";
import { Button } from "@/components/ui/button";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { Skeleton, Switch } from "@/components/ui/misc";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { PageHeader, PageShell, StatCard, StatGrid } from "@/components/ui/page";
import { Table, THead, TBody, TR, TH, TD, TableMessage } from "@/components/ui/table";
import { HelpList, HelpSection } from "@/components/help";
import { useToast } from "@/components/ui/toast";
import { LastChanged } from "@/components/session-lifetime";
import { ago, formatDateTime } from "@/lib/status";

type Bounds = { default: number; min: number; max: number; unit: string };
type Kind = { kind: string; label: string; description: string; default_delivery: Delivery };
type Delivery = "immediate" | "digest";

type NumKey =
  | "licence_first_warning_days"
  | "licence_second_warning_days"
  | "licence_final_warning_days"
  | "offline_after_minutes"
  | "digest_hour_utc"
  | "log_retention_days"
  | "max_attempts";

type TextKey = "smtp_host" | "smtp_username" | "from_address" | "from_name" | "console_url";

export type NotificationSettings = Record<NumKey, number> & Record<TextKey, string> & {
  enabled: boolean;
  smtp_port: number;
  smtp_security: "starttls" | "tls";
  smtp_password_set: boolean;
  last_success_at?: string | null;
  last_error?: string | null;
  last_error_at?: string | null;
  updated_at?: string | null;
  updated_by?: string | null;
  pending: number;
  failed_last_24h: number;
  bounds: Record<NumKey, Bounds>;
  kinds: Kind[];
};

type Subscriptions = { email: string; items: { event_kind: string; delivery: Delivery }[]; kinds: Kind[] };

type LogRow = {
  id: number;
  created_at: string;
  recipient?: string | null;
  event_kind: string;
  delivery: Delivery;
  subject: string;
  status: "pending" | "sent" | "failed" | "dropped";
  attempts: number;
  next_attempt_at?: string | null;
  sent_at?: string | null;
  last_error?: string | null;
};

const TIMING: { key: NumKey; label: string; explain: string }[] = [
  { key: "licence_first_warning_days", label: "First licence warning (days before)", explain: "The first notice that a licence is ending." },
  { key: "licence_second_warning_days", label: "Second licence warning (days before)", explain: "A reminder closer to the end date." },
  { key: "licence_final_warning_days", label: "Final licence warning (days before)", explain: "The last notice. 0 means on the end date." },
  { key: "offline_after_minutes", label: "Offline after (minutes)", explain: "How long an activated appliance can be silent before staff are told it is offline. A healthy appliance contacts Central every 30 seconds." },
  { key: "digest_hour_utc", label: "Daily digest hour (UTC)", explain: "When the daily digest is sent to those who chose it." },
  { key: "max_attempts", label: "Attempts per message", explain: "How often Central retries a message the mail server did not accept before marking it failed." },
  { key: "log_retention_days", label: "Delivery log (days)", explain: "How long sent, failed and dropped messages stay in the delivery log." },
];

const STATUS_TONE = { pending: "info", sent: "ok", failed: "err", dropped: "default" } as const;
const STATUS_LABEL = { pending: "Waiting", sent: "Sent", failed: "Failed", dropped: "Dropped" } as const;

function MyNotifications({ kinds }: { kinds: Kind[] }) {
  const toast = useToast();
  const [sub, setSub] = useState<Subscriptions | null>(null);
  const [choice, setChoice] = useState<Record<string, "" | Delivery>>({});
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback((s: Subscriptions) => {
    setSub(s);
    const c: Record<string, "" | Delivery> = {};
    for (const k of s.kinds) c[k.kind] = s.items.find((i) => i.event_kind === k.kind)?.delivery ?? "";
    setChoice(c);
  }, []);

  useEffect(() => {
    api.get<Subscriptions>("/cloud/v1/notifications/subscriptions/me").then(load).catch(setErr);
  }, [load]);

  const saved: Record<string, string> = {};
  for (const i of sub?.items ?? []) saved[i.event_kind] = i.delivery;
  const changed = !!sub && kinds.some((k) => (choice[k.kind] ?? "") !== (saved[k.kind] ?? ""));

  async function save() {
    setBusy(true);
    setErr(null);
    try {
      const items = kinds.filter((k) => choice[k.kind]).map((k) => ({ event_kind: k.kind, delivery: choice[k.kind] as Delivery }));
      load(await api.put<Subscriptions>("/cloud/v1/notifications/subscriptions/me", { items }));
      toast.success("Your notification choices are saved", items.length ? `Central emails ${sub?.email}.` : "You will not be emailed.");
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  }

  function chooseDefaults() {
    const c: Record<string, "" | Delivery> = {};
    for (const k of kinds) c[k.kind] = k.default_delivery;
    setChoice(c);
  }

  return (
    <SectionCard
      title="My notifications"
      description={sub ? `What Central emails to ${sub.email}. Only new occurrences are sent.` : "What Central emails to you."}
      actions={sub ? <Button size="sm" variant="outline" onClick={chooseDefaults}>Use recommended</Button> : undefined}
      footer={sub ? (
        <>
          {!changed && !busy && <span className="text-xs text-muted-foreground">No changes to save.</span>}
          <Button size="sm" disabled={!changed} loading={busy} onClick={save}>Save my choices</Button>
        </>
      ) : undefined}
      bodyClassName="space-y-3"
    >
      <ErrorBanner err={err} />
      {!sub && !err && <Skeleton className="h-40" />}
      {sub && (
        <ul className="divide-y divide-border">
          {kinds.map((k) => (
            <li key={k.kind} className="flex flex-col gap-2 py-3 first:pt-0 last:pb-0 sm:flex-row sm:items-center sm:justify-between">
              <div className="min-w-0">
                <div className="text-sm font-medium">{k.label}</div>
                <p className="text-xs text-muted-foreground">{k.description}</p>
              </div>
              <Select aria-label={`${k.label}: how you are told`} className="sm:w-44" value={choice[k.kind] ?? ""}
                onChange={(e) => setChoice((c) => ({ ...c, [k.kind]: e.target.value as "" | Delivery }))}>
                <option value="">Off</option>
                <option value="immediate">Immediately</option>
                <option value="digest">Daily digest</option>
              </Select>
            </li>
          ))}
        </ul>
      )}
    </SectionCard>
  );
}

function MailServer({ s, canManage, onSaved }: { s: NotificationSettings; canManage: boolean; onSaved: (v: NotificationSettings) => void }) {
  const toast = useToast();
  const fields = useFieldErrors<string>();
  const init = () => ({
    enabled: s.enabled, smtp_host: s.smtp_host, smtp_port: String(s.smtp_port), smtp_security: s.smtp_security,
    smtp_username: s.smtp_username, from_address: s.from_address, from_name: s.from_name, console_url: s.console_url,
  });
  const [v, setV] = useState(init);
  const [password, setPassword] = useState("");
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState<"" | "save" | "test" | "clear">("");

  useEffect(() => {
    setV({
      enabled: s.enabled, smtp_host: s.smtp_host, smtp_port: String(s.smtp_port), smtp_security: s.smtp_security,
      smtp_username: s.smtp_username, from_address: s.from_address, from_name: s.from_name, console_url: s.console_url,
    });
  }, [s]);

  const changed =
    v.enabled !== s.enabled || v.smtp_host !== s.smtp_host || v.smtp_port !== String(s.smtp_port) ||
    v.smtp_security !== s.smtp_security || v.smtp_username !== s.smtp_username || v.from_address !== s.from_address ||
    v.from_name !== s.from_name || v.console_url !== s.console_url || password !== "";

  async function save() {
    const port = Number(v.smtp_port);
    if (!/^\d+$/.test(v.smtp_port.trim()) || port < 1 || port > 65535) {
      fields.fail({ smtp_port: "Enter a port from 1 to 65535." });
      return;
    }
    fields.clear();
    setBusy("save");
    setErr(null);
    try {
      const out = await withStepUp(async () => {
        if (password) await api.put("/cloud/v1/notifications/smtp-password", { password });
        return api.patch<NotificationSettings>("/cloud/v1/notifications/settings", {
          enabled: v.enabled, smtp_host: v.smtp_host, smtp_port: port, smtp_security: v.smtp_security,
          smtp_username: v.smtp_username, from_address: v.from_address, from_name: v.from_name, console_url: v.console_url,
        });
      });
      setPassword("");
      onSaved(out);
      toast.success("Mail settings saved", out.enabled ? "Notifications are on." : "Notifications are off.");
    } catch (e) {
      setPassword("");
      setErr(e);
    } finally {
      setBusy("");
    }
  }

  async function clearPassword() {
    setBusy("clear");
    setErr(null);
    try {
      await withStepUp(() => api.put("/cloud/v1/notifications/smtp-password", { password: "" }));
      onSaved(await api.get<NotificationSettings>("/cloud/v1/notifications/settings"));
      toast.success("Mail server password removed");
    } catch (e) {
      setErr(e);
    } finally {
      setBusy("");
    }
  }

  async function test() {
    setBusy("test");
    setErr(null);
    try {
      const r = await withStepUp(() => api.post<{ sent_to: string }>("/cloud/v1/notifications/test"));
      toast.success("Test message sent", `Check the inbox of ${r.sent_to}.`);
    } catch (e) {
      setErr(e);
    } finally {
      setBusy("");
      onSaved(await api.get<NotificationSettings>("/cloud/v1/notifications/settings").catch(() => s));
    }
  }

  const set = (k: keyof ReturnType<typeof init>) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) =>
    setV((x) => ({ ...x, [k]: e.target.value }));

  return (
    <SectionCard
      title="Mail server"
      description="The SMTP server Central sends its email through. The connection is always encrypted."
      actions={canManage ? (
        <Button size="sm" variant="outline" loading={busy === "test"} disabled={busy !== "" || changed} onClick={test}
          title={changed ? "Save your changes first" : undefined}>
          <Send aria-hidden /> Send test to me
        </Button>
      ) : undefined}
      footer={canManage ? (
        <>
          <LastChanged at={s.updated_at} by={s.updated_by} />
          {!changed && busy === "" && <span className="text-xs text-muted-foreground">No changes to save.</span>}
          <Button size="sm" disabled={!changed || busy !== ""} loading={busy === "save"} onClick={save}>Save</Button>
        </>
      ) : undefined}
      bodyClassName="space-y-4"
    >
      <ErrorBanner err={err} />
      <div className="flex items-start justify-between gap-4 rounded-md border border-border px-3.5 py-3">
        <div>
          <div className="text-sm font-medium">Send notifications</div>
          <p className="text-xs text-muted-foreground">
            When on, Central checks the fleet every minute and emails each team member what they chose under My notifications.
          </p>
        </div>
        <Switch label="Send notifications" checked={v.enabled} disabled={!canManage}
          onCheckedChange={(on) => setV((x) => ({ ...x, enabled: on }))} />
      </div>
      <FieldGrid cols={2}>
        <Field label="Server name" hint="For example smtp.example.com.">
          <Input value={v.smtp_host} maxLength={253} disabled={!canManage} onChange={set("smtp_host")} autoComplete="off" />
        </Field>
        <FieldGrid cols={2}>
          <Field label="Port" error={fields.error("smtp_port")} hint="587 for STARTTLS, 465 for TLS.">
            <Input id={fields.id("smtp_port")} inputMode="numeric" value={v.smtp_port} disabled={!canManage} onChange={set("smtp_port")} />
          </Field>
          <Field label="Security">
            <Select value={v.smtp_security} disabled={!canManage} onChange={set("smtp_security")}>
              <option value="starttls">STARTTLS</option>
              <option value="tls">TLS</option>
            </Select>
          </Field>
        </FieldGrid>
        <Field label="User name" optional hint="Leave empty if the server accepts Central without signing in.">
          <Input value={v.smtp_username} maxLength={320} disabled={!canManage} onChange={set("smtp_username")} autoComplete="off" />
        </Field>
        <Field label="Password" optional
          hint={s.smtp_password_set ? "A password is saved. It is never shown. Type a new one to replace it." : "No password is saved."}>
          <div className="flex gap-2">
            <Input type="password" value={password} maxLength={1024} disabled={!canManage} autoComplete="new-password"
              placeholder={s.smtp_password_set ? "Saved" : ""} onChange={(e) => setPassword(e.target.value)} />
            {canManage && s.smtp_password_set && (
              <Button size="sm" variant="outline" loading={busy === "clear"} disabled={busy !== ""} onClick={clearPassword}>Remove</Button>
            )}
          </div>
        </Field>
        <Field label="From address" hint="The sender staff see, for example central@example.com.">
          <Input type="email" value={v.from_address} maxLength={320} disabled={!canManage} onChange={set("from_address")} />
        </Field>
        <Field label="From name">
          <Input value={v.from_name} maxLength={100} disabled={!canManage} onChange={set("from_name")} />
        </Field>
        <Field label="Console address" optional hint="Put into every email as a link back to Central, for example https://central.example.com.">
          <Input type="url" value={v.console_url} maxLength={300} disabled={!canManage} onChange={set("console_url")} />
        </Field>
      </FieldGrid>
      {!canManage && <LastChanged at={s.updated_at} by={s.updated_by} />}
    </SectionCard>
  );
}

function Timing({ s, canManage, onSaved }: { s: NotificationSettings; canManage: boolean; onSaved: (v: NotificationSettings) => void }) {
  const toast = useToast();
  const fields = useFieldErrors<string>();
  const from = (x: NotificationSettings) => Object.fromEntries(TIMING.map((f) => [f.key, String(x[f.key])])) as Record<NumKey, string>;
  const [vals, setVals] = useState(() => from(s));
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => setVals(from(s)), [s]);

  const changed = TIMING.some((f) => vals[f.key] !== String(s[f.key]));

  async function save() {
    const body: Partial<Record<NumKey, number>> = {};
    for (const f of TIMING) {
      const b = s.bounds[f.key];
      const n = Number(vals[f.key]);
      if (!/^\d+$/.test(vals[f.key].trim()) || n < b.min || n > b.max) {
        setErr(null);
        fields.fail({ [f.key]: `Enter a whole number from ${b.min} to ${b.max}.` });
        return;
      }
      if (n !== s[f.key]) body[f.key] = n;
    }
    const first = body.licence_first_warning_days ?? s.licence_first_warning_days;
    const second = body.licence_second_warning_days ?? s.licence_second_warning_days;
    const final = body.licence_final_warning_days ?? s.licence_final_warning_days;
    if (!(final < second && second < first)) {
      fields.fail({ licence_second_warning_days: "The warnings must come in order: first, then second, then final." });
      return;
    }
    fields.clear();
    setBusy(true);
    setErr(null);
    try {
      onSaved(await withStepUp(() => api.patch<NotificationSettings>("/cloud/v1/notifications/settings", body)));
      toast.success("Timing saved", "It applies from the next check, within a minute.");
    } catch (e) {
      setErr(e);
    } finally {
      setBusy(false);
    }
  }

  return (
    <SectionCard
      title="Timing"
      description="When Central warns, how long it waits and how often it retries."
      footer={canManage ? (
        <>
          {!changed && !busy && <span className="text-xs text-muted-foreground">No changes to save.</span>}
          <Button size="sm" disabled={!changed} loading={busy} onClick={save}>Save</Button>
        </>
      ) : undefined}
      bodyClassName="space-y-4"
    >
      <ErrorBanner err={err} />
      <FieldGrid cols={2}>
        {TIMING.map((f) => {
          const b = s.bounds[f.key];
          return (
            <Field key={f.key} label={f.label} required error={fields.error(f.key)}
              hint={`${f.explain} From ${b.min} to ${b.max}. Default ${b.default}.`}>
              <Input id={fields.id(f.key)} inputMode="numeric" value={vals[f.key]} disabled={!canManage}
                onChange={(e) => setVals((x) => ({ ...x, [f.key]: e.target.value }))} />
            </Field>
          );
        })}
      </FieldGrid>
    </SectionCard>
  );
}

function DeliveryLog({ kinds, refresh }: { kinds: Kind[]; refresh: number }) {
  const [status, setStatus] = useState("");
  const [rows, setRows] = useState<LogRow[] | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const label = (k: string) => kinds.find((x) => x.kind === k)?.label ?? k;

  const load = useCallback(() => {
    setErr(null);
    api.get<{ items: LogRow[] }>(`/cloud/v1/notifications/log${status ? `?status=${status}` : ""}`)
      .then((r) => setRows(r.items ?? []))
      .catch(setErr);
  }, [status]);

  useEffect(() => {
    setRows(null);
    load();
  }, [load, refresh]);

  return (
    <SectionCard
      title="Delivery log"
      description="The newest 200 messages. Several notices for one person in the same minute are sent as one email."
      actions={
        <>
          <Select aria-label="Status" className="w-36" value={status} onChange={(e) => setStatus(e.target.value)}>
            <option value="">All</option>
            <option value="pending">Waiting</option>
            <option value="sent">Sent</option>
            <option value="failed">Failed</option>
            <option value="dropped">Dropped</option>
          </Select>
          <Button size="sm" variant="outline" onClick={load}>Refresh</Button>
        </>
      }
      flush
    >
      <ErrorBanner err={err} className="m-4" />
      <Table aria-label="Notification delivery log">
        <THead>
          <TR>
            <TH>Created</TH>
            <TH>Recipient</TH>
            <TH>Event</TH>
            <TH>Subject</TH>
            <TH>Status</TH>
          </TR>
        </THead>
        <TBody>
          {!rows && !err && <TableMessage colSpan={5}><Skeleton className="h-6" /></TableMessage>}
          {rows && rows.length === 0 && <TableMessage colSpan={5}>No messages yet.</TableMessage>}
          {rows?.map((r) => (
            <TR key={r.id}>
              <TD><span title={formatDateTime(r.created_at)}>{ago(r.created_at)}</span></TD>
              <TD>{r.recipient ?? <span className="text-muted-foreground">Removed member</span>}</TD>
              <TD>
                {label(r.event_kind)}
                {r.delivery === "digest" && <Badge size="sm" className="ms-1.5">Digest</Badge>}
              </TD>
              <TD className="max-w-md truncate" title={r.subject}>{r.subject}</TD>
              <TD>
                <StatusBadge tone={STATUS_TONE[r.status]}>{STATUS_LABEL[r.status]}</StatusBadge>
                {r.last_error && <p className="mt-1 max-w-xs text-xs text-muted-foreground" title={r.last_error}>{r.last_error}</p>}
                {r.status === "pending" && r.attempts > 0 && r.next_attempt_at && (
                  <p className="mt-1 text-xs text-muted-foreground">Attempt {r.attempts + 1} {ago(r.next_attempt_at)}</p>
                )}
              </TD>
            </TR>
          ))}
        </TBody>
      </Table>
    </SectionCard>
  );
}

export default function NotificationsPage() {
  const { can } = usePermissions();
  const canManage = can["team.manage"];
  const [s, setS] = useState<NotificationSettings | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [refresh, setRefresh] = useState(0);

  useEffect(() => {
    api.get<NotificationSettings>("/cloud/v1/notifications/settings").then(setS).catch(setErr);
  }, []);

  const saved = (v: NotificationSettings) => {
    setS(v);
    setRefresh((n) => n + 1);
  };
  const failing = !!s?.last_error_at && (!s.last_success_at || s.last_error_at > s.last_success_at);

  return (
    <PageShell>
      <PageHeader
        title="Notifications"
        description="Email the Central team when something in the fleet needs attention."
        help={
          <>
            <HelpSection title="How it works">
              <HelpList items={[
                <>Every minute Central checks the same facts the Overview shows and emails each new occurrence once.</>,
                <>A licence is warned about at each of the three warning points. An offline appliance is reported once, and again when it is back.</>,
                <>Several notices for one person at the same time arrive as one email. The daily digest bundles everything set to Daily digest.</>,
                <>When notifications are first switched on, conditions that already exist are sent once.</>,
              ]} />
            </HelpSection>
            <HelpSection title="Who receives what">
              <HelpList items={[
                <>Each team member chooses their own events under My notifications.</>,
                <>Only active Central team members are emailed. Customer users never are.</>,
                <>Emails contain customer and site names and appliance serials. They never contain guest data, which Central does not hold.</>,
              ]} />
            </HelpSection>
          </>
        }
      />

      <ErrorBanner err={err} />
      {s && !s.enabled && (
        <Callout tone="info" title="Notifications are off">
          {canManage ? "Set up the mail server below, send a test and switch Send notifications on." : "A platform admin can switch them on."}
        </Callout>
      )}
      {s && failing && (
        <Callout tone="danger" title="Central could not send its last email">{s.last_error}</Callout>
      )}

      {!s && !err && <Skeleton className="h-[5.25rem]" />}
      {s && (
        <StatGrid cols={4}>
          <StatCard label="Notifications" icon={<BellRing />} tone={s.enabled ? "ok" : "default"} value={s.enabled ? "On" : "Off"}
            hint={s.enabled ? "Checked every minute" : "Nothing is sent"} />
          <StatCard label="Last email sent" icon={<Send />} tone="default"
            value={s.last_success_at ? <span title={formatDateTime(s.last_success_at)}>{ago(s.last_success_at)}</span> : "Never"} />
          <StatCard label="Waiting" icon={<Clock />} tone={s.pending > 0 ? "warn" : "default"} value={String(s.pending)}
            hint="Retrying or held for the digest" />
          <StatCard label="Failed in the last 24 hours" icon={<MailWarning />} tone={s.failed_last_24h > 0 ? "err" : "ok"}
            value={String(s.failed_last_24h)} />
        </StatGrid>
      )}

      {s && <MyNotifications kinds={s.kinds} />}
      {s && <MailServer s={s} canManage={canManage} onSaved={saved} />}
      {s && <Timing s={s} canManage={canManage} onSaved={saved} />}
      {s && <DeliveryLog kinds={s.kinds} refresh={refresh} />}
    </PageShell>
  );
}
