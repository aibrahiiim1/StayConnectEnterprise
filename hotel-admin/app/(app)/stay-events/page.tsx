"use client";

// PMS ACTIVITY — what the property management system has told this appliance.
//
// The screen used to have a column headed "Identity" whose contents were a connector-generated token along the
// lines of `PROTEL:GI:8831:2026-09-09T18:22:04Z`. Asked what this page showed, an operator could not say: the
// identity column identified nothing they recognised, "Type" was a protocol verb, "Reason" was a bounded code,
// and the default filter was MANUAL_REVIEW — so the page usually opened empty with no explanation of what it
// would have contained.
//
// The page's real job is one question, asked at the front desk several times a night: "I just checked that guest
// in — has the Wi-Fi system seen it yet?" So each row now leads with the ROOM the message is about, the event is
// described in words rather than as a protocol verb, and the PMS's own identifier is kept as secondary detail for
// the one case it is needed — quoting a specific message to the PMS vendor.
//
// A row with no room is the meaningful case rather than a rendering gap: it means the appliance received a message
// it could not match to a stay, which is usually exactly why it is still waiting.

import { useCallback, useEffect, useMemo, useState } from "react";
import { api, ListResp, StayEvent } from "@/lib/api";
import { PageShell, PageHeader, StatCard, Toolbar } from "@/components/ui/page";
import { Card, CardBody } from "@/components/ui/card";
import { Table, TBody, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Explain } from "@/components/ui/tooltip";
import { MonoId, SkeletonRows } from "@/components/ui/misc";
import { DetailDialog } from "@/components/ui/dialog";
import { DList } from "@/components/ui/misc";
import { SearchInput } from "@/components/ui/data";
import { LiveStatus, refreshingClass } from "@/components/ui/patterns";
import { cn, formatRelative, formatDate } from "@/lib/utils";
import { Inbox, Send } from "lucide-react";

// THE STATUS, IN CONSEQUENCES. Each one says what it means for the guest list, because that is what an operator
// is deciding about. The raw token stays available on the detail panel.
const STATUS_WORDS: Record<string, { label: string; tone: "ok" | "warn" | "err" | "default" | "info"; meaning: string }> = {
  APPLIED: {
    label: "Applied", tone: "ok",
    meaning: "The guest list was updated from this message.",
  },
  PENDING: {
    label: "Waiting", tone: "warn",
    meaning: "Received and accepted, not yet applied to the guest list.",
  },
  MANUAL_REVIEW: {
    label: "Needs a decision", tone: "warn",
    meaning: "The appliance could not apply this safely without guessing, so it stopped and left it for a person.",
  },
  SKIPPED_DUPLICATE: {
    label: "Duplicate", tone: "default",
    meaning: "The same message had already been applied, so this copy was ignored. Normal.",
  },
  REJECTED: {
    label: "Rejected", tone: "err",
    meaning: "The message could not be understood or contradicted what the appliance already holds.",
  },
};

// Protocol verbs, in hotel words. An unknown type falls through to its raw value rather than being hidden: a
// connector sending something this table has not been taught must still be visible.
const TYPE_WORDS: Record<string, string> = {
  GUEST_IN: "Check-in",
  GI: "Check-in",
  GUEST_OUT: "Check-out",
  GO: "Check-out",
  GUEST_CHANGE: "Stay changed",
  GC: "Stay changed",
  ROOM_CHANGE: "Room change",
  RESERVATION: "Reservation",
  POSTING_ALLOWED: "Charging permission",
  LINK_START: "Connection opened",
  LINK_END: "Connection closed",
  LINK_ALIVE: "Keep-alive",
  DATABASE_RESYNC: "Full guest-list refresh",
  DR: "Full guest-list refresh",
};

const typeWords = (t: string) => TYPE_WORDS[t] ?? t.replace(/_/g, " ").toLowerCase();

const FILTERS: { value: string; label: string }[] = [
  { value: "", label: "Everything" },
  { value: "APPLIED", label: "Applied to the guest list" },
  { value: "PENDING", label: "Waiting to be applied" },
  { value: "MANUAL_REVIEW", label: "Needs a decision" },
  { value: "SKIPPED_DUPLICATE", label: "Duplicates" },
  { value: "REJECTED", label: "Rejected" },
];

export default function StayEventsPage() {
  // OPENS ON EVERYTHING, not on the review queue.
  //
  // The default was MANUAL_REVIEW, so the normal and healthy state of this screen was an empty table reading
  // "Nothing to review" — which an operator checking whether the feed is alive reads as "the feed is dead".
  // The review queue is still one click away and is called out above the table when it is non-empty.
  const [status, setStatus] = useState("");
  const [rows, setRows] = useState<StayEvent[] | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [query, setQuery] = useState("");
  const [detail, setDetail] = useState<StayEvent | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);

  const load = useCallback(async (manual = false) => {
    if (manual) setRefreshing(true);
    try {
      const q = status ? "?processing_status=" + encodeURIComponent(status) : "";
      const r = await api.get<ListResp<StayEvent>>("/pms-events" + q);
      setRows(r.data ?? []);
      setErr(null);
      setUpdatedAt(Date.now());
    } catch (e) {
      setErr(e);
      // A failed refresh keeps the rows the operator was reading; only a first load has nothing to keep.
      setRows((prev) => prev ?? []);
    } finally {
      if (manual) setRefreshing(false);
    }
  }, [status]);

  useEffect(() => { setRows(null); void load(); }, [load]);

  const filtered = useMemo(() => {
    if (!rows) return null;
    const q = query.trim().toLowerCase();
    if (!q) return rows;
    return rows.filter((e) =>
      [e.room, e.primary_guest, e.external_reservation_id, e.event_type, e.external_event_identity]
        .some((v) => typeof v === "string" && v.toLowerCase().includes(q)),
    );
  }, [rows, query]);

  const counts = useMemo(() => {
    const c = { applied: 0, waiting: 0, review: 0, rejected: 0, unmatched: 0 };
    for (const e of rows ?? []) {
      if (e.processing_status === "APPLIED") c.applied++;
      else if (e.processing_status === "PENDING") c.waiting++;
      else if (e.processing_status === "MANUAL_REVIEW") c.review++;
      else if (e.processing_status === "REJECTED") c.rejected++;
      if (!e.stay_id) c.unmatched++;
    }
    return c;
  }, [rows]);

  const newest = rows?.[0]?.received_at;

  return (
    <PageShell width="wide">
      <PageHeader
        eyebrow="Property management system"
        title="PMS activity"
        icon={<Inbox />}
        description="Every message the PMS has sent this appliance — check-ins, check-outs and stay changes — and whether the guest list was updated from it. Answers “has the Wi-Fi seen that check-in yet?”"
        actions={
          <LiveStatus updatedAt={updatedAt} refreshing={refreshing} error={!!err && !!rows?.length} onRefresh={() => void load(true)} />
        }
      />

      <ErrorBanner err={err} />

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatCard
          label="Last message"
          value={<span className="text-lg">{newest ? formatRelative(newest) : "—"}</span>}
          icon={<Send />}
          tone={newest ? "ok" : "warn"}
          hint={
            newest
              ? "The connection is carrying traffic."
              : "Nothing has arrived. Check the PMS connection."
          }
        />
        <StatCard label="Applied" value={counts.applied.toLocaleString()} tone="ok" hint="Guest list updated" />
        <StatCard
          label="Needs a decision"
          value={counts.review.toLocaleString()}
          tone={counts.review > 0 ? "warn" : "default"}
          hint="Stopped rather than guessed"
        />
        <StatCard
          label="Not matched to a stay"
          value={counts.unmatched.toLocaleString()}
          tone={counts.unmatched > 0 ? "warn" : "default"}
          explain={
            <Explain>
              The appliance received the message but could not tell which stay it is about — usually a reservation
              it has not seen, or a room number that does not match the guest list it holds.
            </Explain>
          }
        />
      </div>

      {counts.review > 0 && status !== "MANUAL_REVIEW" && (
        <Callout tone="warning" title={`${counts.review} message${counts.review === 1 ? "" : "s"} need a decision`}>
          These could not be applied without guessing, so the appliance stopped. Until they are resolved the guest
          list may not reflect what the front desk has done.{" "}
          <Button variant="link" size="xs" className="h-auto p-0 font-medium" onClick={() => setStatus("MANUAL_REVIEW")}>
            Show only those
          </Button>
        </Callout>
      )}

      <Card>
        <CardBody className="border-b border-border py-3">
          <Toolbar>
            <SearchInput
              value={query}
              onChange={setQuery}
              placeholder="Room, guest or reservation…"
              label="Search PMS activity"
              className="sm:max-w-xs"
            />
            <Select
              value={status}
              onChange={(e) => setStatus(e.target.value)}
              aria-label="Filter by what happened to the message"
              className="w-full sm:w-64"
            >
              {FILTERS.map((f) => (
                <option key={f.value || "all"} value={f.value}>{f.label}</option>
              ))}
            </Select>
          </Toolbar>
        </CardBody>
        <CardBody className={cn("p-0", refreshing && refreshingClass)}>
          {filtered === null ? (
            <SkeletonRows rows={6} cols={5} />
          ) : filtered.length === 0 ? (
            <EmptyState
              icon={<Send />}
              title={
                rows && rows.length > 0
                  ? "Nothing matches this search"
                  : status
                    ? "No messages in this state"
                    : "The PMS has not sent anything"
              }
              hint={
                rows && rows.length === 0 && !status
                  ? "If the front desk has checked guests in since the connection came up, this is worth investigating on the PMS connection screen."
                  : undefined
              }
            />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>About</TH>
                  <TH>What happened</TH>
                  <TH>Result</TH>
                  <TH className="hidden lg:table-cell">PMS time</TH>
                  <TH className="hidden md:table-cell">Received</TH>
                  <TH><span className="sr-only">Details</span></TH>
                </TR>
              </THead>
              <TBody>
                {filtered.map((e) => {
                  const st = STATUS_WORDS[e.processing_status] ?? {
                    label: e.processing_status.replace(/_/g, " ").toLowerCase(),
                    tone: "default" as const,
                    meaning: "",
                  };
                  return (
                    <TR key={e.id}>
                      <TD>
                        {e.room ? (
                          <>
                            <div className="font-medium">Room {e.room}</div>
                            <div className="truncate text-xs text-muted-foreground">
                              {e.primary_guest ?? e.external_reservation_id ?? ""}
                            </div>
                          </>
                        ) : (
                          <>
                            <div className="text-sm text-muted-foreground">Not matched to a stay</div>
                            <div className="text-2xs text-muted-foreground">
                              {e.external_reservation_id
                                ? `Reservation ${e.external_reservation_id}`
                                : "No room or reservation recognised"}
                            </div>
                          </>
                        )}
                      </TD>
                      <TD>
                        <div className="text-sm">{typeWords(e.event_type)}</div>
                        {e.pms_interface_label && (
                          <div className="text-2xs text-muted-foreground">{e.pms_interface_label}</div>
                        )}
                      </TD>
                      <TD>
                        <Badge tone={st.tone}>{st.label}</Badge>
                        {e.review_code && (
                          <div className="mt-0.5 text-2xs text-muted-foreground">
                            {e.review_code.replace(/_/g, " ").toLowerCase()}
                          </div>
                        )}
                      </TD>
                      <TD className="hidden text-sm text-muted-foreground lg:table-cell" title={e.pms_timestamp_utc ? formatDate(e.pms_timestamp_utc) : undefined}>
                        {e.pms_timestamp_utc ? formatRelative(e.pms_timestamp_utc) : "—"}
                      </TD>
                      <TD className="hidden text-sm text-muted-foreground md:table-cell" title={formatDate(e.received_at)}>{formatRelative(e.received_at)}</TD>
                      <TD className="text-end">
                        <Button size="sm" variant="ghost" onClick={() => setDetail(e)}>Details</Button>
                      </TD>
                    </TR>
                  );
                })}
              </TBody>
            </Table>
          )}
        </CardBody>
      </Card>

      <DetailDialog
        open={detail !== null}
        onOpenChange={(v) => !v && setDetail(null)}
        title={detail?.room ? `Room ${detail.room}` : "PMS message"}
        description={detail ? typeWords(detail.event_type) : undefined}
        size="md"
      >
        {detail && (
          <>
            <Callout
              tone={
                detail.processing_status === "APPLIED" ? "success"
                  : detail.processing_status === "REJECTED" ? "danger"
                    : detail.processing_status === "SKIPPED_DUPLICATE" ? "neutral" : "warning"
              }
              title={(STATUS_WORDS[detail.processing_status] ?? { label: detail.processing_status }).label}
            >
              {(STATUS_WORDS[detail.processing_status] ?? { meaning: "" }).meaning}
            </Callout>
            <DList
              items={[
                { label: "Room", value: detail.room ?? "Not matched" },
                { label: "Guest", value: detail.primary_guest ?? "—" },
                { label: "Reservation", value: detail.external_reservation_id ?? "—" },
                { label: "Stay status", value: detail.stay_status?.replace(/_/g, " ").toLowerCase() ?? "—" },
                { label: "PMS connection", value: detail.pms_interface_label || "—" },
                { label: "Message type", value: `${typeWords(detail.event_type)} (${detail.event_type})` },
                { label: "Reason code", value: detail.review_code ?? "—" },
                { label: "Time at the PMS", value: detail.pms_timestamp_utc ? formatDate(detail.pms_timestamp_utc) : "—" },
                { label: "Received here", value: formatDate(detail.received_at) },
                {
                  label: "PMS message id",
                  span: true,
                  // Kept, and kept here rather than in the table: this is the string to quote when raising a
                  // message with the PMS vendor, and it is useful for nothing else.
                  value: <MonoId value={detail.external_event_identity} head={24} title="PMS message id" />,
                },
                { label: "Event id", value: <MonoId value={detail.id} title="Event" /> },
              ]}
            />
          </>
        )}
      </DetailDialog>
    </PageShell>
  );
}
