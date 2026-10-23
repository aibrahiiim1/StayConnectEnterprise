"use client";

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  api, ApiError, GuestAccount, GuestAccountCreateResp,
  GuestAccountPasswordResp, type GuestAccountsPage as AccountsPageResp,
  type GuestAccountManage, type GuestAccountRemoveResp,
} from "@/lib/api";
import { ManageAccountDialog } from "@/components/guest-accounts/manage-account";
import { canRead, canWrite } from "@/lib/roles";
import { PageShell, PageHeader, StatCard, Toolbar } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { Card, CardBody } from "@/components/ui/card";
import { Table, TBody, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Field } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogDescription, DialogBody,
  ConfirmDialog,
} from "@/components/ui/dialog";
import { Pagination, ResultsCount, SearchInput } from "@/components/ui/data";
import { Select } from "@/components/ui/input";
import { Switch, SkeletonRows } from "@/components/ui/misc";
import { PasswordFormatCard } from "@/components/guest-accounts/password-format";
import { OneTimeReveal, ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { Plus, KeyRound, Eye, EyeOff, Pencil, Power, Users, Send, Settings2, LogIn, Monitor } from "lucide-react";
import { formatRelative } from "@/lib/utils";
import { isRemovedAccount, verificationStatus } from "@/lib/account-verification";
import { ACCOUNT_NAME_MAX, ACCOUNT_NOTES_MAX, accountEditBody, validityWindow } from "@/lib/account-form";
import { useOperatorRoles } from "@/lib/whoami-context";
import { moduleLicensed, useCapabilities } from "@/lib/capabilities";

const PAGE_SIZES = [25, 50, 100, 200];
// No "Locked out" filter or count: protection is per device, and guest_access_accounts.locked_until is never set, so it
// would always read 0.
const STATUS_FILTERS: { value: string; label: string }[] = [
  { value: "", label: "All accounts" },
  { value: "enabled", label: "Can sign in" },
  { value: "disabled", label: "Disabled" },
  { value: "removed", label: "Removed" },
];

type GuestAccountToggleResp = GuestAccount & {
  packages_suspended?: number; packages_resumed?: number;
  disconnected_sessions?: number; disconnect_failures?: number;
};

function weakPassword(pw: string): boolean {
  return pw.length > 0 && pw.length < 8;
}

// The one-time password reveal is a modal so the only copy cannot scroll off-screen; the password is never
// retrievable afterwards.

export default function GuestAccountsPage() {
  const toast = useToast();
  const hospitality = moduleLicensed(useCapabilities(), "hospitality");
  const [rows, setRows] = useState<GuestAccount[] | null>(null);
  const [portalOn, setPortalOn] = useState<boolean>(false);
  const [err, setErr] = useState<string | null>(null);
  // Form errors show inside the dialog; one on the page behind a modal goes unread.
  const [formErr, setFormErr] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);
  const [q, setQ] = useState("");
  const [status, setStatus] = useState("");
  const [pageSize, setPageSize] = useState(PAGE_SIZES[1]);
  const [offset, setOffset] = useState(0);
  const [resp, setResp] = useState<AccountsPageResp | null>(null);
  const [editing, setEditing] = useState<GuestAccount | null>(null);
  const [pwFor, setPwFor] = useState<GuestAccount | null>(null);
  const [reveal, setReveal] = useState<{ username: string; password: string } | null>(null);
  const [disconnecting, setDisconnecting] = useState<GuestAccount | null>(null);
  const [deleting, setDeleting] = useState<GuestAccount | null>(null);
  const [removal, setRemoval] = useState<GuestAccountManage["removal"] | null>(null);
  const [managing, setManaging] = useState<GuestAccount | null>(null);
  const [actionErr, setActionErr] = useState<unknown>(null);
  // Loading has three outcomes (loading, failed, loaded); null alone conflated the first two.
  const [loadFailed, setLoadFailed] = useState(false);

  // The role decides which controls exist; edged refuses writes to other roles. Fails closed while roles load.
  const roles = useOperatorRoles();
  const mayWrite = roles === null ? false : canWrite("guest-accounts", roles);
  // The generated-password format is a separate key from creating accounts.
  const mayEditFormat = roles === null ? false : canWrite("account-password-settings", roles);
  // The portal switch is a sign-in method, so it follows auth-methods.
  const mayOfferOnPortal = roles === null ? false : canWrite("auth-methods", roles);
  // Device actions follow the sessions key and vouching follows code-reveal; the voucher station holds neither.
  const mayReadDevices = roles === null ? false : canRead("sessions", roles);
  const mayActOnDevices = roles === null ? false : canWrite("sessions", roles);
  const mayVouch = roles === null ? false : canWrite("code-reveal", roles);

  // A credential carries no plan: what a guest may acquire is decided by package eligibility rules.

  useEffect(() => { setOffset(0); }, [q, status, pageSize]);

  // Paged and searched on the server; the search text goes in a header (a username or name; edged logs request
  // lines). Responses can return in any order, so only the latest request may apply its answer.
  const latest = useRef(0);
  const load = useCallback(async () => {
    const mine = ++latest.current;
    setErr(null);
    setLoadFailed(false);
    try {
      const params = new URLSearchParams();
      if (status) params.set("status", status);
      params.set("page", String(Math.floor(offset / pageSize) + 1));
      params.set("page_size", String(pageSize));
      const needle = q.trim();
      const ga = await api.get<AccountsPageResp>(
        "/guest-accounts?" + params.toString(),
        // Percent-encoded: a browser refuses non-Latin-1 text (an Arabic or Cyrillic name) in a header.
        needle ? { "X-Account-Search": encodeURIComponent(needle) } : undefined,
      );
      if (mine !== latest.current) return; // superseded
      setResp(ga);
      setRows(Array.isArray(ga?.data) ? ga.data : []);
    } catch (e: any) {
      if (mine !== latest.current) return; // superseded
      setErr(e?.message ?? "Failed to load");
      setLoadFailed(true);
    }
  }, [q, status, offset, pageSize]);
  useEffect(() => { void load(); }, [load]);

  useEffect(() => {
    api.get<{ enabled: boolean }>("/guest-accounts/portal")
      .then((pv) => setPortalOn(!!pv?.enabled))
      .catch(() => setPortalOn(false));
  }, []);

  const filtered = rows ?? [];

  async function onTogglePortal() {
    setErr(null);
    try {
      await api.post("/guest-accounts/portal", { enabled: !portalOn });
      setPortalOn((v) => !v);
      toast.success(`Username & Password sign-in ${!portalOn ? "shown on" : "hidden from"} the captive portal.`);
    } catch (e: any) { setErr(e?.message ?? "Toggle failed"); }
  }

  function openNew() { setFormErr(null); setShowNew(true); setEditing(null); setPwFor(null); }

  async function onCreate(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true); setFormErr(null);
    const form = new FormData(e.currentTarget);
    const generate = form.get("generate") === "on";
    const password = (form.get("password") as string) || "";
    const win = validityWindow(form);
    if (win.error) { setFormErr(win.error); setBusy(false); return; }
    try {
      const resp = await api.post<GuestAccountCreateResp>("/guest-accounts", {
        username: (form.get("username") as string).trim(),
        password: generate ? "" : password,
        generate,
        display_name: (form.get("display_name") as string) || undefined,
        notes: (form.get("notes") as string) || undefined,
        valid_from: (form.get("valid_from") as string) ? new Date(form.get("valid_from") as string).toISOString() : undefined,
        valid_until: (form.get("valid_until") as string) ? new Date(form.get("valid_until") as string).toISOString() : undefined,
      });
      setShowNew(false);
      (e.target as HTMLFormElement).reset();
      const shown = resp.generated_password ?? password;
      if (shown) setReveal({ username: resp.account.username, password: shown });
      else toast.success(`Account "${resp.account.username}" created.`);
      load();
    } catch (e: any) {
      if (e instanceof ApiError && e.code === "conflict") setFormErr("That username already exists.");
      else if (e instanceof ApiError) setFormErr(e.body?.message ?? e.message);
      else setFormErr(e?.message ?? "Create failed");
    } finally { setBusy(false); }
  }

  async function onSaveEdit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!editing) return;
    setBusy(true); setFormErr(null);
    const form = new FormData(e.currentTarget);
    try {
      const win = validityWindow(form);
      if (win.error) { setFormErr(win.error); return; }
      await api.patch(`/guest-accounts/${editing.id}`, accountEditBody(form, editing.self_registered === true));
      setEditing(null);
      toast.success("Account updated.");
      load();
    } catch (e: any) {
      if (e instanceof ApiError && e.code === "conflict") setFormErr("That username already exists.");
      else setFormErr(e?.message ?? "Update failed");
    } finally { setBusy(false); }
  }

  async function onSavePassword(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!pwFor) return;
    setBusy(true); setFormErr(null);
    const form = new FormData(e.currentTarget);
    const generate = form.get("generate") === "on";
    const password = (form.get("password") as string) || "";
    const disconnect = form.get("disconnect_sessions") === "on";
    try {
      const resp = await api.post<GuestAccountPasswordResp>(`/guest-accounts/${pwFor.id}/set-password`, {
        password: generate ? "" : password, generate, disconnect_sessions: disconnect,
      });
      const shown = resp.generated_password ?? password;
      const uname = pwFor.username;
      setPwFor(null);
      if (shown) setReveal({ username: uname, password: shown });
      // The toast claims only what the appliance confirmed: sessions ended, with devices it could not disconnect
      // reported separately.
      const ended = resp.disconnected_sessions;
      toast.success(
        `Password updated for ${uname}.`,
        disconnect
          ? ended === 1 ? "1 device disconnected." : `${ended ?? 0} devices disconnected.`
          : undefined,
      );
      if (resp.disconnect_failures) {
        toast.error(
          `${resp.disconnect_failures} device${resp.disconnect_failures === 1 ? "" : "s"} could not be disconnected.`,
          "The new password is set. End those sessions from Active sessions.",
        );
      }
      load();
    } catch (e: any) {
      setFormErr(e instanceof ApiError ? (e.body?.message ?? e.message) : (e?.message ?? "Reset failed"));
    } finally { setBusy(false); }
  }

  // Disable ends the account's sessions and pauses its package, so it is confirmed first.
  const [disabling, setDisabling] = useState<GuestAccount | null>(null);
  async function onToggle(a: GuestAccount) {
    setActionErr(null);
    try {
      const r = await api.patch<GuestAccountToggleResp>(`/guest-accounts/${a.id}`, { enabled: !a.enabled });
      if (a.enabled) {
        const ended = r?.disconnected_sessions ?? 0;
        const paused = r?.packages_suspended ?? 0;
        toast.success(`"${a.username}" disabled.`,
          `${ended} device${ended === 1 ? "" : "s"} disconnected; ${paused === 0 ? "no package was active" : "its package is paused"}.`);
        if (r?.disconnect_failures) {
          toast.error(`${r.disconnect_failures} device${r.disconnect_failures === 1 ? "" : "s"} could not be disconnected.`,
            "The account is disabled. End those sessions from Active sessions.");
        }
        setDisabling(null);
      } else {
        const resumed = r?.packages_resumed ?? 0;
        toast.success(`"${a.username}" can sign in again.`,
          resumed > 0 ? "Its package is active again; its end date did not move." : undefined);
      }
      load();
    }
    catch (e: any) {
      if (a.enabled) setActionErr(e);
      else setErr(e?.message ?? "Update failed");
    }
  }

  async function onDisconnect() {
    if (!disconnecting) return;
    setBusy(true); setActionErr(null);
    try {
      const r = await api.post<{ disconnected_sessions: number; disconnect_failures?: number }>(
        `/guest-accounts/${disconnecting.id}/disconnect`,
      );
      toast.success(`${r.disconnected_sessions} device${r.disconnected_sessions === 1 ? "" : "s"} disconnected.`);
      if (r.disconnect_failures) {
        toast.error(
          `${r.disconnect_failures} device${r.disconnect_failures === 1 ? "" : "s"} could not be disconnected.`,
          "End those sessions from Active sessions.",
        );
      }
      setDisconnecting(null);
      load();
    } catch (e) { setActionErr(e); }
    finally { setBusy(false); }
  }

  // scd owns the outbox and limits, so a refusal is shown in its words.
  const [resending, setResending] = useState<string | null>(null);
  async function onResend(a: GuestAccount) {
    setResending(a.id);
    try {
      await api.post(`/guest-accounts/${a.id}/resend-verification`);
      toast.success(`Verification email queued for ${a.username}.`, "It is sent as soon as the mail server accepts it.");
    } catch (e: any) {
      const wait = e instanceof ApiError ? Number(e.body?.retry_after_seconds) : NaN;
      if (e instanceof ApiError && e.code === "resend_wait" && wait > 0) {
        toast.error("Too soon to send another email.", `Try again in ${wait} second${wait === 1 ? "" : "s"}.`);
      } else if (e instanceof ApiError && e.code === "resend_limit") {
        toast.error("The daily limit of verification emails is reached.", "The client can try again tomorrow.");
      } else if (e instanceof ApiError && e.code === "resend_unavailable") {
        toast.error("Verification emails cannot be resent from here yet.", e.body?.message ?? e.message);
      } else {
        toast.error("The verification email was not queued.", e instanceof ApiError ? (e.body?.message ?? e.message) : (e?.message ?? undefined));
      }
    } finally { setResending(null); }
  }

  async function onDelete({ password }: { password: string }) {
    if (!deleting) return;
    setBusy(true); setActionErr(null);
    try {
      const r = await api.del<GuestAccountRemoveResp>(`/guest-accounts/${deleting.id}`, { password });
      if (r?.status === "removed") {
        toast.success(`The account "${deleting.username}" has been removed.`,
          "Its history is kept, and the address can be used to register again.");
      } else {
        toast.success(`The account "${deleting.username}" has been deleted.`);
      }
      setDeleting(null);
      setManaging(null);
      load();
    } catch (e) {
      setActionErr(e instanceof ApiError && e.code === "reauth_required" ? new Error("Your password was not accepted.") : e);
    }
    finally { setBusy(false); }
  }

  // Totals come from the server over every matching account, not the page on screen.
  const totals = useMemo(() => {
    const sm = resp?.summary;
    const list = rows ?? [];
    return {
      all: sm?.total ?? list.length,
      enabled: sm?.enabled ?? list.filter((a) => a.enabled).length,
      online: sm?.devices_online ?? list.reduce((n, a) => n + (a.active_devices ?? 0), 0),
    };
  }, [resp, rows]);
  const searching = q.trim() !== "" || status !== "";

  return (
    <PageShell width="wide">
      <PageHeader
        eyebrow="Clients"
        title="Client accounts"
        icon={<KeyRound />}
        description="Usernames and passwords clients can sign in with."
        help={
          <>
            <HelpSection title="What a client account is">
              <p>
                A username and password a client can sign in with, instead of {hospitality ? "a room number or a voucher" : "a voucher"}.
              </p>
            </HelpSection>
            <HelpSection title="What a client can take">
              <p>
                Decided by the eligibility rules on each internet package, not by anything stored on the account.
                The licence capacity is appliance-wide.
              </p>
            </HelpSection>
            <HelpSection title="Good to know">
              <HelpList items={[
                "Accounts staff create can only be used while username-and-password sign-in is offered on the Client Portal (the switch below, which is part of Sign-in methods). Self-service accounts follow the Email accounts switch on Sign-in methods instead.",
                "A password is shown once, when it is set. It cannot be looked up again; if it is lost, set a new one.",
                "Repeated failed sign-ins restrict the DEVICE that is guessing, not the account. See Client sign-in protection under Sign-in methods.",
                "Disable stops an account being used for now: its devices are disconnected and its package is paused. Enable resumes the package if it has not ended — its end date does not move while it is paused. Removing cannot be undone.",
                "Remove deletes an account that was never used. An account with packages or sign-ins is removed instead: its devices are disconnected, it can no longer sign in, its history is kept, and its email address or username can be used again.",
                "Manage shows an account's verification state, its packages and the devices on each package. For a self-service account staff can mark the email verified or start a new verification period.",
                "Accounts marked Self-service were opened by the client on the portal with their email address (Email accounts). Staff cannot set or show their password: the client resets it from the reset email. If the verification email did not arrive, use Resend verification email.",
              ]} />
            </HelpSection>
          </>
        }
        actions={
          mayWrite ? (
            <Button onClick={openNew}>
              <Plus /> Add account
            </Button>
          ) : undefined
        }
      />

      {roles !== null && !mayWrite && <ReadOnlyNotice />}
      <ErrorBanner err={err} />

      <div className="grid gap-4 sm:grid-cols-3">
        <StatCard label="Accounts" value={rows ? totals.all.toLocaleString() : "—"} icon={<Users />} tone="primary" />
        <StatCard icon={<LogIn />} label="Able to sign in" value={rows ? totals.enabled.toLocaleString() : "—"} />
        <StatCard icon={<Monitor />}
          label="Devices online"
          value={rows ? totals.online.toLocaleString() : "—"}
          href="/sessions"
        />
      </div>

      <Card>
        <CardBody className="flex flex-wrap items-center justify-between gap-4">
          <div className="min-w-0 space-y-1">
            <div className="flex flex-wrap items-center gap-2">
              <label htmlFor="portal-offer" className="text-emphasis">
                Offer username-and-password sign-in on the Client Portal
              </label>
              <Badge tone={portalOn ? "ok" : "neutral"} dot>{portalOn ? "Shown on the portal" : "Hidden"}</Badge>
            </div>
            <p className="text-caption text-muted-foreground">
              {portalOn
                ? "Clients see a Username & Password tab and can sign in with the accounts staff created here."
                : "The tab is hidden, so accounts staff created cannot be used even though they exist."}
              {" "}Self-service accounts follow the Email accounts switch on Sign-in methods.
              {roles !== null && !mayOfferOnPortal && " Your role cannot change which sign-in methods are offered."}
            </p>
          </div>
          <Switch
            id="portal-offer"
            checked={portalOn}
            onCheckedChange={onTogglePortal}
            disabled={!mayOfferOnPortal}
            label="Offer username-and-password sign-in"
          />
        </CardBody>
      </Card>

      <PasswordFormatCard canEdit={mayEditFormat} />

      <OneTimeReveal
        open={reveal !== null}
        title={`Password for ${reveal?.username ?? ""}`}
        description="Write it down or hand it over now. It is shown this once and cannot be looked up again."
        value={reveal?.password ?? ""}
        valueLabel="Password"
        concealable
        onAcknowledge={() => setReveal(null)}
      >
        <p className="text-caption text-muted-foreground">
          If this is lost, there is no way to recover it — you would set a new password instead.
        </p>
      </OneTimeReveal>

      <Card className="overflow-hidden">
        <CardBody className="border-b border-border py-3">
          <Toolbar>
            <SearchInput
              value={q}
              onChange={setQ}
              delay={300}
              placeholder="Search username or name…"
              label="Search client accounts"
            />
            <Select
              aria-label="Filter by status"
              value={status}
              onChange={(e) => setStatus(e.target.value)}
              className="w-44"
            >
              {STATUS_FILTERS.map((f) => <option key={f.value || "all"} value={f.value}>{f.label}</option>)}
            </Select>
            <Select
              aria-label="Accounts per page"
              value={String(pageSize)}
              onChange={(e) => setPageSize(Number(e.target.value))}
              className="w-32"
            >
              {PAGE_SIZES.map((n) => <option key={n} value={n}>{n} per page</option>)}
            </Select>
          </Toolbar>
          <ResultsCount count={rows === null ? null : filtered.length} noun="client account" />
        </CardBody>
        {loadFailed ? (
          <EmptyState
            title="Could not load client accounts"
            hint="The appliance did not answer. Nothing has been changed."
            action={<Button onClick={() => void load()}>Try again</Button>}
          />
        ) : rows === null ? (
          <SkeletonRows rows={5} cols={6} />
        ) : filtered.length === 0 ? (
          <EmptyState
            icon={<KeyRound />}
            title={searching ? "No account matches that search" : "No client accounts"}
            hint={searching ? undefined : mayWrite ? "Create one to let a client sign in with a username and password." : undefined}
            action={
              searching ? <Button variant="secondary" onClick={() => { setQ(""); setStatus(""); }}>Clear search</Button>
                : mayWrite ? <Button onClick={openNew}><Plus /> Add the first account</Button> : undefined
            }
          />
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>Account</TH>
                <TH>Devices</TH>
                <TH>Status</TH>
                <TH className="hidden md:table-cell">Valid until</TH>
                <TH className="hidden lg:table-cell">Last sign-in</TH>
                <TH className="hidden text-end lg:table-cell">Sign-ins</TH>
                {mayWrite && <TH><span className="sr-only">Actions</span></TH>}
              </TR>
            </THead>
            <TBody>
              {filtered.map((a) => {
                const cap = a.max_devices;
                const atCap = cap ? (a.active_devices ?? 0) >= cap : false;
                const verification = verificationStatus(a);
                return (
                  <TR key={a.id}>
                    <TD>
                      <div className="flex flex-wrap items-center gap-1.5">
                        <span className="font-mono text-sm font-medium">{a.username}</span>
                        {a.self_registered && <Badge tone="info">Self-service</Badge>}
                        {isRemovedAccount(a) && <Badge tone="neutral">Removed</Badge>}
                      </div>
                      {a.display_name && (
                        <div className="text-caption text-muted-foreground">{a.display_name}</div>
                      )}
                    </TD>
                    <TD className={atCap ? "text-warning-subtle-foreground" : "text-muted-foreground"}>
                      <span className="tabular">{a.active_devices ?? 0}</span>
                      {cap ? <span className="tabular"> / {cap}</span> : ""}
                      {atCap && <div className="text-caption">At the limit</div>}
                    </TD>
                    <TD>
                      <Badge tone={a.enabled ? "ok" : "err"} dot>
                        {a.enabled ? "Can sign in" : "Disabled"}
                      </Badge>
                      {verification && (
                        <div className="mt-1">
                          <Badge tone={verification.tone}>{verification.label}</Badge>
                        </div>
                      )}
                    </TD>
                    <TD className="hidden text-sm text-muted-foreground md:table-cell">
                      {a.valid_until ? formatRelative(a.valid_until) : "No end date"}
                    </TD>
                    <TD className="hidden text-sm text-muted-foreground lg:table-cell">
                      {a.last_login_at ? formatRelative(a.last_login_at) : "Never"}
                    </TD>
                    <TD className="hidden text-end tabular text-muted-foreground lg:table-cell">{a.login_count}</TD>
                    {mayWrite && (
                      <TD className="text-end">
                        <div className="flex flex-wrap justify-end gap-1">
                          <Button size="sm" variant="ghost" aria-label={`Manage ${a.username}`} onClick={() => setManaging(a)}>
                            <Settings2 /> Manage
                          </Button>
                          {!isRemovedAccount(a) && (<>
                          <Button size="sm" variant="ghost" aria-label={`Edit ${a.username}`} onClick={() => { setFormErr(null); setEditing(a); setPwFor(null); setShowNew(false); }}>
                            <Pencil /> Edit
                          </Button>
                          {/* A self-service account's password is the client's, recovered only by reset email;
                              staff cannot set or show it. */}
                          {!a.self_registered && (
                            <Button size="sm" variant="ghost" aria-label={`Password for ${a.username}`} onClick={() => { setFormErr(null); setPwFor(a); setEditing(null); setShowNew(false); }}>
                              <KeyRound /> Password
                            </Button>
                          )}
                          {a.self_registered && !a.email_verified && (
                            <Button size="sm" variant="ghost" disabled={resending === a.id} onClick={() => void onResend(a)}>
                              <Send /> {resending === a.id ? "Sending…" : "Resend verification email"}
                            </Button>
                          )}
                          <Button size="sm" variant="ghost" aria-label={`${a.enabled ? "Disable" : "Enable"} ${a.username}`}
                            onClick={() => (a.enabled ? (setActionErr(null), setDisabling(a)) : void onToggle(a))}>
                            {a.enabled ? "Disable" : "Enable"}
                          </Button>
                          {(a.active_devices ?? 0) > 0 && (
                            <Button size="sm" variant="ghost" aria-label={`Disconnect ${a.username}`} onClick={() => { setActionErr(null); setDisconnecting(a); }}>
                              <Power /> Disconnect
                            </Button>
                          )}
                          <Button size="sm" variant="ghost" className="text-destructive hover:text-destructive" aria-label={`Remove ${a.username}`} onClick={() => { setActionErr(null); setRemoval(null); setDeleting(a); }}>
                            Remove
                          </Button>
                          </>)}
                        </div>
                      </TD>
                    )}
                  </TR>
                );
              })}
            </TBody>
          </Table>
        )}
        {!loadFailed && filtered.length > 0 && (
          <CardBody className="border-t border-border py-3">
            <Pagination
              offset={offset}
              limit={pageSize}
              shown={filtered.length}
              total={resp?.summary?.total ?? null}
              onChange={setOffset}
            />
          </CardBody>
        )}
      </Card>

      <Dialog open={showNew} onOpenChange={(v) => !v && !busy && setShowNew(false)}>
        <DialogContent size="lg">
          <DialogHeader>
            <DialogTitle>Add a client account</DialogTitle>
            <DialogDescription>
              The client signs in with this username and password. The password is shown once, after you save.
            </DialogDescription>
          </DialogHeader>
          <DialogBody>
            <ErrorBanner err={formErr} />
            <AccountForm onSubmit={onCreate} busy={busy} withPassword onCancel={() => setShowNew(false)} />
          </DialogBody>
        </DialogContent>
      </Dialog>

      <Dialog open={editing !== null} onOpenChange={(v) => !v && !busy && setEditing(null)}>
        <DialogContent size="lg">
          <DialogHeader>
            <DialogTitle>Edit {editing?.username}</DialogTitle>
            <DialogDescription>
              Changing the username does not disconnect anyone who is already online.
            </DialogDescription>
          </DialogHeader>
          <DialogBody>
            <ErrorBanner err={formErr} />
            {editing && (
              <AccountForm account={editing} onSubmit={onSaveEdit} busy={busy} onCancel={() => setEditing(null)} />
            )}
          </DialogBody>
        </DialogContent>
      </Dialog>

      <Dialog open={pwFor !== null} onOpenChange={(v) => !v && !busy && setPwFor(null)}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>Set a new password for {pwFor?.username}</DialogTitle>
            <DialogDescription>
              The new password is shown once, after you save. The old one stops working immediately.
            </DialogDescription>
          </DialogHeader>
          <DialogBody>
            <ErrorBanner err={formErr} />
            <PasswordForm onSubmit={onSavePassword} busy={busy} onCancel={() => setPwFor(null)} />
          </DialogBody>
        </DialogContent>
      </Dialog>

      <ConfirmDialog
        open={disconnecting !== null}
        onOpenChange={(v) => !v && setDisconnecting(null)}
        title="Disconnect this account's devices?"
        description={
          disconnecting
            ? `All ${disconnecting.active_devices ?? 0} device${(disconnecting.active_devices ?? 0) === 1 ? "" : "s"} signed in as "${disconnecting.username}" will lose internet access immediately. The account still works, so they can sign in again.`
            : undefined
        }
        confirmLabel="Disconnect devices"
        confirmVariant="danger"
        busy={busy}
        error={actionErr}
        onConfirm={onDisconnect}
      />

      <ConfirmDialog
        open={disabling !== null}
        onOpenChange={(v) => !v && setDisabling(null)}
        title="Disable this client account?"
        description={disabling ? `"${disabling.username}" will not be able to sign in until it is enabled again.` : undefined}
        consequences={disabling ? [
          `${disabling.active_devices ?? 0} device${(disabling.active_devices ?? 0) === 1 ? " that is" : "s that are"} online now will be disconnected.`,
          "Its package is paused. Enable resumes it; the time it was bought for keeps running while it is paused.",
        ] : undefined}
        confirmLabel="Disable account"
        confirmVariant="danger"
        busy={busy}
        error={actionErr}
        onConfirm={() => (disabling ? onToggle(disabling) : undefined)}
      />

      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(v) => !v && setDeleting(null)}
        title={removal === "delete" ? "Delete this client account?" : "Remove this client account?"}
        description={
          deleting
            ? removal === "delete"
              ? `"${deleting.username}" was never used and will be deleted.`
              : removal === "remove"
                ? `"${deleting.username}" will be removed: its devices are disconnected and it can no longer sign in.`
                : `"${deleting.username}" will be deleted if it was never used. If it has packages or sign-ins it is removed instead: its devices are disconnected and it can no longer sign in.`
            : undefined
        }
        consequences={
          deleting
            ? [
                ...(removal !== "delete"
                  ? ["Its packages, usage and sign-in history are kept, and its devices' places on those packages are freed. The email address or username is free to use again: the client registers once more with “Create an account” — signing in with it no longer works."]
                  : []),
                "This cannot be undone.",
                "If you only want to stop it being used for now, Disable it instead — that can be reversed.",
              ]
            : undefined
        }
        confirmLabel={removal === "delete" ? "Delete account" : "Remove account"}
        confirmVariant="danger"
        requirePassword
        busy={busy}
        error={actionErr}
        onConfirm={onDelete}
      />

      <ManageAccountDialog
        account={managing}
        mayWrite={mayWrite}
        mayReadDevices={mayReadDevices}
        mayActOnDevices={mayActOnDevices}
        mayVouch={mayVouch}
        onClose={() => setManaging(null)}
        onChanged={() => void load()}
        onRemove={(acc, how) => { setActionErr(null); setRemoval(how); setDeleting(acc); }}
      />
    </PageShell>
  );
}

function AccountForm({ account, onSubmit, busy, withPassword, onCancel }: {
  account?: GuestAccount;
  onSubmit: (e: React.FormEvent<HTMLFormElement>) => void; busy: boolean; withPassword?: boolean; onCancel?: () => void;
}) {
  const [generate, setGenerate] = useState(false);
  const [pw, setPw] = useState("");
  const [showPw, setShowPw] = useState(false);
  const dt = (s?: string | null) => (s ? new Date(s).toISOString().slice(0, 16) : "");

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Username" required hint="What the client types. Short is fine — even a single character.">
          <Input name="username" required minLength={1} maxLength={64} defaultValue={account?.username} placeholder="alex.morgan" />
        </Field>
        <Field label="Name" hint="For your own reference; the client never sees it.">
          <Input name="display_name" maxLength={ACCOUNT_NAME_MAX} defaultValue={account?.display_name ?? ""} placeholder="Visitor pass" />
        </Field>
      </div>

      {withPassword && (
        <div className="rounded-md border border-border bg-surface/50 p-3.5">
          <Field
            label="Password"
            required={!generate}
            error={!generate && weakPassword(pw) ? "Short passwords are easy to guess. You can still save it." : undefined}
          >
            <div className="flex gap-1.5">
              <Input
                name="password"
                type={showPw ? "text" : "password"}
                autoComplete="new-password"
                value={pw}
                onChange={(e) => setPw(e.target.value)}
                disabled={generate}
                required={!generate}
                minLength={1}
                maxLength={128}
                placeholder={generate ? "Generated when you save" : ""}
              />
              <Button
                type="button"
                size="icon"
                variant="ghost"
                aria-label={showPw ? "Hide the password" : "Show the password"}
                onClick={() => setShowPw((s) => !s)}
              >
                {showPw ? <EyeOff /> : <Eye />}
              </Button>
            </div>
          </Field>
          <label className="mt-2 flex items-center gap-2 text-xs text-muted-foreground">
            <input
              type="checkbox"
              name="generate"
              checked={generate}
              onChange={(e) => setGenerate(e.target.checked)}
              className="size-3.5 accent-[hsl(var(--primary))]"
            />
            Generate a strong password instead
          </label>
        </div>
      )}

      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Valid from" hint="Leave empty to work immediately.">
          <Input name="valid_from" type="datetime-local" defaultValue={dt(account?.valid_from)} />
        </Field>
        <Field label="Valid until" hint="Leave empty for no end date. Must be later than Valid from.">
          <Input name="valid_until" type="datetime-local" defaultValue={dt(account?.valid_until)} />
        </Field>
      </div>

      <Field label="Notes">
        <Input name="notes" maxLength={ACCOUNT_NOTES_MAX} defaultValue={account?.notes ?? ""} placeholder="Optional" />
      </Field>

      <p className="text-caption text-muted-foreground">
        What this client can take is decided by the eligibility rules on each internet package.
      </p>
      <div className="flex justify-end gap-2 border-t border-border pt-4">
        {onCancel && <Button type="button" variant="ghost" onClick={onCancel}>Cancel</Button>}
        <Button type="submit" disabled={busy}>
          {busy ? "Saving…" : account ? "Save changes" : "Create account"}
        </Button>
      </div>
    </form>
  );
}

function PasswordForm({ onSubmit, busy, onCancel }: { onSubmit: (e: React.FormEvent<HTMLFormElement>) => void; busy: boolean; onCancel: () => void }) {
  const [generate, setGenerate] = useState(false);
  const [pw, setPw] = useState("");
  const [showPw, setShowPw] = useState(false);
  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <Field
        label="New password"
        required={!generate}
        error={!generate && weakPassword(pw) ? "Short passwords are easy to guess. You can still save it." : undefined}
      >
        <div className="flex gap-1.5">
          <Input
            name="password"
            type={showPw ? "text" : "password"}
            autoComplete="new-password"
            value={pw}
            onChange={(e) => setPw(e.target.value)}
            disabled={generate}
            required={!generate}
            minLength={1}
            maxLength={128}
            placeholder={generate ? "Generated when you save" : ""}
          />
          <Button
            type="button"
            size="icon"
            variant="ghost"
            aria-label={showPw ? "Hide the password" : "Show the password"}
            onClick={() => setShowPw((s) => !s)}
          >
            {showPw ? <EyeOff /> : <Eye />}
          </Button>
        </div>
      </Field>
      <label className="flex items-center gap-2 text-xs text-muted-foreground">
        <input
          type="checkbox"
          name="generate"
          checked={generate}
          onChange={(e) => setGenerate(e.target.checked)}
          className="size-3.5 accent-[hsl(var(--primary))]"
        />
        Generate a strong password instead
      </label>

      <label className="flex items-start gap-2 rounded-md border border-border bg-surface/50 px-3.5 py-2.5 text-sm">
        <input type="checkbox" name="disconnect_sessions" className="mt-0.5 size-3.5 accent-[hsl(var(--primary))]" />
        <span>
          Disconnect this account&rsquo;s devices now
          <span className="block text-xs text-muted-foreground">
            Ends the sessions that are online now, so they have to sign in again with the new password. What they
            bought is untouched &mdash; the same package and the same remaining allowance carry on. Without this,
            devices already online stay connected on the old password until their access ends.
          </span>
        </span>
      </label>

      <div className="flex justify-end gap-2 border-t border-border pt-4">
        <Button type="button" variant="ghost" onClick={onCancel}>Cancel</Button>
        <Button type="submit" disabled={busy}>{busy ? "Saving…" : "Set password"}</Button>
      </div>
    </form>
  );
}
