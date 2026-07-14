"use client";

import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { useTenant } from "@/lib/use-tenant";
import { Card, CardBody, CardHeader, CardTitle } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Input, Label } from "@/components/ui/input";
import { Badge } from "@/components/ui/badge";
import { EmptyState } from "@/components/ui/empty-state";
import { formatRelative, errMsg } from "@/lib/utils";
import { X } from "lucide-react";

// arr coerces either a bare array or a {data:[...]} envelope into a plain array.
const arr = <T,>(r: any): T[] => (Array.isArray(r) ? r : (r?.data ?? []));

type Tone = "default" | "ok" | "warn" | "err" | "info";

function stateTone(s?: string): Tone {
  switch ((s ?? "").toLowerCase()) {
    case "online":
    case "licensed":
    case "assigned":
    case "active":
      return "ok";
    case "enrolled":
    case "claimed":
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

type MyAppliance = {
  id: string;
  serial?: string;
  name?: string;
  site_id?: string;
  status?: string;
  lifecycle_state?: string;
  version?: string;
  last_seen_at?: string;
};

type RequestKind = "support" | "replacement" | "reassignment";

const KIND_LABEL: Record<RequestKind, string> = {
  support: "Request support",
  replacement: "Request replacement",
  reassignment: "Request reassignment",
};

const KIND_PATH: Record<RequestKind, string> = {
  support: "support-request",
  replacement: "replacement-request",
  reassignment: "reassignment-request",
};

// Modal is a small centered overlay for the request-note form.
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

export default function MyAppliancesPage() {
  const tenantID = useTenant();
  const [rows, setRows] = useState<MyAppliance[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [request, setRequest] = useState<{ appliance: MyAppliance; kind: RequestKind } | null>(null);

  const load = useCallback(async () => {
    if (!tenantID) return;
    setErr(null);
    try {
      setRows(arr<MyAppliance>(await api.get<any>(`/cloud/v1/appliances?tenant_id=${tenantID}`)));
    } catch (e) {
      setErr(errMsg(e));
      setRows([]);
    }
  }, [tenantID]);
  useEffect(() => {
    load();
  }, [load]);

  return (
    <div className="p-6 max-w-7xl mx-auto">
      <div className="mb-4">
        <div className="text-xs text-muted uppercase tracking-wider">Your appliances</div>
        <h1 className="text-2xl font-semibold">My Appliances</h1>
        <p className="text-sm text-muted">
          View the appliances assigned to your organization and raise support, replacement or reassignment requests.
        </p>
      </div>

      {notice && (
        <Card className="mb-4 border-ok">
          <CardBody className="flex items-center justify-between">
            <div className="text-sm text-ok">{notice}</div>
            <Button size="sm" variant="ghost" onClick={() => setNotice(null)}>
              <X size={14} /> Dismiss
            </Button>
          </CardBody>
        </Card>
      )}

      <Card>
        <CardHeader>
          <CardTitle>Appliances{rows ? ` (${rows.length})` : ""}</CardTitle>
          <Button size="sm" variant="secondary" onClick={load}>
            Refresh
          </Button>
        </CardHeader>
        <CardBody className="p-0">
          {err && <div className="text-err text-sm px-5 pt-4">{err}</div>}
          {rows === null ? (
            <EmptyState title="Loading…" />
          ) : rows.length === 0 ? (
            <EmptyState title="No appliances" hint="Appliances assigned to your organization appear here." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Serial</TH>
                  <TH>Site</TH>
                  <TH>Status</TH>
                  <TH>Version</TH>
                  <TH>Last seen</TH>
                  <TH></TH>
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
                      <TD className="text-muted font-mono text-xs">{(a.site_id ?? "—").slice(0, 8)}</TD>
                      <TD>
                        <Badge tone={stateTone(life)}>{life}</Badge>
                      </TD>
                      <TD className="text-muted text-xs">{a.version || "—"}</TD>
                      <TD className="text-muted text-xs">{formatRelative(a.last_seen_at)}</TD>
                      <TD className="text-right whitespace-nowrap">
                        <Button size="sm" variant="ghost" onClick={() => setRequest({ appliance: a, kind: "support" })}>
                          Support
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() => setRequest({ appliance: a, kind: "replacement" })}
                        >
                          Replace
                        </Button>
                        <Button
                          size="sm"
                          variant="ghost"
                          onClick={() => setRequest({ appliance: a, kind: "reassignment" })}
                        >
                          Reassign
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

      {request && (
        <RequestModal
          appliance={request.appliance}
          kind={request.kind}
          onClose={() => setRequest(null)}
          onDone={(msg) => {
            setRequest(null);
            setNotice(msg);
          }}
        />
      )}
    </div>
  );
}

function RequestModal({
  appliance,
  kind,
  onClose,
  onDone,
}: {
  appliance: MyAppliance;
  kind: RequestKind;
  onClose: () => void;
  onDone: (msg: string) => void;
}) {
  const [note, setNote] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: React.FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setErr(null);
    setBusy(true);
    try {
      await api.post(`/cloud/v1/appliances-support/${appliance.id}/${KIND_PATH[kind]}`, { note: note.trim() });
      onDone(`${KIND_LABEL[kind]} submitted for ${appliance.serial || appliance.id.slice(0, 8)}.`);
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal title={`${KIND_LABEL[kind]} — ${appliance.serial || appliance.id.slice(0, 8)}`} onClose={onClose}>
      <form onSubmit={onSubmit} className="space-y-3">
        <div>
          <Label>Note (optional)</Label>
          <textarea
            value={note}
            onChange={(e) => setNote(e.target.value)}
            rows={4}
            placeholder="Describe the issue or reason for this request"
            className="w-full rounded-md bg-panel2 border border-border px-3 py-2 text-sm"
          />
        </div>
        {err && <div className="text-err text-sm">{err}</div>}
        <div className="flex justify-end gap-2 pt-1">
          <Button type="button" variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" disabled={busy}>
            {busy ? "Submitting…" : "Submit request"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
