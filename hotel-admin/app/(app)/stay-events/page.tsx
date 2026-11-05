"use client";

import { useEffect, useMemo, useState } from "react";
import type { StayEvent } from "@/lib/api";
import { useServerPage, type PagedFields } from "@/lib/use-server-page";
import { PageShell, PageHeader, StatCard, StatGrid } from "@/components/ui/page";
import { Card, CardFooter } from "@/components/ui/card";
import { Table, TBody, THead, TR, TH, TD } from "@/components/ui/table";
import { StatusBadge, type Tone } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Explain } from "@/components/ui/tooltip";
import { HelpList, HelpSection } from "@/components/help";
import { MonoId, Skeleton, SkeletonRows } from "@/components/ui/misc";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { DescriptionList, Pagination, SearchInput } from "@/components/ui/data";
import { FilterBar } from "@/components/ui/toolbar";
import { RowActions } from "@/components/ui/row-actions";
import { LiveStatus, refreshingClass } from "@/components/ui/patterns";
import { cn, formatRelative, formatDate } from "@/lib/utils";
import { AlertTriangle, RefreshCw, Send } from "lucide-react";
import { useOperatorRoles } from "@/lib/whoami-context";
import { canWrite } from "@/lib/roles";
import {
  DECISION_WORDS,
  ReviewDecisionActions,
  ReviewExpirySettingCard,
  stayStatusWords,
} from "@/components/phase3/review-decisions";

const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";

const STATUS_WORDS: Record<string, { label: string; tone: Tone; meaning: string }> = {
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
  FAILED: {
    label: "Not applied", tone: "err",
    meaning: "The message was closed without being applied to the guest list.",
  },
};

const REVIEW_CODE_WORDS: Record<string, string> = {
  RETENTION_GIVEN_UP: "Given up: never processed, guest details removed",
};
const reviewCodeWords = (c: string) => REVIEW_CODE_WORDS[c] ?? c.replace(/_/g, " ").toLowerCase();

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
  { value: "UNDECIDED", label: "Needs a decision" },
  { value: "MANUAL_REVIEW", label: "Placed in review (decided or not)" },
  { value: "SKIPPED_DUPLICATE", label: "Duplicates" },
  { value: "REJECTED", label: "Rejected" },
  { value: "FAILED", label: "Not applied" },
];

type StayEventsResp = PagedFields & {
  data?: StayEvent[];
  summary?: {
    applied?: number;
    pending?: number;
    manual_review?: number;
    undecided?: number;
    rejected?: number;
    unmatched?: number;
    newest_received_at?: string;
  };
};

function statusOf(e: StayEvent) {
  const base = STATUS_WORDS[e.processing_status] ?? {
    label: "Status not recognised",
    tone: "default" as const,
    meaning: "",
  };
  return e.processing_status === "MANUAL_REVIEW" && e.decided
    ? { ...base, label: DECISION_WORDS[e.decision ?? ""] ?? "Decided", tone: "default" as const }
    : base;
}

function calloutTone(e: StayEvent): "success" | "danger" | "neutral" | "warning" {
  if (e.processing_status === "APPLIED") return "success";
  if (e.processing_status === "REJECTED" || e.processing_status === "FAILED") return "danger";
  if (e.processing_status === "SKIPPED_DUPLICATE") return "neutral";
  if (e.processing_status === "MANUAL_REVIEW" && e.decided) return "neutral";
  return "warning";
}

export default function StayEventsPage() {
  const [status, setStatus] = useState("");
  const [query, setQuery] = useState("");
  const [detail, setDetail] = useState<StayEvent | null>(null);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);

  useEffect(() => {
    if (new URLSearchParams(window.location.search).get("show") === "undecided") setStatus("UNDECIDED");
  }, []);

  const path = status === "UNDECIDED"
    ? "/pms-events?undecided=true"
    : status ? "/pms-events?processing_status=" + encodeURIComponent(status) : "/pms-events";
  const roles = useOperatorRoles();
  const canDecide = roles ? canWrite("pms-event-decisions", roles) : false;
  const needle = query.trim();
  const headers = useMemo(
    () => (needle ? { "X-Pms-Event-Search": encodeURIComponent(needle) } : undefined),
    [needle],
  );
  const list = useServerPage<StayEventsResp>({ path, headers });
  const { resp, current, err, loading: refreshing } = list;
  const loadFailed = err !== null && !resp;
  const rows = current && resp ? resp.data ?? [] : null;
  useEffect(() => { if (current && resp) setUpdatedAt(Date.now()); }, [current, resp]);

  const sum = resp?.summary;
  const counts = {
    applied: sum?.applied ?? 0,
    review: sum?.undecided ?? sum?.manual_review ?? 0,
    unmatched: sum?.unmatched ?? 0,
  };
  const newest = sum?.newest_received_at;
  const tile = (n: number) =>
    sum ? n.toLocaleString() : loadFailed ? <span className="text-muted-foreground">Not known</span> : <Skeleton className="h-6 w-16" />;

  return (
    <PageShell width="wide">
      <PageHeader
        title="PMS activity"
        description="Messages from the PMS and whether the guest list was updated from them."
        help={
          <>
            <HelpSection title="What this page shows">
              <p>
                Every message the PMS has sent this appliance — check-ins, check-outs and stay changes — and whether
                the guest list was updated from it. It answers &ldquo;has the Wi-Fi seen that check-in yet?&rdquo;
              </p>
            </HelpSection>
            <HelpSection title="What happened to a message">
              <HelpList
                items={[
                  <><strong>Applied</strong> — the guest list was updated from it.</>,
                  <><strong>Needs a decision</strong> — it could not be applied without guessing, so the appliance stopped rather than guessed. Until it is resolved the guest list may not reflect what the front desk has done.</>,
                  <><strong>Not matched to a stay</strong> — the appliance received the message but could not tell which stay it is about, usually a reservation it has not seen or a room number that does not match the guest list it holds.</>,
                ]}
              />
            </HelpSection>
            <HelpSection title="If nothing arrives">
              <p>
                If the front desk has checked guests in since the connection came up and nothing is listed here, look
                at the PMS connection screen.
              </p>
            </HelpSection>
          </>
        }
        actions={
          <LiveStatus updatedAt={updatedAt} refreshing={refreshing} error={!!err && !!resp} onRefresh={() => void list.reload()} />
        }
      />

      <StatGrid cols={4}>
        <StatCard
          label="Last message"
          value={newest ? formatRelative(newest) : sum ? "None yet" : loadFailed ? <span className="text-muted-foreground">Not known</span> : <Skeleton className="h-6 w-16" />}
          tone={sum && !newest ? "warn" : "default"}
          hint={!sum ? undefined : newest ? `Received ${formatDate(newest)}` : "Nothing has arrived yet"}
        />
        <StatCard label="Applied" value={tile(counts.applied)} hint="Guest list updated" />
        <StatCard
          label="Needs a decision"
          value={tile(counts.review)}
          tone={counts.review > 0 ? "warn" : "default"}
          hint="Stopped rather than guessed"
        />
        <StatCard
          label="Not matched to a stay"
          value={tile(counts.unmatched)}
          tone={counts.unmatched > 0 ? "warn" : "default"}
          explain={
            <Explain>
              The appliance received the message but could not tell which stay it is about — usually a reservation
              it has not seen, or a room number that does not match the guest list it holds.
            </Explain>
          }
        />
      </StatGrid>

      {counts.review > 0 && status !== "UNDECIDED" && (
        <Callout
          tone="warning"
          className={FADE}
          title={`${counts.review.toLocaleString()} message${counts.review === 1 ? " needs" : "s need"} a decision`}
        >
          Until they are resolved the guest list may not reflect what the front desk has done.
          {canDecide
            ? " Open one to acknowledge it or link it to a stay."
            : " Your role can see these messages but not decide them."}{" "}
          <Button variant="link" size="xs" className="h-auto p-0 font-medium" onClick={() => setStatus("UNDECIDED")}>
            Show only those
          </Button>
        </Callout>
      )}

      <Card className="overflow-hidden">
        <FilterBar
          inset
          search={
            <SearchInput
              value={query}
              onChange={setQuery}
              placeholder="Room, guest or reservation…"
              label="Search PMS activity"
              delay={300}
            />
          }
          filters={
            <Select
              value={status}
              onChange={(e) => setStatus(e.target.value)}
              aria-label="Filter by what happened to the message"
              className="h-9 w-full sm:w-64"
            >
              {FILTERS.map((f) => (
                <option key={f.value || "all"} value={f.value}>{f.label}</option>
              ))}
            </Select>
          }
        />
        {err !== null && resp && <div className="px-4 pt-3"><ErrorBanner err={err} /></div>}
        <div className={cn(refreshing && rows !== null && refreshingClass)}>
          {rows === null ? (
            loadFailed ? (
              <div role="alert" className={FADE}>
                <EmptyState
                  size="sm"
                  icon={<AlertTriangle />}
                  title="PMS activity could not be loaded"
                  hint="The appliance did not return the PMS messages, so the list and the counts above are not known. Nothing has been changed."
                  action={
                    <Button variant="secondary" disabled={refreshing} onClick={() => void list.reload()}>
                      <RefreshCw className={cn(refreshing && "animate-spin motion-reduce:animate-none")} /> Try again
                    </Button>
                  }
                />
              </div>
            ) : (
              <SkeletonRows rows={6} cols={5} />
            )
          ) : rows.length === 0 ? (
            <div className={FADE}>
              <EmptyState
                size="sm"
                icon={<Send />}
                title={
                  needle
                    ? "Nothing matches this search"
                    : status
                      ? "No messages in this state"
                      : "The PMS has not sent anything"
                }
                hint={!needle && !status ? "If guests have checked in since the connection came up, check the PMS connection." : undefined}
              />
            </div>
          ) : (
            <Table label="PMS messages">
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
                {rows.map((e) => {
                  const st = statusOf(e);
                  const subject = e.room ? `room ${e.room}` : "unmatched message";
                  return (
                    <TR key={e.id} onClick={() => setDetail(e)}>
                      <TD className="max-w-[16rem]">
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
                            <div className="truncate text-2xs text-muted-foreground">
                              {e.external_reservation_id ?? e.message_reservation
                                ? `Reservation ${e.external_reservation_id ?? e.message_reservation}`
                                : e.message_room
                                  ? `Names room ${e.message_room}`
                                  : e.identity_hidden ? "Reservation not shown to your role" : "No room or reservation recognised"}
                            </div>
                          </>
                        )}
                      </TD>
                      <TD nowrap>
                        <div className="text-sm">{typeWords(e.event_type)}</div>
                        {e.pms_interface_label && (
                          <div className="text-2xs text-muted-foreground">{e.pms_interface_label}</div>
                        )}
                      </TD>
                      <TD nowrap>
                        <StatusBadge tone={st.tone}>{st.label}</StatusBadge>
                        {e.review_code && (
                          <div className="mt-0.5 text-2xs text-muted-foreground">{reviewCodeWords(e.review_code)}</div>
                        )}
                      </TD>
                      <TD nowrap muted className="hidden lg:table-cell" title={e.pms_timestamp_utc ? formatDate(e.pms_timestamp_utc) : undefined}>
                        {e.pms_timestamp_utc ? formatRelative(e.pms_timestamp_utc) : "Not given"}
                      </TD>
                      <TD nowrap muted className="hidden md:table-cell" title={formatDate(e.received_at)}>{formatRelative(e.received_at)}</TD>
                      <TD className="w-0">
                        <RowActions subject={subject} primary={{ label: "Details", onSelect: () => setDetail(e) }} />
                      </TD>
                    </TR>
                  );
                })}
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

      {canDecide && <ReviewExpirySettingCard />}

      <DetailsDrawer
        open={detail !== null}
        onOpenChange={(v) => !v && setDetail(null)}
        eyebrow="PMS message"
        title={detail?.room ? `Room ${detail.room}` : "PMS message"}
        description={detail ? typeWords(detail.event_type) : undefined}
        badges={detail ? <StatusBadge tone={statusOf(detail).tone}>{statusOf(detail).label}</StatusBadge> : undefined}
      >
        {detail && (
          <>
            <Callout tone={calloutTone(detail)} title={statusOf(detail).label}>
              {STATUS_WORDS[detail.processing_status]?.meaning ?? "This message is in a state this console does not recognise."}
              {detail.processing_status === "MANUAL_REVIEW" && detail.decided && (
                <> It has since been decided: {(DECISION_WORDS[detail.decision ?? ""] ?? "decided").toLowerCase()}.</>
              )}
            </Callout>
            {detail.processing_status === "MANUAL_REVIEW" && !detail.decided && canDecide && (
              <DetailsSection title="Decide">
                <ReviewDecisionActions
                  eventId={detail.id}
                  onDecided={() => { setDetail(null); void list.reload(); }}
                />
              </DetailsSection>
            )}
            <DetailsSection title="Message">
              <DescriptionList
                items={[
                  { label: "Room", value: detail.room ?? "Not matched to a stay" },
                  { label: "Guest", value: detail.identity_hidden ? "Not shown to your role" : detail.primary_guest ?? "Not given" },
                  { label: "Reservation", value: detail.identity_hidden ? "Not shown to your role" : detail.external_reservation_id ?? detail.message_reservation ?? "Not given" },
                  { label: "Stay status", value: detail.stay_status ? stayStatusWords(detail.stay_status) : "No stay" },
                  { label: "PMS connection", value: detail.pms_interface_label || "Not known" },
                  { label: "Message type", value: typeWords(detail.event_type) },
                  { label: "Reason", value: detail.review_code ? reviewCodeWords(detail.review_code) : "None given" },
                  { label: "Time at the PMS", value: detail.pms_timestamp_utc ? formatDate(detail.pms_timestamp_utc) : "Not given by the PMS" },
                  { label: "Received here", value: formatDate(detail.received_at) },
                  {
                    label: "PMS message id",
                    value: detail.external_event_identity
                      ? <MonoId value={detail.external_event_identity} head={24} title="PMS message id" />
                      : "Not shown to your role",
                  },
                  { label: "Event id", value: <MonoId value={detail.id} title="Event" /> },
                ]}
              />
              <p className="mt-2 text-xs text-muted-foreground">
                For support: message type <span className="font-mono">{detail.event_type}</span>
                {detail.review_code && <>, reason <span className="font-mono">{detail.review_code}</span></>}
                {detail.stay_status && <>, stay <span className="font-mono">{detail.stay_status}</span></>}
              </p>
            </DetailsSection>
          </>
        )}
      </DetailsDrawer>
    </PageShell>
  );
}
