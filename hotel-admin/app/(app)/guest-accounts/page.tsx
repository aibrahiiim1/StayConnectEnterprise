"use client";

import { InfoHint } from "@/components/ui/quiet";
import { Explain } from "@/components/ui/tooltip";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  api, ApiError, GuestAccount, GuestAccountCreateResp,
  GuestAccountPasswordResp, type GuestAccountsPage as AccountsPageResp,
  type GuestAccountManage, type GuestAccountRemoveResp,
} from "@/lib/api";
import { ManageAccountDialog } from "@/components/guest-accounts/manage-account";
import { canRead, canWrite } from "@/lib/roles";
import { PageShell, PageHeader, StatCard, StatGrid } from "@/components/ui/page";
import { FilterBar } from "@/components/ui/toolbar";
import { RowActions } from "@/components/ui/row-actions";
import { HelpList, HelpSection } from "@/components/help";
import { Card, CardBody, CardFooter } from "@/components/ui/card";
import { Table, TBody, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Field, QuietHints } from "@/components/ui/input";
import { Badge, StatusBadge } from "@/components/ui/badge";
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
import { LiveStatus, OneTimeReveal, ReadOnlyNotice, refreshingClass } from "@/components/ui/patterns";
import { usePoll } from "@/lib/use-poll";
import { cn } from "@/lib/utils";
import { useToast } from "@/components/ui/toast";
import { AlertTriangle, Plus, KeyRound, Eye, EyeOff, Pencil, Power, RefreshCw, Users, Send, LogIn, Monitor, Settings2 } from "lucide-react";
import { formatRelative } from "@/lib/utils";
import { REASON_HINT, REASON_MAX_CHARS, reasonOk } from "@/lib/reason";
import { isRemovedAccount, verificationStatus } from "@/lib/account-verification";
import { ACCOUNT_NAME_MAX, ACCOUNT_NOTES_MAX, accountEditBody, validityWindow } from "@/lib/account-form";
import { useOperatorRoles } from "@/lib/whoami-context";
import { moduleLicensed, useCapabilities } from "@/lib/capabilities";

const PAGE_SIZES = [25, 50, 100, 200];
// No "Locked out" filter or count: protection is per device, and guest_access_accounts.locked_until is never set, so it
// would always read 0.
// "Can sign in" first: the accounts in use are what the desk looks for; "All accounts" stays one tap away.
const STATUS_FILTERS: { value: string; label: string }[] = [
  { value: "enabled", label: "Can sign in" },
  { value: "disabled", label: "Disabled" },
  { value: "removed", label: "Removed" },
  { value: "", label: "All accounts" },
];

type GuestAccountToggleResp = GuestAccount & {
  packages_suspended?: number; packages_resumed?: number;
  disconnected_sessions?: number; disconnect_failures?: number;
};

function weakPassword(pw: string): boolean {
  return pw.length > 0 && pw.length < 8;
}

const browserTimeZone = (): string => {
  try { return Intl.DateTimeFormat().resolvedOptions().timeZone || "this computer's time zone"; }
  catch { return "this computer's time zone"; }
};

function localInput(s?: string | null): string {
  if (!s) return "";
  const d = new Date(s);
  if (Number.isNaN(d.getTime())) return "";
  const p = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())}T${p(d.getHours())}:${p(d.getMinutes())}`;
}

function FormError({ err }: { err: string | null }) {
  const ref = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (err) ref.current?.scrollIntoView?.({ block: "nearest", behavior: "smooth" });
  }, [err]);
  return (
    <div ref={ref}>
      {err && (
        <div className="animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none">
          <ErrorBanner err={err} className="mb-0" />
        </div>
      )}
    </div>
  );
}

function DiscardPrompt({ onKeep, onDiscard }: { onKeep: () => void; onDiscard: () => void }) {
  const keepRef = useRef<HTMLButtonElement>(null);
  useEffect(() => { keepRef.current?.focus(); }, []);
  return (
    <>
      <p role="status" className="me-auto text-caption text-foreground">Discard the changes you made?</p>
      <Button ref={keepRef} type="button" variant="secondary" onClick={onKeep}>Keep editing</Button>
      <Button type="button" variant="danger" onClick={onDiscard}>Discard changes</Button>
    </>
  );
}

// The one-time password reveal is a modal so the only copy cannot scroll off-screen; the password is never
// retrievable afterwards.

export default function GuestAccountsPage() {
  const toast = useToast();
  const hospitality = moduleLicensed(useCapabilities(), "hospitality");
  const [rows, setRows] = useState<GuestAccount[] | null>(null);
  const [portalOn, setPortalOn] = useState<boolean | null>(null);
  const [portalLoadErr, setPortalLoadErr] = useState<string | null>(null);
  const [portalBusy, setPortalBusy] = useState(false);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [refreshFailed, setRefreshFailed] = useState(false);
  const [formDirty, setFormDirty] = useState(false);
  const [askDiscard, setAskDiscard] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  // Form errors show inside the dialog; one on the page behind a modal goes unread.
  const [formErr, setFormErr] = useState<string | null>(null);
  const [showNew, setShowNew] = useState(false);
  const [busy, setBusy] = useState(false);
  const [q, setQ] = useState("");
  const [status, setStatus] = useState("enabled");
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [portalErr, setPortalErr] = useState<string | null>(null);
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
  const hasRows = useRef(false);
  const load = useCallback(async () => {
    const mine = ++latest.current;
    setErr(null);
    setLoadFailed(false);
    setRefreshing(true);
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
      if (mine !== latest.current) return;
      setResp(ga);
      setRows(Array.isArray(ga?.data) ? ga.data : []);
      setUpdatedAt(Date.now());
      setRefreshFailed(false);
      hasRows.current = true;
    } catch (e: any) {
      if (mine !== latest.current) return;
      if (hasRows.current) setRefreshFailed(true);
      else {
        setErr(e?.message ?? "The appliance did not answer.");
        setLoadFailed(true);
      }
    } finally {
      if (mine === latest.current) setRefreshing(false);
    }
  }, [q, status, offset, pageSize]);
  useEffect(() => { void load(); }, [load]);

  const loadPortal = useCallback(() => {
    setPortalLoadErr(null);
    api.get<{ enabled: boolean }>("/guest-accounts/portal")
      .then((pv) => setPortalOn(!!pv?.enabled))
      .catch((e: any) => { setPortalOn(null); setPortalLoadErr(e?.message ?? "The appliance did not answer."); });
  }, []);
  useEffect(() => { loadPortal(); }, [loadPortal]);

  const filtered = rows ?? [];

  // Its failure is shown inside Account settings, where the switch is (Codex P2 on PR #295).
  async function onTogglePortal() {
    if (portalOn === null || portalBusy) return;
    setPortalErr(null);
    setPortalBusy(true);
    try {
      await api.post("/guest-accounts/portal", { enabled: !portalOn });
      setPortalOn(!portalOn);
      toast.success(`Username & Password sign-in ${!portalOn ? "shown on" : "hidden from"} the captive portal.`);
    } catch (e: any) { setPortalErr(e?.message ?? "The change was not saved."); }
    finally { setPortalBusy(false); }
  }

  function resetForm() { setFormErr(null); setFormDirty(false); setAskDiscard(false); }
  function openNew() { resetForm(); setShowNew(true); setEditing(null); setPwFor(null); }
  function closeForms() { setShowNew(false); setEditing(null); resetForm(); }
  function requestCloseForm() {
    if (busy) return;
    if (formDirty) { setAskDiscard(true); return; }
    closeForms();
  }

  async function onCreate(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true); setFormErr(null);
    const form = new FormData(e.currentTarget);
    const generate = form.get("generate") === "on";
    const password = (form.get("password") as string) || "";
    if (!((form.get("username") as string) || "").trim()) { setFormErr("Enter a username."); setBusy(false); return; }
    if (!generate && !password) { setFormErr("Enter a password, or tick Generate a strong password instead."); setBusy(false); return; }
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
      closeForms();
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
      if (editing.self_registered !== true && !((form.get("username") as string) || "").trim()) { setFormErr("Enter a username."); return; }
      const win = validityWindow(form);
      if (win.error) { setFormErr(win.error); return; }
      await api.patch(`/guest-accounts/${editing.id}`, accountEditBody(form, editing.self_registered === true));
      closeForms();
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
    const reason = ((form.get("reason") as string) || "").trim();
    if (!generate && !password) {
      setFormErr("Enter a new password, or tick Generate a strong password instead.");
      setBusy(false);
      return;
    }
    if (!reasonOk(reason)) {
      setFormErr("Say why the password is being replaced (4 to 500 characters). Do not write the password in it.");
      setBusy(false);
      return;
    }
    try {
      const resp = await api.post<GuestAccountPasswordResp>(`/guest-accounts/${pwFor.id}/set-password`, {
        password: generate ? "" : password, generate, disconnect_sessions: disconnect, reason,
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
    if (busy) return;
    setActionErr(null);
    setBusy(true);
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
      else toast.error(`"${a.username}" could not be enabled.`, e instanceof ApiError ? (e.body?.message ?? e.message) : e?.message);
    }
    finally { setBusy(false); }
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
  const searching = q.trim() !== "" || (status !== "" && status !== "enabled");
  const onlyUsable = !searching && status === "enabled";
  usePoll(() => load(), 60_000, { enabled: !loadFailed && !showNew && editing === null && pwFor === null });

  return (
    <QuietHints>
    <PageShell width="wide">
      <PageHeader
        title="Client accounts"
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
                "Accounts staff create can only be used while username-and-password sign-in is offered on the Client Portal (the switch below, which is part of Sign-in methods). That switch is for the whole site: a client network can still switch the method off for itself, and Sign-in methods shows the result for each network. Self-service accounts follow the Email accounts switch instead.",
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
        meta={rows !== null ? (
          <LiveStatus updatedAt={updatedAt} refreshing={refreshing} error={refreshFailed} onRefresh={() => void load()} intervalSeconds={60} />
        ) : undefined}
        actions={
          <>
            {roles !== null && (
              <Button variant="secondary" onClick={() => setSettingsOpen(true)}>
                <Settings2 /> Account settings
              </Button>
            )}
            {mayWrite && (
              <Button onClick={openNew}>
                <Plus /> Add account
              </Button>
            )}
          </>
        }
      />

      {roles !== null && !mayWrite && <ReadOnlyNotice />}

      <StatGrid cols={3}>
        <StatCard label={status === "" && q.trim() === "" ? "Accounts" : "Matching accounts"} value={rows ? totals.all.toLocaleString() : "—"} icon={<Users />} />
        <StatCard icon={<LogIn />} label="Able to sign in" value={rows ? totals.enabled.toLocaleString() : "—"} />
        <StatCard icon={<Monitor />}
          label="Devices online"
          value={rows ? totals.online.toLocaleString() : "—"}
          href="/sessions"
        />
      </StatGrid>

      <Dialog open={settingsOpen} onOpenChange={(v) => { setSettingsOpen(v); if (!v) setPortalErr(null); }}>
        <DialogContent size="lg">
          <DialogHeader>
            <DialogTitle>Account settings</DialogTitle>
            <DialogDescription className="sr-only">Whether clients see username-and-password sign-in, and the format of generated passwords.</DialogDescription>
          </DialogHeader>
          <DialogBody className="space-y-5">
            <ErrorBanner err={portalErr} />
            {portalOn === null && portalLoadErr && (
              <div className="flex flex-wrap items-center gap-2">
                <ErrorBanner err={`Whether this is shown on the Client Portal could not be read. ${portalLoadErr}`} className="mb-0 flex-1" />
                <Button size="sm" variant="secondary" onClick={loadPortal}><RefreshCw /> Try again</Button>
              </div>
            )}
            <div className="flex flex-wrap items-center justify-between gap-4">
              <div className="flex min-w-0 flex-wrap items-center gap-2">
                <label htmlFor="portal-offer" className="text-emphasis">Show on the Client Portal</label>
                {portalOn !== null && <StatusBadge tone={portalOn ? "ok" : "default"}>{portalOn ? "Shown" : "Hidden"}</StatusBadge>}
                <InfoHint>
                  {portalOn
                    ? "Clients can sign in with the accounts staff created here, on every network that has not switched this method off."
                    : "The tab is hidden, so accounts staff created cannot be used even though they exist."}
                  {roles !== null && !mayOfferOnPortal && " Your role cannot change which sign-in methods are offered."}
                </InfoHint>
              </div>
              <Switch
                id="portal-offer"
                checked={portalOn === true}
                onCheckedChange={onTogglePortal}
                disabled={!mayOfferOnPortal || portalOn === null || portalBusy}
                label="Offer username-and-password sign-in"
              />
            </div>
            <PasswordFormatCard canEdit={mayEditFormat} />
          </DialogBody>
        </DialogContent>
      </Dialog>

      <OneTimeReveal
        open={reveal !== null}
        title={`Password for ${reveal?.username ?? ""}`}
        description="Write it down or hand it over now. It is shown this once and cannot be looked up again."
        value={reveal?.password ?? ""}
        valueLabel="Password"
        concealable
        onAcknowledge={() => setReveal(null)}
      >
        <p className="flex items-center gap-1 text-caption text-muted-foreground">
          It cannot be recovered later.
          <InfoHint>If this is lost, there is no way to recover it — you would set a new password instead.</InfoHint>
        </p>
      </OneTimeReveal>

      <Card className="overflow-hidden">
        <FilterBar
          inset
          search={
            <SearchInput
              value={q}
              onChange={setQ}
              delay={300}
              placeholder="Search username or name…"
              label="Search client accounts"
            />
          }
          actions={<ResultsCount count={rows === null ? null : resp?.summary?.total ?? filtered.length} noun="client account" />}
        >
          <Select
            aria-label="Filter by status"
            value={status}
            onChange={(e) => setStatus(e.target.value)}
            className="w-full sm:w-44"
          >
            {STATUS_FILTERS.map((f) => <option key={f.value || "all"} value={f.value}>{f.label}</option>)}
          </Select>
          <Select
            aria-label="Accounts per page"
            value={String(pageSize)}
            onChange={(e) => setPageSize(Number(e.target.value))}
            className="w-full sm:w-32"
          >
            {PAGE_SIZES.map((n) => <option key={n} value={n}>{n} per page</option>)}
          </Select>
        </FilterBar>
        {loadFailed ? (
          <EmptyState
            icon={<AlertTriangle />}
            title="The client accounts could not be loaded"
            hint={err ?? undefined}
            action={<Button variant="secondary" disabled={refreshing} onClick={() => void load()}><RefreshCw /> Try again</Button>}
          />
        ) : rows === null ? (
          <SkeletonRows rows={5} cols={6} />
        ) : filtered.length === 0 ? (
          <EmptyState
            size="sm"
            icon={<KeyRound />}
            title={searching ? "No account matches that search" : onlyUsable ? "No account can sign in" : "No client accounts"}
            hint={searching ? undefined
              : onlyUsable ? "Disabled and removed accounts are listed under All accounts."
                : mayWrite ? "Create one to let a client sign in with a username and password." : undefined}
            action={
              searching ? <Button variant="secondary" onClick={() => { setQ(""); setStatus(""); }}>Clear search</Button>
                : onlyUsable ? (
                  <div className="flex flex-wrap justify-center gap-2">
                    <Button variant="secondary" onClick={() => setStatus("")}>Show all accounts</Button>
                    {mayWrite && <Button onClick={openNew}><Plus /> Add account</Button>}
                  </div>
                )
                : mayWrite ? <Button onClick={openNew}><Plus /> Add the first account</Button> : undefined
            }
          />
        ) : (
          <div className={cn(refreshing && refreshingClass)}>
          <Table label="Client accounts">
            <THead>
              <TR>
                <TH>Account</TH>
                <TH numeric>Devices</TH>
                <TH>Status</TH>
                <TH className="hidden md:table-cell">Valid until</TH>
                <TH className="hidden lg:table-cell">Last sign-in</TH>
                {roles !== null && <TH><span className="sr-only">Actions</span></TH>}
              </TR>
            </THead>
            <TBody>
              {filtered.map((a) => {
                const cap = a.max_devices;
                const atCap = cap ? (a.active_devices ?? 0) >= cap : false;
                const verification = verificationStatus(a);
                return (
                  <TR key={a.id} onClick={() => setManaging(a)}>
                    <TD className="max-w-[15rem]">
                      <div className="flex flex-wrap items-center gap-1.5">
                        <span className="min-w-0 max-w-full truncate font-mono text-sm font-medium" title={a.username}>{a.username}</span>
                        {a.self_registered && <Badge tone="info">Self-service</Badge>}
                        {isRemovedAccount(a) && <Badge tone="neutral">Removed</Badge>}
                      </div>
                      {a.display_name && (
                        <div className="truncate text-caption text-muted-foreground" title={a.display_name}>{a.display_name}</div>
                      )}
                    </TD>
                    <TD numeric className={atCap ? "text-warning-subtle-foreground" : "text-muted-foreground"}>
                      {a.active_devices ?? 0}
                      {cap ? ` / ${cap}` : ""}
                      {atCap && <div className="text-caption">At the limit</div>}
                    </TD>
                    <TD nowrap>
                      <StatusBadge tone={a.enabled ? "ok" : "err"}>
                        {a.enabled ? "Can sign in" : "Disabled"}
                      </StatusBadge>
                      {verification && (
                        <div className="mt-1 max-w-[10rem]">
                          <Badge tone={verification.tone} title={verification.label}>
                            <span className="min-w-0 truncate">{verification.label}</span>
                          </Badge>
                        </div>
                      )}
                    </TD>
                    <TD nowrap muted className="hidden md:table-cell">
                      {a.valid_until ? formatRelative(a.valid_until) : "No end date"}
                    </TD>
                    <TD nowrap muted className="hidden lg:table-cell">
                      {a.last_login_at ? formatRelative(a.last_login_at) : "Never"}
                      {a.login_count > 0 && (
                        <div className="tabular text-caption">
                          {a.login_count.toLocaleString()} sign-in{a.login_count === 1 ? "" : "s"}
                        </div>
                      )}
                    </TD>
                    {roles !== null && (
                      <TD className="w-0">
                        <RowActions
                          subject={a.username}
                          primary={{ label: "Manage", onSelect: () => setManaging(a) }}
                          items={!mayWrite || isRemovedAccount(a) ? [] : [
                            { label: "Edit", icon: <Pencil />, onSelect: () => { resetForm(); setEditing(a); setPwFor(null); setShowNew(false); } },
                            { label: "Set password", icon: <KeyRound />, hidden: a.self_registered,
                              onSelect: () => { resetForm(); setPwFor(a); setEditing(null); setShowNew(false); } },
                            { label: resending === a.id ? "Sending…" : "Resend verification email", icon: <Send />,
                              hidden: !(a.self_registered && !a.email_verified), disabled: resending === a.id,
                              onSelect: () => void onResend(a) },
                            { label: "Disconnect devices", icon: <Power />, hidden: (a.active_devices ?? 0) === 0,
                              onSelect: () => { setActionErr(null); setDisconnecting(a); } },
                            { label: a.enabled ? "Disable" : "Enable",
                              onSelect: () => (a.enabled ? (setActionErr(null), setDisabling(a)) : void onToggle(a)) },
                            { label: "Remove…", danger: true, onSelect: () => { setActionErr(null); setRemoval(null); setDeleting(a); } },
                          ]}
                        />
                      </TD>
                    )}
                  </TR>
                );
              })}
            </TBody>
          </Table>
          </div>
        )}
        {!loadFailed && filtered.length > 0 && (
          <CardFooter>
            <Pagination
              offset={offset}
              limit={pageSize}
              shown={filtered.length}
              total={resp?.summary?.total ?? null}
              onChange={setOffset}
            />
          </CardFooter>
        )}
      </Card>

      <Dialog open={showNew} onOpenChange={(v) => !v && requestCloseForm()}>
        <DialogContent size="lg">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-1">Add a client account<Explain>The client signs in with this username and password. The password is shown once, after you save.</Explain></DialogTitle>
            <DialogDescription className="sr-only">The client signs in with this username and password. The password is shown once, after you save.</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <AccountForm onSubmit={onCreate} busy={busy} withPassword onCancel={closeForms} error={formErr}
              onEdited={() => setFormDirty(true)} asking={askDiscard} onKeep={() => setAskDiscard(false)} onDiscard={closeForms} />
          </DialogBody>
        </DialogContent>
      </Dialog>

      <Dialog open={editing !== null} onOpenChange={(v) => !v && requestCloseForm()}>
        <DialogContent size="lg">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-1">Edit {editing?.username}<Explain>Changing the username does not disconnect anyone who is already online.</Explain></DialogTitle>
            <DialogDescription className="sr-only">Changing the username does not disconnect anyone who is already online.</DialogDescription>
          </DialogHeader>
          <DialogBody>
            {editing && (
              <AccountForm account={editing} onSubmit={onSaveEdit} busy={busy} onCancel={closeForms} error={formErr}
                onEdited={() => setFormDirty(true)} asking={askDiscard} onKeep={() => setAskDiscard(false)} onDiscard={closeForms} />
            )}
          </DialogBody>
        </DialogContent>
      </Dialog>

      <Dialog open={pwFor !== null} onOpenChange={(v) => !v && !busy && setPwFor(null)}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-1">Set a new password for {pwFor?.username}<Explain>The new password is shown once, after you save. The old one stops working immediately.</Explain></DialogTitle>
            <DialogDescription className="sr-only">The new password is shown once, after you save. The old one stops working immediately.</DialogDescription>
          </DialogHeader>
          <DialogBody>
            <PasswordForm onSubmit={onSavePassword} busy={busy} onCancel={() => setPwFor(null)} error={formErr} />
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
        onOpenChange={(v) => !v && !busy && setDisabling(null)}
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
                "This is not a data-protection erasure and is not recorded in the erasure ledger. To erase a client’s personal data, use Guest data → Erase.",
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
    </QuietHints>
  );
}

function PasswordBox({ value, onChange, shown, onToggle, generate, ...aria }: {
  value: string; onChange: (v: string) => void; shown: boolean; onToggle: () => void; generate: boolean;
  id?: string; "aria-describedby"?: string; "aria-invalid"?: boolean;
}) {
  return (
    <div className="flex gap-1.5">
      <Input
        {...aria}
        name="password"
        type={shown ? "text" : "password"}
        autoComplete="new-password"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        disabled={generate}
        aria-required={!generate}
        maxLength={128}
        placeholder={generate ? "Generated when you save" : ""}
      />
      <Button
        type="button"
        size="icon"
        variant="ghost"
        aria-label={shown ? "Hide the password" : "Show the password"}
        onClick={onToggle}
      >
        {shown ? <EyeOff /> : <Eye />}
      </Button>
    </div>
  );
}

function AccountForm({ account, onSubmit, busy, withPassword, onCancel, error, onEdited, asking, onKeep, onDiscard }: {
  account?: GuestAccount;
  onSubmit: (e: React.FormEvent<HTMLFormElement>) => void; busy: boolean; withPassword?: boolean; onCancel?: () => void;
  error: string | null; onEdited: () => void; asking: boolean; onKeep: () => void; onDiscard: () => void;
}) {
  const [generate, setGenerate] = useState(false);
  const [pw, setPw] = useState("");
  const [showPw, setShowPw] = useState(false);
  const tz = browserTimeZone();

  return (
    <form onSubmit={onSubmit} noValidate onInput={onEdited} onChange={onEdited} className="space-y-4">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Username" required hint="What the client types. Short is fine — even a single character.">
          <Input name="username" aria-required maxLength={64} defaultValue={account?.username} placeholder="alex.morgan" />
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
            <PasswordBox
              value={pw}
              onChange={setPw}
              shown={showPw}
              onToggle={() => setShowPw((v) => !v)}
              generate={generate}
            />
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
        <Field label="Valid from" hint={`Leave empty to work immediately. In ${tz}.`}>
          <Input name="valid_from" type="datetime-local" defaultValue={localInput(account?.valid_from)} />
        </Field>
        <Field label="Valid until" hint={`Leave empty for no end date. Must be later than Valid from. In ${tz}.`}>
          <Input name="valid_until" type="datetime-local" defaultValue={localInput(account?.valid_until)} />
        </Field>
      </div>

      <Field label="Notes">
        <Input name="notes" maxLength={ACCOUNT_NOTES_MAX} defaultValue={account?.notes ?? ""} placeholder="Optional" />
      </Field>

      <p className="flex items-center gap-1 text-caption text-muted-foreground">
        Packages
        <InfoHint>What this client can take is decided by the eligibility rules on each internet package.</InfoHint>
      </p>
      <FormError err={error} />
      <div className="flex flex-wrap items-center justify-end gap-2 border-t border-border pt-4">
        {asking ? (
          <DiscardPrompt onKeep={onKeep} onDiscard={onDiscard} />
        ) : (
          <>
            {onCancel && <Button type="button" variant="ghost" onClick={onCancel}>Cancel</Button>}
            <Button type="submit" disabled={busy}>
              {busy ? "Saving…" : account ? "Save changes" : "Create account"}
            </Button>
          </>
        )}
      </div>
    </form>
  );
}

function PasswordForm({ onSubmit, busy, onCancel, error }: { onSubmit: (e: React.FormEvent<HTMLFormElement>) => void; busy: boolean; onCancel: () => void; error: string | null }) {
  const [generate, setGenerate] = useState(false);
  const [pw, setPw] = useState("");
  const [showPw, setShowPw] = useState(false);
  return (
    <form onSubmit={onSubmit} noValidate className="space-y-4">
      <Field
        label="New password"
        required={!generate}
        error={!generate && weakPassword(pw) ? "Short passwords are easy to guess. You can still save it." : undefined}
      >
        <PasswordBox
          value={pw}
          onChange={setPw}
          shown={showPw}
          onToggle={() => setShowPw((v) => !v)}
          generate={generate}
        />
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

      <Field label="Reason" required hint={REASON_HINT}>
        <Input name="reason" aria-required maxLength={REASON_MAX_CHARS} placeholder="For example: the client forgot it at the desk" />
      </Field>

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

      <FormError err={error} />
      <div className="flex justify-end gap-2 border-t border-border pt-4">
        <Button type="button" variant="ghost" onClick={onCancel}>Cancel</Button>
        <Button type="submit" disabled={busy}>{busy ? "Saving…" : "Set password"}</Button>
      </div>
    </form>
  );
}
