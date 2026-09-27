"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import type { ApplianceRow } from "@/lib/api";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { ActivationBadge, ConnectionBadge, LicenseBadge } from "@/components/status-badge";
import { connectionSentence, licenseSentence } from "@/lib/status";

/** Waiting-for-activation first (they are the ones that need someone), then by serial. */
export function sortAppliances(rows: ApplianceRow[]): ApplianceRow[] {
  return [...rows].sort((a, b) => {
    const wa = a.activation === "waiting" ? 0 : 1;
    const wb = b.activation === "waiting" ? 0 : 1;
    return wa - wb || a.serial.localeCompare(b.serial);
  });
}

/**
 * The one appliance table: the fleet list and a customer's Appliances tab. A row opens the appliance, where
 * every action lives; the only action on the row itself is Activate, because a waiting appliance is the one
 * thing an operator comes to this list to find.
 */
export function ApplianceTable({
  rows,
  showCustomer = true,
  onActivate,
}: {
  rows: ApplianceRow[];
  showCustomer?: boolean;
  /** Offered on waiting rows when the role can activate. */
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
          <TH><span className="sr-only">Actions</span></TH>
        </TR>
      </THead>
      <tbody>
        {rows.map((a) => {
          const href = `/appliances/${a.id}`;
          return (
            <TR key={a.id} className="cursor-pointer" onClick={() => router.push(href)}>
              <TD>
                <Link
                  href={href}
                  onClick={(e) => e.stopPropagation()}
                  className="rounded font-mono text-sm font-semibold hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  {a.serial}
                </Link>
                <div className="text-caption text-muted-foreground">
                  {[a.hostname, a.model].filter(Boolean).join(" · ") || "—"}
                </div>
              </TD>
              {showCustomer && (
                <TD className="hidden md:table-cell">
                  {a.customer_name ? (
                    <>
                      <div className="text-sm">{a.customer_name}</div>
                      <div className="text-caption text-muted-foreground">{a.site_name ?? "—"}</div>
                    </>
                  ) : (
                    <span className="text-caption text-muted-foreground">Not assigned yet</span>
                  )}
                </TD>
              )}
              <TD><ActivationBadge value={a.activation} /></TD>
              <TD className="hidden sm:table-cell">
                <div className="flex flex-col items-start gap-1">
                  <ConnectionBadge value={a.connection} />
                  {a.connection !== "connected" && (
                    <span className="text-caption text-muted-foreground">{connectionSentence(a)}</span>
                  )}
                </div>
              </TD>
              <TD className="hidden lg:table-cell">
                <div className="flex flex-col items-start gap-1">
                  <LicenseBadge value={a.license?.state} />
                  {a.license && a.license.state !== "none" && (
                    <span className="text-caption text-muted-foreground">{licenseSentence(a.license)}</span>
                  )}
                </div>
              </TD>
              <TD className="text-end">
                {a.activation === "waiting" && onActivate && (
                  <Button
                    size="sm"
                    onClick={(e) => {
                      e.stopPropagation();
                      onActivate(a);
                    }}
                  >
                    Activate<span className="sr-only"> {a.serial}</span>
                  </Button>
                )}
              </TD>
            </TR>
          );
        })}
      </tbody>
    </Table>
  );
}
