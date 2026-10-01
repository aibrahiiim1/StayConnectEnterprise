"use client";

// STAYS — who the property management system says is in the building.
//
// The list used to identify a stay by its reservation string and room number only, which meant an operator
// looking for "the party in 412" or "Mr Andersen" had to open rows until they found the right one. Arrival
// and departure were already in the API and shown nowhere; the primary guest's name was already on the
// detail view but not in the list; and posting_allowed rendered as "closed" with no way to learn why.
//
// It is now a list an operator can scan and search, plus a detail view for one stay. Guest names appear
// because operating a hotel front desk requires them — but they are treated as what they are: they are not
// written to logs or diagnostics, and the search runs entirely in the browser over rows the operator is
// already authorised to see.
//
// TWO THINGS CHANGED IN THIS PASS.
//
//   1. WHICH INTERNET THIS ROOM HAS. "Which package is room 412 on?" was a question the product could not
//      answer from any screen: Stays knew the room, Internet packages knew the packages, and nothing joined
//      them. edged now projects the live entitlement's package and service plan onto the stay, so it is a
//      column here and a block in the detail — including how many of the allowed devices are currently online,
//      which is the answer to "the family says they can't connect their fourth phone".
//
//   2. THE DETAIL IS A DIALOG. It used to append a card BELOW the table: on a full hotel the operator clicked
//      View and nothing appeared to happen, because the detail had opened several screens further down.
//
// ROOM CHARGE ON A STAY (Phase-0 Amendment A1). A room charge targets the reservation (room number +
// reservation number); there are no folios to list. The stay says whether room charge is allowed, why not, who
// decided it, and the history of its posting blocks. Only ADMIN_BLOCK is an operator's: a site administrator on
// an appliance where room charge is licensed can set or remove it (reason + password). PMS_NO_POST and
// PMS_DATA_SUSPECT clear only from fresh Protel data, POSTING_UNRESOLVED only from the charge's manual review —
// the page never offers to lift them and says who does.

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
import { PageShell, PageHeader, StatCard, Toolbar } from "@/components/ui/page";
import { HelpSection } from "@/components/help";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input, Select } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { DetailDialog } from "@/components/ui/dialog";
import { Explain } from "@/components/ui/tooltip";
import { DList, Metric, SkeletonRows } from "@/components/ui/misc";
import { Pagination, SearchInput } from "@/components/ui/data";
import { formatDate, formatRelative } from "@/lib/utils";
import { speedPair } from "@/lib/session-words";
import { Hotel, Wifi, LogIn, LogOut, Ban, Undo2, Star } from "lucide-react";

// Operator wording for the lifecycle. The wire values are unchanged; nobody outside the domain should have
// to read SCREAMING_SNAKE to find out whether a guest is in the building.
const STATUS_LABELS: Record<string, string> = {
  IN_HOUSE: "In house",
  RESERVED: "Arriving",
  CHECKED_OUT: "Checked out",
  POST_STAY_ACTIVE: "Post-stay access",
  CANCELLED: "Cancelled",
  NO_SHOW: "No show",
};
const STATUSES = ["", "IN_HOUSE", "RESERVED", "CHECKED_OUT", "POST_STAY_ACTIVE", "CANCELLED", "NO_SHOW"];

const toneFor = (status: string) =>
  status === "IN_HOUSE" ? "info" : status === "CHECKED_OUT" ? "default" : "warn";

const PAGE_SIZES = [25, 50, 100, 200];

const label = (s: string) => STATUS_LABELS[s] ?? s.replace(/_/g, " ").toLowerCase();

/** A date the operator reads, not an ISO timestamp. Arrival/departure are dates in the PMS, not instants. */
function shortDate(v?: string | null): string {
  if (!v) return "—";
  const d = new Date(v);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleDateString(undefined, { day: "2-digit", month: "short" });
}

export default function StaysPage() {
  const [status, setStatus] = useState("IN_HOUSE"); // the question an operator asks by default
  const [q, setQ] = useState("");
  const [vipOnly, setVipOnly] = useState(false);
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
  // The administrative block is a site administrator's, and only where room charge is licensed and its
  // surface is served here. Anything unknown fails closed: no button.
  const canManageRoomCharge =
    (roles?.includes("site_admin") ?? false) &&
    surfaceAvailable(caps, "pms-financial-onboarding") &&
    moduleLicensed(caps, "room_charge");
  const [blockAction, setBlockAction] = useState<"SET" | "CLEAR" | null>(null);
  const [blockBusy, setBlockBusy] = useState(false);
  const [blockErr, setBlockErr] = useState<string | null>(null);

  // A new question starts at its first page.
  useEffect(() => { setOffset(0); }, [status, q, vipOnly, pageSize]);

  // PAGED AND SEARCHED ON THE SERVER. The list used to stop at 200 rows and search only those in the browser,
  // so a property with 400 rooms in house saw half of them and its counters counted that half. The search text
  // goes in a header, not the URL: it is often a guest's name, and edged logs request lines.
  //
  // ONLY THE LATEST QUESTION MAY ANSWER. A filter changed on page 3 fires the old offset's request and then the
  // reset's page-1 request; quick typing fires one per pause. Responses can return in any order, so each request
  // takes a number and a response is applied only if no newer request has started since -- otherwise an old page
  // could land last and show rows and totals for a question the controls no longer ask.
  const latest = useRef(0);
  const load = useCallback(async () => {
    const mine = ++latest.current;
    setRows(null);
    setErr(null);
    try {
      const params = new URLSearchParams();
      if (status) params.set("status", status);
      if (vipOnly) params.set("vip", "true");
      params.set("page", String(Math.floor(offset / pageSize) + 1));
      params.set("page_size", String(pageSize));
      const needle = q.trim();
      const r = await api.get<StaysPageResp>(
        "/pms-stays?" + params.toString(),
        needle ? { "X-Stay-Search": needle } : undefined,
      );
      if (mine !== latest.current) return; // superseded
      setResp(r);
      setRows(r.data ?? []);
    } catch (e) {
      if (mine !== latest.current) return; // superseded
      setErr(e);
      setResp(null);
      setRows([]);
    }
  }, [status, vipOnly, offset, pageSize, q]);

  useEffect(() => { void load(); }, [load]);

  const filtered = rows;

  // Totals over EVERY stay that matches, from the server -- not over the page on screen.
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

  return (
    <PageShell width="wide">
      <PageHeader
        eyebrow="Hotel"
        title="Stays"
        description="Who the PMS reports in the building, and the internet each room has."
        help={
          <>
            <HelpSection title="Where stays come from">
              <p>
                This is what the property management system reports about who is in the building, and which
                internet package each room has been given. Stays themselves are changed in the PMS, not here.
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

      <ErrorBanner err={err} />

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatCard
          label={status ? `${label(status)} stays` : "Stays listed"}
          value={rows ? summary.total.toLocaleString() : "—"}
          icon={<Hotel />}
          tone="primary"
        />
        <StatCard
          label="With an internet package"
          value={rows ? summary.withInternet.toLocaleString() : "—"}
          icon={<Wifi />}
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
        <StatCard
          label="VIP guests"
          value={rows ? summary.vip.toLocaleString() : "—"}
          icon={<Star />}
          hint="Marked VIP by the PMS"
        />
      </div>

      {inHouseNoInternet && (
        <Callout tone="warning" title="No in-house room has an internet package">
          The guest list has arrived, but nobody has been given internet. If guests are reporting that they cannot
          get online, check that a package applies to these stays on{" "}
          <Link href="/internet-packages" className="underline underline-offset-2">Internet packages</Link>, and
          that their Wi-Fi network is pointed at this PMS on{" "}
          <Link href="/pms-routing" className="underline underline-offset-2">PMS routing</Link>.
        </Callout>
      )}

      <Card>
        <CardBody className="border-b border-border py-3">
          <Toolbar>
            <SearchInput
              label="Search stays"
              placeholder="Room, guest, reservation, travel agent or package…"
              className="w-full max-w-xs"
              value={q}
              onChange={setQ}
              delay={300}
            />
            <Select
              aria-label="Filter by status"
              value={status}
              onChange={(e) => setStatus(e.target.value)}
              className="w-48"
            >
              {STATUSES.map((s) => (
                <option key={s || "all"} value={s}>{s === "" ? "All stays" : label(s)}</option>
              ))}
            </Select>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={vipOnly}
                onChange={(e) => setVipOnly(e.target.checked)}
                className="size-4 accent-primary"
              />
              VIP only
            </label>
            <Select
              aria-label="Stays per page"
              value={String(pageSize)}
              onChange={(e) => setPageSize(Number(e.target.value))}
              className="w-32"
            >
              {PAGE_SIZES.map((n) => <option key={n} value={n}>{n} per page</option>)}
            </Select>
          </Toolbar>
        </CardBody>

        <CardBody className="p-0">
          {filtered === null ? (
            <SkeletonRows rows={6} cols={6} />
          ) : filtered.length === 0 ? (
            <EmptyState
              icon={<Hotel />}
              title={q ? "No stays match that search" : "No stays to show"}
              hint={q || vipOnly
                ? "Nothing matches across all stays for the selected status."
                : "Nothing has arrived from the property management system for this filter yet. If you expect guests here, check the PMS connection."}
            />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Room</TH>
                  <TH>Guest</TH>
                  <TH>Stay</TH>
                  <TH>Status</TH>
                  <TH>Internet package</TH>
                  <TH>Room charge</TH>
                  <TH />
                </TR>
              </THead>
              <tbody>
                {filtered.map((s) => (
                  <TR key={s.id}>
                    <TD>
                      <div className="flex items-center gap-1.5 font-medium">
                        {s.room ?? "—"}
                        {s.vip ? <Badge tone="warn">VIP</Badge> : null}
                      </div>
                      <div className="font-mono text-2xs text-muted-foreground">
                        {s.external_reservation_id}
                      </div>
                    </TD>
                    <TD>
                      <div>{s.primary_guest ?? <span className="text-muted-foreground">Not provided</span>}</div>
                      {s.occupants > 1 && (
                        <div className="text-xs text-muted-foreground">+{s.occupants - 1} sharing</div>
                      )}
                      {s.travel_agent && (
                        <div className="text-xs text-muted-foreground">Agent: {s.travel_agent}</div>
                      )}
                    </TD>
                    <TD className="whitespace-nowrap text-sm">
                      {shortDate(s.arrival)} → {shortDate(s.departure)}
                    </TD>
                    <TD>
                      <Badge tone={toneFor(s.status) as any}>{label(s.status)}</Badge>
                      {s.effective_checkout_at && (
                        <div className="text-2xs text-muted-foreground">
                          left {formatRelative(s.effective_checkout_at)}
                        </div>
                      )}
                    </TD>
                    <TD>
                      <AccessCell stay={s} />
                    </TD>
                    <TD>
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
                    <TD className="text-right">
                      <Button size="sm" variant="ghost" disabled={detailBusy} onClick={() => void open(s.id)}>
                        View
                      </Button>
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
        {filtered && filtered.length > 0 && (
          <CardBody className="border-t border-border py-3">
            <Pagination
              offset={offset}
              limit={pageSize}
              shown={filtered.length}
              total={resp?.summary?.total ?? null}
              onChange={setOffset}
            />
          </CardBody>
        )}
      </Card>

      <DetailDialog
        open={detail !== null}
        onOpenChange={(v) => !v && setDetail(null)}
        title={detail ? `Room ${detail.room ?? "—"}${detail.primary_guest ? ` · ${detail.primary_guest}` : ""}` : ""}
        description={
          detail
            ? `Reservation ${detail.external_reservation_id}${detail.pms_interface_label ? ` · from ${detail.pms_interface_label}` : ""}`
            : undefined
        }
      >
        {detail && (
          <>
            <div className="flex flex-wrap items-center gap-2">
              <Badge tone={toneFor(detail.status) as any}>{label(detail.status)}</Badge>
              {detail.vip && <Badge tone="warn">VIP</Badge>}
              {detail.posting_allowed
                ? <Badge tone="ok">Room charge allowed</Badge>
                : <Badge tone="default">Room charge not allowed</Badge>}
            </div>

            {/* THE INTERNET BLOCK. First, because it is the reason a front-desk operator opens a stay in this
                product at all — everything else here they can read in the PMS. */}
            <section className="rounded-md border border-border bg-surface/40 p-4">
              <h3 className="mb-3 text-sm font-semibold">Internet for this room</h3>
              {detail.access_status ? (
                <>
                  <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
                    <Metric label="Package" value={<span className="text-sm">{detail.access_package_name || detail.access_package_code || "—"}</span>} />
                    <Metric label="Service plan" value={<span className="text-sm">{detail.access_plan_code ?? "—"}</span>} />
                    <Metric
                      label="Speed"
                      value={<span className="text-sm">{speedPair(detail.access_down_kbps, detail.access_up_kbps) ?? "—"}</span>}
                    />
                    <Metric
                      label="Devices online"
                      value={
                        <span className="text-sm">
                          {detail.access_active_devices ?? 0}
                          {typeof detail.access_max_devices === "number" && detail.access_max_devices > 0
                            ? ` of ${detail.access_max_devices}`
                            : ""}
                        </span>
                      }
                      tone={
                        typeof detail.access_max_devices === "number" &&
                        detail.access_max_devices > 0 &&
                        (detail.access_active_devices ?? 0) >= detail.access_max_devices
                          ? "warn"
                          : "default"
                      }
                    />
                  </div>
                  {detail.access_status !== "ACTIVE" && (
                    <p className="mt-3 text-xs text-muted-foreground">
                      The access record is <strong>{detail.access_status.toLowerCase()}</strong> rather than active —
                      granted but not yet in force, or suspended.
                    </p>
                  )}
                  <Link
                    href="/sessions"
                    className="mt-3 inline-block text-xs text-primary underline underline-offset-2 hover:decoration-2"
                  >
                    See this room&rsquo;s devices →
                  </Link>
                </>
              ) : (
                <p className="text-sm text-muted-foreground">
                  No internet package has been given to this stay yet.
                </p>
              )}
            </section>

            <DList
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
                // Rendered ONLY when the connector supplied them. A permanent row of dashes would suggest the
                // PMS is failing to send something, when in fact this feed was never asked for it.
                ...(detail.room_type ? [{ label: "Room type", value: detail.room_type }] : []),
                ...(detail.rate_plan ? [{ label: "Rate plan", value: detail.rate_plan }] : []),
                ...(detail.travel_agent ? [{ label: "Travel agent", value: detail.travel_agent }] : []),
              ]}
            />

            <div>
              <h3 className="mb-1.5 text-sm font-semibold">Guests on this stay</h3>
              {detail.occupant_list.length === 0 ? (
                <p className="text-sm text-muted-foreground">
                  The PMS did not send guest names for this stay.
                </p>
              ) : (
                <ul className="space-y-1 text-sm">
                  {detail.occupant_list.map((o, i) => (
                    <li key={i} className="flex items-center gap-2">
                      {o.display_name ?? <span className="text-muted-foreground">Name not provided</span>}
                      {o.is_primary && <Badge tone="info">Main guest</Badge>}
                    </li>
                  ))}
                </ul>
              )}
            </div>

            <RoomChargeSection
              stay={detail}
              canManage={canManageRoomCharge}
              onBlock={() => { setBlockErr(null); setBlockAction("SET"); }}
              onUnblock={() => { setBlockErr(null); setBlockAction("CLEAR"); }}
            />
          </>
        )}
      </DetailDialog>

      {canManageRoomCharge && (
        <ConfirmDialog
          open={blockAction !== null}
          onOpenChange={(v) => { if (!v && !blockBusy) setBlockAction(null); }}
          title={blockAction === "SET" ? "Block room charge for this stay" : "Remove the administrative block"}
          description={blockAction === "SET"
            ? "No room charge is offered or accepted for this stay until a site administrator removes the block. Internet access the guest already has is not affected."
            : "Room charge becomes possible again for this stay, unless another block remains. Blocks placed by Protel or by an unresolved charge are not affected."}
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

/** One posting block, in words: what it is, where it came from, since when, and (for an active one) who clears it. */
function BlockLine({ b }: { b: StayPostingBlock }) {
  const active = !b.cleared_at;
  return (
    <li className="space-y-0.5 text-sm">
      <div className="flex flex-wrap items-center gap-1.5">
        <Badge tone={active ? "warn" : "default"}>{active ? "Active" : "Cleared"}</Badge>
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

/** Whether room charge is allowed on this stay, why not, and the posting-block history. */
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
    <section className="space-y-3 rounded-md border border-border p-4" aria-label="Room charge">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0 space-y-1">
          <h3 className="text-sm font-semibold">Room charge</h3>
          {stay.room_charge_open && (
            <p className="text-sm" data-testid="room-charge-open">
              <Badge tone="info">In progress</Badge>{" "}
              <span className="text-muted-foreground">
                A room charge for this stay is pending, being sent or under review. No other is accepted until it
                concludes.
              </span>
            </p>
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
        </div>
        {canManage && (
          adminBlocked ? (
            <Button size="sm" variant="secondary" onClick={onUnblock}>
              <Undo2 /> Remove administrative block
            </Button>
          ) : (
            <Button size="sm" variant="secondary" onClick={onBlock}>
              <Ban /> Block room charge
            </Button>
          )
        )}
      </div>

      {active.length > 0 && (
        <div>
          <h4 className="mb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">Active blocks</h4>
          <ul className="space-y-2">{active.map((b, i) => <BlockLine key={`a${i}`} b={b} />)}</ul>
        </div>
      )}
      {cleared.length > 0 && (
        <div>
          <h4 className="mb-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">Earlier blocks</h4>
          <ul className="space-y-2">{cleared.map((b, i) => <BlockLine key={`c${i}`} b={b} />)}</ul>
        </div>
      )}
      {blocks.length === 0 && (
        <p className="text-xs text-muted-foreground">Room charge has never been blocked on this stay.</p>
      )}
      {canManage && active.some((b) => b.reason !== "ADMIN_BLOCK") && (
        <p className="text-xs text-muted-foreground">
          Only an administrative block can be removed here. Blocks from Protel clear when fresh Protel data allows
          posting, and a block from an unresolved charge clears when that charge is decided in Manual review.
        </p>
      )}
    </section>
  );
}

/** The package this room is on, or the honest absence of one. */
function AccessCell({ stay }: { stay: Stay }) {
  if (!stay.access_status) {
    return (
      <span className="text-xs text-muted-foreground">
        None yet
        {stay.status === "IN_HOUSE" && <span className="block text-2xs">Nobody has signed in</span>}
      </span>
    );
  }
  const atLimit =
    typeof stay.access_max_devices === "number" &&
    stay.access_max_devices > 0 &&
    (stay.access_active_devices ?? 0) >= stay.access_max_devices;
  return (
    <>
      <div className="text-sm">{stay.access_package_name || stay.access_package_code || "Package"}</div>
      <div className="text-2xs text-muted-foreground">
        {speedPair(stay.access_down_kbps, stay.access_up_kbps) ?? stay.access_plan_code ?? ""}
      </div>
      <div className={atLimit ? "text-2xs text-warning-subtle-foreground" : "text-2xs text-muted-foreground"}>
        {stay.access_active_devices ?? 0}
        {typeof stay.access_max_devices === "number" && stay.access_max_devices > 0
          ? ` / ${stay.access_max_devices} devices`
          : " devices"}
        {atLimit && " — at the limit"}
      </div>
    </>
  );
}
