"use client";

// Read-only for every role: which connection wins is decided on the PMS connection itself. The raw id is shown
// only when there is no label.

import { useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { Layers, AlertTriangle, Hourglass } from "lucide-react";
import { api, PmsSourceConflict } from "@/lib/api";
import { PageHeader, PageShell, StatCard } from "@/components/ui/page";
import { Card } from "@/components/ui/card";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { HelpSection } from "@/components/help";
import { MonoId, SkeletonRows } from "@/components/ui/misc";

const SEVERITY: Record<string, { tone: "err" | "warn" | "info"; label: string }> = {
  HIGH: { tone: "err", label: "high" },
  MEDIUM: { tone: "warn", label: "medium" },
  LOW: { tone: "info", label: "low" },
  // Detected from published configuration: two connections whose live versions dial the same PMS source.
  DUPLICATE_SOURCE: { tone: "err", label: "same PMS source" },
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
        eyebrow="Hotel"
        title="Duplicate sources"
        icon={<Layers />}
        description="Rooms claimed by two PMS connections. Their guests cannot be verified until one is given authority."
        help={
          <>
            <HelpSection title="What a duplicate source is">
              <p>
                Two PMS connections claiming the same rooms. Until one of them is given authority, guests in the
                contested rooms cannot be verified.
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
        <div className="grid gap-4 sm:grid-cols-3">
          <StatCard label="Conflicts" value={counts.all.toLocaleString()} icon={<Layers />} tone="primary" />
          <StatCard icon={<AlertTriangle />} label="High severity" value={counts.high.toLocaleString()} tone={counts.high > 0 ? "err" : "default"} />
          <StatCard icon={<Hourglass />} label="Not decided yet" value={counts.open.toLocaleString()} tone={counts.open > 0 ? "warn" : "default"} />
        </div>
      )}

      <Card className="overflow-hidden">
        {rows === null ? (
          <SkeletonRows rows={3} cols={4} />
        ) : rows.length === 0 ? (
          err ? null : (
            <EmptyState
              icon={<Layers />}
              title="No source conflicts"
              hint="No two PMS connections are claiming the same rooms."
            />
          )
        ) : (
          <Table>
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
                    <TD>
                      {c.severity ? (
                        <Badge tone={sev?.tone ?? "default"} dot>{sev?.label ?? c.severity.toLowerCase()}</Badge>
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
