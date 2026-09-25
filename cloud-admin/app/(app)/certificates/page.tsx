"use client";

import { useEffect, useMemo, useState } from "react";
import { FileBadge } from "lucide-react";
import { api } from "@/lib/api";
import { Card } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { PageHeader, PageShell, StatCard, Toolbar } from "@/components/ui/page";
import { SearchInput } from "@/components/ui/data";
import { MonoId, SkeletonRows, Switch } from "@/components/ui/misc";
import { statusWord } from "@/lib/license-state";
import { formatDate, formatRelative } from "@/lib/utils";

type Cert = {
  id: string;
  appliance_id: string;
  serial: string;
  tenant_name: string;
  site_name: string;
  fingerprint_sha256: string;
  cert_serial: string;
  issuer: string;
  ca_version: number;
  not_before: string;
  not_after: string;
  status: string;
  created_at: string;
  revoked_at?: string | null;
  revocation_reason: string;
  last_rotation: string;
};

const tone = (s: string, expired: boolean) =>
  s === "revoked" ? "err" :
  expired ? "warn" :
  s === "active" ? "ok" :
  s === "superseded" ? "default" : "default";

/**
 * Read-only Certificate inventory. Shows metadata ONLY — public fingerprint, issuer, validity, status,
 * revocation reason and last rotation. Private keys and certificate PEM are never returned by the API or shown.
 */
export default function CertificatesPage() {
  const [rows, setRows] = useState<Cert[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [showSuperseded, setShowSuperseded] = useState(false);
  const [query, setQuery] = useState("");

  useEffect(() => {
    api.get<{ data: Cert[] }>("/cloud/v1/certificates")
      .then((r) => setRows(r.data ?? []))
      .catch((e) => { setErr(e?.message ?? "Failed to load"); setRows((p) => p ?? []); });
  }, []);

  const isExpired = (c: Cert) => new Date(c.not_after).getTime() < Date.now() && c.status === "active";

  const counts = useMemo(() => {
    const all = rows ?? [];
    const soon = Date.now() + 30 * 86400000;
    return {
      active: all.filter((c) => c.status === "active" && !isExpired(c)).length,
      expiring: all.filter((c) => c.status === "active" && !isExpired(c) && new Date(c.not_after).getTime() < soon).length,
      expired: all.filter(isExpired).length,
      revoked: all.filter((c) => c.status === "revoked").length,
    };
  }, [rows]);

  const visible = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (rows ?? [])
      .filter((c) => showSuperseded || c.status !== "superseded")
      .filter((c) => !q || [c.serial, c.tenant_name, c.site_name, c.fingerprint_sha256, c.issuer].join(" ").toLowerCase().includes(q));
  }, [rows, showSuperseded, query]);

  return (
    <PageShell width="wide">
      <PageHeader
        eyebrow="Administration"
        title="Certificates"
        icon={<FileBadge />}
        description="Appliance client certificates issued by Central's internal certificate authority. Read-only and metadata only — no private keys or certificate material are ever shown."
      />

      <ErrorBanner err={err} />

      <section aria-label="Certificate counts" className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <StatCard label="Active" value={rows ? counts.active : "—"} tone="ok" icon={<FileBadge />} />
        <StatCard label="Expiring in 30 days" value={rows ? counts.expiring : "—"} tone="warn" />
        <StatCard label="Expired" value={rows ? counts.expired : "—"} tone="warn" />
        <StatCard label="Revoked" value={rows ? counts.revoked : "—"} tone="err" />
      </section>

      <Card>
        <Toolbar className="border-b border-border px-4 py-3">
          <SearchInput value={query} onChange={setQuery} placeholder="Search appliance, customer, fingerprint" label="Search certificates" />
          <label className="flex items-center gap-2 text-sm">
            <Switch checked={showSuperseded} onCheckedChange={setShowSuperseded} label="Show superseded" />
            Show superseded
          </label>
        </Toolbar>
        {rows === null ? (
          <SkeletonRows rows={5} cols={8} />
        ) : visible.length === 0 ? (
          rows.length === 0 ? (
            <EmptyState icon={<FileBadge />} title="No certificates" hint="Certificates appear here as appliances are activated." />
          ) : (
            <EmptyState title="No certificates match" hint="Nothing matches the search or the superseded filter."
              action={<Button variant="secondary" onClick={() => { setQuery(""); setShowSuperseded(true); }}>Show everything</Button>} />
          )
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>Appliance</TH><TH>Customer</TH><TH className="hidden md:table-cell">Site</TH>
                <TH className="hidden lg:table-cell">Fingerprint (SHA-256)</TH><TH className="hidden xl:table-cell">Issuer</TH>
                <TH className="hidden xl:table-cell">Issued</TH><TH>Expires</TH><TH>Status</TH>
                <TH className="hidden lg:table-cell">Last rotation</TH><TH className="hidden xl:table-cell">Revocation</TH>
              </TR>
            </THead>
            <tbody>
              {visible.map((c) => {
                const expired = isExpired(c);
                return (
                  <TR key={c.id}>
                    <TD className="font-mono text-xs">{c.serial || c.appliance_id.slice(0, 8)}</TD>
                    <TD>{c.tenant_name || "—"}</TD>
                    <TD className="hidden text-muted-foreground md:table-cell">{c.site_name || "—"}</TD>
                    <TD className="hidden lg:table-cell"><MonoId value={c.fingerprint_sha256} head={16} title="SHA-256" /></TD>
                    <TD className="hidden text-xs text-muted-foreground xl:table-cell">{c.issuer}</TD>
                    <TD className="hidden whitespace-nowrap text-xs text-muted-foreground xl:table-cell">{formatDate(c.not_before)}</TD>
                    <TD className="whitespace-nowrap text-xs text-muted-foreground">{formatDate(c.not_after)}</TD>
                    <TD><Badge tone={tone(c.status, expired) as any} dot>{expired ? "Expired" : statusWord(c.status)}</Badge></TD>
                    <TD className="hidden text-xs text-muted-foreground lg:table-cell">{formatRelative(c.last_rotation)}</TD>
                    <TD className="hidden text-xs text-muted-foreground xl:table-cell">
                      {c.revoked_at ? `${formatDate(c.revoked_at)}${c.revocation_reason ? ` · ${c.revocation_reason}` : ""}` : "—"}
                    </TD>
                  </TR>
                );
              })}
            </tbody>
          </Table>
        )}
      </Card>
    </PageShell>
  );
}
