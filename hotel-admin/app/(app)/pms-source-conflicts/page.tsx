"use client";

import { useCallback, useEffect, useMemo, useState } from "react";
import Link from "next/link";
import { AlertTriangle, Layers, RefreshCw } from "lucide-react";
import { api, PmsSourceConflict } from "@/lib/api";
import { PageHeader, PageShell, StatCard, StatGrid } from "@/components/ui/page";
import { Card } from "@/components/ui/card";
import { Table, TBody, TD, TH, THead, TR } from "@/components/ui/table";
import { StatusBadge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout } from "@/components/ui/error-banner";
import { HelpSection } from "@/components/help";
import { MonoId, SkeletonRows } from "@/components/ui/misc";
import { cn } from "@/lib/utils";

const FADE = "animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none";

const SEVERITY: Record<string, { tone: "err" | "warn" | "info"; label: string }> = {
  HIGH: { tone: "err", label: "High" },
  MEDIUM: { tone: "warn", label: "Medium" },
  LOW: { tone: "info", label: "Low" },
  DUPLICATE_SOURCE: { tone: "err", label: "Same PMS source" },
};

const LINK = "font-medium underline underline-offset-2";

function isOpen(c: PmsSourceConflict): boolean {
  return !c.resolution || c.resolution === "UNRESOLVED";
}

function ConnectionName({ label, id }: { label?: string; id: string }) {
  if (label) return <span className="font-medium">{label}</span>;
  return (
    <span className="inline-flex items-center gap-2 text-sm text-muted-foreground">
      Unnamed PMS connection <MonoId value={id} title="PMS connection" />
    </span>
  );
}

function WhatToDo({ c }: { c: PmsSourceConflict }) {
  if (c.detected) {
    return (
      <>
        Both point at the same PMS. Retire one, or point it at its own PMS, on{" "}
        <Link href="/pms-interfaces" className={LINK}>PMS connection</Link>.
      </>
    );
  }
  if (isOpen(c)) {
    return (
      <>
        Not fixed yet. Check both on <Link href="/pms-interfaces" className={LINK}>PMS connection</Link>.
      </>
    );
  }
  return <>Recorded with an outcome this console does not recognise.</>;
}

export default function PMSSourceConflictsPage() {
  const [rows, setRows] = useState<PmsSourceConflict[] | null>(null);
  const [failed, setFailed] = useState(false);
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const r = await api.get<{ conflicts: PmsSourceConflict[] }>("/pms-source-conflicts");
      setRows(r.conflicts ?? []);
      setFailed(false);
    } catch {
      setFailed(true);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load]);

  const counts = useMemo(() => {
    const list = rows ?? [];
    return {
      all: list.length,
      same: list.filter((c) => c.severity === "DUPLICATE_SOURCE").length,
      open: list.filter(isOpen).length,
    };
  }, [rows]);

  const spin = cn(loading && "animate-spin motion-reduce:animate-none");

  return (
    <PageShell>
      <PageHeader
        title="Duplicate sources"
        description="Two PMS connections that point at the same PMS server, so they cannot both be trusted for the same guests."
        actions={rows ? (
          <Button size="sm" variant="secondary" onClick={() => void load()} disabled={loading}>
            <RefreshCw className={spin} /> Refresh
          </Button>
        ) : undefined}
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

      {failed && rows && (
        <div className={FADE}>
          <Callout tone="warning">The list could not be refreshed. What is shown may be out of date.</Callout>
        </div>
      )}

      {rows !== null && rows.length > 0 && (
        <>
          <StatGrid cols={3} className={FADE}>
            <StatCard label="Pairs found" value={counts.all.toLocaleString()} />
            <StatCard label="Same PMS source" value={counts.same.toLocaleString()} tone={counts.same > 0 ? "err" : "default"} />
            <StatCard label="Still to fix" value={counts.open.toLocaleString()} tone={counts.open > 0 ? "warn" : "default"} />
          </StatGrid>
          <p className="text-sm text-muted-foreground">
            While both connections of a pair are in use, one client network cannot check both of them on{" "}
            <Link href="/pms-routing" className={LINK}>PMS routing</Link>.
          </p>
        </>
      )}

      <Card className="overflow-hidden">
        {failed && !rows ? (
          <div role="alert" className={FADE}>
            <EmptyState
              size="sm"
              icon={<AlertTriangle />}
              title="Duplicate sources could not be loaded"
              hint="Check the appliance is reachable, then try again. Nothing has been changed."
              action={
                <Button size="sm" variant="secondary" onClick={() => void load()} disabled={loading}>
                  <RefreshCw className={spin} /> Try again
                </Button>
              }
            />
          </div>
        ) : rows === null ? (
          <SkeletonRows rows={3} cols={4} />
        ) : rows.length === 0 ? (
          <EmptyState
            size="sm"
            icon={<Layers />}
            title="No duplicate sources"
            hint="No two PMS connections point at the same PMS."
          />
        ) : (
          <Table label="Duplicate sources" aria-label="Duplicate sources" className={FADE}>
            <THead>
              <TR>
                <TH>Connection</TH>
                <TH>Same PMS as</TH>
                <TH>Type</TH>
                <TH>What to do</TH>
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
                      {sev ? (
                        <StatusBadge tone={sev.tone}>{sev.label}</StatusBadge>
                      ) : (
                        <span className="text-muted-foreground">{c.severity ? "Not recognised" : "Not recorded"}</span>
                      )}
                    </TD>
                    <TD className="text-sm"><WhatToDo c={c} /></TD>
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
