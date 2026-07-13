"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
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
 * Read-only Certificate inventory (Platform). Shows metadata ONLY — public
 * fingerprint, issuer, validity, status, revocation reason and last rotation.
 * Private keys and cert PEM are never returned by the API or shown here.
 */
export default function CertificatesPage() {
  const [rows, setRows] = useState<Cert[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [showSuperseded, setShowSuperseded] = useState(false);

  useEffect(() => {
    api.get<{ data: Cert[] }>("/cloud/v1/certificates")
      .then((r) => setRows(r.data ?? []))
      .catch((e) => setErr(e?.message ?? "Failed to load"));
  }, []);

  const visible = (rows ?? []).filter((c) => showSuperseded || c.status !== "superseded");

  return (
    <div className="p-6 max-w-[90rem] mx-auto">
      <div className="flex items-baseline justify-between mb-4">
        <div>
          <div className="text-xs text-muted uppercase tracking-wider">Administration · Security inventory</div>
          <h1 className="text-2xl font-semibold">Certificates</h1>
        </div>
        <label className="text-sm text-muted flex items-center gap-2">
          <input type="checkbox" checked={showSuperseded} onChange={(e) => setShowSuperseded(e.target.checked)} />
          Show superseded
        </label>
      </div>

      <p className="text-sm text-muted mb-4">
        Appliance client certificates issued by the internal CA (mTLS identity). Read-only, metadata only —
        no private keys or certificate material are ever exposed.
      </p>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      <Card>
        <CardBody className="p-0 overflow-x-auto">
          {rows === null ? <EmptyState title="Loading…" /> : visible.length === 0 ? (
            <EmptyState title="No certificates" hint="Certificates appear here as appliances are activated." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Appliance</TH><TH>Customer</TH><TH>Site</TH>
                  <TH>Fingerprint (SHA-256)</TH><TH>Issuer</TH>
                  <TH>Issued</TH><TH>Expires</TH><TH>Status</TH>
                  <TH>Last rotation</TH><TH>Revocation</TH>
                </TR>
              </THead>
              <tbody>
                {visible.map((c) => {
                  const expired = new Date(c.not_after).getTime() < Date.now() && c.status === "active";
                  return (
                    <TR key={c.id}>
                      <TD className="font-mono text-xs">{c.serial || c.appliance_id.slice(0, 8)}</TD>
                      <TD>{c.tenant_name || "—"}</TD>
                      <TD className="text-muted">{c.site_name || "—"}</TD>
                      <TD className="font-mono text-[11px] break-all max-w-[16rem]" title={c.fingerprint_sha256}>{c.fingerprint_sha256}</TD>
                      <TD className="text-muted text-xs">{c.issuer}</TD>
                      <TD className="text-muted">{formatDate(c.not_before)}</TD>
                      <TD className="text-muted">{formatDate(c.not_after)}</TD>
                      <TD><Badge tone={tone(c.status, expired) as any}>{expired ? "expired" : c.status}</Badge></TD>
                      <TD className="text-muted text-xs">{formatRelative(c.last_rotation)}</TD>
                      <TD className="text-muted text-xs">
                        {c.revoked_at ? `${formatDate(c.revoked_at)}${c.revocation_reason ? ` · ${c.revocation_reason}` : ""}` : "—"}
                      </TD>
                    </TR>
                  );
                })}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>
    </div>
  );
}
