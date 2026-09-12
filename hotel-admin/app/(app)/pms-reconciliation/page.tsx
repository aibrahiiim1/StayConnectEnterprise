"use client";

// PMS RECONCILIATION — the departures the appliance could not place, as decisions rather than as rows.
//
// WHAT THIS PAGE IS FOR. The dashboard said "12,026 messages need attention". There were 397 unresolved
// departures; each one had been restaged on every reconnect for three weeks. A list of twelve thousand is a
// list nobody opens, so the warning was true and ignored, which is the worst state a warning can be in.
//
// THERE IS NO ACTION ON THIS PAGE, AND THAT IS THE ANSWER RATHER THAN A GAP. It briefly had a "re-evaluate"
// button that handed a recorded departure back to the ingestion engine. It could never have worked:
// stay_events is strictly one-way, and a checkout boundary must be an APPLIED departure event. A departure
// that went to review is resolved by the PMS sending one that can be applied — the PMS is the source of
// truth for whether a guest has left, and this system asserting it from a re-read of an old message would be
// claiming to know something it does not.
//
// THE FOUR RULES THIS PAGE OBEYS, all of them because breaking one would disconnect a resident guest:
//
//   1. A PLANNED DEPARTURE DATE IS NOT A CHECKOUT. "Past their departure date" is its own tab and closes
//      nothing. Guests extend; PMSs run late.
//   2. TWO STAYS IN ONE ROOM ARE NOT A DUPLICATE. Sharing is ordinary. The rooms tab is titled as a fact,
//      not as a fault, and it exists because it is what makes a room-keyed departure undecidable.
//   3. AN OLD DEPARTURE NEVER APPLIES TO TODAY'S OCCUPANT. If the one stay in that room arrived after the
//      departure was raised, the case is LATER OCCUPANT and there is no button.
//   4. AN EMPTY ROOM DOES NOT PROVE AN OLD DEPARTURE WAS HANDLED. Those cases read "nothing outstanding",
//      never "resolved", because nobody proved anything — the subject simply left.
//
// AND THE ONE IT OBEYS MOST: A CASE WITH NO ANSWER STAYS ON THE LIST. The count is not reduced by hiding
// anything. Every case says which evidence it is waiting for.

import { useCallback, useEffect, useState } from "react";
import { ClipboardCheck, DoorOpen, CalendarClock, RefreshCw, Users } from "lucide-react";
import {
  api, ReconciliationCase, ReconciliationState, ReconciliationSummary,
  MultiOccupancyRoom, StayPastDeparture, Whoami,
} from "@/lib/api";
import { formatDate } from "@/lib/utils";
import { PageShell, PageHeader, StatCard, Toolbar } from "@/components/ui/page";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { SkeletonRows } from "@/components/ui/misc";

// STATE_WORDS is the vocabulary. Each entry says what the state means and, for the ones that cannot be
// acted on, WHAT EVIDENCE IS MISSING — because "we can't do anything" without a reason is indistinguishable
// from "we didn't try".
const STATE_WORDS: Record<
  ReconciliationState,
  { label: string; tone: "ok" | "warn" | "err" | "info" | "neutral"; meaning: string }
> = {
  RESOLVABLE: {
    label: "The PMS can settle this now",
    tone: "ok",
    meaning:
      "Exactly one stay matches, it began before this departure was raised, and the PMS's own latest " +
      "complete in-house list does not contain it. Two independent facts agree that this guest has left, " +
      "so a departure sent from the PMS for this room will apply cleanly.",
  },
  SUPERSEDED_ROOM_EMPTY: {
    label: "Nothing outstanding",
    tone: "neutral",
    meaning:
      "No one is in that room now, so there is no stay this departure could close. Note that this is not " +
      "proof the departure was ever applied — only that nothing is waiting on it.",
  },
  ROOM_SHARED: {
    label: "Room has more than one stay",
    tone: "warn",
    meaning:
      "Sharing a room is ordinary and legal. A departure that names only a room cannot say which of them " +
      "it is about, and guessing would check out a guest who is still resident. Needs the reservation " +
      "number from the PMS.",
  },
  LATER_OCCUPANT: {
    label: "A later guest is in that room",
    tone: "warn",
    meaning:
      "The stay in that room began after this departure was raised, so the departure belongs to somebody " +
      "who has already gone. Applying it would check out the current guest. Needs the reservation number.",
  },
  ROSTER_CONTRADICTS: {
    label: "The PMS still lists them as in house",
    tone: "info",
    meaning:
      "The matching stay appears on the PMS's own latest complete in-house list. Fresh evidence says the " +
      "guest is here, so the older departure is not applied. Needs the PMS to say which is right.",
  },
  NEEDS_PMS_EVIDENCE: {
    label: "Needs evidence from the PMS",
    tone: "warn",
    meaning:
      "Nothing available is enough to decide this one — most often because no complete in-house list has " +
      "been received to compare against.",
  },
};

const num = (v: number) => v.toLocaleString();

export default function PMSReconciliationPage() {
  const [roles, setRoles] = useState<string[]>([]);
  const [summary, setSummary] = useState<ReconciliationSummary | null>(null);
  const [cases, setCases] = useState<ReconciliationCase[] | null>(null);
  const [rooms, setRooms] = useState<MultiOccupancyRoom[]>([]);
  const [stays, setStays] = useState<StayPastDeparture[]>([]);
  const [tab, setTab] = useState<"cases" | "rooms" | "past">("cases");
  const [filter, setFilter] = useState<ReconciliationState | "">("");
  const [err, setErr] = useState<unknown>(null);


  const load = useCallback(async () => {
    try {
      const q = filter ? `?state=${encodeURIComponent(filter)}` : "";
      const [s, c, r, p] = await Promise.all([
        api.get<ReconciliationSummary>("/pms-reconciliation/summary"),
        api.get<{ cases: ReconciliationCase[] }>(`/pms-reconciliation${q}`),
        api.get<{ rooms: MultiOccupancyRoom[] }>("/pms-reconciliation/rooms"),
        api.get<{ stays: StayPastDeparture[] }>("/pms-reconciliation/past-departure"),
      ]);
      setSummary(s);
      setCases(c.cases ?? []);
      setRooms(r.rooms ?? []);
      setStays(p.stays ?? []);
      setErr(null);
    } catch (e) {
      setErr(e);
      setCases([]);
    }
  }, [filter]);

  useEffect(() => {
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => {});
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  return (
    <PageShell>
      <PageHeader
        title="PMS reconciliation"
        description="Departures the appliance received but could not match to exactly one stay."
      />

      <ErrorBanner err={err} />
      {/* THE HEADLINE THAT REPLACED A ROW COUNT. Both numbers are shown: the number of decisions, and the
          number of recorded copies behind them. The second is the single most useful fact about this feed
          and is exactly what the old count was reporting as if it were the first. */}
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard label="Unresolved departures" value={summary ? num(summary.cases) : "—"} />
        <StatCard
          label="Recorded copies of them"
          value={summary ? num(summary.recorded_rows) : "—"}
          hint="One departure is restaged every time the PMS connection is re-established."
        />
        <StatCard
          label="The PMS could settle now"
          value={summary ? num(summary.by_state.find((b) => b.state === "RESOLVABLE")?.cases ?? 0) : "—"}
          hint="Two independent facts already agree; a departure sent from the PMS would apply cleanly."
        />
        <StatCard
          label="Rooms with more than one stay"
          value={summary ? num(summary.rooms_multi_occupancy) : "—"}
          hint="Shared occupancy is ordinary — it is what makes a room-only departure undecidable."
        />
      </div>

      <Callout tone="neutral" title="How a case on this list is resolved">
        <strong>By the PMS, not from here.</strong> A departure the appliance could not place is answered when
        the PMS sends one it can — that is why there is no button on this page. The property&apos;s lever is
        the PMS record itself.
        <br />
        <br />
        This screen does not close a stay because a planned departure date has passed, does not treat two
        stays in one room as a duplicate, and never applies an old room-only departure to whoever is in that
        room today. A case it cannot answer stays on this list and says which evidence is missing.
      </Callout>

      <Toolbar>
        <Button variant={tab === "cases" ? "primary" : "ghost"} size="sm" onClick={() => setTab("cases")}>
          <ClipboardCheck className="mr-1 h-4 w-4" /> Unresolved departures
        </Button>
        <Button variant={tab === "rooms" ? "primary" : "ghost"} size="sm" onClick={() => setTab("rooms")}>
          <Users className="mr-1 h-4 w-4" /> Rooms with several stays ({rooms.length})
        </Button>
        <Button variant={tab === "past" ? "primary" : "ghost"} size="sm" onClick={() => setTab("past")}>
          <CalendarClock className="mr-1 h-4 w-4" /> Past their departure date ({stays.length})
        </Button>
        <Button variant="ghost" size="sm" onClick={() => void load()}>
          <RefreshCw className="mr-1 h-4 w-4" /> Refresh
        </Button>
      </Toolbar>

      {tab === "cases" && (
        <>
          {summary && summary.by_state.length > 0 && (
            <div className="flex flex-wrap gap-2">
              <Button
                size="sm"
                variant={filter === "" ? "secondary" : "ghost"}
                onClick={() => setFilter("")}
              >
                All ({num(summary.cases)})
              </Button>
              {summary.by_state.map((b) => (
                <Button
                  key={b.state}
                  size="sm"
                  variant={filter === b.state ? "secondary" : "ghost"}
                  onClick={() => setFilter(b.state)}
                >
                  {STATE_WORDS[b.state]?.label ?? b.state} ({num(b.cases)})
                </Button>
              ))}
            </div>
          )}

          <Card>
            {cases === null ? (
              <SkeletonRows rows={6} cols={6} />
            ) : cases.length === 0 ? (
              <EmptyState
                icon={<ClipboardCheck />}
                title="No unresolved departures"
                hint="Every departure the PMS has sent was matched to exactly one stay."
              />
            ) : (
              <Table>
                <THead>
                  <TR>
                    <TH>Room</TH>
                    <TH>Reservation</TH>
                    <TH>Departure raised</TH>
                    <TH>Recorded copies</TH>
                    <TH>State</TH>
                  </TR>
                </THead>
                <tbody>
                  {cases.map((c) => {
                    const w = STATE_WORDS[c.resolution_state];
                    return (
                      <TR key={c.case_key}>
                        <TD className="font-mono text-xs">{c.room || "—"}</TD>
                        <TD className="font-mono text-xs">{c.reservation || "—"}</TD>
                        <TD className="whitespace-nowrap text-sm text-muted-foreground">
                          {c.event_at ? formatDate(c.event_at) : formatDate(c.last_seen_at)}
                        </TD>
                        <TD className="text-sm">
                          {num(c.repeat_count)}
                          <span className="ml-1 text-xs text-muted-foreground">
                            across {num(c.generations)} reconnect{c.generations === 1 ? "" : "s"}
                          </span>
                        </TD>
                        <TD>
                          <Badge tone={w?.tone ?? "neutral"} dot>
                            {w?.label ?? c.resolution_state}
                          </Badge>
                          <p className="mt-1 max-w-md text-xs text-muted-foreground">{w?.meaning}</p>
                        </TD>
                      </TR>
                    );
                  })}
                </tbody>
              </Table>
            )}
          </Card>
        </>
      )}

      {tab === "rooms" && (
        <Card>
          <CardBody className="pb-0">
            {/* TITLED AS A FACT, NOT A FAULT. */}
            <Callout tone="neutral" title="Sharing a room is ordinary">
              These rooms currently hold more than one in-house stay. That is legitimate — several occupants,
              connecting rooms, a group booking. It is listed here because it is the reason a departure that
              names only a room cannot be matched to one guest.
            </Callout>
          </CardBody>
          {rooms.length === 0 ? (
            <EmptyState icon={<Users />} title="Every occupied room holds exactly one stay" />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Room</TH>
                  <TH>Stays in the room</TH>
                  <TH>Earliest arrival</TH>
                  <TH>Latest planned departure</TH>
                </TR>
              </THead>
              <tbody>
                {rooms.map((r) => (
                  <TR key={r.room}>
                    <TD className="font-mono text-xs">{r.room}</TD>
                    <TD className="text-sm">{r.stays_in_room}</TD>
                    <TD className="text-sm text-muted-foreground">
                      {r.earliest_arrival ? formatDate(r.earliest_arrival) : "—"}
                    </TD>
                    <TD className="text-sm text-muted-foreground">
                      {r.latest_planned_departure ? formatDate(r.latest_planned_departure) : "—"}
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </Card>
      )}

      {tab === "past" && (
        <Card>
          <CardBody className="pb-0">
            {/* THE SENTENCE THAT KEEPS THIS TAB HONEST. */}
            <Callout tone="neutral" title="A planned departure date is not a checkout">
              These stays are still in house in our mirror and their planned departure date has passed.
              Nothing here is closed on that basis — guests extend, and a PMS can process a departure late.
              The column that decides what it means is the one beside it: whether the stay still appears on
              the PMS&apos;s own latest complete in-house list.
            </Callout>
          </CardBody>
          {stays.length === 0 ? (
            <EmptyState icon={<DoorOpen />} title="No in-house stay is past its planned departure" />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Room</TH>
                  <TH>Reservation</TH>
                  <TH>Planned departure</TH>
                  <TH>Days past</TH>
                  <TH>On the PMS&apos;s current list</TH>
                </TR>
              </THead>
              <tbody>
                {stays.map((s) => (
                  <TR key={s.stay_id}>
                    <TD className="font-mono text-xs">{s.room || "—"}</TD>
                    <TD className="font-mono text-xs">{s.reservation || "—"}</TD>
                    <TD className="text-sm text-muted-foreground">
                      {s.departure ? formatDate(s.departure) : "—"}
                    </TD>
                    <TD className="text-sm">{s.days_past_departure}</TD>
                    <TD>
                      {s.roster_present ? (
                        <Badge tone="info" dot>
                          Still listed — they may have extended
                        </Badge>
                      ) : (
                        <Badge tone="warn" dot>
                          Not listed — our mirror may be behind
                        </Badge>
                      )}
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </Card>
      )}

    </PageShell>
  );
}
