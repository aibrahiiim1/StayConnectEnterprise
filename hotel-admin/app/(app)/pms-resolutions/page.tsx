"use client";

// GUEST SIGN-IN CHECKS — did the room number a guest typed match the guest list, and if not, why not.
//
// The screen was titled "Resolution evidence" and consisted of a column of uuids under the heading "Guest
// network", a column of SHOUTING_ENUM outcome codes, and a yes/no. An operator could not say what it was for,
// what a "resolution" was, or what the ids belonged to — which is exactly the feedback that produced this
// rewrite.
//
// Two changes, and the second one is the point:
//
//   * THE NETWORK HAS A NAME. edged now returns the guest network's name alongside its id. This matters more
//     than it sounds: the single most useful thing this page can show is "every failure is on one network",
//     and with a column of uuids that pattern was invisible because every row looked equally opaque.
//
//   * THE OUTCOMES ARE EXPLAINED. Each code now carries what it means and what to do about it, because
//     "AMBIGUOUS_ROOM" is a conclusion the system reached, not an instruction anyone can act on.
//
// WHAT IS STILL ABSENT, DELIBERATELY: no guest, room number or reservation appears anywhere on this page. A
// list of who tried to sign in and failed would be a way for any read-only admin session to enumerate who is
// staying at the property, which is precisely what the guest-facing uniform failure message exists to prevent.
// The per-guest view is a separate, role-gated screen. That is a constraint on this page, not an oversight.

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { api, ListResp, PmsResolution } from "@/lib/api";
import { PageShell, PageHeader, StatCard } from "@/components/ui/page";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { SplitBar } from "@/components/ui/chart";
import { MonoId, SkeletonRows, Meter } from "@/components/ui/misc";
import { formatRelative } from "@/lib/utils";
import { ShieldCheck, RefreshCw } from "lucide-react";

// EVERY OUTCOME, IN WORDS, WITH THE ACTION IT IMPLIES.
//
// An unknown code falls through to a de-underscored version rather than being hidden — a connector producing an
// outcome this table has not been taught must still be countable.
const OUTCOMES: Record<string, { label: string; meaning: string; fix?: string; tone: "ok" | "warn" | "err" }> = {
  VERIFIED: {
    label: "Verified",
    meaning: "The room number and name matched a guest who is checked in. The guest was let online.",
    tone: "ok",
  },
  NO_MATCH: {
    label: "No matching room",
    meaning: "Nothing in the guest list matched what the guest typed.",
    fix: "Usually a typo, or a guest whose check-in has not reached the appliance yet. Check PMS activity.",
    tone: "warn",
  },
  NAME_MISMATCH: {
    label: "Name did not match",
    meaning: "The room exists and is occupied, but the surname the guest typed does not match the one on the stay.",
    fix: "Guests often give a first name, a spouse's name or a company name. The desk can confirm the name on the reservation.",
    tone: "warn",
  },
  AMBIGUOUS: {
    label: "More than one match",
    meaning: "The details matched more than one stay, so the appliance refused rather than picking one.",
    fix: "Usually a duplicated room in the guest list. Check Duplicate sources.",
    tone: "warn",
  },
  AMBIGUOUS_ROOM: {
    label: "Room matched twice",
    meaning: "Two stays currently claim the same room, so the appliance could not tell which guest this is.",
    fix: "A departing and an arriving guest overlapping on one room is the usual cause. It clears itself once the PMS sends the check-out.",
    tone: "warn",
  },
  NOT_IN_HOUSE: {
    label: "Not checked in",
    meaning: "The room and name matched a stay that is not currently in house.",
    fix: "A guest trying to sign in before check-in or after check-out. Checkout grace and post-stay access cover the intended cases.",
    tone: "warn",
  },
  STALE_OCCUPANCY: {
    label: "Guest list too old",
    meaning: "The appliance's copy of the guest list is older than the connection allows it to trust.",
    fix: "The PMS connection is not delivering updates. Open PMS connection.",
    tone: "err",
  },
  INDETERMINATE: {
    label: "Could not be decided",
    meaning: "The appliance could not reach a safe conclusion, so it refused rather than guessing.",
    fix: "If this is the common outcome, the PMS feed is the place to look.",
    tone: "err",
  },
  FEED_UNAVAILABLE: {
    label: "PMS unavailable",
    meaning: "The property management system could not be consulted at all.",
    fix: "Open PMS connection. Vouchers and guest accounts still work while it is down.",
    tone: "err",
  },
  NO_ROUTE: {
    label: "Network not pointed at a PMS",
    meaning: "The guest's Wi-Fi network is not mapped to any property management system, so there was nothing to check against.",
    fix: "Set the mapping on Network routing.",
    tone: "err",
  },
};

const outcome = (code: string) =>
  OUTCOMES[code] ?? {
    label: code.replace(/_/g, " ").toLowerCase(),
    meaning: "This outcome is not one this screen has wording for. The code is the connector's own.",
    tone: "warn" as const,
  };

export default function PMSResolutionsPage() {
  const [rows, setRows] = useState<PmsResolution[] | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [refreshing, setRefreshing] = useState(false);

  const load = useCallback(async (manual = false) => {
    if (manual) setRefreshing(true);
    try {
      const r = await api.get<ListResp<PmsResolution>>("/pms-resolutions");
      setRows(r.data ?? []);
      setErr(null);
    } catch (e) {
      setErr(e);
      setRows([]);
    } finally {
      if (manual) setRefreshing(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  const summary = useMemo(() => {
    if (!rows) return null;
    const byOutcome = new Map<string, number>();
    // Grouped by NAME where there is one, so the per-network breakdown is readable. Networks that have been
    // deleted since a check ran keep their id, which is the honest label for a network that no longer exists.
    const byNetwork = new Map<string, { name: string; total: number; verified: number }>();
    let verified = 0;
    for (const r of rows) {
      byOutcome.set(r.outcome_code, (byOutcome.get(r.outcome_code) ?? 0) + 1);
      if (r.resolved) verified += 1;
      const key = r.guest_network_id;
      const cur = byNetwork.get(key) ?? {
        name: r.guest_network_name || "Network no longer configured",
        total: 0,
        verified: 0,
      };
      cur.total += 1;
      if (r.resolved) cur.verified += 1;
      byNetwork.set(key, cur);
    }
    return {
      total: rows.length,
      verified,
      outcomes: [...byOutcome.entries()].sort((a, b) => b[1] - a[1]),
      networks: [...byNetwork.entries()].sort((a, b) => b[1].total - a[1].total),
      newest: rows[0]?.resolved_at,
    };
  }, [rows]);

  // THE PATTERN WORTH SURFACING. One network failing while the others succeed is a routing problem, and it is the
  // single most common cause of "the Wi-Fi doesn't work in the annexe" — so it is called out rather than left to
  // be spotted in the table.
  const suspectNetwork = useMemo(() => {
    if (!summary || summary.networks.length < 2) return null;
    const bad = summary.networks.filter(([, n]) => n.total >= 3 && n.verified === 0);
    const good = summary.networks.filter(([, n]) => n.verified > 0);
    return bad.length > 0 && good.length > 0 ? bad : null;
  }, [summary]);

  const failed = summary ? summary.total - summary.verified : 0;

  return (
    <PageShell width="wide">
      <PageHeader
        eyebrow="Property management system"
        title="Guest sign-in checks"
        description="Every recent attempt to verify a guest against the property management system, and what the appliance concluded. This is where to look when guests say they cannot get online with their room number."
        actions={
          <Button variant="secondary" size="sm" onClick={() => void load(true)} disabled={refreshing}>
            <RefreshCw className={refreshing ? "animate-spin" : undefined} /> Refresh
          </Button>
        }
      />

      <ErrorBanner err={err} />

      <Callout tone="neutral" title="No guest is named on this page">
        Only the outcome and the network are recorded here. A list of who tried to sign in and failed would let
        anyone with a read-only account work out who is staying at the property, so it is deliberately not
        collected — the guest&rsquo;s own stay is on{" "}
        <Link href="/stays" className="underline underline-offset-2">Stays</Link>, where looking at one person is
        the point and the access is gated accordingly.
      </Callout>

      {summary && summary.total > 0 && (
        <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
          <StatCard
            label="Checks recorded"
            value={summary.total.toLocaleString()}
            icon={<ShieldCheck />}
            hint={summary.newest ? `Most recent ${formatRelative(summary.newest)}` : undefined}
          />
          <StatCard
            label="Let online"
            value={summary.verified.toLocaleString()}
            tone="ok"
            hint={`${Math.round((summary.verified / summary.total) * 100)}% of attempts`}
          />
          <StatCard
            label="Refused"
            value={failed.toLocaleString()}
            tone={failed > 0 ? "warn" : "default"}
            hint={failed > 0 ? "See why below" : "Everyone who tried got in"}
          />
          <StatCard
            label="Networks involved"
            value={summary.networks.length.toLocaleString()}
            hint="Wi-Fi networks these attempts came from"
          />
        </div>
      )}

      {suspectNetwork && (
        <Callout tone="warning" title="Sign-in is failing on one network and working on others">
          {suspectNetwork.map(([id, n]) => (
            <div key={id}>
              Every one of the {n.total} attempts from <strong>{n.name}</strong> was refused, while other networks
              are succeeding.
            </div>
          ))}
          <div className="mt-1.5 text-xs">
            That pattern usually means the network is pointed at the wrong property management system, or at none.{" "}
            <Link href="/pms-routing" className="font-medium underline underline-offset-2">
              Check which PMS it uses
            </Link>
            .
          </div>
        </Callout>
      )}

      {summary && summary.total > 0 && (
        <div className="grid gap-4 lg:grid-cols-2">
          <Card>
            <CardHeader><CardTitle>Why guests were refused</CardTitle></CardHeader>
            <CardBody className="space-y-4">
              <SplitBar
                total={summary.total}
                parts={[
                  { name: "Let online", value: summary.verified, tone: "ok" },
                  { name: "Refused", value: failed, tone: failed > 0 ? "err" : "neutral" },
                ]}
              />
              <ul className="space-y-3">
                {summary.outcomes.map(([code, n]) => {
                  const o = outcome(code);
                  return (
                    <li key={code} className="flex gap-3">
                      <Badge tone={o.tone} className="mt-0.5 shrink-0">{n}</Badge>
                      <div className="min-w-0">
                        <div className="text-sm font-medium">{o.label}</div>
                        <p className="text-xs text-muted-foreground">{o.meaning}</p>
                        {o.fix && <p className="mt-0.5 text-xs text-muted-foreground">{o.fix}</p>}
                      </div>
                    </li>
                  );
                })}
              </ul>
            </CardBody>
          </Card>

          <Card>
            <CardHeader>
              <div>
                <CardTitle>By Wi-Fi network</CardTitle>
                <p className="mt-0.5 text-xs text-muted-foreground">
                  Where the attempts came from, and how many got in.
                </p>
              </div>
              <Link href="/pms-routing" className="text-xs text-muted-foreground hover:text-foreground">
                Network routing →
              </Link>
            </CardHeader>
            <CardBody className="space-y-4">
              {summary.networks.map(([id, n]) => (
                <div key={id}>
                  <div className="flex items-baseline justify-between gap-3">
                    <span className="truncate text-sm font-medium">{n.name}</span>
                    <span className="shrink-0 text-xs tabular text-muted-foreground">
                      {n.verified} of {n.total} let online
                    </span>
                  </div>
                  <Meter
                    className="mt-1.5"
                    value={n.verified}
                    max={n.total}
                    tone={n.verified === 0 ? "err" : n.verified === n.total ? "ok" : "warn"}
                  />
                </div>
              ))}
            </CardBody>
          </Card>
        </div>
      )}

      <Card>
        <CardHeader>
          <CardTitle>Recent attempts</CardTitle>
          <span className="text-xs text-muted-foreground">Newest first · up to 200</span>
        </CardHeader>
        <CardBody className="p-0">
          {rows === null ? (
            <SkeletonRows rows={6} cols={4} />
          ) : rows.length === 0 ? (
            <EmptyState
              icon={<ShieldCheck />}
              title="No guest has tried to sign in with a room number"
              hint="Either nobody has tried yet, or no guest network is set up to offer room sign-in."
            />
          ) : (
            <Table>
              {/*
                THE TABLE CARRIES NO PROSE, and that is deliberate twice over.

                Repeating the same explanatory sentence on two hundred rows is noise — the meaning belongs to the
                OUTCOME, not to each occurrence of it, so it lives once in the breakdown card above.

                It also keeps a hard guarantee easy to verify: every cell in this table is a timestamp, a network
                name or an outcome label. A test asserts that the rendered table contains no "room",
                "reservation", "stay" or "folio" text at all, which is a check that only works if the table holds
                nothing but those three kinds of value. Explanatory copy using the word "room" generically would
                defeat it, and the guarantee is worth more than the convenience.
              */}
              <THead>
                <TR>
                  <TH>When</TH>
                  <TH>Wi-Fi network</TH>
                  <TH>Result</TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((r) => {
                  const o = outcome(r.outcome_code);
                  return (
                    <TR key={r.id}>
                      <TD className="whitespace-nowrap text-sm text-muted-foreground">
                        {formatRelative(r.resolved_at)}
                      </TD>
                      <TD>
                        {r.guest_network_name ? (
                          <span className="text-sm">{r.guest_network_name}</span>
                        ) : (
                          // The id is shown only when there is no name to show — a network deleted since the
                          // check ran. It is a chip rather than raw text so it is clearly an identifier.
                          <span className="inline-flex items-center gap-2 text-xs text-muted-foreground">
                            Network no longer configured
                            <MonoId value={r.guest_network_id} title="Guest network" />
                          </span>
                        )}
                      </TD>
                      <TD>
                        <Badge tone={r.resolved ? "ok" : o.tone}>{o.label}</Badge>
                      </TD>
                    </TR>
                  );
                })}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>
    </PageShell>
  );
}
