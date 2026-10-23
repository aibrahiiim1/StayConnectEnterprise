"use client";

// A row with no room is meaningful, not a gap: the message could not be matched to a stay.

import { useEffect, useMemo, useState } from "react";
import type { StayEvent } from "@/lib/api";
import { useServerPage, type PagedFields } from "@/lib/use-server-page";
import { PageShell, PageHeader, StatCard, Toolbar } from "@/components/ui/page";
import { Card, CardBody } from "@/components/ui/card";
import { Table, TBody, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Select } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Explain } from "@/components/ui/tooltip";
import { HelpList, HelpSection } from "@/components/help";
import { MonoId, SkeletonRows } from "@/components/ui/misc";
import { DetailDialog } from "@/components/ui/dialog";
import { DList } from "@/components/ui/misc";
import { Pagination, SearchInput } from "@/components/ui/data";
import { LiveStatus, refreshingClass } from "@/components/ui/patterns";
import { cn, formatRelative, formatDate } from "@/lib/utils";
import { Inbox, Send } from "lucide-react";
import { useOperatorRoles } from "@/lib/whoami-context";
import { canWrite } from "@/lib/roles";
import { DECISION_WORDS, ReviewDecisionActions, ReviewExpirySettingCard } from "@/components/phase3/review-decisions";

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
  FAILED: {
    label: "Not applied", tone: "err",
    meaning: "The message was closed without being applied to the guest list.",
  },
};

const REVIEW_CODE_WORDS: Record<string, string> = {
  RETENTION_GIVEN_UP: "Given up: never processed, guest details removed",
};
const reviewCodeWords = (c: string) => REVIEW_CODE_WORDS[c] ?? c.replace(/_/g, " ").toLowerCase();

// Unknown types fall through to their raw value so nothing a connector sends is hidden.
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

export default function StayEventsPage() {
  // Defaults to everything, not the review queue: an empty "Nothing to review" table reads as a dead feed.
  const [status, setStatus] = useState("");
  const [query, setQuery] = useState("");
  const [detail, setDetail] = useState<StayEvent | null>(null);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);

  // The search term goes in a header, never the URL, because it can be a guest's name and edged logs request lines.
  // "Needs a decision" means in review with no decision yet, not every message ever placed in review.
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
  const rows = current && resp ? resp.data ?? [] : err && !resp ? [] : null;
  useEffect(() => { if (current && resp) setUpdatedAt(Date.now()); }, [current, resp]);

  // Tiles are server counts over every message, regardless of the filter.
  const sum = resp?.summary;
  const counts = {
    applied: sum?.applied ?? 0,
    waiting: sum?.pending ?? 0,
    review: sum?.undecided ?? sum?.manual_review ?? 0,
    rejected: sum?.rejected ?? 0,
    unmatched: sum?.unmatched ?? 0,
  };
  const newest = sum?.newest_received_at;

  return (
    <PageShell width="wide">
      <PageHeader
        eyebrow="Hotel"
        title="PMS activity"
        icon={<Inbox />}
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
          <LiveStatus updatedAt={updatedAt} refreshing={refreshing} error={!!err && !!rows?.length} onRefresh={() => void list.reload()} />
        }
      />

      <ErrorBanner err={err} />

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatCard
          label="Last message"
          value={<span className="text-lg">{newest ? formatRelative(newest) : "—"}</span>}
          icon={<Send />}
          tone={!sum ? "default" : newest ? "ok" : "warn"}
          hint={
            !sum
              ? undefined
              : newest
                ? "The connection is carrying traffic."
                : "Nothing has arrived. Check the PMS connection."
          }
        />
        <StatCard label="Applied" value={sum ? counts.applied.toLocaleString() : "—"} tone="ok" hint="Guest list updated" />
        <StatCard
          label="Needs a decision"
          value={sum ? counts.review.toLocaleString() : "—"}
          tone={counts.review > 0 ? "warn" : "default"}
          hint="Stopped rather than guessed"
        />
        <StatCard
          label="Not matched to a stay"
          value={sum ? counts.unmatched.toLocaleString() : "—"}
          tone={counts.unmatched > 0 ? "warn" : "default"}
          explain={
            <Explain>
              The appliance received the message but could not tell which stay it is about — usually a reservation
              it has not seen, or a room number that does not match the guest list it holds.
            </Explain>
          }
        />
      </div>

      {canDecide && <ReviewExpirySettingCard />}

      {counts.review > 0 && status !== "UNDECIDED" && (
        <Callout tone="warning" title={`${counts.review} message${counts.review === 1 ? "" : "s"} need a decision`}>
          These could not be applied without guessing, so the appliance stopped. Until they are resolved the guest
          list may not reflect what the front desk has done.
          {canDecide ? " Open one to acknowledge it or link it to a stay." : " The site admin or IT manager decides them."}{" "}
          <Button variant="link" size="xs" className="h-auto p-0 font-medium" onClick={() => setStatus("UNDECIDED")}>
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
              delay={300}
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
          {rows === null ? (
            <SkeletonRows rows={6} cols={5} />
          ) : rows.length === 0 ? (
            <EmptyState
              icon={<Send />}
              title={
                needle
                  ? "Nothing matches this search"
                  : status
                    ? "No messages in this state"
                    : "The PMS has not sent anything"
              }
              hint={
                !needle && !status
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
                {rows.map((e) => {
                  const base = STATUS_WORDS[e.processing_status] ?? {
                    label: e.processing_status.replace(/_/g, " ").toLowerCase(),
                    tone: "default" as const,
                    meaning: "",
                  };
                  const st = e.processing_status === "MANUAL_REVIEW" && e.decided
                    ? { ...base, label: DECISION_WORDS[e.decision ?? ""] ?? "Decided", tone: "default" as const }
                    : base;
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
                              {e.external_reservation_id ?? e.message_reservation
                                ? `Reservation ${e.external_reservation_id ?? e.message_reservation}`
                                : e.message_room
                                  ? `Names room ${e.message_room}`
                                  : e.identity_hidden ? "Reservation not shown to your role" : "No room or reservation recognised"}
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
                            {reviewCodeWords(e.review_code)}
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
        {rows && rows.length > 0 && (
          <CardBody className="border-t border-border py-3">
            <Pagination
              offset={list.offset}
              limit={list.pageSize}
              shown={rows.length}
              total={list.total}
              hasMore={list.hasMore}
              onChange={list.setOffset}
            />
          </CardBody>
        )}
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
              {detail.processing_status === "MANUAL_REVIEW" && detail.decided && (
                <> Decided: {DECISION_WORDS[detail.decision ?? ""] ?? detail.decision}.</>
              )}
            </Callout>
            {detail.processing_status === "MANUAL_REVIEW" && !detail.decided && canDecide && (
              <ReviewDecisionActions
                eventId={detail.id}
                onDecided={() => { setDetail(null); void list.reload(); }}
              />
            )}
            <DList
              items={[
                { label: "Room", value: detail.room ?? "Not matched" },
                { label: "Guest", value: detail.identity_hidden ? "Not shown to your role" : detail.primary_guest ?? "—" },
                { label: "Reservation", value: detail.identity_hidden ? "Not shown to your role" : detail.external_reservation_id ?? detail.message_reservation ?? "—" },
                { label: "Stay status", value: detail.stay_status?.replace(/_/g, " ").toLowerCase() ?? "—" },
                { label: "PMS connection", value: detail.pms_interface_label || "—" },
                { label: "Message type", value: `${typeWords(detail.event_type)} (${detail.event_type})` },
                { label: "Reason code", value: detail.review_code ? `${reviewCodeWords(detail.review_code)} (${detail.review_code})` : "—" },
                { label: "Time at the PMS", value: detail.pms_timestamp_utc ? formatDate(detail.pms_timestamp_utc) : "—" },
                { label: "Received here", value: formatDate(detail.received_at) },
                {
                  label: "PMS message id",
                  span: true,
                  value: detail.external_event_identity
                    ? <MonoId value={detail.external_event_identity} head={24} title="PMS message id" />
                    : "Not shown to your role",
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
