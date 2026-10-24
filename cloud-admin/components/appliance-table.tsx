"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import type { ApplianceRow } from "@/lib/api";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { RowActions } from "@/components/ui/row-actions";
import { ActivationBadge, ConnectionBadge, LicenseBadge } from "@/components/status-badge";
import { connectionInfo, connectionSentence, licenseInfo, licenseSentence } from "@/lib/status";

/** The detail under a state badge, omitted when it would only repeat the badge. */
function SecondLine({ text, unless }: { text: string; unless: string }) {
  if (!text || text === unless) return null;
  return <div className="mt-0.5 text-xs text-muted-foreground">{text}</div>;
}

export function sortAppliances(rows: ApplianceRow[]): ApplianceRow[] {
  return [...rows].sort((a, b) => {
    const wa = a.activation === "waiting" ? 0 : 1;
    const wb = b.activation === "waiting" ? 0 : 1;
    return wa - wb || a.serial.localeCompare(b.serial);
  });
}

export const ROW_LINK =
  "rounded font-mono text-sm font-medium text-foreground hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

export function ApplianceTable({
  rows,
  showCustomer = true,
  onActivate,
}: {
  rows: ApplianceRow[];
  showCustomer?: boolean;
  onActivate?: (a: ApplianceRow) => void;
}) {
  const router = useRouter();
  return (
    <Table aria-label="Appliances">
      <THead>
        <TR>
          <TH>Appliance</TH>
          {showCustomer && <TH className="hidden md:table-cell">Customer · site</TH>}
          <TH>Activation</TH>
          <TH className="hidden sm:table-cell">Connection</TH>
          <TH className="hidden lg:table-cell">License</TH>
          <TH className="w-0"><span className="sr-only">Actions</span></TH>
        </TR>
      </THead>
      <TBody>
        {rows.map((a) => {
          const href = `/appliances/${a.id}`;
          return (
            <TR key={a.id} onClick={() => router.push(href)}>
              <TD className="max-w-[16rem]">
                <Link href={href} onClick={(e) => e.stopPropagation()} className={ROW_LINK}>
                  {a.serial}
                </Link>
                <div className="truncate text-xs text-muted-foreground" title={[a.hostname, a.model].filter(Boolean).join(" · ") || undefined}>
                  {[a.hostname, a.model].filter(Boolean).join(" · ") || "—"}
                </div>
              </TD>
              {showCustomer && (
                <TD className="hidden max-w-[16rem] md:table-cell">
                  {a.customer_name ? (
                    <>
                      <div className="truncate" title={a.customer_name}>{a.customer_name}</div>
                      <div className="truncate text-xs text-muted-foreground" title={a.site_name ?? undefined}>{a.site_name ?? "—"}</div>
                    </>
                  ) : (
                    <span className="text-muted-foreground">Not assigned yet</span>
                  )}
                </TD>
              )}
              <TD nowrap><ActivationBadge value={a.activation} /></TD>
              <TD className="hidden sm:table-cell">
                <ConnectionBadge value={a.connection} />
                <SecondLine text={connectionSentence(a)} unless={connectionInfo(a.connection).label} />
              </TD>
              <TD className="hidden lg:table-cell">
                <LicenseBadge value={a.license?.state} />
                {a.license && a.license.state !== "none" && (
                  <SecondLine text={licenseSentence(a.license)} unless={licenseInfo(a.license.state).label} />
                )}
              </TD>
              <TD className="w-0">
                {a.activation === "waiting" && onActivate && (
                  <RowActions subject={a.serial} primary={{ label: "Activate", onSelect: () => onActivate(a) }} />
                )}
              </TD>
            </TR>
          );
        })}
      </TBody>
    </Table>
  );
}
