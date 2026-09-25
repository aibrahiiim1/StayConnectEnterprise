"use client";

import { useEffect, useMemo, useState } from "react";
import { KeyRound } from "lucide-react";
import { api } from "@/lib/api";
import { Card } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { PageHeader, PageShell, StatCard } from "@/components/ui/page";
import { RoleRestricted } from "@/components/role-restricted";
import { usePermissions } from "@/lib/permissions";
import { MonoId, SkeletonRows } from "@/components/ui/misc";
import { statusWord } from "@/lib/license-state";
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

const stateWord = (s: string) => (s === "verify_only" ? "Verify-only" : statusWord(s));

/**
 * Read-only inventory of the keys that sign appliance-to-site assignment documents. Metadata + PUBLIC
 * fingerprint only — the private signing key lives solely in the API's signer and is never persisted or shown.
 */
export default function AssignmentKeysPage() {
  // Reading needs "assignmentKeys.read" (lib/permissions.ts); the server refuses this page's list to other roles.
  const { can } = usePermissions();
  const canRead = can["assignmentKeys.read"];
  const [rows, setRows] = useState<Key[] | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    api.get<{ data: Key[] }>("/cloud/v1/assignment-keys")
      .then((r) => setRows(r.data ?? []))
      .catch((e) => { setErr(e?.message ?? "Failed to load"); setRows((p) => p ?? []); });
  }, []);

  const retiredOn = (k: Key) => k.revoked_at || k.verify_only_at || k.retired_at || null;

  const counts = useMemo(() => {
    const all = rows ?? [];
    return {
      active: all.filter((k) => k.state === "active").length,
      verifyOnly: all.filter((k) => k.state === "verify_only").length,
      revoked: all.filter((k) => k.state === "revoked").length,
      assignments: all.reduce((n, k) => n + (k.current_assignments || 0), 0),
    };
  }, [rows]);

  return (
    <PageShell width="wide">
      <PageHeader
        eyebrow="Administration"
        title="Assignment keys"
        icon={<KeyRound />}
        description="Keys that sign the documents binding an appliance to its customer and site. Active keys sign and verify; verify-only keys still verify existing assignments but sign nothing; revoked keys are no longer trusted. Read-only — no private key material is shown."
      />

      {!canRead ? (
        <RoleRestricted what="Assignment keys are the vendor's signing keys." />
      ) : (
      <>
      <ErrorBanner err={err} />

      <section aria-label="Key counts" className="grid grid-cols-2 gap-4 lg:grid-cols-4">
        <StatCard label="Active" value={rows ? counts.active : "—"} tone="ok" icon={<KeyRound />} />
        <StatCard label="Verify-only" value={rows ? counts.verifyOnly : "—"} tone="warn" />
        <StatCard label="Revoked" value={rows ? counts.revoked : "—"} tone="err" />
        <StatCard label="Current assignments" value={rows ? counts.assignments : "—"} hint="Appliances whose assignment one of these keys signed" />
      </section>

      <Card>
        {rows === null ? (
          <SkeletonRows rows={3} cols={7} />
        ) : rows.length === 0 ? (
          <EmptyState icon={<KeyRound />} title="No assignment keys" hint="Keys appear here once Central has signed its first assignment." />
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>Key ID</TH><TH className="hidden md:table-cell">Fingerprint</TH><TH>State</TH><TH className="hidden lg:table-cell">Rotation</TH>
                <TH>Dependencies</TH><TH className="hidden md:table-cell">Created</TH><TH className="hidden lg:table-cell">Retired</TH>
                <TH className="hidden xl:table-cell">Reason</TH>
              </TR>
            </THead>
            <tbody>
              {rows.map((k) => (
                <TR key={k.key_id}>
                  <TD className="font-mono text-xs">{k.key_id}</TD>
                  <TD className="hidden md:table-cell"><MonoId value={k.fingerprint} head={16} title="Fingerprint" /></TD>
                  <TD>
                    <span className="inline-flex flex-wrap items-center gap-1">
                      <Badge tone={tone(k.state) as any} dot>{stateWord(k.state)}</Badge>
                      {k.emergency && <Badge tone="err">Emergency</Badge>}
                    </span>
                  </TD>
                  <TD className="hidden text-xs text-muted-foreground lg:table-cell">{statusWord(k.rotation_status)}</TD>
                  <TD className="tabular" title="Appliances whose current assignment was signed by this key">
                    {k.current_assignments}
                  </TD>
                  <TD className="hidden whitespace-nowrap text-xs text-muted-foreground md:table-cell">{formatDate(k.activated_at)}</TD>
                  <TD className="hidden whitespace-nowrap text-xs text-muted-foreground lg:table-cell">{retiredOn(k) ? formatDate(retiredOn(k)!) : "—"}</TD>
                  <TD className="hidden max-w-[16rem] break-words text-xs text-muted-foreground xl:table-cell">{[k.reason, k.note].filter(Boolean).join(" · ") || "—"}</TD>
                </TR>
              ))}
            </tbody>
          </Table>
        )}
      </Card>
      </>
      )}
    </PageShell>
  );
}
