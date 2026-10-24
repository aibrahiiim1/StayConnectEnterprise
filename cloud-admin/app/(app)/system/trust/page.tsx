"use client";

import Link from "next/link";
import { useEffect, useMemo, useState } from "react";
import { FileKey2, KeyRound, ShieldCheck } from "lucide-react";
import { api } from "@/lib/api";
import { SectionCard } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { PageHeader, PageShell, StatCard, StatGrid } from "@/components/ui/page";
import { FilterBar } from "@/components/ui/toolbar";
import { DescriptionList, SearchInput } from "@/components/ui/data";
import { DetailsDrawer, DetailsSection } from "@/components/ui/details-drawer";
import { MonoId, Skeleton } from "@/components/ui/misc";
import { StateBadge } from "@/components/status-badge";
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

type Selected = { kind: "ca"; item: CA } | { kind: "key"; item: SigningKey } | { kind: "cert"; item: Cert };

const fullId = (v?: string | null) => (v ? <span className="break-all font-mono text-xs">{v}</span> : null);
const SERIAL_LINK = "rounded font-mono text-xs underline-offset-2 hover:underline focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring";

export default function TrustPage() {
  const [t, setT] = useState<Trust | null>(null);
  const [err, setErr] = useState<unknown>(null);
  const [query, setQuery] = useState("");
  const [selected, setSelected] = useState<Selected | null>(null);

  useEffect(() => {
    api.get<Trust>("/cloud/v1/trust").then(setT).catch(setErr);
  }, []);

  const certs = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (t?.certificates?.items ?? []).filter(
      (c) => !q || [c.serial, c.customer_name, c.site_name, c.fingerprint_sha256, c.status].join(" ").toLowerCase().includes(q),
    );
  }, [t, query]);

  const keys = t?.assignment_keys ?? [];
  const cas = t?.ca ?? [];
  const expiring = t?.certificates?.expiring_30d ?? 0;

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
                  <><strong>Assignment signing keys</strong>: sign the document that binds an appliance to its customer and site. A key moves from active to verify-only to revoked; each step&apos;s date is in its details.</>,
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
        <div className="space-y-5" aria-busy="true">
          <span className="sr-only">Loading</span>
          <StatGrid cols={4}>
            {Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-[5.25rem]" />)}
          </StatGrid>
          <Skeleton className="h-48" />
          <Skeleton className="h-64" />
        </div>
      ) : t ? (
        <>
          <StatGrid cols={4}>
            <StatCard label="Active certificates" icon={<ShieldCheck />} value={(t.certificates?.active ?? 0).toLocaleString()} />
            <StatCard
              label="Expiring within 30 days"
              value={expiring.toLocaleString()}
              tone={expiring > 0 ? "warn" : "default"}
            />
            <StatCard label="Revoked certificates" value={(t.certificates?.revoked ?? 0).toLocaleString()} />
            <StatCard
              label="Key registry"
              icon={<KeyRound />}
              value={t.registry?.version != null ? `v${t.registry.version}` : "—"}
              hint={t.registry?.issued_at ? `Signed ${formatDay(t.registry.issued_at)}` : "Not signed yet"}
            />
          </StatGrid>

          <div className="grid gap-5 xl:grid-cols-2">
            <SectionCard title="Certificate authority" flush>
              {cas.length === 0 ? (
                <EmptyState size="sm" title="No certificate authority reported" />
              ) : (
                <Table aria-label="Certificate authority">
                  <THead>
                    <TR>
                      <TH>Authority</TH>
                      <TH>State</TH>
                      <TH className="hidden sm:table-cell">Valid until</TH>
                    </TR>
                  </THead>
                  <TBody>
                    {cas.map((ca, i) => (
                      <TR key={ca.id ?? i} onClick={() => setSelected({ kind: "ca", item: ca })}>
                        <TD truncate title={ca.subject} className="max-w-[12rem]">
                          <div className="truncate font-medium">{ca.subject ?? `Version ${ca.version ?? "—"}`}</div>
                          {ca.version != null && <div className="text-xs text-muted-foreground">Version {ca.version}</div>}
                        </TD>
                        <TD nowrap>{ca.status ? <StateBadge info={credentialStatusInfo(ca.status)} /> : "—"}</TD>
                        <TD nowrap muted className="hidden sm:table-cell">{formatDay(ca.not_after)}</TD>
                      </TR>
                    ))}
                  </TBody>
                </Table>
              )}
            </SectionCard>

            <SectionCard title="Assignment signing keys" flush>
              {keys.length === 0 ? (
                <EmptyState size="sm" title="No signing keys reported" />
              ) : (
                <Table aria-label="Assignment signing keys">
                  <THead>
                    <TR>
                      <TH>Key</TH>
                      <TH>State</TH>
                      <TH numeric className="hidden sm:table-cell" title="Appliances signed">Signed</TH>
                      <TH className="hidden md:table-cell">In use since</TH>
                    </TR>
                  </THead>
                  <TBody>
                    {keys.map((k) => (
                      <TR key={k.key_id} onClick={() => setSelected({ kind: "key", item: k })}>
                        <TD truncate className="max-w-[9rem] font-mono text-xs" title={k.key_id}>{k.key_id}</TD>
                        <TD nowrap><StateBadge info={credentialStatusInfo(k.state)} /></TD>
                        <TD numeric className="hidden sm:table-cell">{k.current_assignments ?? "—"}</TD>
                        <TD nowrap muted className="hidden md:table-cell">{formatDay(k.activated_at)}</TD>
                      </TR>
                    ))}
                  </TBody>
                </Table>
              )}
            </SectionCard>
          </div>

          <SectionCard title="Appliance certificates" flush>
            <FilterBar
              inset
              search={<SearchInput value={query} onChange={setQuery} placeholder="Search serial or customer" label="Search certificates" />}
            />
            {certs.length === 0 ? (
              <EmptyState size="sm" icon={<FileKey2 />} title={query ? "No certificates match" : "No certificates yet"} />
            ) : (
              <Table aria-label="Appliance certificates">
                <THead>
                  <TR>
                    <TH>Appliance</TH>
                    <TH className="hidden md:table-cell">Customer · site</TH>
                    <TH>State</TH>
                    <TH>Expires</TH>
                    <TH className="hidden lg:table-cell">Fingerprint</TH>
                  </TR>
                </THead>
                <TBody>
                  {certs.map((c) => {
                    const left = daysUntil(c.not_after);
                    return (
                      <TR key={c.id} onClick={() => setSelected({ kind: "cert", item: c })}>
                        <TD nowrap>
                          {c.appliance_id ? (
                            <Link href={`/appliances/${c.appliance_id}`} onClick={(e) => e.stopPropagation()} className={SERIAL_LINK}>
                              {c.serial ?? "Appliance"}
                            </Link>
                          ) : <span className="font-mono text-xs">{c.serial ?? "—"}</span>}
                        </TD>
                        <TD className="hidden max-w-[16rem] md:table-cell">
                          <div className="truncate" title={c.customer_name ?? undefined}>{c.customer_name ?? "—"}</div>
                          {c.site_name && <div className="truncate text-xs text-muted-foreground" title={c.site_name}>{c.site_name}</div>}
                        </TD>
                        <TD nowrap><StateBadge info={credentialStatusInfo(c.status)} /></TD>
                        <TD nowrap>
                          {formatDay(c.not_after)}
                          {left !== null && c.status === "active" && (
                            <div className="text-xs text-muted-foreground">{left < 0 ? "expired" : `in ${left} days`}</div>
                          )}
                        </TD>
                        <TD className="hidden lg:table-cell" onClick={(e) => e.stopPropagation()}>
                          <MonoId value={c.fingerprint_sha256} head={16} title="Fingerprint" />
                        </TD>
                      </TR>
                    );
                  })}
                </TBody>
              </Table>
            )}
          </SectionCard>
        </>
      ) : null}

      <TrustDrawer selected={selected} onClose={() => setSelected(null)} />
    </PageShell>
  );
}

function TrustDrawer({ selected, onClose }: { selected: Selected | null; onClose: () => void }) {
  const open = !!selected;
  const common = { open, onOpenChange: (v: boolean) => { if (!v) onClose(); } };
  if (!selected) return <DetailsDrawer {...common} title="" />;

  if (selected.kind === "ca") {
    const ca = selected.item;
    return (
      <DetailsDrawer
        {...common}
        eyebrow="Certificate authority"
        title={ca.subject ?? `Version ${ca.version ?? "—"}`}
        badges={ca.status ? <StateBadge info={credentialStatusInfo(ca.status)} /> : undefined}
      >
        <DetailsSection title="Certificate">
          <DescriptionList
            items={[
              { label: "Version", value: ca.version != null ? String(ca.version) : null },
              { label: "Valid from", value: formatDateTime(ca.not_before) },
              { label: "Valid until", value: formatDateTime(ca.not_after) },
              { label: "Fingerprint (SHA-256)", value: fullId(ca.fingerprint_sha256 ?? ca.fingerprint) },
            ]}
          />
        </DetailsSection>
      </DetailsDrawer>
    );
  }

  if (selected.kind === "key") {
    const k = selected.item;
    return (
      <DetailsDrawer
        {...common}
        eyebrow="Assignment signing key"
        title={<span className="break-all font-mono text-base">{k.key_id}</span>}
        badges={<StateBadge info={credentialStatusInfo(k.state)} />}
      >
        <DetailsSection title="Key">
          <DescriptionList
            items={[
              { label: "Appliances signed", value: k.current_assignments != null ? k.current_assignments.toLocaleString() : null },
              { label: "Fingerprint", value: fullId(k.fingerprint) },
            ]}
          />
        </DetailsSection>
        <DetailsSection title="History">
          <DescriptionList
            items={[
              { label: "In use since", value: formatDateTime(k.activated_at) },
              { label: "Verify only since", value: formatDateTime(k.verify_only_at), hidden: !k.verify_only_at },
              { label: "Revoked", value: formatDateTime(k.revoked_at), hidden: !k.revoked_at },
            ]}
          />
        </DetailsSection>
      </DetailsDrawer>
    );
  }

  const c = selected.item;
  return (
    <DetailsDrawer
      {...common}
      eyebrow="Appliance certificate"
      title={<span className="font-mono">{c.serial ?? "Appliance"}</span>}
      description={[c.customer_name, c.site_name].filter(Boolean).join(" · ") || undefined}
      badges={<StateBadge info={credentialStatusInfo(c.status)} />}
    >
      <DetailsSection title="Certificate">
        <DescriptionList
          items={[
            { label: "Valid from", value: formatDateTime(c.not_before) },
            { label: "Valid until", value: formatDateTime(c.not_after) },
            { label: "Signed by CA version", value: c.ca_version != null ? String(c.ca_version) : null },
            { label: "Fingerprint (SHA-256)", value: fullId(c.fingerprint_sha256) },
            { label: "Revoked", value: formatDateTime(c.revoked_at), hidden: !c.revoked_at },
            { label: "Revocation reason", value: c.revocation_reason ?? null, hidden: !c.revocation_reason },
          ]}
        />
      </DetailsSection>
      {c.appliance_id && (
        <p className="text-sm">
          <Link href={`/appliances/${c.appliance_id}`} className="font-medium text-primary underline-offset-2 hover:underline">
            Open the appliance
          </Link>
        </p>
      )}
    </DetailsDrawer>
  );
}
