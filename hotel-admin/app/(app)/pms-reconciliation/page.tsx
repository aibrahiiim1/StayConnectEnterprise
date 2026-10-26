"use client";

// Read-only by design: stay_events is one-way and a checkout boundary must be an applied departure event, so
// a case is resolved when the PMS sends one that applies. Rules, each to avoid disconnecting a resident guest:
//   1. A planned departure date is not a checkout (guests extend, PMSs run late).
//   2. Two stays in one room are not a duplicate (sharing is ordinary).
//   3. An old departure never applies to today's occupant (LATER_OCCUPANT has no action).
//   4. An empty room does not prove an old departure was handled ("nothing outstanding", never "resolved").
// A case with no answer stays on the list, saying which evidence it waits for.

import { usePmsConnections } from "@/lib/pms-scope";
import { PmsTag } from "@/components/pms/pms-tag";
import { useCallback, useEffect, useState } from "react";
import { ClipboardCheck, DoorOpen, Users } from "lucide-react";
import {
  api, ReconciliationCase, ReconciliationState, ReconciliationSummary,
  MultiOccupancyRoom, StayPastDeparture, Whoami,
} from "@/lib/api";
import { cn, formatDate } from "@/lib/utils";
import { PageShell, PageHeader, StatCard, StatGrid } from "@/components/ui/page";
import { Card } from "@/components/ui/card";
import { Table, TBody, THead, TR, TH, TD } from "@/components/ui/table";
import { StatusBadge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { HelpSection } from "@/components/help";
import { SkeletonRows } from "@/components/ui/misc";
import { DescriptionList, FilterChips } from "@/components/ui/data";
import { FilterBar } from "@/components/ui/toolbar";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { LiveStatus, refreshingClass } from "@/components/ui/patterns";

// Each entry says what the state means and, where nothing can be done, which evidence is missing.
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
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [viewing, setViewing] = useState<string | null>(null);

  const load = useCallback(async () => {
    setRefreshing(true);
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
      setUpdatedAt(Date.now());
    } catch (e) {
      setErr(e);
      // Keep the last answer under the error rather than blanking it.
      setCases((prev) => prev ?? []);
    } finally {
      setRefreshing(false);
    }
  }, [filter]);

  useEffect(() => {
    api.get<Whoami>("/auth/whoami").then((m) => setRoles(m.roles ?? [])).catch(() => {});
  }, []);
  useEffect(() => {
    void load();
  }, [load]);

  const caseId = (c: { pms_interface_id: string; case_key: string }) => `${c.pms_interface_id}:${c.case_key}`;
  const { multi } = usePmsConnections();
  const selectedCase = cases?.find((c) => caseId(c) === viewing) ?? null;
  const selectedWords = selectedCase ? STATE_WORDS[selectedCase.resolution_state] : undefined;

  return (
    <PageShell>
      <PageHeader
        title="Unresolved departures"
        description="Departures the appliance received but could not match to exactly one stay."
        help={
          <>
            <HelpSection title="How a case on this list is resolved">
              <p>
                <strong>By the PMS, not from here.</strong> This screen is read-only: there is no action on it. A
                departure the appliance could not place is answered when the PMS sends one it can — that is why
                there is no button on this page. The site&apos;s lever is the PMS record itself.
              </p>
              <p>
                This screen does not close a stay because a planned departure date has passed, does not treat two
                stays in one room as a duplicate, and never applies an old room-only departure to whoever is in that
                room today. A case it cannot answer stays on this list and says which evidence is missing.
              </p>
            </HelpSection>
            <HelpSection title="Recorded copies">
              <p>
                One departure is restaged every time the PMS connection is re-established, so a single departure
                can have several recorded copies.
              </p>
            </HelpSection>
            <HelpSection title="Rooms with several stays">
              <p>
                These rooms currently hold more than one in-house stay. That is legitimate — several occupants,
                connecting rooms, a group booking. They are listed because it is the reason a departure that names
                only a room cannot be matched to one guest.
              </p>
            </HelpSection>
            <HelpSection title="Past their departure date">
              <p>
                A planned departure date is not a checkout. These stays are still in house in this
                appliance&apos;s guest list and their planned departure date has passed. Nothing is closed on that
                basis — guests extend, and a PMS can process a departure late. The column that decides what it
                means is the one beside it: whether the stay still appears on the PMS&apos;s own latest complete
                in-house list.
              </p>
            </HelpSection>
          </>
        }
        actions={
          <LiveStatus updatedAt={updatedAt} refreshing={refreshing} error={!!err && cases !== null} onRefresh={() => void load()} />
        }
      />

      <ErrorBanner err={err} />
      <StatGrid cols={4}>
        <StatCard label="Unresolved departures" value={summary ? num(summary.cases) : "—"} />
        <StatCard
          label="Recorded copies of them"
          value={summary ? num(summary.recorded_rows) : "—"}
          hint="Restaged on every reconnect"
        />
        <StatCard
          label="The PMS could settle now"
          value={summary ? num(summary.by_state.find((b) => b.state === "RESOLVABLE")?.cases ?? 0) : "—"}
          hint="A PMS departure would apply cleanly"
        />
        <StatCard
          label="Rooms with more than one stay"
          value={summary ? num(summary.rooms_multi_occupancy) : "—"}
          hint="Shared occupancy is ordinary"
        />
      </StatGrid>

      <Tabs value={tab} onValueChange={(v) => setTab(v as typeof tab)}>
        <TabsList aria-label="Which view">
          <TabsTrigger value="cases">Unresolved departures</TabsTrigger>
          <TabsTrigger value="rooms">Rooms with several stays ({rooms.length})</TabsTrigger>
          <TabsTrigger value="past">Past their departure date ({stays.length})</TabsTrigger>
        </TabsList>

        <div className={cn("mt-5", refreshing && cases !== null && refreshingClass)}>
          <TabsContent value="cases">
            <Card className="overflow-hidden">
              {summary && summary.by_state.length > 0 && (
                <FilterBar
                  inset
                  filters={
                    <FilterChips
                      label="Filter by state"
                      value={filter}
                      onChange={(v) => setFilter(v as ReconciliationState | "")}
                      options={[
                        { value: "" as ReconciliationState | "", label: "All", count: summary.cases },
                        ...summary.by_state.map((b) => ({
                          value: b.state as ReconciliationState | "",
                          label: STATE_WORDS[b.state]?.label ?? b.state.replace(/_/g, " ").toLowerCase(),
                          count: b.cases,
                          tone: (STATE_WORDS[b.state]?.tone === "neutral" ? undefined : STATE_WORDS[b.state]?.tone) as "ok" | "warn" | "err" | "info" | undefined,
                        })),
                      ]}
                    />
                  }
                />
              )}
              {cases === null ? (
                <SkeletonRows rows={6} cols={6} />
              ) : cases.length === 0 ? (
                <EmptyState
                  size="sm"
                  icon={<ClipboardCheck />}
                  title="No unresolved departures"
                  hint="Every departure the PMS has sent was matched to exactly one stay."
                />
              ) : (
                <Table label="Unresolved departures">
                  <THead>
                    <TR>
                      <TH>Room</TH>
                      {multi && <TH>PMS</TH>}
                      <TH>Reservation</TH>
                      <TH>Departure raised</TH>
                      <TH numeric>Recorded copies</TH>
                      <TH>State</TH>
                    </TR>
                  </THead>
                  <TBody>
                    {cases.map((c) => {
                      const w = STATE_WORDS[c.resolution_state];
                      return (
                        <TR key={caseId(c)} onClick={() => setViewing(caseId(c))} selected={viewing === caseId(c)}>
                          <TD nowrap className="font-mono text-xs">{c.room || "—"}</TD>
                          {multi && <TD nowrap><PmsTag label={c.pms_interface_label} /></TD>}
                          <TD nowrap className="font-mono text-xs">{c.reservation || "—"}</TD>
                          <TD nowrap muted>
                            {c.event_at ? formatDate(c.event_at) : formatDate(c.last_seen_at)}
                          </TD>
                          <TD numeric>
                            {num(c.repeat_count)}
                            <span className="ms-1 text-xs text-muted-foreground">
                              across {num(c.generations)} reconnect{c.generations === 1 ? "" : "s"}
                            </span>
                          </TD>
                          <TD nowrap>
                            <StatusBadge tone={w?.tone ?? "neutral"}>{w?.label ?? c.resolution_state}</StatusBadge>
                          </TD>
                        </TR>
                      );
                    })}
                  </TBody>
                </Table>
              )}
            </Card>
          </TabsContent>

          <TabsContent value="rooms">
            <Card className="overflow-hidden">
              {rooms.length === 0 ? (
                <EmptyState size="sm" icon={<Users />} title="Every occupied room holds exactly one stay" />
              ) : (
                <Table label="Rooms with several stays">
                  <THead>
                    <TR>
                      <TH>Room</TH>
                      {multi && <TH>PMS</TH>}
                      <TH numeric>Stays in the room</TH>
                      <TH>Earliest arrival</TH>
                      <TH>Latest planned departure</TH>
                    </TR>
                  </THead>
                  <TBody>
                    {rooms.map((r) => (
                      <TR key={`${r.pms_interface_id}:${r.room}`}>
                        <TD nowrap className="font-mono text-xs">{r.room}</TD>
                        {multi && <TD nowrap><PmsTag label={r.pms_interface_label} /></TD>}
                        <TD numeric>{r.stays_in_room}</TD>
                        <TD nowrap muted>{r.earliest_arrival ? formatDate(r.earliest_arrival) : "—"}</TD>
                        <TD nowrap muted>{r.latest_planned_departure ? formatDate(r.latest_planned_departure) : "—"}</TD>
                      </TR>
                    ))}
                  </TBody>
                </Table>
              )}
            </Card>
          </TabsContent>

          <TabsContent value="past">
            <Card className="overflow-hidden">
              {stays.length === 0 ? (
                <EmptyState size="sm" icon={<DoorOpen />} title="No in-house stay is past its planned departure" />
              ) : (
                <Table label="Past their departure date">
                  <THead>
                    <TR>
                      <TH>Room</TH>
                      {multi && <TH>PMS</TH>}
                      <TH>Reservation</TH>
                      <TH>Planned departure</TH>
                      <TH numeric>Days past</TH>
                      <TH>On the PMS&apos;s current list</TH>
                    </TR>
                  </THead>
                  <TBody>
                    {stays.map((s) => (
                      <TR key={s.stay_id}>
                        <TD nowrap className="font-mono text-xs">{s.room || "—"}</TD>
                        {multi && <TD nowrap><PmsTag label={s.pms_interface_label} /></TD>}
                        <TD nowrap className="font-mono text-xs">{s.reservation || "—"}</TD>
                        <TD nowrap muted>{s.departure ? formatDate(s.departure) : "—"}</TD>
                        <TD numeric>{s.days_past_departure}</TD>
                        <TD>
                          {s.roster_present ? (
                            <StatusBadge tone="info">Still listed — they may have extended</StatusBadge>
                          ) : s.room_now_holds_another_stay ? (
                            // The PMS has the room under another reservation, so our record of this guest is stale.
                            <StatusBadge tone="err">Room re-let — the PMS has it under another reservation</StatusBadge>
                          ) : (
                            <StatusBadge tone="warn">Not listed — this appliance&apos;s guest list may be behind</StatusBadge>
                          )}
                        </TD>
                      </TR>
                    ))}
                  </TBody>
                </Table>
              )}
            </Card>
          </TabsContent>
        </div>
      </Tabs>

      <DetailsDrawer
        open={selectedCase !== null}
        onOpenChange={(v) => { if (!v) setViewing(null); }}
        eyebrow="Unresolved departure"
        title={selectedCase ? `Room ${selectedCase.room || "—"}${multi && selectedCase.pms_interface_label ? ` · ${selectedCase.pms_interface_label}` : ""}` : ""}
        description={selectedCase?.reservation ? `Reservation ${selectedCase.reservation}` : undefined}
        badges={selectedCase ? (
          <StatusBadge tone={selectedWords?.tone ?? "neutral"}>{selectedWords?.label ?? selectedCase.resolution_state}</StatusBadge>
        ) : undefined}
      >
        {selectedCase && (
          <>
            <DetailsSection title="What this state means">
              <p className="text-sm text-muted-foreground">{selectedWords?.meaning ?? "No explanation is recorded for this state."}</p>
            </DetailsSection>
            <DetailsSection title="Case">
              <DescriptionList
                items={[
                  { label: "Room", value: selectedCase.room || "—" },
                  { label: "PMS connection", value: selectedCase.pms_interface_label || "—" },
                  { label: "Reservation", value: selectedCase.reservation || "—" },
                  { label: "Departure raised", value: selectedCase.event_at ? formatDate(selectedCase.event_at) : "—" },
                  { label: "Last seen", value: formatDate(selectedCase.last_seen_at) },
                  { label: "Recorded copies", value: `${num(selectedCase.repeat_count)} across ${num(selectedCase.generations)} reconnect${selectedCase.generations === 1 ? "" : "s"}` },
                ]}
              />
            </DetailsSection>
            <p className="text-xs text-muted-foreground">
              Read-only: a case is resolved when the PMS sends a departure that applies.
            </p>
          </>
        )}
      </DetailsDrawer>
    </PageShell>
  );
}
