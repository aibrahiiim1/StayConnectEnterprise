"use client";

// Read-only for every role: which connection wins is decided on the PMS connection itself. The raw id is shown
// only when there is no label.

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { Layers } from "lucide-react";
import { api, PmsSourceConflict } from "@/lib/api";
import { PageHeader, PageShell, StatCard, StatGrid } from "@/components/ui/page";
import { Card } from "@/components/ui/card";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { StatusBadge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { HelpSection } from "@/components/help";
import { MonoId, SkeletonRows } from "@/components/ui/misc";

const SEVERITY: Record<string, { tone: "err" | "warn" | "info"; label: string }> = {
  HIGH: { tone: "err", label: "High" },
  MEDIUM: { tone: "warn", label: "Medium" },
  LOW: { tone: "info", label: "Low" },
  // Detected from published configuration: two connections whose live versions dial the same PMS source.
  DUPLICATE_SOURCE: { tone: "err", label: "Same PMS source" },
};

const RESOLUTION_WORDS: Record<string, string> = {
  UNRESOLVED: "Not decided yet",
};
const resolutionWords = (r?: string) =>
  !r ? "Not decided yet" : RESOLUTION_WORDS[r] ?? r.replace(/_/g, " ").toLowerCase().replace(/^./, (c) => c.toUpperCase());

function ConnectionName({ label, id }: { label?: string; id: string }) {
  if (label) return <span className="font-medium">{label}</span>;
  return (
    <span className="inline-flex items-center gap-2 text-sm text-muted-foreground">
      Unnamed connection <MonoId value={id} title="PMS connection" />
    </span>
  );
}

export default function PMSSourceConflictsPage() {
  const [rows, setRows] = useState<PmsSourceConflict[] | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let alive = true;
    (async () => {
      try {
        const r = await api.get<{ conflicts: PmsSourceConflict[] }>("/pms-source-conflicts");
        if (alive) setRows(r.conflicts ?? []);
      } catch (e: any) {
        if (!alive) return;
        setErr(e?.message ?? "Failed to load source conflicts");
        setRows([]);
      }
    })();
    return () => {
      alive = false;
    };
  }, []);

  const counts = useMemo(() => {
    const list = rows ?? [];
    return {
      all: list.length,
      high: list.filter((c) => c.severity === "HIGH").length,
      open: list.filter((c) => !c.resolution || c.resolution === "UNRESOLVED").length,
    };
  }, [rows]);

  return (
    <PageShell>
      <PageHeader
        title="Duplicate sources"
        description="Two PMS connections that point at the same PMS server, so they cannot both be trusted for the same guests."
        help={
          <>
            <HelpSection title="What a duplicate source is">
              <p>
                Two PMS connections that are the same physical PMS, reached twice. Both would write to it and both would
                claim its guests, so a guest could be verified against either. Only one connection may own a PMS server.
              </p>
              <p>
                The same room number at two <em>different</em> PMS servers is not a duplicate: each connection has its own
                rooms and stays, and a guest is only ever matched within the connections their network is routed to.
              </p>
            </HelpSection>
            <HelpSection title="Nothing is changed from here">
              <p>
                This list is for information. Which connection is authoritative is set on{" "}
                <Link href="/pms-interfaces" className="font-medium underline underline-offset-2">PMS connection</Link>.
              </p>
            </HelpSection>
          </>
        }
      />

      <ErrorBanner err={err} />

      {rows !== null && rows.length > 0 && (
        <StatGrid cols={3}>
          <StatCard label="Conflicts" value={counts.all.toLocaleString()} />
          <StatCard label="High severity" value={counts.high.toLocaleString()} tone={counts.high > 0 ? "err" : "default"} />
          <StatCard label="Not decided yet" value={counts.open.toLocaleString()} tone={counts.open > 0 ? "warn" : "default"} />
        </StatGrid>
      )}

      <Card className="overflow-hidden">
        {rows === null ? (
          <SkeletonRows rows={3} cols={4} />
        ) : rows.length === 0 ? (
          err ? null : (
            <EmptyState size="sm"
              icon={<Layers />}
              title="No source conflicts"
              hint="No two PMS connections are claiming the same rooms."
            />
          )
        ) : (
          <Table label="Source conflicts">
            <THead>
              <TR>
                <TH>Connection</TH>
                <TH>Conflicts with</TH>
                <TH>Severity</TH>
                <TH>Resolution</TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((c) => {
                const sev = c.severity ? SEVERITY[c.severity] : undefined;
                return (
                  <TR key={c.id}>
                    <TD><ConnectionName label={c.interface_a_label} id={c.interface_a} /></TD>
                    <TD><ConnectionName label={c.interface_b_label} id={c.interface_b} /></TD>
                    <TD nowrap>
                      {c.severity ? (
                        <StatusBadge tone={sev?.tone ?? "default"}>{sev?.label ?? c.severity.toLowerCase()}</StatusBadge>
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </TD>
                    <TD className="text-sm">
                      {c.detected
                        ? "Both live versions point at the same PMS. Retire or reconfigure one of them on PMS connection."
                        : resolutionWords(c.resolution)}
                    </TD>
                  </TR>
                );
              })}
            </TBody>
          </Table>
        )}
      </Card>
    </PageShell>
  );
}
