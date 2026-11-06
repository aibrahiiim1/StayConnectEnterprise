"use client";

import { usePmsConnections } from "@/lib/pms-scope";
import { PmsTag } from "@/components/pms/pms-tag";
import { useCallback, useEffect, useState } from "react";
import { ClipboardCheck, DoorOpen, PlugZap, Users } from "lucide-react";
import {
  api, ReconciliationCase, ReconciliationState, ReconciliationSummary,
  MultiOccupancyRoom, StayPastDeparture,
} from "@/lib/api";
import { cn, formatDate } from "@/lib/utils";
import { PageShell, PageHeader, StatCard, StatGrid } from "@/components/ui/page";
import { Card } from "@/components/ui/card";
import { Table, TBody, THead, TR, TH, TD } from "@/components/ui/table";
import { StatusBadge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { HelpSection } from "@/components/help";
import { SkeletonRows } from "@/components/ui/misc";
import { DescriptionList, FilterChips } from "@/components/ui/data";
import { FilterBar } from "@/components/ui/toolbar";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { LiveStatus, refreshingClass } from "@/components/ui/patterns";
import { FADE, LoadFailure, StaleNotice } from "@/app/(app)/backups/load-failure";

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
      "No one is in that room now, so there is no stay this departure could close. This is not proof the " +
      "departure was ever applied, only that nothing is waiting on it.",
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
      "Nothing available is enough to decide this one. Most often no complete in-house list has been " +
      "received to compare against.",
  },
};

const num = (v: number) => v.toLocaleString();

type Read<T> = { data: T | null; failed: boolean };
const NOT_READ = { data: null, failed: false };

type Outcome<T> = { ok: true; value: T } | { ok: false; notOn: boolean };

async function readOne<T>(path: string): Promise<Outcome<T>> {
  try {
    return { ok: true, value: await api.get<T>(path) };
  } catch (e) {
    return { ok: false, notOn: (e as { status?: unknown } | null)?.status === 404 };
  }
}

function settle<T>(set: React.Dispatch<React.SetStateAction<Read<T>>>, o: Outcome<T>) {
  if (o.ok) set({ data: o.value, failed: false });
  else set((prev) => ({ data: prev.data, failed: true }));
}

export default function PMSReconciliationPage() {
  const [summary, setSummary] = useState<Read<ReconciliationSummary>>(NOT_READ);
  const [cases, setCases] = useState<Read<ReconciliationCase[]>>(NOT_READ);
  const [rooms, setRooms] = useState<Read<MultiOccupancyRoom[]>>(NOT_READ);
  const [stays, setStays] = useState<Read<StayPastDeparture[]>>(NOT_READ);
  const [notOn, setNotOn] = useState(false);
  const [tab, setTab] = useState<"cases" | "rooms" | "past">("cases");
  const [filter, setFilter] = useState<ReconciliationState | "">("");
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [refreshing, setRefreshing] = useState(false);
  const [viewing, setViewing] = useState<string | null>(null);

  const load = useCallback(async () => {
    setRefreshing(true);
    const q = filter ? `?state=${encodeURIComponent(filter)}` : "";
    const [s, c, r, p] = await Promise.all([
      readOne<ReconciliationSummary>("/pms-reconciliation/summary"),
      readOne<{ cases: ReconciliationCase[] }>(`/pms-reconciliation${q}`),
      readOne<{ rooms: MultiOccupancyRoom[] }>("/pms-reconciliation/rooms"),
      readOne<{ stays: StayPastDeparture[] }>("/pms-reconciliation/past-departure"),
    ]);
    const all = [s, c, r, p];
    setNotOn(all.every((o) => !o.ok && o.notOn));
    settle(setSummary, s);
    settle(setCases, c.ok ? { ok: true, value: c.value.cases ?? [] } : c);
    settle(setRooms, r.ok ? { ok: true, value: r.value.rooms ?? [] } : r);
    settle(setStays, p.ok ? { ok: true, value: p.value.stays ?? [] } : p);
    if (all.every((o) => o.ok)) setUpdatedAt(Date.now());
    setRefreshing(false);
  }, [filter]);

  useEffect(() => {
    void load();
  }, [load]);

  const caseId = (c: { pms_interface_id: string; case_key: string }) => `${c.pms_interface_id}:${c.case_key}`;
  const { multi } = usePmsConnections();
  const selectedCase = cases.data?.find((c) => caseId(c) === viewing) ?? null;
  const selectedWords = selectedCase ? STATE_WORDS[selectedCase.resolution_state] : undefined;
  const reads = [summary, cases, rooms, stays];
  const staleAfterGood = reads.some((x) => x.failed && x.data !== null);
  const stat = (v: number | undefined) =>
    v !== undefined ? num(v) : summary.failed ? "Not known" : "Loading";
  const counted = (label: string, list: unknown[] | null) => list ? `${label} (${list.length})` : label;
  const busy = refreshing && reads.some((x) => x.data !== null);

  const failedOrLoading = (read: Read<unknown>, what: string, cols: number) =>
    read.failed
      ? <LoadFailure what={what} busy={refreshing} onRetry={() => void load()} />
      : <SkeletonRows rows={4} cols={cols} />;

  const help = (
    <>
      <HelpSection title="How a case on this list is resolved">
        <p>
          <strong>By the PMS, not from here.</strong> This screen is read-only: there is no action on it. A
          departure the appliance could not place is answered when the PMS sends one it can, which is why there is
          no button on this page. The site&apos;s lever is the PMS record itself.
        </p>
        <p>
          This screen does not close a stay because a planned departure date has passed, does not treat two
          stays in one room as a duplicate, and never applies an old room-only departure to whoever is in that
          room today. A case it cannot answer stays on this list and says which evidence is missing.
        </p>
      </HelpSection>
      <HelpSection title="Copies received">
        <p>
          One departure is restaged every time the PMS connection is re-established, so a single departure
          can arrive several times.
        </p>
      </HelpSection>
      <HelpSection title="Rooms with several stays">
        <p>
          These rooms currently hold more than one in-house stay. That is legitimate: several occupants,
          connecting rooms, a group booking. They are listed because it is the reason a departure that names
          only a room cannot be matched to one guest.
        </p>
      </HelpSection>
      <HelpSection title="Past their departure date">
        <p>
          A planned departure date is not a checkout. These stays are still in house in this
          appliance&apos;s guest list and their planned departure date has passed. Nothing is closed on that
          basis: guests extend, and a PMS can process a departure late. The column that decides what it
          means is the one beside it: whether the stay still appears on the PMS&apos;s own latest complete
          in-house list.
        </p>
      </HelpSection>
    </>
  );

  if (notOn) {
    return (
      <PageShell>
        <PageHeader
          title="Unresolved departures"
          description="Departures the appliance received but could not match to exactly one stay."
          help={help}
        />
        <Card className={FADE}>
          <EmptyState
            size="sm"
            icon={<PlugZap />}
            title="PMS administration is not turned on for this appliance"
            hint="So there is nothing to reconcile here."
          />
        </Card>
      </PageShell>
    );
  }

  return (
    <PageShell>
      <PageHeader
        title="Unresolved departures"
        description="Departures the appliance received but could not match to exactly one stay."
        help={help}
        actions={
          <LiveStatus updatedAt={updatedAt} refreshing={refreshing} error={staleAfterGood} onRefresh={() => void load()} />
        }
      />

      {staleAfterGood && <StaleNotice what="Part of this page" />}
      <StatGrid cols={4}>
        <StatCard label="Unresolved departures" value={stat(summary.data?.cases)} />
        <StatCard
          label="Copies received"
          value={stat(summary.data?.recorded_rows)}
          hint="Restaged on every reconnect"
        />
        <StatCard
          label="The PMS could settle now"
          value={stat(summary.data ? summary.data.by_state.find((b) => b.state === "RESOLVABLE")?.cases ?? 0 : undefined)}
          hint="A PMS departure would apply cleanly"
        />
        <StatCard
          label="Rooms with more than one stay"
          value={stat(summary.data?.rooms_multi_occupancy)}
          hint="Shared occupancy is ordinary"
        />
      </StatGrid>

      <Tabs value={tab} onValueChange={(v) => setTab(v as typeof tab)}>
        <TabsList aria-label="Which view">
          <TabsTrigger value="cases">Unresolved departures</TabsTrigger>
          <TabsTrigger value="rooms">{counted("Rooms with several stays", rooms.data)}</TabsTrigger>
          <TabsTrigger value="past">{counted("Past their departure date", stays.data)}</TabsTrigger>
        </TabsList>

        <div className={cn("mt-5", busy && refreshingClass)}>
          <TabsContent value="cases">
            <Card className="overflow-hidden">
              {summary.data && summary.data.by_state.length > 0 && (
                <FilterBar
                  inset
                  filters={
                    <FilterChips
                      label="Filter by state"
                      value={filter}
                      onChange={(v) => setFilter(v as ReconciliationState | "")}
                      options={[
                        { value: "" as ReconciliationState | "", label: "All", count: summary.data.cases },
                        ...summary.data.by_state.map((b) => ({
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
              {cases.data === null ? (
                failedOrLoading(cases, "The unresolved departures", 6)
              ) : cases.data.length === 0 ? (
                <div className={FADE}>
                  {filter ? (
                    <EmptyState size="sm" icon={<ClipboardCheck />} title="No departure is in this state" />
                  ) : (
                    <EmptyState
                      size="sm"
                      icon={<ClipboardCheck />}
                      title="No unresolved departures"
                      hint="Every departure the PMS has sent was matched to exactly one stay."
                    />
                  )}
                </div>
              ) : (
                <div className={FADE}>
                  <Table label="Unresolved departures">
                    <THead>
                      <TR>
                        <TH>Room</TH>
                        {multi && <TH>PMS</TH>}
                        <TH>Reservation</TH>
                        <TH>Departure raised</TH>
                        <TH numeric>Copies received</TH>
                        <TH>State</TH>
                      </TR>
                    </THead>
                    <TBody>
                      {cases.data.map((c) => {
                        const w = STATE_WORDS[c.resolution_state];
                        return (
                          <TR key={caseId(c)} onClick={() => setViewing(caseId(c))} selected={viewing === caseId(c)}>
                            <TD nowrap className="font-mono text-xs">{c.room || "No room"}</TD>
                            {multi && <TD nowrap><PmsTag label={c.pms_interface_label} /></TD>}
                            <TD nowrap className="font-mono text-xs">{c.reservation || "Not recorded"}</TD>
                            <TD nowrap muted>{c.event_at ? formatDate(c.event_at) : "Not recorded"}</TD>
                            <TD numeric>
                              {num(c.repeat_count)}
                              <span className="ms-1 text-xs text-muted-foreground">
                                across {num(c.generations)} reconnect{c.generations === 1 ? "" : "s"}
                              </span>
                            </TD>
                            <TD nowrap>
                              <StatusBadge tone={w?.tone ?? "neutral"}>{w?.label ?? "Another state"}</StatusBadge>
                            </TD>
                          </TR>
                        );
                      })}
                    </TBody>
                  </Table>
                </div>
              )}
            </Card>
          </TabsContent>

          <TabsContent value="rooms">
            <Card className="overflow-hidden">
              {rooms.data === null ? (
                failedOrLoading(rooms, "The rooms with several stays", 4)
              ) : rooms.data.length === 0 ? (
                <div className={FADE}><EmptyState size="sm" icon={<Users />} title="Every occupied room holds exactly one stay" /></div>
              ) : (
                <div className={FADE}>
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
                      {rooms.data.map((r) => (
                        <TR key={`${r.pms_interface_id}:${r.room}`}>
                          <TD nowrap className="font-mono text-xs">{r.room}</TD>
                          {multi && <TD nowrap><PmsTag label={r.pms_interface_label} /></TD>}
                          <TD numeric>{r.stays_in_room}</TD>
                          <TD nowrap muted>{r.earliest_arrival ? formatDate(r.earliest_arrival) : "Not recorded"}</TD>
                          <TD nowrap muted>{r.latest_planned_departure ? formatDate(r.latest_planned_departure) : "Not recorded"}</TD>
                        </TR>
                      ))}
                    </TBody>
                  </Table>
                </div>
              )}
            </Card>
          </TabsContent>

          <TabsContent value="past">
            <Card className="overflow-hidden">
              {stays.data === null ? (
                failedOrLoading(stays, "The stays past their departure date", 5)
              ) : stays.data.length === 0 ? (
                <div className={FADE}><EmptyState size="sm" icon={<DoorOpen />} title="No in-house stay is past its planned departure" /></div>
              ) : (
                <div className={FADE}>
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
                      {stays.data.map((s) => (
                        <TR key={s.stay_id}>
                          <TD nowrap className="font-mono text-xs">{s.room || "No room"}</TD>
                          {multi && <TD nowrap><PmsTag label={s.pms_interface_label} /></TD>}
                          <TD nowrap className="font-mono text-xs">{s.reservation || "Not recorded"}</TD>
                          <TD nowrap muted>{s.departure ? formatDate(s.departure) : "Not recorded"}</TD>
                          <TD numeric>{s.days_past_departure}</TD>
                          <TD>
                            {s.roster_present ? (
                              <StatusBadge tone="info">Still listed: they may have extended</StatusBadge>
                            ) : s.room_now_holds_another_stay ? (
                              <StatusBadge tone="err">Room re-let: the PMS has it under another reservation</StatusBadge>
                            ) : (
                              <StatusBadge tone="warn">Not listed: this appliance&apos;s guest list may be behind</StatusBadge>
                            )}
                          </TD>
                        </TR>
                      ))}
                    </TBody>
                  </Table>
                </div>
              )}
            </Card>
          </TabsContent>
        </div>
      </Tabs>

      <DetailsDrawer
        open={selectedCase !== null}
        onOpenChange={(v) => { if (!v) setViewing(null); }}
        eyebrow="Unresolved departure"
        title={selectedCase ? `${selectedCase.room ? `Room ${selectedCase.room}` : "No room"}${multi && selectedCase.pms_interface_label ? ` · ${selectedCase.pms_interface_label}` : ""}` : ""}
        description={selectedCase?.reservation ? `Reservation ${selectedCase.reservation}` : undefined}
        badges={selectedCase ? (
          <StatusBadge tone={selectedWords?.tone ?? "neutral"}>{selectedWords?.label ?? "Another state"}</StatusBadge>
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
                  { label: "Room", value: selectedCase.room || "No room" },
                  { label: "PMS connection", value: selectedCase.pms_interface_label || "Not recorded" },
                  { label: "Reservation", value: selectedCase.reservation || "Not recorded" },
                  { label: "Departure raised", value: selectedCase.event_at ? formatDate(selectedCase.event_at) : "Not recorded" },
                  { label: "Last seen", value: formatDate(selectedCase.last_seen_at) },
                  { label: "Copies received", value: `${num(selectedCase.repeat_count)} across ${num(selectedCase.generations)} reconnect${selectedCase.generations === 1 ? "" : "s"}` },
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
