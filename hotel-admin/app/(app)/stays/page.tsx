"use client";

// Guest names are PII: never write them to logs or diagnostics.
// Only ADMIN_BLOCK is an operator's (site administrator, reason + password); PMS_NO_POST and PMS_DATA_SUSPECT
// clear only from fresh PMS data and POSTING_UNRESOLVED only from manual review, so the page never offers to lift them.
// A room number belongs to the PMS connection it came from: the same number at two connections is two rooms, never a duplicate.

import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import Link from "next/link";
import { api, Stay, StayDetail, StayPostingBlock, StaysPage as StaysPageResp } from "@/lib/api";
import { useOperatorRoles } from "@/lib/whoami-context";
import { moduleLicensed, surfaceAvailable, useCapabilities } from "@/lib/capabilities";
import {
  POSTING_BLOCK_CLEARED_BY, postingBlockWords, postingSourceWords, saveErrorMessage,
} from "@/lib/payment-admin";
import { ConfirmDialog } from "@/components/ui/dialog";
import { useToast } from "@/components/ui/toast";
import { PageShell, PageHeader, StatCard, StatGrid } from "@/components/ui/page";
import { HelpSection } from "@/components/help";
import { Card, CardFooter } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Badge, StatusBadge, type Tone } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Checkbox, Select } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { Explain } from "@/components/ui/tooltip";
import { SkeletonRows } from "@/components/ui/misc";
import { DescriptionList, MetricStrip, Pagination, ResultsCount, SearchInput } from "@/components/ui/data";
import { FilterBar } from "@/components/ui/toolbar";
import { RowActions } from "@/components/ui/row-actions";
import { formatDate, formatRelative } from "@/lib/utils";
import { speedPair } from "@/lib/session-words";
import { Hotel, Ban, Undo2 } from "lucide-react";
import { usePmsConnections } from "@/lib/pms-scope";
import { PmsTag, roomWithPms } from "@/components/pms/pms-tag";
import { FilterChips } from "@/components/ui/data";

const STATUS_LABELS: Record<string, string> = {
  IN_HOUSE: "In house",
  RESERVED: "Arriving",
  CHECKED_OUT: "Checked out",
  POST_STAY_ACTIVE: "Post-stay access",
  CANCELLED: "Cancelled",
  NO_SHOW: "No show",
};
const STATUSES = ["", "IN_HOUSE", "RESERVED", "CHECKED_OUT", "POST_STAY_ACTIVE", "CANCELLED", "NO_SHOW"];

const toneFor = (status: string): Tone =>
  status === "IN_HOUSE" ? "ok" : status === "CHECKED_OUT" ? "default" : status === "RESERVED" ? "info" : "warn";

const PAGE_SIZES = [25, 50, 100, 200];

const label = (s: string) => STATUS_LABELS[s] ?? s.replace(/_/g, " ").toLowerCase();

/** Arrival/departure are dates in the PMS, not instants. */
function shortDate(v?: string | null): string {
  if (!v) return "—";
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleDateString(undefined, { day: "2-digit", month: "short" });
}

export default function StaysPage() {
  const [status, setStatus] = useState("IN_HOUSE");
  const [q, setQ] = useState("");
  const [vipOnly, setVipOnly] = useState(false);
  // "" = every connection together; otherwise one connection's stays only.
  const [pms, setPms] = useState("");
  const { multi } = usePmsConnections();
  const [pageSize, setPageSize] = useState(PAGE_SIZES[1]);
  const [offset, setOffset] = useState(0);
  const [rows, setRows] = useState<Stay[] | null>(null);
  const [resp, setResp] = useState<StaysPageResp | null>(null);
  const [detail, setDetail] = useState<StayDetail | null>(null);
  const [detailBusy, setDetailBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const toast = useToast();
  const roles = useOperatorRoles();
  const caps = useCapabilities();
  // Site administrator only, and only where room charge is licensed and served; unknown fails closed.
  const canManageRoomCharge =
    (roles?.includes("site_admin") ?? false) &&
    surfaceAvailable(caps, "pms-financial-onboarding") &&
    moduleLicensed(caps, "room_charge");
  const [blockAction, setBlockAction] = useState<"SET" | "CLEAR" | null>(null);
  const [blockBusy, setBlockBusy] = useState(false);
  const [blockErr, setBlockErr] = useState<string | null>(null);

  useEffect(() => { setOffset(0); }, [status, q, vipOnly, pms, pageSize]);

  // The search text goes in a header, not the URL: it is often a guest's name, and edged logs request lines.
  // Responses can arrive out of order, so only the newest request's response is applied.
  const latest = useRef(0);
  const load = useCallback(async () => {
    const mine = ++latest.current;
    setRows(null);
    setErr(null);
    try {
      const params = new URLSearchParams();
      if (status) params.set("status", status);
      if (vipOnly) params.set("vip", "true");
      if (pms) params.set("pms_interface_id", pms);
      params.set("page", String(Math.floor(offset / pageSize) + 1));
      params.set("page_size", String(pageSize));
      const needle = q.trim();
      const r = await api.get<StaysPageResp>(
        "/pms-stays?" + params.toString(),
        // Percent-encoded: browsers refuse non-Latin-1 text in a header.
        needle ? { "X-Stay-Search": encodeURIComponent(needle) } : undefined,
      );
      if (mine !== latest.current) return;
      setResp(r);
      setRows(r.data ?? []);
    } catch (e) {
      if (mine !== latest.current) return;
      setErr(e);
      setResp(null);
      setRows([]);
    }
  }, [status, vipOnly, pms, offset, pageSize, q]);

  useEffect(() => { void load(); }, [load]);

  // Server totals over every matching stay, not just the page on screen.
  const summary = useMemo(() => {
    const s = resp?.summary;
    const list = rows ?? [];
    return {
      total: s?.total ?? list.length,
      withInternet: s?.with_internet ?? list.filter((r) => r.access_status).length,
      online: s?.devices_online ?? list.reduce((a, r) => a + (r.access_active_devices ?? 0), 0),
      arriving: s?.arriving ?? list.filter((r) => r.status === "RESERVED").length,
      vip: s?.vip ?? list.filter((r) => r.vip).length,
    };
  }, [resp, rows]);

  async function open(id: string) {
    if (detailBusy) return;
    setErr(null); setDetailBusy(true);
    try { setDetail(await api.get<StayDetail>("/pms-stays/" + id)); }
    catch (e) { setErr(e); }
    finally { setDetailBusy(false); }
  }

  async function changeAdminBlock(action: "SET" | "CLEAR", reason: string, password: string) {
    if (!detail) return;
    setBlockBusy(true); setBlockErr(null);
    try {
      const r = await api.post<{ result: string }>(
        `/pms-financial-onboarding/stays/${encodeURIComponent(detail.id)}/admin-block`,
        { action, reason, password },
      );
      setBlockAction(null);
      toast.success(
        r?.result === "SET" ? "Room charge blocked for this stay"
          : r?.result === "CLEARED" ? "Administrative block removed"
          : "There was no administrative block to remove",
      );
      await open(detail.id);
      void load();
    } catch (e) {
      setBlockErr(saveErrorMessage(e));
    } finally {
      setBlockBusy(false);
    }
  }

  const inHouseNoInternet =
    status === "IN_HOUSE" && rows !== null && rows.length > 0 && summary.withInternet === 0;
  const filtering = q.trim() !== "" || vipOnly || pms !== "";
  const byPms = resp?.by_interface ?? [];
  const showPms = multi || byPms.length > 1;
  const pmsTotal = byPms.reduce((a, b) => a + b.total, 0);

  return (
    <PageShell width="wide">
      <PageHeader
        title="Stays"
        description="Who each PMS reports in the building, and the internet each room has."
        help={
          <>
            <HelpSection title="Where stays come from">
              <p>
                This is what the property management systems report about who is in the building, and which
                internet package each room has been given. Stays themselves are changed in the PMS, not here.
              </p>
              <p>
                With more than one PMS connection, each stay belongs to the connection that reported it. The same
                room number at two connections is two different rooms, not a duplicate. Use the connection tabs to
                look at one PMS, or leave them on All connections for the combined list.
              </p>
            </HelpSection>
            <HelpSection title="Internet packages on a stay">
              <p>
                A stay has a package once a guest from that room has signed in and been given one &mdash; a package
                is granted at sign-in, not at check-in. A room with no package is not a fault: it usually means
                nobody from it has connected yet.
              </p>
              <p>A stay&rsquo;s details show how many of the allowed devices are online right now.</p>
            </HelpSection>
            <HelpSection title="Search and pages">
              <p>
                Search covers every stay for the selected status &mdash; room, reservation, guest name, travel agent
                or internet package &mdash; not only the page on screen. The counters above the list count every
                matching stay.
              </p>
            </HelpSection>
            <HelpSection title="VIP and travel agent">
              <p>
                Both come from the PMS with each guest record. Internet packages can be limited to VIP guests or to
                the guests of chosen travel agents.
              </p>
            </HelpSection>
          </>
        }
      />

      <StatGrid cols={4}>
        <StatCard
          label={status ? `${label(status)} stays` : "Stays listed"}
          value={rows ? summary.total.toLocaleString() : "—"}
        />
        <StatCard
          label="With an internet package"
          value={rows ? summary.withInternet.toLocaleString() : "—"}
          tone={inHouseNoInternet ? "warn" : "default"}
          explain={
            <Explain>
              A stay has a package once a guest from that room has signed in and been given one. A room with no
              package is not a fault — it usually means nobody from it has connected yet.
            </Explain>
          }
        />
        <StatCard
          label="Devices online"
          value={rows ? summary.online.toLocaleString() : "—"}
          href="/sessions"
          hint="Across every matching stay"
        />
        <StatCard label="VIP guests" value={rows ? summary.vip.toLocaleString() : "—"} hint="Marked VIP in the PMS" />
      </StatGrid>

      {inHouseNoInternet && (
        <Callout tone="warning" title="No in-house room has an internet package">
          If guests cannot get online, check that a package applies to these stays on{" "}
          <Link href="/internet-packages" className="underline underline-offset-2">Internet packages</Link>, and
          that their Wi-Fi network is routed to the PMS they belong to on{" "}
          <Link href="/pms-routing" className="underline underline-offset-2">PMS routing</Link>.
        </Callout>
      )}

      <Card className="overflow-hidden">
        <FilterBar
          inset
          search={
            <SearchInput
              label="Search stays"
              placeholder="Room, guest, reservation, agent or package…"
              value={q}
              onChange={setQ}
              delay={300}
            />
          }
          filters={
            <>
              <Select
                aria-label="Filter by status"
                value={status}
                onChange={(e) => setStatus(e.target.value)}
                className="h-9 w-44"
              >
                {STATUSES.map((s) => (
                  <option key={s || "all"} value={s}>{s === "" ? "All stays" : label(s)}</option>
                ))}
              </Select>
              <Checkbox label="VIP only" checked={vipOnly} onChange={(e) => setVipOnly(e.target.checked)} className="items-center" />
            </>
          }
          actions={
            <Select
              aria-label="Stays per page"
              value={String(pageSize)}
              onChange={(e) => setPageSize(Number(e.target.value))}
              className="h-9 w-32"
            >
              {PAGE_SIZES.map((n) => <option key={n} value={n}>{n} per page</option>)}
            </Select>
          }
        />
        {showPms && byPms.length > 0 && (
          <div className="border-b border-border px-4 py-2.5">
            <FilterChips<string>
              label="PMS connection"
              value={pms}
              onChange={setPms}
              options={[
                { value: "", label: "All connections", count: pmsTotal },
                ...byPms.map((b) => ({
                  value: b.pms_interface_id,
                  label: b.lifecycle_state === "DECOMMISSIONED" ? `${b.label} (retired)` : b.label,
                  count: b.total,
                })),
              ]}
            />
          </div>
        )}
        <ResultsCount count={rows?.length} noun="stay" />
        {err !== null && <div className="px-4 pt-3"><ErrorBanner err={err} /></div>}

        {rows === null ? (
          <SkeletonRows rows={6} cols={6} />
        ) : rows.length === 0 ? (
          <EmptyState
            size="sm"
            icon={<Hotel />}
            title={q ? "No stays match that search" : "No stays to show"}
            hint={filtering
              ? "Nothing matches across all stays for the selected status."
              : "Nothing has arrived from a PMS for this filter yet."}
          />
        ) : (
          <Table label="Stays">
            <THead>
              <TR>
                <TH>{showPms ? "Room · PMS" : "Room"}</TH>
                <TH>Guest</TH>
                <TH className="hidden sm:table-cell">Stay</TH>
                <TH>Status</TH>
                <TH className="hidden md:table-cell">Internet package</TH>
                <TH className="hidden lg:table-cell">Room charge</TH>
                <TH><span className="sr-only">Actions</span></TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((s) => (
                <TR key={s.id} onClick={() => void open(s.id)}>
                  <TD nowrap className="max-w-[10rem]">
                    <div className="flex items-center gap-1.5 font-medium">
                      {s.room ?? "—"}
                      {s.vip ? <Badge tone="warn" size="sm">VIP</Badge> : null}
                    </div>
                    {showPms && <div className="mt-0.5"><PmsTag label={s.pms_interface_label} /></div>}
                    {s.external_reservation_id && (
                      <div className="truncate font-mono text-2xs text-muted-foreground" title={s.external_reservation_id}>{s.external_reservation_id}</div>
                    )}
                  </TD>
                  <TD className="max-w-[16rem]">
                    <div className="truncate" title={!s.identity_hidden && s.primary_guest ? s.primary_guest : undefined}>{s.identity_hidden
                      ? <span className="text-muted-foreground">Not shown to your role</span>
                      : s.primary_guest ?? <span className="text-muted-foreground">Not provided</span>}</div>
                    {(s.occupants > 1 || s.travel_agent) && (
                      <div className="truncate text-xs text-muted-foreground">
                        {s.occupants > 1 && <span>+{s.occupants - 1} sharing</span>}
                        {s.occupants > 1 && s.travel_agent && " · "}
                        {s.travel_agent && <span>Agent: {s.travel_agent}</span>}
                      </div>
                    )}
                  </TD>
                  <TD nowrap className="hidden tabular sm:table-cell">{shortDate(s.arrival)} → {shortDate(s.departure)}</TD>
                  <TD nowrap>
                    <StatusBadge tone={toneFor(s.status)}>{label(s.status)}</StatusBadge>
                    {s.effective_checkout_at && (
                      <div className="text-2xs text-muted-foreground">left {formatRelative(s.effective_checkout_at)}</div>
                    )}
                  </TD>
                  <TD className="hidden md:table-cell"><AccessCell stay={s} /></TD>
                  <TD className="hidden lg:table-cell">
                    {s.posting_allowed ? (
                      <Badge tone="ok">Allowed</Badge>
                    ) : (
                      <span className="text-xs text-muted-foreground">
                        Not allowed
                        {s.posting_block_reason && (
                          <span className="block text-2xs">{postingBlockWords(s.posting_block_reason)}</span>
                        )}
                      </span>
                    )}
                  </TD>
                  <TD className="w-0">
                    <RowActions
                      subject={`stay, ${roomWithPms(s.room, s.pms_interface_label, showPms)}`}
                      primary={{ label: "View", onSelect: () => void open(s.id), disabled: detailBusy }}
                    />
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
        {rows && rows.length > 0 && (
          <CardFooter>
            <Pagination
              className="w-full"
              offset={offset}
              limit={pageSize}
              shown={rows.length}
              total={resp?.summary?.total ?? null}
              onChange={setOffset}
            />
          </CardFooter>
        )}
      </Card>

      <DetailsDrawer
        open={detail !== null}
        onOpenChange={(v) => !v && setDetail(null)}
        width="lg"
        eyebrow="Stay"
        title={detail ? `Room ${detail.room ?? "—"}${detail.primary_guest ? ` · ${detail.primary_guest}` : ""}` : ""}
        description={
          detail
            ? `${detail.identity_hidden ? "Reservation not shown to your role" : `Reservation ${detail.external_reservation_id}`}${detail.pms_interface_label ? ` · from ${detail.pms_interface_label}` : ""}`
            : undefined
        }
        badges={detail && (
          <>
            <StatusBadge tone={toneFor(detail.status)}>{label(detail.status)}</StatusBadge>
            {detail.vip && <Badge tone="warn">VIP</Badge>}
            {detail.posting_allowed
              ? <Badge tone="ok">Room charge allowed</Badge>
              : <Badge tone="default">Room charge not allowed</Badge>}
          </>
        )}
      >
        {detail && (
          <>
            <DetailsSection title="Internet for this room">
              {detail.access_status ? (
                <>
                  <MetricStrip
                    items={[
                      { label: "Package", value: <span className="text-sm">{detail.access_package_name || detail.access_package_code || "—"}</span> },
                      { label: "Service plan", value: <span className="text-sm">{detail.access_plan_code ?? "—"}</span> },
                      { label: "Speed", value: <span className="text-sm">{speedPair(detail.access_down_kbps, detail.access_up_kbps) ?? "—"}</span> },
                      {
                        label: "Devices with a place",
                        tone: atDeviceLimit(detail) ? "warn" : undefined,
                        value: (
                          <span className="text-sm">
                            {placesHeld(detail)}
                            {typeof detail.access_max_devices === "number" && detail.access_max_devices > 0
                              ? ` of ${detail.access_max_devices}`
                              : ""}
                            <span className="block text-2xs font-normal text-muted-foreground">
                              {detail.access_active_devices ?? 0} online now
                            </span>
                          </span>
                        ),
                      },
                    ]}
                  />
                  {detail.access_status !== "ACTIVE" && (
                    <p className="text-xs text-muted-foreground">
                      The access record is <strong>{detail.access_status.toLowerCase()}</strong> rather than active —
                      granted but not yet in force, or suspended.
                    </p>
                  )}
                  <Link href="/sessions" className="inline-block text-xs font-medium text-primary hover:underline">
                    See this room&rsquo;s devices →
                  </Link>
                </>
              ) : (
                <p className="text-sm text-muted-foreground">No internet package has been given to this stay yet.</p>
              )}
            </DetailsSection>

            <DetailsSection title="Stay">
              <DescriptionList
                items={[
                  { label: "Arrival", value: shortDate(detail.arrival) },
                  { label: "Departure", value: shortDate(detail.departure) },
                  {
                    label: "Left the site",
                    value: detail.effective_checkout_at ? formatRelative(detail.effective_checkout_at) : "Still in house",
                  },
                  { label: "Occupants", value: String(detail.occupants) },
                  {
                    label: "PMS last confirmed this stay",
                    value: detail.occupancy_evidence_at
                      ? formatRelative(detail.occupancy_evidence_at)
                      : "No confirmation recorded",
                  },
                  // Only when the connector supplied them; dashes would suggest the PMS failed to send them.
                  { label: "Room type", value: detail.room_type, hidden: !detail.room_type },
                  { label: "Rate plan", value: detail.rate_plan, hidden: !detail.rate_plan },
                  { label: "Travel agent", value: detail.travel_agent, hidden: !detail.travel_agent },
                ]}
              />
            </DetailsSection>

            <DetailsSection title="Guests on this stay">
              {detail.occupant_list.length === 0 ? (
                <p className="text-sm text-muted-foreground">The PMS did not send guest names for this stay.</p>
              ) : (
                <ul className="divide-y divide-border rounded-md border border-border text-sm">
                  {detail.occupant_list.map((o, i) => (
                    <li key={i} className="flex items-center gap-2 px-3 py-2">
                      {o.display_name ?? <span className="text-muted-foreground">{detail.identity_hidden ? "Name not shown to your role" : "Name not provided"}</span>}
                      {o.is_primary && <Badge tone="info" size="sm">Main guest</Badge>}
                    </li>
                  ))}
                </ul>
              )}
            </DetailsSection>

            <RoomChargeSection
              stay={detail}
              canManage={canManageRoomCharge}
              onBlock={() => { setBlockErr(null); setBlockAction("SET"); }}
              onUnblock={() => { setBlockErr(null); setBlockAction("CLEAR"); }}
            />
          </>
        )}
      </DetailsDrawer>

      {canManageRoomCharge && (
        <ConfirmDialog
          open={blockAction !== null}
          onOpenChange={(v) => { if (!v && !blockBusy) setBlockAction(null); }}
          title={blockAction === "SET" ? "Block room charge for this stay" : "Remove the administrative block"}
          description={blockAction === "SET"
            ? "No room charge is offered or accepted for this stay until a site administrator removes the block. Internet access the guest already has is not affected."
            : "Room charge becomes possible again for this stay, unless another block remains. Blocks placed by the PMS or by an unresolved charge are not affected."}
          confirmLabel={blockAction === "SET" ? "Block room charge" : "Remove administrative block"}
          confirmVariant={blockAction === "SET" ? "danger" : "primary"}
          busy={blockBusy}
          error={blockErr}
          requireReason
          reasonMinLength={4}
          requirePassword
          reasonPlaceholder={blockAction === "SET" ? "e.g. Guest asked to settle in cash" : "e.g. Guest cleared with Front Office"}
          onConfirm={({ reason, password }) => { if (blockAction) return changeAdminBlock(blockAction, reason, password); }}
        />
      )}
    </PageShell>
  );
}

function BlockLine({ b }: { b: StayPostingBlock }) {
  const active = !b.cleared_at;
  return (
    <li className="space-y-0.5 px-3 py-2.5 text-sm">
      <div className="flex flex-wrap items-center gap-1.5">
        <StatusBadge tone={active ? "warn" : "default"}>{active ? "Active" : "Cleared"}</StatusBadge>
        <span className="font-medium">{postingBlockWords(b.reason)}</span>
        {b.pa_as_status && <span className="font-mono text-xs text-muted-foreground">PMS answer {b.pa_as_status}</span>}
      </div>
      <div className="text-xs text-muted-foreground">
        Placed by {b.created_by ?? postingSourceWords(b.source).toLowerCase()} · {formatDate(b.created_at)}
        {b.cleared_at && (
          <> · cleared {formatDate(b.cleared_at)}{b.cleared_by ? ` by ${b.cleared_by}` : b.cleared_by_source ? ` by ${postingSourceWords(b.cleared_by_source).toLowerCase()}` : ""}</>
        )}
      </div>
      {b.note && <div className="text-xs text-muted-foreground">&ldquo;{b.note}&rdquo;</div>}
      {b.cleared_reason && <div className="text-xs text-muted-foreground">Cleared because: &ldquo;{b.cleared_reason}&rdquo;</div>}
      {active && POSTING_BLOCK_CLEARED_BY[b.reason] && (
        <div className="text-xs">{POSTING_BLOCK_CLEARED_BY[b.reason]}</div>
      )}
    </li>
  );
}

function RoomChargeSection({
  stay, canManage, onBlock, onUnblock,
}: {
  stay: StayDetail;
  canManage: boolean;
  onBlock: () => void;
  onUnblock: () => void;
}) {
  const blocks = stay.posting_blocks ?? [];
  const active = blocks.filter((b) => !b.cleared_at);
  const cleared = blocks.filter((b) => b.cleared_at);
  const adminBlocked = active.some((b) => b.reason === "ADMIN_BLOCK");
  // A reason with no matching block row (NOT_IN_HOUSE, NO_RESERVATION) comes from the PMS feed itself.
  const reason = stay.posting_block_reason ?? null;
  const reasonHasBlock = reason !== null && active.some((b) => b.reason === reason);
  return (
    <section className="space-y-3" aria-label="Room charge">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <h3 className="text-label text-foreground">Room charge</h3>
        {canManage && (
          adminBlocked ? (
            <Button size="sm" variant="secondary" onClick={onUnblock}>
              <Undo2 /> Remove administrative block
            </Button>
          ) : (
            <Button size="sm" variant="danger-outline" onClick={onBlock}>
              <Ban /> Block room charge
            </Button>
          )
        )}
      </div>
      {stay.room_charge_open && (
        <Callout tone="info" title="In progress">
          <span data-testid="room-charge-open">
            A room charge for this stay is pending, being sent or under review. No other is accepted until it
            concludes.
          </span>
        </Callout>
      )}
      {stay.posting_allowed ? (
        <p className="text-sm">
          <Badge tone="ok">Allowed</Badge>{" "}
          <span className="text-muted-foreground">
            A charge goes to this guest&rsquo;s reservation (room and reservation number together).
          </span>
        </p>
      ) : (
        <div className="space-y-0.5 text-sm">
          <div>
            <Badge tone="default">Not allowed</Badge>{" "}
            <span>{postingBlockWords(reason)}</span>
          </div>
          {stay.posting_permission_source && (
            <div className="text-xs text-muted-foreground">
              Decided by: {postingSourceWords(stay.posting_permission_source)}
            </div>
          )}
          {reason && !reasonHasBlock && POSTING_BLOCK_CLEARED_BY[reason] && (
            <div className="text-xs">{POSTING_BLOCK_CLEARED_BY[reason]}</div>
          )}
        </div>
      )}

      {active.length > 0 && (
        <div>
          <h4 className="mb-1 text-xs font-medium text-muted-foreground">Active blocks</h4>
          <ul className="divide-y divide-border rounded-md border border-border">{active.map((b, i) => <BlockLine key={`a${i}`} b={b} />)}</ul>
        </div>
      )}
      {cleared.length > 0 && (
        <div>
          <h4 className="mb-1 text-xs font-medium text-muted-foreground">Earlier blocks</h4>
          <ul className="divide-y divide-border rounded-md border border-border">{cleared.map((b, i) => <BlockLine key={`c${i}`} b={b} />)}</ul>
        </div>
      )}
      {blocks.length === 0 && (
        <p className="text-xs text-muted-foreground">Room charge has never been blocked on this stay.</p>
      )}
      {canManage && active.some((b) => b.reason !== "ADMIN_BLOCK") && (
        <p className="text-xs text-muted-foreground">
          Only an administrative block can be removed here. Blocks from the PMS clear when fresh PMS data allows
          posting, and a block from an unresolved charge clears when that charge is decided in Manual review.
        </p>
      )}
    </section>
  );
}

function AccessCell({ stay }: { stay: Stay }) {
  if (!stay.access_status) {
    return (
      <span className="text-xs text-muted-foreground">
        None yet
        {stay.status === "IN_HOUSE" && <span className="block text-2xs">Nobody has signed in</span>}
      </span>
    );
  }
  const atLimit = atDeviceLimit(stay);
  const speed = speedPair(stay.access_down_kbps, stay.access_up_kbps) ?? stay.access_plan_code ?? "";
  return (
    <>
      <div className="whitespace-nowrap text-sm">{stay.access_package_name || stay.access_package_code || "Package"}</div>
      <div className={atLimit ? "text-2xs text-warning-subtle-foreground" : "text-2xs text-muted-foreground"}>
        {speed ? `${speed} · ` : ""}
        {placesHeld(stay)}
        {typeof stay.access_max_devices === "number" && stay.access_max_devices > 0
          ? ` / ${stay.access_max_devices} devices`
          : " devices"}
        {atLimit && " — at the limit"}
      </div>
    </>
  );
}

// The device limit counts places, not sessions: a device keeps its place after signing out. Older appliances fall back to the online count.
function placesHeld(s: Stay): number {
  return s.access_authorized_devices ?? s.access_active_devices ?? 0;
}

function atDeviceLimit(s: Stay): boolean {
  return typeof s.access_max_devices === "number" && s.access_max_devices > 0 && placesHeld(s) >= s.access_max_devices;
}
