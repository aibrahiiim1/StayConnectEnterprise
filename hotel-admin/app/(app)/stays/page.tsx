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

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { api, ListResp, Stay, StayDetail } from "@/lib/api";
import { PageShell, PageHeader, StatCard, Toolbar } from "@/components/ui/page";
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
import { formatRelative } from "@/lib/utils";
import { speedPair } from "@/lib/session-words";
import { Search, Hotel, Wifi, LogIn, LogOut } from "lucide-react";

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
  const [rows, setRows] = useState<Stay[] | null>(null);
  const [detail, setDetail] = useState<StayDetail | null>(null);
  const [detailBusy, setDetailBusy] = useState(false);
  const [err, setErr] = useState<unknown>(null);

  const load = useCallback(async () => {
    setRows(null);
    setErr(null);
    try {
      const query = status ? "?status=" + encodeURIComponent(status) : "";
      const r = await api.get<ListResp<Stay>>("/pms-stays" + query);
      setRows(r.data ?? []);
    } catch (e) {
      setErr(e);
      setRows([]);
    }
  }, [status]);

  useEffect(() => { void load(); }, [load]);

  // Client-side, over the page already loaded. The list is capped server-side at 200 rows, so this is a
  // filter over what is on screen rather than a search of the whole property — worth being honest about in
  // the empty state below.
  const filtered = useMemo(() => {
    if (!rows) return null;
    const needle = q.trim().toLowerCase();
    if (!needle) return rows;
    return rows.filter((s) =>
      (s.room ?? "").toLowerCase().includes(needle) ||
      (s.primary_guest ?? "").toLowerCase().includes(needle) ||
      (s.access_package_name ?? "").toLowerCase().includes(needle) ||
      s.external_reservation_id.toLowerCase().includes(needle));
  }, [rows, q]);

  const summary = useMemo(() => {
    const list = rows ?? [];
    return {
      total: list.length,
      withInternet: list.filter((s) => s.access_status).length,
      online: list.reduce((a, s) => a + (s.access_active_devices ?? 0), 0),
      arriving: list.filter((s) => s.status === "RESERVED").length,
    };
  }, [rows]);

  async function open(id: string) {
    setErr(null); setDetailBusy(true);
    try { setDetail(await api.get<StayDetail>("/pms-stays/" + id)); }
    catch (e) { setErr(e); }
    finally { setDetailBusy(false); }
  }

  const inHouseNoInternet =
    status === "IN_HOUSE" && rows !== null && rows.length > 0 && summary.withInternet === 0;

  return (
    <PageShell width="wide">
      <PageHeader
        eyebrow="Guests"
        title="Stays"
        description="What the property management system reports about who is in the building, and which internet package each room has been given. Stays themselves are changed in the PMS, not here."
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
          hint="Across the stays listed here"
        />
        <StatCard
          label="Arriving"
          value={rows ? summary.arriving.toLocaleString() : "—"}
          icon={<LogIn />}
          hint="Reserved, not yet checked in"
        />
      </div>

      {inHouseNoInternet && (
        <Callout tone="warning" title="No in-house room has an internet package">
          The guest list has arrived, but nobody has been given internet. If guests are reporting that they cannot
          get online, check that a package applies to these stays on{" "}
          <Link href="/internet-packages" className="underline underline-offset-2">Internet packages</Link>, and
          that their Wi-Fi network is pointed at this PMS on{" "}
          <Link href="/pms-routing" className="underline underline-offset-2">Network routing</Link>.
        </Callout>
      )}

      <Card>
        <CardBody className="border-b border-border py-3">
          <Toolbar>
            <div className="relative w-full max-w-xs">
              <Search className="pointer-events-none absolute left-2.5 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                aria-label="Search stays"
                placeholder="Room, guest, reservation or package…"
                className="pl-8"
                value={q}
                onChange={(e) => setQ(e.target.value)}
              />
            </div>
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
          </Toolbar>
        </CardBody>

        <CardBody className="p-0">
          {filtered === null ? (
            <SkeletonRows rows={6} cols={6} />
          ) : filtered.length === 0 ? (
            <EmptyState
              icon={<Hotel />}
              title={q ? "No stays match that search" : "No stays to show"}
              hint={q
                ? "Search covers the most recent 200 stays for the selected status."
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
                  <TH>Charges</TH>
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
                        <Badge tone="ok">Can be charged</Badge>
                      ) : (
                        <span className="text-xs text-muted-foreground">
                          Closed
                          {s.posting_block_reason && (
                            <span className="block text-2xs">
                              {s.posting_block_reason.replace(/_/g, " ").toLowerCase()}
                            </span>
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
                ? <Badge tone="ok">Charges allowed</Badge>
                : <Badge tone="default">Charges closed</Badge>}
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
                    className="mt-3 inline-block text-xs text-primary hover:underline"
                  >
                    See this room&rsquo;s devices →
                  </Link>
                </>
              ) : (
                <p className="text-sm text-muted-foreground">
                  No internet package has been given to this stay. That normally means nobody from the room has
                  signed in yet — a package is granted at sign-in, not at check-in.
                </p>
              )}
            </section>

            <DList
              items={[
                { label: "Arrival", value: shortDate(detail.arrival) },
                { label: "Departure", value: shortDate(detail.departure) },
                {
                  label: "Left the property",
                  value: detail.effective_checkout_at ? formatRelative(detail.effective_checkout_at) : "Still in house",
                },
                { label: "Occupants", value: String(detail.occupants) },
                {
                  label: "Charges to room",
                  value: detail.posting_allowed
                    ? "Allowed"
                    : `Closed${detail.posting_block_reason ? ` — ${detail.posting_block_reason.replace(/_/g, " ").toLowerCase()}` : ""}`,
                },
                ...(detail.posting_permission_source
                  ? [{ label: "Decided by", value: detail.posting_permission_source }]
                  : []),
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

            <div className="grid gap-5 sm:grid-cols-2">
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
              <div>
                <h3 className="mb-1.5 text-sm font-semibold">Folios</h3>
                {detail.folios.length === 0 ? (
                  <p className="text-sm text-muted-foreground">No folio is linked to this stay.</p>
                ) : (
                  <ul className="space-y-1 text-sm">
                    {detail.folios.map((f) => (
                      <li key={f.external_folio_id} className="flex flex-wrap items-center gap-1.5">
                        <span className="font-mono text-xs">{f.external_folio_id}</span>
                        <span className="text-xs text-muted-foreground">
                          {f.folio_kind.toLowerCase()} · {f.status.toLowerCase()}
                        </span>
                        {f.is_default_posting_target && <Badge tone="info">Charges go here</Badge>}
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            </div>
          </>
        )}
      </DetailDialog>
    </PageShell>
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
