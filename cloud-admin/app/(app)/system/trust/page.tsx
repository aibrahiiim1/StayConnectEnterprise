"use client";

import Link from "next/link";
import { useEffect, useMemo, useState } from "react";
import { api } from "@/lib/api";
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { PageHeader, PageShell } from "@/components/ui/page";
import { SearchInput } from "@/components/ui/data";
import { MonoId, Skeleton } from "@/components/ui/misc";
import { Fact, StateBadge } from "@/components/status-badge";
import { HelpList, HelpSection } from "@/components/help";
import { credentialStatusInfo, daysUntil, formatDateTime, formatDay } from "@/lib/status";

type CA = {
  id?: string;
  version?: number;
  subject?: string;
  fingerprint?: string;
  fingerprint_sha256?: string;
  not_before?: string;
  not_after?: string;
  status?: string;
};

type Cert = {
  id: string;
  appliance_id?: string;
  serial?: string;
  customer_name?: string;
  site_name?: string;
  fingerprint_sha256?: string;
  ca_version?: number;
  not_before?: string;
  not_after?: string;
  status: string;
  revoked_at?: string | null;
  revocation_reason?: string;
};

type SigningKey = {
  key_id: string;
  fingerprint?: string;
  state: string;
  current_assignments?: number;
  activated_at?: string;
  verify_only_at?: string | null;
  revoked_at?: string | null;
};

type Trust = {
  ca?: CA[];
  certificates?: { active?: number; revoked?: number; expiring_30d?: number; items?: Cert[] };
  assignment_keys?: SigningKey[];
  registry?: { version?: number; issued_at?: string } | null;
};

/** Read-only: the chain of trust between Central and the appliances. Nothing here can be changed from the console. */
export default function TrustPage() {
  const [t, setT] = useState<Trust | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [query, setQuery] = useState("");

  useEffect(() => {
    api.get<Trust>("/cloud/v1/trust").then(setT).catch(setErr);
  }, []);

  const certs = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (t?.certificates?.items ?? []).filter(
      (c) => !q || [c.serial, c.customer_name, c.site_name, c.fingerprint_sha256, c.status].join(" ").toLowerCase().includes(q),
    );
  }, [t, query]);

  return (
    <PageShell width="wide">
      <PageHeader
        title="Trust & keys"
        description="What lets appliances and Central trust each other. Read only."
        help={
          <>
            <HelpSection title="The pieces">
              <HelpList
                items={[
                  <><strong>Certificate authority</strong>: signs every appliance certificate. Its root key is kept offline.</>,
                  <><strong>Appliance certificates</strong>: each activated appliance proves who it is with one. Only public fingerprints are shown; private keys never leave the appliance.</>,
                  <><strong>Assignment signing keys</strong>: sign the document that binds an appliance to its customer and site.</>,
                  <><strong>Key registry</strong>: the signed list of signing keys that appliances accept.</>,
                ]}
              />
            </HelpSection>
            <HelpSection title="Fixing one appliance">
              <p>To reissue one appliance&apos;s certificate, open the appliance and use <strong>Advanced</strong>.</p>
            </HelpSection>
          </>
        }
      />

      <ErrorBanner err={err} />

      {!t && !err ? (
        <div className="space-y-4" aria-busy="true">
          <span className="sr-only">Loading</span>
          <Skeleton className="h-32" /><Skeleton className="h-64" />
        </div>
      ) : t ? (
        <>
          <div className="grid gap-5 lg:grid-cols-2">
            <Card>
              <CardHeader><CardTitle>Certificate authority</CardTitle></CardHeader>
              {(t.ca ?? []).length === 0 ? (
                <EmptyState title="No certificate authority reported" className="py-8" />
              ) : (
                <ul className="divide-y divide-border">
                  {t.ca!.map((ca, i) => (
                    <li key={ca.id ?? i} className="flex flex-wrap items-start justify-between gap-3 px-5 py-3">
                      <div className="min-w-0 space-y-1">
                        <div className="text-sm font-medium">{ca.subject ?? `Version ${ca.version ?? "—"}`}</div>
                        <div className="text-caption text-muted-foreground">
                          Valid {formatDay(ca.not_before)} to {formatDay(ca.not_after)}
                        </div>
                        <MonoId value={ca.fingerprint_sha256 ?? ca.fingerprint} head={16} title="Fingerprint" />
                      </div>
                      {ca.status && <StateBadge info={credentialStatusInfo(ca.status)} />}
                    </li>
                  ))}
                </ul>
              )}
            </Card>

            <Card>
              <CardHeader>
                <div className="space-y-0.5">
                  <CardTitle>Key registry</CardTitle>
                  <CardDescription>The signed list of assignment keys appliances accept.</CardDescription>
                </div>
              </CardHeader>
              <dl className="grid grid-cols-2 gap-4 px-5 py-4">
                <Fact label="Version">{t.registry?.version != null ? `v${t.registry.version}` : "—"}</Fact>
                <Fact label="Signed">{formatDateTime(t.registry?.issued_at)}</Fact>
              </dl>
            </Card>
          </div>

          <Card>
            <CardHeader><CardTitle>Assignment signing keys</CardTitle></CardHeader>
            {(t.assignment_keys ?? []).length === 0 ? (
              <EmptyState title="No signing keys reported" className="py-8" />
            ) : (
              <Table aria-label="Assignment signing keys">
                <THead>
                  <TR>
                    <TH>Key</TH><TH>State</TH><TH className="hidden sm:table-cell text-end">Appliances signed</TH>
                    <TH className="hidden md:table-cell">In use since</TH><TH className="hidden lg:table-cell">Fingerprint</TH>
                  </TR>
                </THead>
                <tbody>
                  {t.assignment_keys!.map((k) => (
                    <TR key={k.key_id}>
                      <TD className="font-mono text-xs">{k.key_id}</TD>
                      <TD><StateBadge info={credentialStatusInfo(k.state)} /></TD>
                      <TD className="hidden text-end tabular sm:table-cell">{k.current_assignments ?? "—"}</TD>
                      <TD className="hidden text-muted-foreground md:table-cell">{formatDay(k.activated_at)}</TD>
                      <TD className="hidden lg:table-cell"><MonoId value={k.fingerprint} head={16} title="Fingerprint" /></TD>
                    </TR>
                  ))}
                </tbody>
              </Table>
            )}
          </Card>

          <Card>
            <CardHeader>
              <div className="space-y-0.5">
                <CardTitle>Appliance certificates</CardTitle>
                <CardDescription>
                  {t.certificates?.active ?? 0} active · {t.certificates?.expiring_30d ?? 0} expiring within 30 days ·{" "}
                  {t.certificates?.revoked ?? 0} revoked
                </CardDescription>
              </div>
              <SearchInput value={query} onChange={setQuery} placeholder="Search serial or customer" label="Search certificates" />
            </CardHeader>
            {certs.length === 0 ? (
              <EmptyState title={query ? "No certificates match" : "No certificates yet"} className="py-8" />
            ) : (
              <Table aria-label="Appliance certificates">
                <THead>
                  <TR>
                    <TH>Appliance</TH><TH className="hidden md:table-cell">Customer · site</TH><TH>State</TH>
                    <TH>Expires</TH><TH className="hidden lg:table-cell">Fingerprint</TH>
                  </TR>
                </THead>
                <tbody>
                  {certs.map((c) => {
                    const left = daysUntil(c.not_after);
                    return (
                      <TR key={c.id}>
                        <TD>
                          {c.appliance_id ? (
                            <Link href={`/appliances/${c.appliance_id}`} className="font-mono text-xs underline-offset-2 hover:underline">{c.serial ?? "Appliance"}</Link>
                          ) : <span className="font-mono text-xs">{c.serial ?? "—"}</span>}
                        </TD>
                        <TD className="hidden md:table-cell">
                          <div className="text-sm">{c.customer_name ?? "—"}</div>
                          <div className="text-caption text-muted-foreground">{c.site_name ?? ""}</div>
                        </TD>
                        <TD>
                          <StateBadge info={credentialStatusInfo(c.status)} />
                          {c.revocation_reason && <div className="mt-1 text-caption text-muted-foreground">{c.revocation_reason}</div>}
                        </TD>
                        <TD>
                          {formatDay(c.not_after)}
                          {left !== null && c.status === "active" && (
                            <div className="text-caption text-muted-foreground">{left < 0 ? "expired" : `in ${left} days`}</div>
                          )}
                        </TD>
                        <TD className="hidden lg:table-cell"><MonoId value={c.fingerprint_sha256} head={16} title="Fingerprint" /></TD>
                      </TR>
                    );
                  })}
                </tbody>
              </Table>
            )}
          </Card>
        </>
      ) : null}
    </PageShell>
  );
}
