"use client";

// Submitted and accepted VALUES need View_Guest_SignIn_Credentials, enforced by edged; hiding a panel here is a
// courtesy, not the boundary.
// With no stay in the local mirror nothing would have been accepted, and the panel says so.

import { useCallback, useEffect, useMemo, useState } from "react";
import { KeyRound, ShieldAlert } from "lucide-react";
import {
  api, SignInAttempt, SignInAttemptDetail, SignInAttemptCredentials } from "@/lib/api";
import { canRead, canWrite } from "@/lib/roles";
import { ActiveRestrictions } from "@/components/guest-signin-restrictions";
import { NetworkPausesNotice } from "@/components/guest-signin-network-pause";
import { cn, formatDate, formatRelative } from "@/lib/utils";
import { PageShell, PageHeader, StatCard, StatGrid } from "@/components/ui/page";
import { Card, CardFooter } from "@/components/ui/card";
import { Table, TBody, THead, TR, TH, TD } from "@/components/ui/table";
import { StatusBadge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { HelpList, HelpSection } from "@/components/help";
import { SkeletonRows, DList, MonoId } from "@/components/ui/misc";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { DescriptionList, Pagination, SearchInput } from "@/components/ui/data";
import { FilterBar } from "@/components/ui/toolbar";
import { RowActions } from "@/components/ui/row-actions";
import { useServerPage, type PagedFields } from "@/lib/use-server-page";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { LiveStatus, NotAvailable, refreshingClass } from "@/components/ui/patterns";
import { useOperatorRoles } from "@/lib/whoami-context";

// The label comes from the server's Go map so a new result code never shows as a raw enum.
const RESULT_WORDS: Record<string, { tone: "ok" | "warn" | "err" | "info" | "neutral"; meaning: string }> = {
  VERIFIED: { tone: "ok", meaning: "The guest proved who they are and was offered access." },
  CREDENTIAL_MISMATCH: {
    tone: "warn",
    meaning:
      "The room is in the appliance's guest list and has an eligible stay, but the value entered matched none of the accepted ones.",
  },
  ROOM_NOT_IN_MIRROR: {
    tone: "warn",
    meaning: "No stay from any connected PMS carries that room number in the appliance's guest list.",
  },
  STAY_NOT_ELIGIBLE: {
    tone: "warn",
    meaning: "A stay for that room exists but is outside the state or time window that may sign in.",
  },
  AMBIGUOUS_ROOM_CANDIDATES: {
    tone: "warn",
    meaning: "More than one live stay matched. Choosing between them is not a decision the system makes.",
  },
  MIRROR_STALE_OR_MISSING_CHANGE: {
    tone: "err",
    meaning: "The appliance's guest list could not authorise anybody at that moment — this affected every guest, not just this one.",
  },
  RATE_LIMITED: { tone: "info", meaning: "Refused before any details were evaluated: too many recent attempts." },
  ROUTING_OR_INTERFACE_FAILURE: {
    tone: "err",
    meaning: "The request reached no PMS interface: the device was on no mapped client network, or the network maps to none.",
  },
  SERVICE_UNAVAILABLE: { tone: "err", meaning: "An internal failure. The details were never compared." },
  SPENT_REQUEST_ID: {
    tone: "err",
    meaning: "The portal page re-used a request id that had already been refused. This is a stale portal build, not a guest error.",
  },
  MALFORMED_SUBMISSION: { tone: "neutral", meaning: "The submission could not be read: a missing room, no value, or an unusable device." },
  VERIFIED_NO_ELIGIBLE_PACKAGE: {
    tone: "err",
    meaning: "The guest's details were RIGHT. The site had no package to offer that stay — this is a configuration problem, not theirs.",
  },
  LICENSE_REFUSED: {
    tone: "err",
    meaning:
      "The appliance's licence did not admit a new client at that moment (missing, expired or suspended, or room sign-in not included). Every sign-in method was refused the same way.",
  },
  LICENSE_CAPACITY_REACHED: {
    tone: "warn",
    meaning:
      "The guest's details were right, but the appliance was already at the number of clients online its licence allows. The same limit applies to every sign-in method.",
  },
};

const RESULT_FILTER_LABELS: Record<string, string> = {
  VERIFIED: "Connected",
  CREDENTIAL_MISMATCH: "Details did not match",
  ROOM_NOT_IN_MIRROR: "Room not in the guest list",
  STAY_NOT_ELIGIBLE: "Stay not eligible",
  AMBIGUOUS_ROOM_CANDIDATES: "More than one stay matched",
  MIRROR_STALE_OR_MISSING_CHANGE: "Guest list out of date",
  RATE_LIMITED: "Too many attempts",
  ROUTING_OR_INTERFACE_FAILURE: "Network not pointed at a PMS",
  SERVICE_UNAVAILABLE: "Internal failure",
  SPENT_REQUEST_ID: "Stale sign-in page",
  MALFORMED_SUBMISSION: "Unreadable submission",
  VERIFIED_NO_ELIGIBLE_PACKAGE: "Right details, no package to offer",
  LICENSE_REFUSED: "Licence refused new clients",
  LICENSE_CAPACITY_REACHED: "Licensed capacity full",
};

const RESULT_FILTERS = [
  { value: "", label: "Every result" },
  ...Object.keys(RESULT_WORDS).map((v) => ({
    value: v,
    label: RESULT_FILTER_LABELS[v] ?? v.replace(/_/g, " ").toLowerCase(),
  })),
];

const KIND_FILTERS = [
  { value: "", label: "Any credential type" },
  { value: "FULL_NAME", label: "Name" },
  { value: "RESERVATION_NUMBER_LIKE", label: "Reservation-number-like" },
  { value: "UNKNOWN", label: "Unknown" },
];

const RANGES = [
  { value: "24", label: "Last 24 hours" },
  { value: "72", label: "Last 3 days" },
  { value: "168", label: "Last 7 days" },
  { value: "720", label: "Last 30 days (everything retained)" },
];

function tone(result: string) {
  return RESULT_WORDS[result]?.tone ?? "neutral";
}

function mirrorAge(seconds?: number | null): string {
  if (seconds === null || seconds === undefined) return "—";
  if (seconds < 90) return `${seconds}s`;
  const mins = Math.floor(seconds / 60);
  if (mins < 90) return `${mins}m`;
  const hrs = Math.floor(mins / 60);
  if (hrs < 48) return `${hrs}h`;
  return `${Math.floor(hrs / 24)}d`;
}

type AttemptsResp = PagedFields & {
  data?: SignInAttempt[];
  summary?: { total?: number; failed?: number; mismatch?: number; systemic?: number };
};

export default function GuestSignInAttemptsPage() {
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [anchor, setAnchor] = useState(() => Date.now());
  const [openErr, setOpenErr] = useState<unknown>(null);

  const [room, setRoom] = useState("");
  const [result, setResult] = useState("");
  const [kind, setKind] = useState("");
  const [range, setRange] = useState("72");
  const [query, setQuery] = useState("");

  const [detail, setDetail] = useState<SignInAttemptDetail | null>(null);
  const [creds, setCreds] = useState<SignInAttemptCredentials | null>(null);
  const [credsErr, setCredsErr] = useState<unknown>(null);
  // Typed values are shown only after the operator confirms their password; it is cleared once sent and when the
  // panel closes.
  const [credsPw, setCredsPw] = useState("");
  const [revealing, setRevealing] = useState(false);

  // The credential permission fails closed while it loads; edged is the real boundary.
  const roles = useOperatorRoles();
  const maySeeCredentials = roles === null ? false : canRead("guest-signin-credentials", roles);
  // Three separate permissions, none implying another; edged enforces all of them.
  const maySeeRestrictions = roles === null ? false : canRead("guest-signin-restrictions", roles);
  const mayRelease = roles === null ? false : canWrite("guest-signin-restrictions", roles);

  const [tab, setTab] = useState<"attempts" | "restrictions">("attempts");

  // Paged and searched on the server (search in a header, never the URL).
  // The period is measured from a fixed `anchor` so paging walks one stable question.
  const path = useMemo(() => {
    const params = new URLSearchParams({ from: new Date(anchor - Number(range) * 3600_000).toISOString() });
    if (room.trim()) params.set("room", room.trim());
    if (result) params.set("result", result);
    if (kind) params.set("credential_type", kind);
    return "/guest-signin-attempts?" + params.toString();
  }, [anchor, range, room, result, kind]);
  const needle = query.trim();
  const headers = useMemo(
    () => (needle ? { "X-Signin-Search": encodeURIComponent(needle) } : undefined),
    [needle],
  );
  const list = useServerPage<AttemptsResp>({ path, headers });
  const { resp, current, err, loading: refreshing } = list;
  const rows = current && resp ? resp.data ?? [] : err && !resp ? [] : null;
  useEffect(() => { if (current && resp) setUpdatedAt(Date.now()); }, [current, resp]);
  const load = (_manual?: boolean) => setAnchor(Date.now());

  const sum = resp?.summary;
  const counts = {
    total: sum?.total ?? 0,
    failed: sum?.failed ?? 0,
    mismatch: sum?.mismatch ?? 0,
    systemic: sum?.systemic ?? 0,
  };

  async function open(a: SignInAttempt) {
    setCreds(null);
    setCredsErr(null);
    try {
      const d = await api.get<SignInAttemptDetail>("/guest-signin-attempts/" + a.id);
      setDetail(d);
    } catch (e) {
      setOpenErr(e);
    }
  }

  async function reveal() {
    if (!detail) return;
    setRevealing(true);
    setCredsErr(null);
    try {
      setCreds(await api.post<SignInAttemptCredentials>("/guest-signin-credentials/" + detail.id, { password: credsPw }));
    } catch (e) {
      setCredsErr(e);
    } finally {
      setCredsPw("");
      setRevealing(false);
    }
  }

  const restrictionsView = (
    <ActiveRestrictions
      canRelease={mayRelease}
      onShowAttempts={(mac) => {
        // Widen the period too: the attempts behind a restriction may predate the default window.
        setTab("attempts");
        setQuery(mac);
        setRoom("");
        setResult("");
        setKind("");
        setRange("24");
        setAnchor(Date.now());
      }}
    />
  );

  const filtered = needle !== "" || room.trim() !== "" || result !== "" || kind !== "";

  const attemptsView = (
    <div className="space-y-5">
      <StatGrid cols={4}>
        <StatCard label="Attempts" value={rows === null ? "—" : counts.total} hint="In the selected period" />
        <StatCard label="Did not connect" value={rows === null ? "—" : counts.failed} tone={counts.failed ? "warn" : "default"} />
        <StatCard label="Details did not match" value={rows === null ? "—" : counts.mismatch}
          hint="Wrong value, or room not in the guest list" />
        <StatCard label="System-side failures" value={rows === null ? "—" : counts.systemic}
          tone={counts.systemic ? "err" : "default"} hint="Nothing the guest typed could help" />
      </StatGrid>

      {counts.systemic > 0 && (
        <Callout tone="warning" title={`${counts.systemic} attempt${counts.systemic === 1 ? "" : "s"} failed for reasons no guest could fix`}>
          Refused because the guest list was out of date, the PMS routing, or an internal fault. Guests saw &ldquo;we
          are unable to verify your stay right now&rdquo;, not a request to re-check their details.
        </Callout>
      )}

      <Card className="overflow-hidden">
        <FilterBar
          inset
          search={
            <SearchInput value={query} onChange={setQuery}
              placeholder="Room, network, device or ID…"
              label="Search the attempts" delay={300} />
          }
          filters={
            <>
              <Input value={room} onChange={(e) => setRoom(e.target.value)} placeholder="Room"
                aria-label="Filter by room number" className="h-9 w-24" inputMode="numeric" />
              <Select value={result} onChange={(e) => setResult(e.target.value)}
                aria-label="Filter by result" className="h-9 w-full sm:w-56">
                {RESULT_FILTERS.map((f) => <option key={f.value || "all"} value={f.value}>{f.label}</option>)}
              </Select>
              <Select value={kind} onChange={(e) => setKind(e.target.value)}
                aria-label="Filter by credential type" className="h-9 w-full sm:w-48">
                {KIND_FILTERS.map((f) => <option key={f.value || "any"} value={f.value}>{f.label}</option>)}
              </Select>
              <Select value={range} onChange={(e) => { setRange(e.target.value); setAnchor(Date.now()); }}
                aria-label="Filter by time range" className="h-9 w-full sm:w-60">
                {RANGES.map((f) => <option key={f.value} value={f.value}>{f.label}</option>)}
              </Select>
            </>
          }
        />

        <div className={cn(refreshing && rows !== null && refreshingClass)}>
          {rows === null ? (
            <SkeletonRows rows={8} cols={7} />
          ) : rows.length === 0 ? (
            <EmptyState
              size="sm"
              icon={<KeyRound />}
              title={needle ? "Nothing matches this search" : "No sign-in attempts in this period"}
              hint={filtered
                ? "Clear the search box, or widen the filters above."
                : "Every Connect submission is recorded here, including the ones that fail."}
            />
          ) : (
            <Table label="Sign-in attempts">
              <THead>
                <TR>
                  <TH>When</TH>
                  <TH>Room</TH>
                  <TH className="hidden md:table-cell">Network</TH>
                  <TH>Result</TH>
                  <TH className="hidden lg:table-cell">Why</TH>
                  <TH className="hidden xl:table-cell">Entered as</TH>
                  <TH className="hidden xl:table-cell" numeric>Guest-list age</TH>
                  <TH className="hidden lg:table-cell">Device</TH>
                  <TH><span className="sr-only">Details</span></TH>
                </TR>
              </THead>
              <TBody>
                {rows.map((a) => (
                  <TR key={a.id} onClick={() => void open(a)}>
                    <TD nowrap muted title={formatDate(a.occurred_at)}>{formatRelative(a.occurred_at)}</TD>
                    <TD nowrap className="font-medium">{a.room || "—"}</TD>
                    <TD truncate muted title={a.guest_network || undefined} className="hidden max-w-[12rem] md:table-cell">{a.guest_network || "—"}</TD>
                    <TD nowrap>
                      <StatusBadge tone={tone(a.result)}>{a.succeeded ? "Connected" : "Refused"}</StatusBadge>
                    </TD>
                    <TD className="hidden lg:table-cell">{a.result_label}</TD>
                    <TD nowrap muted className="hidden xl:table-cell">
                      {a.verifier_kind === "FULL_NAME" ? "Name"
                        : a.verifier_kind === "RESERVATION_NUMBER_LIKE" ? "Reservation number"
                          : "—"}
                    </TD>
                    <TD numeric muted className="hidden xl:table-cell">{mirrorAge(a.mirror_age_seconds)}</TD>
                    <TD className="hidden whitespace-nowrap text-xs text-muted-foreground lg:table-cell">
                      {a.device_ip || "—"}
                      {a.device_mac ? <span className="block font-mono">{a.device_mac}</span> : null}
                    </TD>
                    <TD className="w-0">
                      <RowActions subject={a.room ? `room ${a.room}` : "attempt"} primary={{ label: "Details", onSelect: () => void open(a) }} />
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
          )}
        </div>
        {rows && rows.length > 0 && (
          <CardFooter>
            <Pagination
              className="w-full"
              offset={list.offset}
              limit={list.pageSize}
              shown={rows.length}
              total={list.total}
              hasMore={list.hasMore}
              onChange={list.setOffset}
            />
          </CardFooter>
        )}
      </Card>
    </div>
  );

  return (
    <PageShell>
      <PageHeader
        title="Guest sign-in attempts"
        description="Every Connect submission and why it succeeded or failed."
        actions={tab === "attempts" ? <LiveStatus updatedAt={updatedAt} refreshing={refreshing} onRefresh={() => void load(true)} /> : undefined}
        help={
          <>
            <HelpSection title="What this page is for">
              <p>
                The desk&rsquo;s &ldquo;why can&rsquo;t this guest get online?&rdquo; tool: every Connect
                submission, why it succeeded or failed, and — for roles allowed to see guest credentials — what the
                guest entered beside what would have been accepted. Attempts are kept for 30 days.
              </p>
            </HelpSection>
            <HelpSection title="Reading the figures">
              <HelpList
                items={[
                  <><strong>Details did not match</strong> — a wrong value, or a room the guest list does not hold.</>,
                  <><strong>System-side failures</strong> — refused because the guest list was out of date, the PMS routing, or an internal fault. Nothing the guest typed could have helped, and the guest saw &ldquo;we are unable to verify your stay right now&rdquo;.</>,
                  <><strong>Guest-list age</strong> — how old the appliance&rsquo;s copy of the guest list was when the attempt was checked.</>,
                ]}
              />
            </HelpSection>
            {maySeeRestrictions && (
              <HelpSection title="Active restrictions">
                <p>
                  Devices currently being asked to wait after too many incorrect sign-in details. A device disappears
                  from that list on its own when its wait ends. The room shown is the last room that device{" "}
                  <strong>typed</strong> — it is not a statement about who is using it or where they are staying.
                </p>
              </HelpSection>
            )}
          </>
        }
      />

      <ErrorBanner err={openErr ?? err} />
      {maySeeRestrictions && <NetworkPausesNotice canAct={mayRelease} />}

      {maySeeRestrictions ? (
        <Tabs value={tab} onValueChange={(v) => setTab(v as "attempts" | "restrictions")}>
          <TabsList aria-label="Guest sign-in">
            <TabsTrigger value="attempts">Sign-in attempts</TabsTrigger>
            <TabsTrigger value="restrictions">Active restrictions</TabsTrigger>
          </TabsList>
          <TabsContent value="attempts" className="mt-5">{attemptsView}</TabsContent>
          <TabsContent value="restrictions" className="mt-5">{restrictionsView}</TabsContent>
        </Tabs>
      ) : (
        attemptsView
      )}

      <DetailsDrawer
        open={detail !== null}
        onOpenChange={(v) => { if (!v) { setDetail(null); setCreds(null); setCredsErr(null); setCredsPw(""); } }}
        width="lg"
        eyebrow="Sign-in attempt"
        title={detail?.room ? `Room ${detail.room}` : "Sign-in attempt"}
        description={detail ? detail.result_label : undefined}
        badges={detail ? <StatusBadge tone={tone(detail.result)}>{detail.succeeded ? "Connected" : "Refused"}</StatusBadge> : undefined}
      >
        {detail && (
          <>
            <Callout tone={tone(detail.result) === "ok" ? "success" : tone(detail.result) === "err" ? "danger" : "warning"}
              title={detail.result_label}>
              {RESULT_WORDS[detail.result]?.meaning ?? "No further explanation is recorded for this result."}
            </Callout>

            <DetailsSection title="What was entered, and what would have been accepted">
            {!maySeeCredentials ? (
              <NotAvailable
                icon={<ShieldAlert />}
                title="You do not have permission to see what the guest typed"
                reason={
                  <>
                    What was entered and what would have been accepted are guest credentials. Only roles allowed to
                    see guest sign-in details can view them; your role is not one of them, and
                    the appliance does not send them without it. Everything else about this attempt is below.
                  </>
                }
              />
            ) : !detail.credentials_available ? (
              <Callout tone="neutral" title="No values were recorded for this attempt">
                This attempt was recorded while the appliance&rsquo;s sealing key was unavailable, so what the guest
                entered was never stored. Everything else below is unaffected.
              </Callout>
            ) : creds === null ? (
              <form
                className="space-y-3 rounded-md border border-border bg-surface/50 p-3.5"
                onSubmit={(e) => { e.preventDefault(); if (credsPw !== "") reveal(); }}
              >
                <p className="text-sm text-muted-foreground">
                  What the guest typed is guest data. Confirm your password to see it; every view is recorded in the
                  activity log.
                </p>
                {credsErr ? <ErrorBanner err={credsErr} className="mb-0" /> : null}
                <div className="flex flex-wrap items-end gap-2">
                  <Field label="Your password" htmlFor="signin-attempt-creds-password">
                    <Input id="signin-attempt-creds-password" type="password" autoComplete="current-password" value={credsPw}
                      onChange={(e) => setCredsPw(e.target.value)} className="w-60" />
                  </Field>
                  <Button type="submit" loading={revealing} disabled={credsPw === ""}>
                    <KeyRound /> Show what was entered
                  </Button>
                </div>
              </form>
            ) : detail.room_in_mirror === false ? (
              <Callout tone="warning" title="No eligible stay for this room exists in the appliance's guest list">
                <p>
                  There is nothing this attempt could have matched, so no expected values are shown. Inventing them
                  would be worse than showing none.
                </p>
                <DList
                  columns={1}
                  items={[
                    { label: "Verification value entered", value: <span className="font-medium">{creds.submitted_verifier || "—"}</span> },
                    {
                      label: "PMS connection",
                      value: `${detail.pms_transport_status || "unknown"} · last full sync ${detail.mirror_last_complete_sync_at ? formatDate(detail.mirror_last_complete_sync_at) : "never"}`,
                    },
                  ]}
                />
                <p className="mt-2 text-xs">
                  Changes made in the PMS after the displayed last-sync time are not available on this appliance.
                </p>
              </Callout>
            ) : (
              <DescriptionList
                items={[
                  { label: "Room entered", value: <span className="font-medium">{detail.room || "—"}</span> },
                  { label: "Verification value entered", value: <span className="font-medium">{creds.submitted_verifier || "—"}</span> },
                  { label: "Compared as", value: creds.normalized_verifier || "—" },
                  {
                    label: "Which field matched",
                    value: detail.matched_field
                      ? detail.matched_field.replace(/_/g, " ").toLowerCase()
                      : "none of them",
                  },
                  { label: "First / given name expected", value: <span className="font-medium">{creds.accepted_first_name || "—"}</span> },
                  { label: "Family name expected", value: <span className="font-medium">{creds.accepted_family_name || "—"}</span> },
                  { label: "Reservation number expected", value: <span className="font-medium">{creds.accepted_reservation_number || "—"}</span> },
                  {
                    label: "Other guests on this stay",
                    value: creds.additional_accepted_guests
                      ? `${creds.additional_accepted_guests} — their names are accepted too`
                      : "none",
                  },
                ]}
              />
            )}
            </DetailsSection>

            <DetailsSection title="Diagnostics">
            <DescriptionList
              items={[
                { label: "Local date and time", value: formatDate(detail.occurred_at) },
                { label: "Client network", value: detail.guest_network || "—" },
                { label: "Room found in the guest list", value: detail.room_in_mirror === null || detail.room_in_mirror === undefined ? "not reached" : detail.room_in_mirror ? "yes" : "no" },
                {
                  label: "Eligible stays on that room",
                  value: detail.eligible_stay_candidates === null || detail.eligible_stay_candidates === undefined
                    ? "not reached" : String(detail.eligible_stay_candidates),
                },
                { label: "PMS connection", value: detail.pms_transport_status || "—" },
                {
                  label: "Guest list last full refresh",
                  value: detail.mirror_last_complete_sync_at
                    ? `${formatDate(detail.mirror_last_complete_sync_at)} (${mirrorAge(detail.mirror_age_seconds)} old)`
                    : "never",
                },
                { label: "Correlation id", value: <MonoId value={detail.request_id ?? ""} title="Correlation id" /> },
                { label: "Response time", value: detail.latency_ms === null || detail.latency_ms === undefined ? "—" : `${detail.latency_ms} ms` },
                { label: "Device", value: `${detail.device_ip || "—"}${detail.device_mac ? " · " + detail.device_mac : ""}` },
                {
                  label: "Access created",
                  value: detail.session_id
                    ? <MonoId value={detail.session_id} title="Session" />
                    : detail.succeeded ? "identity proved, no session recorded" : "none",
                },
                { label: "Attempt id", value: <MonoId value={detail.id} title="Attempt" /> },
              ]}
            />
            </DetailsSection>
          </>
        )}
      </DetailsDrawer>
    </PageShell>
  );
}
