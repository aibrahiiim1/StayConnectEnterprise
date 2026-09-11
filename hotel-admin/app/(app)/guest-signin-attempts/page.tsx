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
import { KeyRound, Search, RefreshCw, ShieldAlert } from "lucide-react";
import {
  api, ListResp, SignInAttempt, SignInAttemptDetail, SignInAttemptCredentials, Whoami,
} from "@/lib/api";
import { canRead } from "@/lib/roles";
import { formatDate, formatRelative } from "@/lib/utils";
import { PageShell, PageHeader, StatCard, Toolbar } from "@/components/ui/page";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input, Select } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { SkeletonRows, DList, MonoId } from "@/components/ui/misc";
import { DetailDialog } from "@/components/ui/dialog";

// RESULT_WORDS turns a recorded code into a tone. The LABEL itself is not here: it comes from the server,
// from the same Go map the codes are declared in, so a new result cannot appear on this screen as a raw
// enum because somebody forgot to update a second list in the browser.
const RESULT_WORDS: Record<string, { tone: "ok" | "warn" | "err" | "info" | "neutral"; meaning: string }> = {
  VERIFIED: { tone: "ok", meaning: "The guest proved who they are and was offered access." },
  CREDENTIAL_MISMATCH: {
    tone: "warn",
    meaning:
      "The room is in the local mirror and has an eligible stay, but the value entered matched none of the accepted ones.",
  },
  ROOM_NOT_IN_MIRROR: {
    tone: "warn",
    meaning: "No stay on any mapped interface carries that room number in the local mirror.",
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
    meaning: "The local mirror could not authorise anybody at that moment — this affected every guest, not just this one.",
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

const RESULT_FILTERS = [
  { value: "", label: "Every result" },
  ...Object.keys(RESULT_WORDS).map((v) => ({ value: v, label: v.replace(/_/g, " ").toLowerCase() })),
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

  return (
    <PageShell>
      <PageHeader
        eyebrow="Property management system"
        title="Guest sign-in attempts"
        description="Every deliberate Connect submission, why it succeeded or failed, and — for authorised operators — exactly what the guest entered beside what the property would have accepted. Records are kept for 30 days and then deleted."
        actions={
          <Button variant="secondary" size="sm" onClick={() => void load(true)} disabled={refreshing}>
            <RefreshCw className={refreshing ? "size-4 animate-spin" : "size-4"} aria-hidden /> Refresh
          </Button>
        }
      />

      <ErrorBanner err={err} />

      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard label="Attempts" value={rows === null ? "—" : counts.total} hint="in the selected period" />
        <StatCard label="Did not connect" value={rows === null ? "—" : counts.failed} tone={counts.failed ? "warn" : "default"} />
        <StatCard label="Details did not match" value={rows === null ? "—" : counts.mismatch}
          hint="wrong value, or a room the mirror does not hold" />
        <StatCard label="System-side failures" value={rows === null ? "—" : counts.systemic}
          tone={counts.systemic ? "err" : "default"} hint="nothing the guest typed could have helped" />
      </div>

      {counts.systemic > 0 && (
        <Callout tone="warning" title={`${counts.systemic} attempt${counts.systemic === 1 ? "" : "s"} failed for reasons no guest could fix`}>
          These were refused by the mirror, the routing or an internal fault. Guests saw &ldquo;we are unable to verify your
          stay right now&rdquo;, not a request to re-check their details.
        </Callout>
      )}

      <Card>
        <CardBody className="border-b border-border py-3">
          <Toolbar>
            <div className="relative w-full max-w-xs">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" aria-hidden />
              <Input value={query} onChange={(e) => setQuery(e.target.value)}
                placeholder="Room, network, device or correlation id…"
                aria-label="Search the attempts on screen" className="pl-8" />
            </div>
            <Input value={room} onChange={(e) => setRoom(e.target.value)} placeholder="Room"
              aria-label="Filter by room number" className="w-28" />
            <Select value={result} onChange={(e) => setResult(e.target.value)}
              aria-label="Filter by result" className="w-60">
              {RESULT_FILTERS.map((f) => <option key={f.value || "all"} value={f.value}>{f.label}</option>)}
            </Select>
            <Select value={kind} onChange={(e) => setKind(e.target.value)}
              aria-label="Filter by credential type" className="w-56">
              {KIND_FILTERS.map((f) => <option key={f.value || "any"} value={f.value}>{f.label}</option>)}
            </Select>
            <Select value={range} onChange={(e) => setRange(e.target.value)}
              aria-label="Filter by time range" className="w-64">
              {RANGES.map((f) => <option key={f.value} value={f.value}>{f.label}</option>)}
            </Select>
            <span className="text-xs text-muted-foreground">Newest first · up to 200</span>
          </Toolbar>
        </CardBody>

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
                <TH>Network</TH>
                <TH>Result</TH>
                <TH>Why</TH>
                <TH>Entered as</TH>
                <TH>Mirror age</TH>
                <TH>Device</TH>
                <TH />
              </TR>
            </THead>
            <tbody>
              {filtered.map((a) => (
                <TR key={a.id}>
                  <TD className="whitespace-nowrap text-sm text-muted-foreground" title={formatDate(a.occurred_at)}>
                    {formatRelative(a.occurred_at)}
                  </TD>
                  <TD className="font-medium">{a.room || "—"}</TD>
                  <TD className="text-sm text-muted-foreground">{a.guest_network || "—"}</TD>
                  <TD>
                    <Badge tone={tone(a.result)} dot>{a.succeeded ? "Connected" : "Refused"}</Badge>
                  </TD>
                  <TD className="text-sm">{a.result_label}</TD>
                  <TD className="text-sm text-muted-foreground">
                    {a.verifier_kind === "FULL_NAME" ? "Name"
                      : a.verifier_kind === "RESERVATION_NUMBER_LIKE" ? "Reservation number"
                        : "—"}
                  </TD>
                  <TD className="whitespace-nowrap text-sm text-muted-foreground">{mirrorAge(a.mirror_age_seconds)}</TD>
                  <TD className="text-xs text-muted-foreground">
                    {a.device_ip || "—"}
                    {a.device_mac ? <span className="block">{a.device_mac}</span> : null}
                  </TD>
                  <TD>
                    <Button size="sm" variant="ghost" onClick={() => void open(a)}>Details</Button>
                  </TD>
                </TR>
              ))}
            </tbody>
          </Table>
        )}
      </Card>

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
            <h3 className="mt-4 text-sm font-medium">What was entered, and what would have been accepted</h3>
            {!maySeeCredentials ? (
              <Callout tone="neutral" title="You do not have permission to see the entered and accepted values" icon={<ShieldAlert className="size-4" aria-hidden />}>
                Everything else on this page is available to you. The values themselves need the
                View_Guest_SignIn_Credentials permission, and the server does not return them without it.
              </Callout>
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
              <Callout tone="warning" title="No eligible stay for this room exists in the local mirror">
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

            <h3 className="mt-4 text-sm font-medium">Diagnostics</h3>
            <DList
              columns={2}
              items={[
                { label: "Local date and time", value: formatDate(detail.occurred_at) },
                { label: "Guest network", value: detail.guest_network || "—" },
                { label: "Room found in the mirror", value: detail.room_in_mirror === null || detail.room_in_mirror === undefined ? "not reached" : detail.room_in_mirror ? "yes" : "no" },
                {
                  label: "Eligible stays on that room",
                  value: detail.eligible_stay_candidates === null || detail.eligible_stay_candidates === undefined
                    ? "not reached" : String(detail.eligible_stay_candidates),
                },
                { label: "PMS connection", value: detail.pms_transport_status || "—" },
                {
                  label: "Mirror last full sync",
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
