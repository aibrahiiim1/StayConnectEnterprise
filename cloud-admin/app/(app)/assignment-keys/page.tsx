"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { formatDate } from "@/lib/utils";

type Key = {
  key_id: string;
  fingerprint: string;
  state: string;
  purpose: string;
  rotation_status: string;
  can_sign: boolean;
  can_verify: boolean;
  current_assignments: number;
  activated_at: string;
  verify_only_at?: string | null;
  revoked_at?: string | null;
  retired_at?: string | null;
  reason: string;
  note: string;
  emergency: boolean;
};

const tone = (s: string) =>
  s === "active" ? "ok" :
  s === "verify_only" ? "warn" :
  s === "revoked" ? "err" : "default";

/**
 * Read-only Assignment signing key inventory (Platform). Assignment documents
 * (which bind an appliance to a tenant/site) are signed with these keys.
 * Metadata + PUBLIC fingerprint only — the private signing key lives solely in
 * ctrlapi's signer and is never persisted or exposed.
 */
export default function AssignmentKeysPage() {
  const [rows, setRows] = useState<Key[] | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    api.get<{ data: Key[] }>("/cloud/v1/assignment-keys")
      .then((r) => setRows(r.data ?? []))
      .catch((e) => setErr(e?.message ?? "Failed to load"));
  }, []);

  const retiredOn = (k: Key) => k.revoked_at || k.verify_only_at || k.retired_at || null;

  return (
    <div className="p-6 max-w-[88rem] mx-auto">
      <div className="mb-1 text-xs text-muted uppercase tracking-wider">Administration · Security inventory</div>
      <h1 className="text-2xl font-semibold mb-1">Assignment signing keys</h1>
      <p className="text-sm text-muted mb-4">
        Keys that sign appliance→tenant/site assignment documents. <strong>active</strong> may sign + verify,
        <strong> verify-only</strong> still verifies existing assignments but signs nothing, <strong>revoked</strong> is
        no longer trusted. Read-only — no private key material is shown.
      </p>

      {err && <div className="text-err text-sm mb-4">{err}</div>}

      <Card>
        <CardBody className="p-0 overflow-x-auto">
          {rows === null ? <EmptyState title="Loading…" /> : rows.length === 0 ? (
            <EmptyState title="No assignment keys" />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Key ID</TH><TH>Fingerprint</TH><TH>State</TH><TH>Rotation</TH>
                  <TH>Dependencies</TH><TH>Created</TH><TH>Retired</TH><TH>Reason / note</TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((k) => (
                  <TR key={k.key_id}>
                    <TD className="font-mono text-xs">{k.key_id}</TD>
                    <TD className="font-mono text-[11px] break-all max-w-[14rem]" title={k.fingerprint}>{k.fingerprint}</TD>
                    <TD>
                      <Badge tone={tone(k.state) as any}>{k.state}</Badge>
                      {k.emergency && <span className="ml-1 text-[10px] text-err">emergency</span>}
                    </TD>
                    <TD className="text-muted text-xs">{k.rotation_status}</TD>
                    <TD className="text-muted" title="Appliances whose current assignment was signed by this key">
                      {k.current_assignments}
                    </TD>
                    <TD className="text-muted">{formatDate(k.activated_at)}</TD>
                    <TD className="text-muted">{retiredOn(k) ? formatDate(retiredOn(k)!) : "—"}</TD>
                    <TD className="text-muted text-xs max-w-[16rem] break-words">{[k.reason, k.note].filter(Boolean).join(" · ") || "—"}</TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>
    </div>
  );
}
