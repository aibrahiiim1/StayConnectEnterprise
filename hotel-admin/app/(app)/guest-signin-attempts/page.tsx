"use client";

// GUEST SIGN-IN ATTEMPTS — the screen that answers "why can't this guest get online".
//
// It exists because that question had no answer. A guest arrives at the desk, the operator has the PMS on one
// screen and nothing on the other, and the only available move is to ask the guest to type it again while
// somebody watches a log. Everything on this page is arranged around the one comparison that ends that
// conversation: what they entered, beside what we would have accepted.
//
// TWO PERMISSIONS, AND THE SECOND ONE IS REAL. The list, the rooms, the results and the diagnostic reasons
// need View_Guest_SignIn_Attempts. The submitted and accepted VALUES need View_Guest_SignIn_Credentials as
// well, they come from a different endpoint, and edged refuses that endpoint outright to an operator who
// lacks it — this file hiding a panel is a courtesy, not the boundary. When the operator does hold it the
// values are shown IN FULL and unmasked: these are the same people who already hold the guest's room, stay
// and access credentials, and "OK••••••" beside "OK•••••••" answers nothing at all.
//
// IT DOES NOT INVENT EXPECTATIONS. When no stay for that room exists in the local mirror there is nothing
// that would have been accepted, and the panel says so in words rather than showing empty fields that read
// as "we have this and won't tell you".

import { useCallback, useEffect, useMemo, useState } from "react";
import { KeyRound, ShieldAlert, UserX } from "lucide-react";
import {
  api, ListResp, SignInAttempt, SignInAttemptDetail, SignInAttemptCredentials, Whoami,
} from "@/lib/api";
import { canRead, canWrite } from "@/lib/roles";
import { ActiveRestrictions } from "@/components/guest-signin-restrictions";
import { cn, formatDate, formatRelative } from "@/lib/utils";
import { PageShell, PageHeader, StatCard, Toolbar } from "@/components/ui/page";
import { Card, CardBody } from "@/components/ui/card";
import { Table, TBody, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input, Select } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { SkeletonRows, DList, MonoId } from "@/components/ui/misc";
import { DetailDialog } from "@/components/ui/dialog";
import { SearchInput } from "@/components/ui/data";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { LiveStatus, NotAvailable, refreshingClass } from "@/components/ui/patterns";

// RESULT_WORDS turns a recorded code into a tone. The LABEL itself is not here: it comes from the server,
// from the same Go map the codes are declared in, so a new result cannot appear on this screen as a raw
// enum because somebody forgot to update a second list in the browser.
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
    meaning: "The request reached no PMS interface: the device was on no mapped guest network, or the network maps to none.",
  },
  SERVICE_UNAVAILABLE: { tone: "err", meaning: "An internal failure. The details were never compared." },
  SPENT_REQUEST_ID: {
    tone: "err",
    meaning: "The client re-used a request id that had already been refused. This is a stale portal build, not a guest error.",
  },
  MALFORMED_SUBMISSION: { tone: "neutral", meaning: "The submission could not be read: a missing room, no value, or an unusable device." },
  VERIFIED_NO_ELIGIBLE_PACKAGE: {
    tone: "err",
    meaning: "The guest's details were RIGHT. The property had no package to offer that stay — this is a configuration problem, not theirs.",
  },
};

// The filter's own words. The row labels still come from the server; these only name the choices.
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

export default function GuestSignInAttemptsPage() {
  const [rows, setRows] = useState<SignInAttempt[] | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);

  const [room, setRoom] = useState("");
  const [result, setResult] = useState("");
  const [kind, setKind] = useState("");
  const [range, setRange] = useState("72");
  const [query, setQuery] = useState("");

  const [detail, setDetail] = useState<SignInAttemptDetail | null>(null);
  const [creds, setCreds] = useState<SignInAttemptCredentials | null>(null);
  const [credsErr, setCredsErr] = useState<unknown>(null);

  // The credential permission is read once and FAILS CLOSED while it loads. The panel is not the boundary —
  // edged is — but a screen that flashed the values open before roles arrived would be its own small leak.
  const [roles, setRoles] = useState<string[] | null>(null);
  useEffect(() => {
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => setRoles([]));
  }, []);
  const maySeeCredentials = roles === null ? false : canRead("guest-signin-credentials", roles);
  // THREE SEPARATE PERMISSIONS, AND NONE IMPLIES ANOTHER. Seeing the restrictions is not seeing what guests
  // typed, and releasing one is not permission to change the property's thresholds. edged enforces all three;
  // this file only decides what to offer.
  const maySeeRestrictions = roles === null ? false : canRead("guest-signin-restrictions", roles);
  const mayRelease = roles === null ? false : canWrite("guest-signin-restrictions", roles);

  // The two halves of one question: why a guest could not connect, and whether their device is being asked to
  // wait before trying again. A tab rather than a second page, because an operator moves between them while
  // the guest is still standing there.
  const [tab, setTab] = useState<"attempts" | "restrictions">("attempts");

  const load = useCallback(async (manual = false) => {
    if (manual) setRefreshing(true);
    try {
      const from = new Date(Date.now() - Number(range) * 3600_000).toISOString();
      const params = new URLSearchParams({ from });
      if (room.trim()) params.set("room", room.trim());
      if (result) params.set("result", result);
      if (kind) params.set("credential_type", kind);
      const r = await api.get<ListResp<SignInAttempt>>("/guest-signin-attempts?" + params.toString());
      setRows(r.data ?? []);
      setErr(null);
      setUpdatedAt(Date.now());
    } catch (e) {
      setErr(e);
      setRows([]);
    } finally {
      if (manual) setRefreshing(false);
    }
  }, [room, result, kind, range]);

  useEffect(() => { setRows(null); void load(); }, [load]);

  // The search box filters what is ON SCREEN. The room, result, type and range go to the server, because a
  // desk looking for one room must not have to pull thirty days of rows to find it.
  const filtered = useMemo(() => {
    if (rows === null) return null;
    const q = query.trim().toLowerCase();
    if (!q) return rows;
    return rows.filter((a) =>
      [a.room, a.guest_network, a.result_label, a.device_ip, a.device_mac, a.request_id]
        .filter(Boolean).some((v) => String(v).toLowerCase().includes(q)));
  }, [rows, query]);

  const counts = useMemo(() => {
    const r = rows ?? [];
    return {
      total: r.length,
      failed: r.filter((a) => !a.succeeded).length,
      mismatch: r.filter((a) => a.result === "CREDENTIAL_MISMATCH" || a.result === "ROOM_NOT_IN_MIRROR").length,
      systemic: r.filter((a) =>
        a.result === "MIRROR_STALE_OR_MISSING_CHANGE" ||
        a.result === "ROUTING_OR_INTERFACE_FAILURE" ||
        a.result === "SERVICE_UNAVAILABLE").length,
    };
  }, [rows]);

  async function open(a: SignInAttempt) {
    setCreds(null);
    setCredsErr(null);
    try {
      const d = await api.get<SignInAttemptDetail>("/guest-signin-attempts/" + a.id);
      setDetail(d);
      if (maySeeCredentials && d.credentials_available) {
        try {
          setCreds(await api.get<SignInAttemptCredentials>("/guest-signin-credentials/" + a.id));
        } catch (e) {
          setCredsErr(e);
        }
      }
    } catch (e) {
      setErr(e);
    }
  }

  const restrictionsView = (
    <ActiveRestrictions
      canRelease={mayRelease}
      onShowAttempts={(mac) => {
        // "The related sign-in attempts" is the same list, filtered to that device. Widening the period
        // as well, because the attempts that caused a restriction can already be older than the default
        // window by the time somebody looks.
        setTab("attempts");
        setQuery(mac);
        setRoom("");
        setResult("");
        setKind("");
        setRange("24");
      }}
    />
  );

  const attemptsView = (
      <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <LiveStatus updatedAt={updatedAt} refreshing={refreshing} onRefresh={() => void load(true)} />
      </div>
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard label="Attempts" value={rows === null ? "—" : counts.total} hint="in the selected period" />
        <StatCard label="Did not connect" value={rows === null ? "—" : counts.failed} tone={counts.failed ? "warn" : "default"} />
        <StatCard label="Details did not match" value={rows === null ? "—" : counts.mismatch}
          hint="wrong value, or a room the guest list does not hold" />
        <StatCard label="System-side failures" value={rows === null ? "—" : counts.systemic}
          tone={counts.systemic ? "err" : "default"} hint="nothing the guest typed could have helped" />
      </div>

      {counts.systemic > 0 && (
        <Callout tone="warning" title={`${counts.systemic} attempt${counts.systemic === 1 ? "" : "s"} failed for reasons no guest could fix`}>
          These were refused because the guest list was out of date, the network routing, or an internal fault. Guests saw &ldquo;we are unable to verify your
          stay right now&rdquo;, not a request to re-check their details.
        </Callout>
      )}

      <Card className="overflow-hidden">
        <CardBody className="border-b border-border py-3">
          <Toolbar className="justify-start">
            <SearchInput value={query} onChange={setQuery}
              placeholder="Room, network, device or correlation id…"
              label="Search the attempts on screen" className="sm:max-w-xs" />
            <Input value={room} onChange={(e) => setRoom(e.target.value)} placeholder="Room"
              aria-label="Filter by room number" className="h-9 w-28" inputMode="numeric" />
            <Select value={result} onChange={(e) => setResult(e.target.value)}
              aria-label="Filter by result" className="h-9 w-full sm:w-60">
              {RESULT_FILTERS.map((f) => <option key={f.value || "all"} value={f.value}>{f.label}</option>)}
            </Select>
            <Select value={kind} onChange={(e) => setKind(e.target.value)}
              aria-label="Filter by credential type" className="h-9 w-full sm:w-52">
              {KIND_FILTERS.map((f) => <option key={f.value || "any"} value={f.value}>{f.label}</option>)}
            </Select>
            <Select value={range} onChange={(e) => setRange(e.target.value)}
              aria-label="Filter by time range" className="h-9 w-full sm:w-64">
              {RANGES.map((f) => <option key={f.value} value={f.value}>{f.label}</option>)}
            </Select>
            <span className="text-xs text-muted-foreground">Newest first · up to 200</span>
          </Toolbar>
        </CardBody>

        <div className={cn(refreshing && rows !== null && refreshingClass)}>
        {filtered === null ? (
          <SkeletonRows rows={8} cols={7} />
        ) : filtered.length === 0 ? (
          <EmptyState
            icon={<KeyRound />}
            title={rows && rows.length > 0 ? "Nothing matches this search" : "No sign-in attempts in this period"}
            hint={rows && rows.length > 0
              ? "Clear the search box, or widen the filters above."
              : "Every deliberate Connect submission is recorded here, including the ones that fail."}
          />
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>When</TH>
                <TH>Room</TH>
                <TH className="hidden md:table-cell">Network</TH>
                <TH>Result</TH>
                <TH className="hidden lg:table-cell">Why</TH>
                <TH className="hidden xl:table-cell">Entered as</TH>
                <TH className="hidden xl:table-cell">Guest-list age</TH>
                <TH className="hidden lg:table-cell">Device</TH>
                <TH><span className="sr-only">Details</span></TH>
              </TR>
            </THead>
            <TBody>
              {filtered.map((a) => (
                <TR key={a.id}>
                  <TD className="whitespace-nowrap text-sm text-muted-foreground" title={formatDate(a.occurred_at)}>
                    {formatRelative(a.occurred_at)}
                  </TD>
                  <TD className="font-medium">{a.room || "—"}</TD>
                  <TD className="hidden text-sm text-muted-foreground md:table-cell">{a.guest_network || "—"}</TD>
                  <TD>
                    <Badge tone={tone(a.result)} dot>{a.succeeded ? "Connected" : "Refused"}</Badge>
                  </TD>
                  <TD className="hidden text-sm lg:table-cell">{a.result_label}</TD>
                  <TD className="hidden text-sm text-muted-foreground xl:table-cell">
                    {a.verifier_kind === "FULL_NAME" ? "Name"
                      : a.verifier_kind === "RESERVATION_NUMBER_LIKE" ? "Reservation number"
                        : "—"}
                  </TD>
                  <TD className="hidden whitespace-nowrap text-sm text-muted-foreground xl:table-cell">{mirrorAge(a.mirror_age_seconds)}</TD>
                  <TD className="hidden text-xs text-muted-foreground lg:table-cell">
                    {a.device_ip || "—"}
                    {a.device_mac ? <span className="block">{a.device_mac}</span> : null}
                  </TD>
                  <TD className="text-end">
                    <Button size="sm" variant="ghost" onClick={() => void open(a)}>Details</Button>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
        </div>
      </Card>
      </div>
  );

  return (
    <PageShell>
      <PageHeader
        eyebrow="Property management system"
        title="Guest sign-in attempts"
        icon={<UserX />}
        description="The desk's “why can't this guest get online?” tool: every Connect submission, why it succeeded or failed, and — for roles allowed to see guest credentials — what the guest entered beside what would have been accepted. Kept for 30 days."
      />

      <ErrorBanner err={err} />

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

      <DetailDialog
        open={detail !== null}
        onOpenChange={(v) => { if (!v) { setDetail(null); setCreds(null); setCredsErr(null); } }}
        title={detail?.room ? `Room ${detail.room}` : "Sign-in attempt"}
        description={detail ? detail.result_label : undefined}
        size="lg"
      >
        {detail && (
          <>
            <Callout tone={tone(detail.result) === "ok" ? "success" : tone(detail.result) === "err" ? "danger" : "warning"}
              title={detail.result_label}>
              {RESULT_WORDS[detail.result]?.meaning ?? "No further explanation is recorded for this result."}
            </Callout>

            {/* THE COMPARISON. It is the reason this screen exists, so it sits above the diagnostics. */}
            <h3 className="text-emphasis">What was entered, and what would have been accepted</h3>
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
            ) : credsErr ? (
              <ErrorBanner err={credsErr} />
            ) : creds === null ? (
              <SkeletonRows rows={2} cols={2} />
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
              <DList
                columns={2}
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

            <h3 className="text-emphasis">Diagnostics</h3>
            <DList
              columns={2}
              items={[
                { label: "Local date and time", value: formatDate(detail.occurred_at) },
                { label: "Guest network", value: detail.guest_network || "—" },
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
                { label: "Attempt id", value: <MonoId value={detail.id} title="Attempt" />, span: true },
              ]}
            />
          </>
        )}
      </DetailDialog>
    </PageShell>
  );
}
