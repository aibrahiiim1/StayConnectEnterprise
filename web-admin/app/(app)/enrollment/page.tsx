"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, ListResp, Site, BootstrapToken, BootstrapTokenCreated } from "@/lib/api";
import { useTenant } from "@/lib/use-tenant";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { formatRelative, errMsg } from "@/lib/utils";
import Link from "next/link";
import { Plus, X, Copy, Trash2, ShieldCheck, Download, Terminal, ExternalLink } from "lucide-react";

// ---------------------------------------------------------------------------
// Shared helpers
// ---------------------------------------------------------------------------

// arr coerces either a bare array or a {data:[...]} envelope into a plain array.
const arr = <T,>(r: any): T[] => (Array.isArray(r) ? r : (r?.data ?? []));

type Tone = "default" | "ok" | "warn" | "err" | "info";

// stateTone maps an appliance lifecycle/status string onto a Badge tone.
function stateTone(s?: string): Tone {
  switch ((s ?? "").toLowerCase()) {
    case "online":
    case "licensed":
    case "assigned":
    case "active":
      return "ok";
    case "enrolled":
    case "claimed":
    case "pending_approval":
    case "pending_enrollment":
    case "pending":
      return "info";
    case "suspended":
    case "license_expired":
    case "offline":
      return "warn";
    case "revoked":
    case "decommissioned":
      return "err";
    default:
      return "default";
  }
}

// CopyButton copies text to the clipboard and briefly confirms.
function CopyButton({ value }: { value: string }) {
  const [done, setDone] = useState(false);
  return (
    <Button
      size="sm"
      variant="secondary"
      onClick={() => {
        navigator.clipboard?.writeText(value);
        setDone(true);
        setTimeout(() => setDone(false), 1500);
      }}
    >
      <Copy size={12} /> {done ? "Copied" : "Copy"}
    </Button>
  );
}

// Modal is a small centered overlay used by the various action forms.
function Modal({ title, onClose, children }: { title: string; onClose: () => void; children: React.ReactNode }) {
  return (
    <div className="fixed inset-0 bg-black/60 flex items-center justify-center p-6 z-50" onClick={onClose}>
      <div
        className="bg-panel border border-border rounded-md max-w-lg w-full max-h-[85vh] overflow-auto"
        onClick={(e) => e.stopPropagation()}
      >
        <div className="p-4 border-b border-border flex items-center justify-between">
          <div className="font-semibold">{title}</div>
          <Button size="sm" variant="ghost" onClick={onClose}>
            <X size={14} />
          </Button>
        </div>
        <div className="p-4">{children}</div>
      </div>
    </div>
  );
}

// Sensitive is the reauth helper: run a sensitive action, and if the backend
// answers 403 reauth_required, prompt for the password, POST /v1/auth/reauth,
// and retry the original action exactly once. The password is never stored.
type Sensitive = <T>(fn: () => Promise<T>) => Promise<T>;

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

const TABS = [
  "Pending",
  "Enrolled",
  "Tokens",
  "Certificates",
  "Licenses",
  "Commands",
  "Offline Packages",
  "Audit",
  "Security Alerts",
] as const;
type Tab = (typeof TABS)[number];

export default function EnrollmentPage() {
  const tenantID = useTenant();
  const [tab, setTab] = useState<Tab>("Pending");

  // Reauth modal plumbing — a single pending resolver is enough because the
  // operator triggers one sensitive action at a time.
  const [reauthOpen, setReauthOpen] = useState(false);
  const [reauthErr, setReauthErr] = useState<string | null>(null);
  const [reauthBusy, setReauthBusy] = useState(false);
  const resolverRef = useRef<((pw: string | null) => void) | null>(null);

  const askPassword = useCallback((): Promise<string | null> => {
    setReauthErr(null);
    setReauthOpen(true);
    return new Promise<string | null>((resolve) => {
      resolverRef.current = resolve;
    });
  }, []);

  const sensitive = useCallback<Sensitive>(
    async (fn) => {
      try {
        return await fn();
      } catch (e) {
        if (e instanceof ApiError && e.code === "reauth_required") {
          const pw = await askPassword();
          if (!pw) throw e; // operator cancelled
          await api.post("/v1/auth/reauth", { password: pw });
          return await fn(); // retry once
        }
        throw e;
      }
    },
    [askPassword],
  );

  async function submitReauth(pw: string) {
    // Validate the password up-front so a wrong entry surfaces in the modal
    // rather than as a failed retry. On success we hand the password back to
    // the waiting sensitive() call, which re-POSTs reauth (idempotent) + retries.
    setReauthBusy(true);
    setReauthErr(null);
    try {
      await api.post("/v1/auth/reauth", { password: pw });
      setReauthOpen(false);
      resolverRef.current?.(pw);
      resolverRef.current = null;
    } catch (e) {
      setReauthErr(errMsg(e));
    } finally {
      setReauthBusy(false);
    }
  }

  function cancelReauth() {
    setReauthOpen(false);
    resolverRef.current?.(null);
    resolverRef.current = null;
  }

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="mb-4">
        <div className="text-xs text-muted uppercase tracking-wider">Platform · across all customers</div>
        <h1 className="text-2xl font-semibold">Enrollment Console</h1>
        <p className="text-sm text-muted">
          Approve appliances, mint bootstrap tokens, issue certificates and licenses, and watch for identity anomalies.
        </p>
      </div>

      <div className="flex flex-wrap gap-1 mb-6 border-b border-border">
        {TABS.map((t) => (
          <button
            key={t}
            onClick={() => setTab(t)}
            className={
              "px-3 py-2 text-sm -mb-px border-b-2 transition-colors " +
              (tab === t
                ? "border-brand text-text"
                : "border-transparent text-muted hover:text-text")
            }
          >
            {t}
          </button>
        ))}
      </div>

      {tab === "Pending" && <PendingTab sensitive={sensitive} tenantID={tenantID} />}
      {tab === "Enrolled" && <EnrolledTab tenantID={tenantID} />}
      {tab === "Tokens" && <TokensTab sensitive={sensitive} tenantID={tenantID} />}
      {tab === "Certificates" && <CertificatesTab sensitive={sensitive} />}
      {tab === "Licenses" && <LicensesTab sensitive={sensitive} tenantID={tenantID} />}
      {tab === "Commands" && <CommandsTab sensitive={sensitive} />}
      {tab === "Offline Packages" && <OfflinePackagesTab sensitive={sensitive} />}
      {tab === "Audit" && <AuditTab />}
      {tab === "Security Alerts" && <AlertsTab />}

      {reauthOpen && (
        <Modal title="Confirm your password" onClose={cancelReauth}>
          <p className="text-sm text-muted mb-3">
            This is a sensitive action. Re-enter your password to continue.
          </p>
          <form
            onSubmit={(e) => {
              e.preventDefault();
              const pw = (new FormData(e.currentTarget).get("password") as string) || "";
              if (pw) submitReauth(pw);
            }}
          >
            <Label>Password</Label>
            <Input name="password" type="password" autoFocus autoComplete="current-password" required />
            {reauthErr && <div className="text-err text-sm mt-2">{reauthErr}</div>}
            <div className="flex justify-end gap-2 mt-4">
              <Button type="button" variant="ghost" onClick={cancelReauth}>
                Cancel
              </Button>
              <Button type="submit" disabled={reauthBusy}>
                {reauthBusy ? "Verifying…" : "Confirm"}
              </Button>
            </div>
          </form>
        </Modal>
      )}
    </div>
  );
}

// ---------------------------------------------------------------------------
// 1. Pending
// ---------------------------------------------------------------------------

type PendingAppliance = {
  id: string;
  serial: string;
  hardware_fingerprint: string;
  public_key_fingerprint: string;
  source_ip: string;
  version: string;
  state: string;
  first_seen?: string;
  last_seen?: string;
};

function PendingTab({ sensitive, tenantID }: { sensitive: Sensitive; tenantID: string | null }) {
  const [rows, setRows] = useState<PendingAppliance[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [assignFor, setAssignFor] = useState<PendingAppliance | null>(null);

  const load = useCallback(async () => {
    setErr(null);
    try {
      setRows(arr<PendingAppliance>(await api.get<any>("/cloud/v1/appliances-admin/pending")));
    } catch (e) {
      setErr(errMsg(e));
      setRows([]);
    }
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  async function onClaim(a: PendingAppliance) {
    setErr(null);
    try {
      await api.post(`/cloud/v1/appliances-admin/${a.id}/claim`);
      load();
    } catch (e) {
      setErr(errMsg(e));
    }
  }

  async function onRevoke(a: PendingAppliance) {
    const reason = prompt(`Revoke ${a.serial}? Optional reason:`);
    if (reason === null) return;
    setErr(null);
    try {
      await sensitive(() => api.post(`/cloud/v1/appliances-admin/${a.id}/revoke`, { reason }));
      load();
    } catch (e) {
      setErr(errMsg(e));
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle>Pending appliances{rows ? ` (${rows.length})` : ""}</CardTitle>
        <Button size="sm" variant="secondary" onClick={load}>
          Refresh
        </Button>
      </CardHeader>
      <CardBody className="p-0">
        {err && <div className="text-err text-sm px-5 pt-4">{err}</div>}
        {rows === null ? (
          <EmptyState title="Loading…" />
        ) : rows.length === 0 ? (
          <EmptyState title="No appliances awaiting approval" hint="Appliances appear here after their first-boot enrollment." />
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>Serial</TH>
                <TH>State</TH>
                <TH>Public-key fp</TH>
                <TH>Hardware fp</TH>
                <TH>Source IP</TH>
                <TH>Version</TH>
                <TH>First seen</TH>
                <TH></TH>
              </TR>
            </THead>
            <tbody>
              {rows.map((a) => (
                <TR key={a.id}>
                  <TD className="font-mono">{a.serial || "—"}</TD>
                  <TD>
                    <Badge tone={stateTone(a.state)}>{a.state}</Badge>
                  </TD>
                  <TD className="font-mono text-xs">{a.public_key_fingerprint || "—"}</TD>
                  <TD className="font-mono text-xs">{a.hardware_fingerprint || "—"}</TD>
                  <TD className="font-mono text-xs text-muted">{a.source_ip || "—"}</TD>
                  <TD className="text-muted text-xs">{a.version || "—"}</TD>
                  <TD className="text-muted text-xs">{formatRelative(a.first_seen)}</TD>
                  <TD className="text-right whitespace-nowrap">
                    <Button size="sm" variant="ghost" onClick={() => onClaim(a)}>
                      Claim
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => setAssignFor(a)}>
                      Assign
                    </Button>
                    <Button size="sm" variant="ghost" onClick={() => onRevoke(a)}>
                      Revoke
                    </Button>
                  </TD>
                </TR>
              ))}
            </tbody>
          </Table>
        )}
      </CardBody>

      {assignFor && (
        <AssignModal
          appliance={assignFor}
          tenantID={tenantID}
          sensitive={sensitive}
          onClose={() => setAssignFor(null)}
          onDone={() => {
            setAssignFor(null);
            load();
          }}
        />
      )}
    </Card>
  );
}

function AssignModal({
  appliance,
  tenantID,
  sensitive,
  onClose,
  onDone,
}: {
  appliance: PendingAppliance;
  tenantID: string | null;
  sensitive: Sensitive;
  onClose: () => void;
  onDone: () => void;
}) {
  const [sites, setSites] = useState<Site[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!tenantID) return;
    (async () => {
      try {
        const r = await api.get<ListResp<Site>>(`/cloud/v1/sites?tenant_id=${tenantID}`);
        setSites(r.data ?? []);
      } catch (e) {
        setErr(errMsg(e));
      }
    })();
  }, [tenantID]);

  async function onSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    const form = new FormData(e.currentTarget);
    try {
      await sensitive(() =>
        api.post(`/cloud/v1/appliances-admin/${appliance.id}/assign`, {
          tenant_id: form.get("tenant_id"),
          site_id: form.get("site_id"),
          reason: (form.get("reason") as string) || undefined,
        }),
      );
      onDone();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal title={`Assign ${appliance.serial}`} onClose={onClose}>
      <form onSubmit={onSubmit} className="space-y-3">
        <div>
          <Label>Tenant ID</Label>
          <Input name="tenant_id" required defaultValue={tenantID ?? ""} placeholder="tenant uuid" />
        </div>
        <div>
          <Label>Site</Label>
          <select
            name="site_id"
            required
            className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm"
          >
            <option value="">Select a site…</option>
            {sites.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name} — {s.code}
              </option>
            ))}
          </select>
        </div>
        <div>
          <Label>Reason (optional)</Label>
          <Input name="reason" placeholder="why this assignment" />
        </div>
        {err && <div className="text-err text-sm">{err}</div>}
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={busy}>
            {busy ? "Assigning…" : "Assign"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// 2. Enrolled
// ---------------------------------------------------------------------------

type EnrolledAppliance = {
  id: string;
  serial?: string;
  tenant_id?: string;
  site_id?: string;
  name?: string;
  status?: string;
  lifecycle_state?: string;
  version?: string;
  last_seen_at?: string;
};

function EnrolledTab({ tenantID }: { tenantID: string | null }) {
  const [rows, setRows] = useState<EnrolledAppliance[] | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    if (!tenantID) return;
    setErr(null);
    (async () => {
      try {
        setRows(arr<EnrolledAppliance>(await api.get<any>(`/cloud/v1/appliances?tenant_id=${tenantID}`)));
      } catch (e) {
        setErr(errMsg(e));
        setRows([]);
      }
    })();
  }, [tenantID]);

  return (
    <Card>
      <CardHeader>
        <CardTitle>Enrolled appliances{rows ? ` (${rows.length})` : ""}</CardTitle>
      </CardHeader>
      <CardBody className="p-0">
        {err && <div className="text-err text-sm px-5 pt-4">{err}</div>}
        {rows === null ? (
          <EmptyState title="Loading…" />
        ) : rows.length === 0 ? (
          <EmptyState title="No enrolled appliances" />
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>Serial</TH>
                <TH>Tenant</TH>
                <TH>Site</TH>
                <TH>Lifecycle</TH>
                <TH>Version</TH>
                <TH>Last seen</TH>
              </TR>
            </THead>
            <tbody>
              {rows.map((a) => {
                const life = a.lifecycle_state || a.status || "—";
                return (
                  <TR key={a.id}>
                    <TD className="font-mono">
                      {a.serial || a.name || "—"}
                      <div className="text-xs text-muted font-mono">{a.id.slice(0, 8)}</div>
                    </TD>
                    <TD className="text-muted font-mono text-xs">{(a.tenant_id ?? "—").slice(0, 8)}</TD>
                    <TD className="text-muted font-mono text-xs">{(a.site_id ?? "—").slice(0, 8)}</TD>
                    <TD>
                      <Badge tone={stateTone(life)}>{life}</Badge>
                    </TD>
                    <TD className="text-muted text-xs">{a.version || "—"}</TD>
                    <TD className="text-muted text-xs">{formatRelative(a.last_seen_at)}</TD>
                  </TR>
                );
              })}
            </tbody>
          </Table>
        )}
      </CardBody>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// 3. Tokens
// ---------------------------------------------------------------------------

function TokensTab({ sensitive, tenantID }: { sensitive: Sensitive; tenantID: string | null }) {
  const [rows, setRows] = useState<BootstrapToken[] | null>(null);
  const [sites, setSites] = useState<Site[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [showCreate, setShowCreate] = useState(false);
  const [minted, setMinted] = useState<BootstrapTokenCreated | null>(null);

  const load = useCallback(async () => {
    if (!tenantID) return;
    setErr(null);
    try {
      const [tk, st] = await Promise.all([
        api.get<ListResp<BootstrapToken>>(`/cloud/v1/appliance-bootstrap-tokens?tenant_id=${tenantID}`),
        api.get<ListResp<Site>>(`/cloud/v1/sites?tenant_id=${tenantID}`),
      ]);
      setRows(tk.data ?? []);
      setSites(st.data ?? []);
    } catch (e) {
      setErr(errMsg(e));
      setRows([]);
    }
  }, [tenantID]);
  useEffect(() => {
    load();
  }, [load]);

  const siteName = (sid: string) => sites.find((s) => s.id === sid)?.name ?? sid.slice(0, 8);

  async function onCreate(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!tenantID) return;
    setErr(null);
    const form = new FormData(e.currentTarget);
    try {
      const res = await sensitive(() =>
        api.post<BootstrapTokenCreated>(`/cloud/v1/appliance-bootstrap-tokens?tenant_id=${tenantID}`, {
          site_id: form.get("site_id"),
          expected_serial: (form.get("expected_serial") as string) || undefined,
          ttl_hours: Number(form.get("ttl_hours")) || 24,
        }),
      );
      setMinted(res);
      setShowCreate(false);
      load();
    } catch (e) {
      setErr(errMsg(e));
    }
  }

  async function onRevoke(id: string) {
    if (!tenantID) return;
    if (!confirm("Revoke this bootstrap token?")) return;
    setErr(null);
    try {
      await api.del(`/cloud/v1/appliance-bootstrap-tokens/${id}?tenant_id=${tenantID}`);
      load();
    } catch (e) {
      setErr(errMsg(e));
    }
  }

  return (
    <>
      {minted && (
        <Card className="mb-6 border-ok">
          <CardHeader>
            <CardTitle className="text-ok">New bootstrap token — copy it now</CardTitle>
            <Button size="sm" variant="ghost" onClick={() => setMinted(null)}>
              <X size={14} /> Dismiss
            </Button>
          </CardHeader>
          <CardBody>
            <div className="text-sm text-warn mb-2">
              This is the only time the full token is shown. It cannot be retrieved again.
            </div>
            <div className="flex items-center gap-2">
              <code className="flex-1 bg-panel2 border border-border rounded px-3 py-2 font-mono text-sm break-all">
                {minted.token}
              </code>
              <CopyButton value={minted.token} />
            </div>
            <div className="text-xs text-muted mt-3">
              Site: {siteName(minted.row.site_id)}
              {minted.row.expected_serial ? (
                <>
                  {" "}· Serial lock: <span className="font-mono">{minted.row.expected_serial}</span>
                </>
              ) : null}{" "}
              · Expires: {formatRelative(minted.row.expires_at)}
            </div>
          </CardBody>
        </Card>
      )}

      <Card>
        <CardHeader>
          <CardTitle>Bootstrap tokens{rows ? ` (${rows.length})` : ""}</CardTitle>
          <Button size="sm" onClick={() => setShowCreate(true)} disabled={sites.length === 0}>
            <Plus size={14} /> Create
          </Button>
        </CardHeader>
        <CardBody className="p-0">
          {err && <div className="text-err text-sm px-5 pt-4">{err}</div>}
          {rows === null ? (
            <EmptyState title="Loading…" />
          ) : rows.length === 0 ? (
            <EmptyState title="No bootstrap tokens" hint="Create one to enroll a new appliance." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Hint</TH>
                  <TH>Site</TH>
                  <TH>Serial lock</TH>
                  <TH>Status</TH>
                  <TH>Expires</TH>
                  <TH>Created</TH>
                  <TH></TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((t) => {
                  const consumed = !!t.consumed_at;
                  const expired = !consumed && new Date(t.expires_at) < new Date();
                  const tone: Tone = consumed ? "default" : expired ? "warn" : "info";
                  const label = consumed ? "consumed" : expired ? "expired" : "pending";
                  return (
                    <TR key={t.id}>
                      <TD className="font-mono">…{t.token_hint}</TD>
                      <TD className="text-muted">{siteName(t.site_id)}</TD>
                      <TD className="font-mono text-xs">{t.expected_serial || "—"}</TD>
                      <TD>
                        <Badge tone={tone}>{label}</Badge>
                      </TD>
                      <TD className="text-muted text-xs">{formatRelative(t.expires_at)}</TD>
                      <TD className="text-muted text-xs">{formatRelative(t.created_at)}</TD>
                      <TD className="text-right">
                        {!consumed && (
                          <Button size="sm" variant="ghost" onClick={() => onRevoke(t.id)}>
                            <Trash2 size={12} /> Revoke
                          </Button>
                        )}
                      </TD>
                    </TR>
                  );
                })}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      {showCreate && (
        <Modal title="Create bootstrap token" onClose={() => setShowCreate(false)}>
          <form onSubmit={onCreate} className="space-y-3">
            <div>
              <Label>Site</Label>
              <select
                name="site_id"
                required
                className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm"
              >
                <option value="">Select a site…</option>
                {sites.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name} — {s.code}
                  </option>
                ))}
              </select>
            </div>
            <div>
              <Label>Expected serial (optional)</Label>
              <Input name="expected_serial" placeholder="APP-HQ-0001 (locks the token to this serial)" />
            </div>
            <div>
              <Label>TTL (hours)</Label>
              <Input name="ttl_hours" type="number" defaultValue={24} min={1} max={168} />
            </div>
            <div className="flex justify-end gap-2 pt-1">
              <Button type="button" variant="ghost" onClick={() => setShowCreate(false)}>
                Cancel
              </Button>
              <Button type="submit">Create</Button>
            </div>
          </form>
        </Modal>
      )}
    </>
  );
}

// ---------------------------------------------------------------------------
// 4. Certificates
// ---------------------------------------------------------------------------

type Certificate = {
  id: string;
  appliance_id: string;
  serial: string;
  fingerprint_sha256: string;
  cert_serial: string;
  ca_version: number;
  not_before?: string;
  not_after?: string;
  status: string;
  created_at?: string;
};

type CertRequest = {
  id: string;
  appliance_id: string;
  serial: string;
  status: string;
  requested_at?: string;
};

type CAInfo = { ca_version: number; subject: string };

function certTone(s?: string): Tone {
  switch ((s ?? "").toLowerCase()) {
    case "active":
      return "ok";
    case "superseded":
      return "info";
    case "revoked":
      return "err";
    default:
      return "default";
  }
}

function CertificatesTab({ sensitive }: { sensitive: Sensitive }) {
  const [certs, setCerts] = useState<Certificate[] | null>(null);
  const [requests, setRequests] = useState<CertRequest[] | null>(null);
  const [ca, setCa] = useState<CAInfo | null>(null);
  const [err, setErr] = useState<string | null>(null);

  const load = useCallback(async () => {
    setErr(null);
    try {
      const [c, rq, caInfo] = await Promise.all([
        api.get<any>("/cloud/v1/certificates"),
        api.get<any>("/cloud/v1/certificates/requests"),
        api.get<CAInfo>("/cloud/v1/certificates/ca"),
      ]);
      setCerts(arr<Certificate>(c));
      setRequests(arr<CertRequest>(rq));
      setCa(caInfo);
    } catch (e) {
      setErr(errMsg(e));
      setCerts([]);
      setRequests([]);
    }
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  async function onIssue(rq: CertRequest) {
    setErr(null);
    try {
      await sensitive(() => api.post(`/cloud/v1/certificates/${rq.appliance_id}/issue`));
      load();
    } catch (e) {
      setErr(errMsg(e));
    }
  }

  async function onRevoke(c: Certificate) {
    const reason = prompt(`Revoke certificate ${c.cert_serial}? Optional reason:`);
    if (reason === null) return;
    setErr(null);
    try {
      await sensitive(() => api.post(`/cloud/v1/certificates/${c.id}/revoke`, { reason }));
      load();
    } catch (e) {
      setErr(errMsg(e));
    }
  }

  return (
    <>
      {err && <div className="text-err text-sm mb-4">{err}</div>}

      <Card className="mb-6">
        <CardHeader>
          <CardTitle>Certificate authority</CardTitle>
          <Button size="sm" variant="secondary" onClick={load}>
            Refresh
          </Button>
        </CardHeader>
        <CardBody>
          {ca ? (
            <div className="text-sm">
              <span className="text-muted">CA version</span>{" "}
              <Badge tone="info">v{ca.ca_version}</Badge>{" "}
              <span className="text-muted ml-3">Subject</span>{" "}
              <span className="font-mono text-xs">{ca.subject}</span>
            </div>
          ) : (
            <div className="text-sm text-muted">Loading CA…</div>
          )}
        </CardBody>
      </Card>

      <Card className="mb-6">
        <CardHeader>
          <CardTitle>Pending CSRs{requests ? ` (${requests.length})` : ""}</CardTitle>
        </CardHeader>
        <CardBody className="p-0">
          {requests === null ? (
            <EmptyState title="Loading…" />
          ) : requests.length === 0 ? (
            <EmptyState title="No pending certificate requests" />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Serial</TH>
                  <TH>Appliance</TH>
                  <TH>Status</TH>
                  <TH>Requested</TH>
                  <TH></TH>
                </TR>
              </THead>
              <tbody>
                {requests.map((rq) => (
                  <TR key={rq.id}>
                    <TD className="font-mono">{rq.serial || "—"}</TD>
                    <TD className="text-muted font-mono text-xs">{rq.appliance_id.slice(0, 8)}</TD>
                    <TD>
                      <Badge tone="info">{rq.status}</Badge>
                    </TD>
                    <TD className="text-muted text-xs">{formatRelative(rq.requested_at)}</TD>
                    <TD className="text-right">
                      <Button size="sm" variant="ghost" onClick={() => onIssue(rq)}>
                        Issue
                      </Button>
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Certificates{certs ? ` (${certs.length})` : ""}</CardTitle>
        </CardHeader>
        <CardBody className="p-0">
          {certs === null ? (
            <EmptyState title="Loading…" />
          ) : certs.length === 0 ? (
            <EmptyState title="No certificates issued" />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Serial</TH>
                  <TH>Cert serial</TH>
                  <TH>Fingerprint</TH>
                  <TH>CA</TH>
                  <TH>Status</TH>
                  <TH>Not after</TH>
                  <TH></TH>
                </TR>
              </THead>
              <tbody>
                {certs.map((c) => (
                  <TR key={c.id}>
                    <TD className="font-mono">{c.serial || "—"}</TD>
                    <TD className="font-mono text-xs">{c.cert_serial || "—"}</TD>
                    <TD className="font-mono text-xs">{(c.fingerprint_sha256 || "").slice(0, 16) || "—"}…</TD>
                    <TD className="text-muted text-xs">v{c.ca_version}</TD>
                    <TD>
                      <Badge tone={certTone(c.status)}>{c.status}</Badge>
                    </TD>
                    <TD className="text-muted text-xs">
                      {c.not_after ? new Date(c.not_after).toLocaleDateString() : "—"}
                    </TD>
                    <TD className="text-right">
                      {c.status === "active" && (
                        <Button size="sm" variant="ghost" onClick={() => onRevoke(c)}>
                          Revoke
                        </Button>
                      )}
                    </TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>
    </>
  );
}

// ---------------------------------------------------------------------------
// 5. Licenses
// ---------------------------------------------------------------------------

type LicenseRow = {
  id: string;
  site_id?: string;
  commercial_plan_code?: string;
  status?: string;
  valid_until?: string;
  key_id?: string;
};

function LicensesTab({ sensitive, tenantID }: { sensitive: Sensitive; tenantID: string | null }) {
  const [rows, setRows] = useState<LicenseRow[] | null>(null);
  const [sites, setSites] = useState<Site[]>([]);
  const [err, setErr] = useState<string | null>(null);
  const [showIssue, setShowIssue] = useState(false);

  const load = useCallback(async () => {
    if (!tenantID) return;
    setErr(null);
    try {
      const [lc, st] = await Promise.all([
        api.get<any>(`/cloud/v1/licenses?tenant_id=${tenantID}`),
        api.get<ListResp<Site>>(`/cloud/v1/sites?tenant_id=${tenantID}`),
      ]);
      setRows(arr<LicenseRow>(lc));
      setSites(st.data ?? []);
    } catch (e) {
      setErr(errMsg(e));
      setRows([]);
    }
  }, [tenantID]);
  useEffect(() => {
    load();
  }, [load]);

  const siteName = (sid?: string) => (sid ? sites.find((s) => s.id === sid)?.name ?? sid.slice(0, 8) : "—");

  async function action(id: string, verb: "suspend" | "resume" | "revoke" | "renew") {
    if (verb === "revoke" && !confirm("Revoke this license?")) return;
    setErr(null);
    try {
      await sensitive(() => api.post(`/cloud/v1/licenses/${id}/${verb}`));
      load();
    } catch (e) {
      setErr(errMsg(e));
    }
  }

  async function onIssue(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setErr(null);
    const form = new FormData(e.currentTarget);
    try {
      await sensitive(() =>
        api.post(`/cloud/v1/licenses`, {
          tenant_id: form.get("tenant_id"),
          site_id: form.get("site_id"),
          valid_days: Number(form.get("valid_days")) || 365,
          offline_grace_days: Number(form.get("offline_grace_days")) || 30,
        }),
      );
      setShowIssue(false);
      load();
    } catch (e) {
      setErr(errMsg(e));
    }
  }

  return (
    <>
      <Card>
        <CardHeader>
          <CardTitle>Licenses{rows ? ` (${rows.length})` : ""}</CardTitle>
          <Button size="sm" onClick={() => setShowIssue(true)}>
            <Plus size={14} /> Issue
          </Button>
        </CardHeader>
        <CardBody className="p-0">
          {err && <div className="text-err text-sm px-5 pt-4">{err}</div>}
          {rows === null ? (
            <EmptyState title="Loading…" />
          ) : rows.length === 0 ? (
            <EmptyState title="No licenses" hint="Issue one for a licensed site." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>License</TH>
                  <TH>Site</TH>
                  <TH>Plan</TH>
                  <TH>Status</TH>
                  <TH>Valid until</TH>
                  <TH></TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((l) => {
                  const st = l.status || "—";
                  return (
                    <TR key={l.id}>
                      <TD className="font-mono text-xs">{l.id.slice(0, 8)}</TD>
                      <TD className="text-muted">{siteName(l.site_id)}</TD>
                      <TD>{l.commercial_plan_code || "—"}</TD>
                      <TD>
                        <Badge tone={stateTone(st)}>{st}</Badge>
                      </TD>
                      <TD className="text-muted text-xs">
                        {l.valid_until ? new Date(l.valid_until).toLocaleDateString() : "—"}
                      </TD>
                      <TD className="text-right whitespace-nowrap">
                        <Button size="sm" variant="ghost" onClick={() => action(l.id, "renew")}>
                          Renew
                        </Button>
                        {st === "suspended" ? (
                          <Button size="sm" variant="ghost" onClick={() => action(l.id, "resume")}>
                            Resume
                          </Button>
                        ) : (
                          <Button size="sm" variant="ghost" onClick={() => action(l.id, "suspend")}>
                            Suspend
                          </Button>
                        )}
                        <Button size="sm" variant="ghost" onClick={() => action(l.id, "revoke")}>
                          Revoke
                        </Button>
                      </TD>
                    </TR>
                  );
                })}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      {showIssue && (
        <Modal title="Issue license" onClose={() => setShowIssue(false)}>
          <form onSubmit={onIssue} className="space-y-3">
            <div>
              <Label>Tenant ID</Label>
              <Input name="tenant_id" required defaultValue={tenantID ?? ""} placeholder="tenant uuid" />
            </div>
            <div>
              <Label>Site</Label>
              <select
                name="site_id"
                required
                className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm"
              >
                <option value="">Select a site…</option>
                {sites.map((s) => (
                  <option key={s.id} value={s.id}>
                    {s.name} — {s.code}
                  </option>
                ))}
              </select>
            </div>
            <div className="grid grid-cols-2 gap-3">
              <div>
                <Label>Valid days</Label>
                <Input name="valid_days" type="number" defaultValue={365} min={1} />
              </div>
              <div>
                <Label>Offline grace days</Label>
                <Input name="offline_grace_days" type="number" defaultValue={30} min={0} />
              </div>
            </div>
            <div className="flex justify-end gap-2 pt-1">
              <Button type="button" variant="ghost" onClick={() => setShowIssue(false)}>
                Cancel
              </Button>
              <Button type="submit">Issue</Button>
            </div>
          </form>
        </Modal>
      )}
    </>
  );
}

// ---------------------------------------------------------------------------
// 6. Commands
// ---------------------------------------------------------------------------

type CommandRow = {
  command_id: string;
  appliance_id: string;
  command_type: string;
  status: string;
  issued_at?: string;
  result?: unknown;
};

// The command allow-list mirrors control-plane/internal/commands.Allowed exactly.
const COMMAND_TYPES = [
  "request_heartbeat",
  "refresh_license",
  "retry_telemetry",
  "collect_sanitized_diagnostics",
  "rotate_certificate",
  "restart_stayconnect_service",
  "schedule_update",
  "controlled_reboot",
] as const;

// Restart units mirror commands.RestartAllowList (full systemd unit names).
const RESTART_SERVICES = [
  "stayconnect-scd",
  "stayconnect-edged",
  "stayconnect-netd",
  "stayconnect-portald",
  "stayconnect-acctd",
  "stayconnect-hotel-admin",
] as const;

function commandTone(s?: string): Tone {
  switch ((s ?? "").toLowerCase()) {
    case "succeeded":
      return "ok";
    case "pending":
    case "delivered":
    case "running":
    case "acknowledged":
      return "warn";
    case "failed":
    case "rejected":
    case "expired":
    case "cancelled":
      return "err";
    default:
      return "default";
  }
}

function CommandsTab({ sensitive }: { sensitive: Sensitive }) {
  const [rows, setRows] = useState<CommandRow[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [showIssue, setShowIssue] = useState(false);

  const load = useCallback(async () => {
    setErr(null);
    try {
      setRows(arr<CommandRow>(await api.get<any>("/cloud/v1/commands")));
    } catch (e) {
      setErr(errMsg(e));
      setRows([]);
    }
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  return (
    <>
      <Card>
        <CardHeader>
          <CardTitle>Commands{rows ? ` (${rows.length})` : ""}</CardTitle>
          <div className="flex gap-2">
            <Button size="sm" variant="secondary" onClick={load}>
              Refresh
            </Button>
            <Button size="sm" onClick={() => setShowIssue(true)}>
              <Terminal size={14} /> Issue command
            </Button>
          </div>
        </CardHeader>
        <CardBody className="p-0">
          {err && <div className="text-err text-sm px-5 pt-4">{err}</div>}
          {rows === null ? (
            <EmptyState title="Loading…" />
          ) : rows.length === 0 ? (
            <EmptyState title="No commands issued" hint="Issue an allow-listed command to an appliance." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Type</TH>
                  <TH>Appliance</TH>
                  <TH>Status</TH>
                  <TH>Issued</TH>
                  <TH>Result</TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((c) => (
                  <TR key={c.command_id}>
                    <TD className="font-mono text-xs">{c.command_type}</TD>
                    <TD className="text-muted font-mono text-xs">{(c.appliance_id ?? "—").slice(0, 8)}</TD>
                    <TD>
                      <Badge tone={commandTone(c.status)}>{c.status}</Badge>
                    </TD>
                    <TD className="text-muted text-xs">{formatRelative(c.issued_at)}</TD>
                    <TD className="text-muted text-xs break-all max-w-xs">{detailText(c.result) || "—"}</TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>

      {showIssue && (
        <IssueCommandModal
          sensitive={sensitive}
          onClose={() => setShowIssue(false)}
          onDone={() => {
            setShowIssue(false);
            load();
          }}
        />
      )}
    </>
  );
}

function IssueCommandModal({
  sensitive,
  onClose,
  onDone,
}: {
  sensitive: Sensitive;
  onClose: () => void;
  onDone: () => void;
}) {
  const [applianceID, setApplianceID] = useState("");
  const [commandType, setCommandType] = useState<(typeof COMMAND_TYPES)[number]>("request_heartbeat");
  const [service, setService] = useState<(typeof RESTART_SERVICES)[number]>("stayconnect-scd");
  const [paramsText, setParamsText] = useState("");
  const [reason, setReason] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const isRestart = commandType === "restart_stayconnect_service";
  const isReboot = commandType === "controlled_reboot";

  async function onSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setErr(null);

    if (!applianceID.trim()) {
      setErr("appliance_id is required");
      return;
    }
    if (isReboot && !reason.trim()) {
      setErr("controlled_reboot requires a reason");
      return;
    }

    // Build params: restart uses the picked service; otherwise optional JSON.
    let params: Record<string, unknown> | undefined;
    if (isRestart) {
      params = { service };
    } else if (paramsText.trim()) {
      try {
        params = JSON.parse(paramsText);
      } catch {
        setErr("params must be valid JSON");
        return;
      }
    }

    setBusy(true);
    try {
      await sensitive(() =>
        api.post("/cloud/v1/commands", {
          appliance_id: applianceID.trim(),
          command_type: commandType,
          params: params ?? {},
          reason: reason.trim() || undefined,
        }),
      );
      onDone();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal title="Issue command" onClose={onClose}>
      <form onSubmit={onSubmit} className="space-y-3">
        <div>
          <Label>Appliance ID</Label>
          <Input
            value={applianceID}
            onChange={(e) => setApplianceID(e.target.value)}
            required
            placeholder="appliance uuid"
          />
        </div>
        <div>
          <Label>Command type</Label>
          <select
            value={commandType}
            onChange={(e) => setCommandType(e.target.value as (typeof COMMAND_TYPES)[number])}
            className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm"
          >
            {COMMAND_TYPES.map((t) => (
              <option key={t} value={t}>
                {t}
              </option>
            ))}
          </select>
        </div>
        {isRestart && (
          <div>
            <Label>Service</Label>
            <select
              value={service}
              onChange={(e) => setService(e.target.value as (typeof RESTART_SERVICES)[number])}
              className="h-9 w-full rounded-md bg-panel2 border border-border px-3 text-sm"
            >
              {RESTART_SERVICES.map((s) => (
                <option key={s} value={s}>
                  {s}
                </option>
              ))}
            </select>
          </div>
        )}
        {!isRestart && (
          <div>
            <Label>Params (optional JSON)</Label>
            <textarea
              value={paramsText}
              onChange={(e) => setParamsText(e.target.value)}
              placeholder='{"key": "value"}'
              rows={3}
              className="w-full rounded-md bg-panel2 border border-border px-3 py-2 text-sm font-mono"
            />
          </div>
        )}
        <div>
          <Label>Reason{isReboot ? " (required)" : " (optional)"}</Label>
          <Input
            value={reason}
            onChange={(e) => setReason(e.target.value)}
            required={isReboot}
            placeholder="why this command is being issued"
          />
        </div>
        {err && <div className="text-err text-sm">{err}</div>}
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={busy}>
            {busy ? "Issuing…" : "Issue"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// 7. Offline Packages
// ---------------------------------------------------------------------------

type OfflinePackage = {
  package_id: string;
  appliance_id: string;
  serial?: string;
  issued_at?: string;
  expires_at?: string;
  consumed_at?: string;
  reconciled_at?: string;
};

function OfflinePackagesTab({ sensitive }: { sensitive: Sensitive }) {
  const [rows, setRows] = useState<OfflinePackage[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [applianceID, setApplianceID] = useState("");
  const [validHours, setValidHours] = useState(168);
  const [busy, setBusy] = useState(false);
  // The generated package JSON is held only in memory for a one-time download.
  const [generated, setGenerated] = useState<{ package_id: string; pkg: unknown } | null>(null);

  const load = useCallback(async () => {
    setErr(null);
    try {
      setRows(arr<OfflinePackage>(await api.get<any>("/cloud/v1/offline-packages")));
    } catch (e) {
      setErr(errMsg(e));
      setRows([]);
    }
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  async function onGenerate(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (!applianceID.trim()) {
      setErr("appliance_id is required");
      return;
    }
    setErr(null);
    setBusy(true);
    try {
      const res = await sensitive(() =>
        api.post<{ package_id: string; package: unknown }>(
          `/cloud/v1/offline-packages/${encodeURIComponent(applianceID.trim())}/generate`,
          { valid_hours: Number(validHours) || 168 },
        ),
      );
      setGenerated({ package_id: res.package_id, pkg: res.package });
      setApplianceID("");
      load();
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  }

  function download() {
    if (!generated) return;
    const blob = new Blob([JSON.stringify(generated.pkg, null, 2)], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = `offline-package-${generated.package_id}.json`;
    document.body.appendChild(a);
    a.click();
    a.remove();
    URL.revokeObjectURL(url);
  }

  return (
    <>
      {generated && (
        <Card className="mb-6 border-ok">
          <CardHeader>
            <CardTitle className="text-ok">Package generated — download it now</CardTitle>
            <Button size="sm" variant="ghost" onClick={() => setGenerated(null)}>
              <X size={14} /> Dismiss
            </Button>
          </CardHeader>
          <CardBody>
            <div className="text-sm text-warn mb-2">
              The signed package JSON is downloadable once and is never stored here. Import it on the appliance.
            </div>
            <div className="flex items-center gap-2">
              <code className="flex-1 bg-panel2 border border-border rounded px-3 py-2 font-mono text-sm break-all">
                {generated.package_id}
              </code>
              <Button size="sm" onClick={download}>
                <Download size={12} /> Download JSON
              </Button>
            </div>
          </CardBody>
        </Card>
      )}

      <Card className="mb-6">
        <CardHeader>
          <CardTitle>Generate offline package</CardTitle>
        </CardHeader>
        <CardBody>
          <form onSubmit={onGenerate} className="flex flex-wrap items-end gap-3">
            <div className="flex-1 min-w-[16rem]">
              <Label>Appliance ID</Label>
              <Input
                value={applianceID}
                onChange={(e) => setApplianceID(e.target.value)}
                required
                placeholder="appliance uuid"
              />
            </div>
            <div className="w-40">
              <Label>Valid hours</Label>
              <Input
                type="number"
                value={validHours}
                onChange={(e) => setValidHours(Number(e.target.value))}
                min={1}
                max={8760}
              />
            </div>
            <Button type="submit" disabled={busy}>
              {busy ? "Generating…" : "Generate"}
            </Button>
          </form>
          {err && <div className="text-err text-sm mt-3">{err}</div>}
        </CardBody>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Offline packages{rows ? ` (${rows.length})` : ""}</CardTitle>
          <Button size="sm" variant="secondary" onClick={load}>
            Refresh
          </Button>
        </CardHeader>
        <CardBody className="p-0">
          {rows === null ? (
            <EmptyState title="Loading…" />
          ) : rows.length === 0 ? (
            <EmptyState title="No offline packages" hint="Generate one for an offline/air-gapped appliance." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Package</TH>
                  <TH>Serial</TH>
                  <TH>Appliance</TH>
                  <TH>Issued</TH>
                  <TH>Expires</TH>
                  <TH>Consumed</TH>
                  <TH>Reconciled</TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((p) => (
                  <TR key={p.package_id}>
                    <TD className="font-mono text-xs">{p.package_id.slice(0, 8)}</TD>
                    <TD className="font-mono">{p.serial || "—"}</TD>
                    <TD className="text-muted font-mono text-xs">{(p.appliance_id ?? "—").slice(0, 8)}</TD>
                    <TD className="text-muted text-xs">{formatRelative(p.issued_at)}</TD>
                    <TD className="text-muted text-xs">
                      {p.expires_at ? new Date(p.expires_at).toLocaleDateString() : "—"}
                    </TD>
                    <TD>
                      <Badge tone={p.consumed_at ? "ok" : "info"}>{p.consumed_at ? "consumed" : "pending"}</Badge>
                    </TD>
                    <TD className="text-muted text-xs">{p.reconciled_at ? formatRelative(p.reconciled_at) : "—"}</TD>
                  </TR>
                ))}
              </tbody>
            </Table>
          )}
        </CardBody>
      </Card>
    </>
  );
}

// ---------------------------------------------------------------------------
// 8. Audit
// ---------------------------------------------------------------------------

// There is no platform-wide /cloud/v1/audit list endpoint. Rather than invent
// one, this tab points operators at the dedicated Platform Audit page, which
// is backed by a real (tenant-scoped) audit endpoint.
function AuditTab() {
  return (
    <Card>
      <CardHeader>
        <CardTitle>Audit</CardTitle>
      </CardHeader>
      <CardBody>
        <p className="text-sm text-muted mb-4">
          Enrollment, certificate, license, command and offline-package actions are recorded in the platform audit
          log. There is no consolidated audit feed on this console — open the dedicated Platform Audit page for the
          full, filterable event history.
        </p>
        <Link
          href="/audit"
          className="inline-flex items-center gap-2 h-9 px-3 rounded-md bg-panel2 border border-border text-sm hover:text-text text-muted transition-colors"
        >
          <ExternalLink size={14} /> Open Platform Audit
        </Link>
      </CardBody>
    </Card>
  );
}

// ---------------------------------------------------------------------------
// 9. Security Alerts
// ---------------------------------------------------------------------------

type SecurityAlert = {
  id: string;
  appliance_id: string;
  serial: string;
  kind: string;
  detail?: unknown;
  source_ip: string;
  resolved: boolean;
  at?: string;
};

function detailText(d: unknown): string {
  if (d == null) return "";
  if (typeof d === "string") return d;
  try {
    return JSON.stringify(d);
  } catch {
    return String(d);
  }
}

function AlertsTab() {
  const [rows, setRows] = useState<SecurityAlert[] | null>(null);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    (async () => {
      try {
        setRows(arr<SecurityAlert>(await api.get<any>("/cloud/v1/appliances-admin/security-alerts")));
      } catch (e) {
        setErr(errMsg(e));
        setRows([]);
      }
    })();
  }, []);

  return (
    <Card>
      <CardHeader>
        <CardTitle>Security alerts{rows ? ` (${rows.length})` : ""}</CardTitle>
      </CardHeader>
      <CardBody className="p-0">
        {err && <div className="text-err text-sm px-5 pt-4">{err}</div>}
        {rows === null ? (
          <EmptyState title="Loading…" />
        ) : rows.length === 0 ? (
          <EmptyState title="No security alerts" hint="Identity anomalies from re-enrollment appear here." />
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>Kind</TH>
                <TH>Serial</TH>
                <TH>Detail</TH>
                <TH>Source IP</TH>
                <TH>Resolved</TH>
                <TH>When</TH>
              </TR>
            </THead>
            <tbody>
              {rows.map((a) => (
                <TR key={a.id}>
                  <TD>
                    <Badge tone="err">{a.kind}</Badge>
                  </TD>
                  <TD className="font-mono">{a.serial || "—"}</TD>
                  <TD className="text-muted text-xs break-all">{detailText(a.detail) || "—"}</TD>
                  <TD className="font-mono text-xs text-muted">{a.source_ip || "—"}</TD>
                  <TD>
                    <Badge tone={a.resolved ? "ok" : "warn"}>{a.resolved ? "resolved" : "open"}</Badge>
                  </TD>
                  <TD className="text-muted text-xs">{formatRelative(a.at)}</TD>
                </TR>
              ))}
            </tbody>
          </Table>
        )}
      </CardBody>
    </Card>
  );
}
