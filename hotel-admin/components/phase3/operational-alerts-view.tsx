"use client";

// Phase 3 (DARK) — Operational alerts. The queue shows only alerts whose lifecycle head is not RESOLVED,
// because an operator's queue is what still needs attention.
//
// Every action carries the state the operator was looking at. That is what makes two people clicking
// Acknowledge at the same moment produce exactly one winner and one clear "someone got there first", instead
// of one silently overwriting the other. A conflict reloads the queue rather than retrying blindly.

import { useEffect, useState } from "react";
import { api, ListResp, OperationalAlert } from "@/lib/api";
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { formatRelative } from "@/lib/utils";

const stateTone = (s: string) => (s === "ACKNOWLEDGED" ? "info" : "warn");

export function OperationalAlertsView({ canAct = true }: { canAct?: boolean }) {
  const [rows, setRows] = useState<OperationalAlert[] | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  async function load() {
    setErr(null);
    try {
      const r = await api.get<ListResp<OperationalAlert>>("/operational-alerts");
      setRows(r.data);
    } catch (e: any) {
      setErr(e?.message ?? "Failed to load alerts");
      setRows([]);
    }
  }

  useEffect(() => {
    load();
  }, []);

  async function act(a: OperationalAlert, action: "acknowledge" | "resolve") {
    setBusy(a.audit_id + action);
    setErr(null);
    setNote(null);
    try {
      await api.post("/operational-alerts/" + a.audit_id + "/" + action, {
        // what this operator was looking at when they clicked
        expected_state: a.state,
        reason_code: action === "resolve" ? "OPERATOR_RESOLVED" : "OPERATOR_ACKNOWLEDGED",
      });
      await load();
    } catch (e: any) {
      if (e?.status === 409) {
        // someone else moved this alert; show them the current truth instead of overwriting it
        setNote("This alert changed while you were looking at it. The queue has been refreshed.");
        await load();
      } else {
        setErr(e?.message ?? "The action was refused");
      }
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="space-y-4">
      <h1 className="text-xl font-semibold">Operational alerts</h1>
      <p className="text-sm">
        Alerts raised when a checkout could not be handled with the configured policy — for example an
        emergency grace fallback. Resolved alerts leave the queue.
      </p>

      {err && (
        <p role="alert" className="text-sm text-red-600">
          {err}
        </p>
      )}
      {note && (
        <p role="status" className="text-sm">
          {note}
        </p>
      )}

      <Card>
        <CardBody>
          {rows === null ? (
            <p className="text-sm">Loading…</p>
          ) : rows.length === 0 ? (
            <EmptyState title="No open alerts" hint="Every checkout was handled with the configured policy." />
          ) : (
            <Table>
              <THead>
                <TR>
                  <TH>Alert</TH>
                  <TH>State</TH>
                  <TH>Trigger</TH>
                  <TH>Reason</TH>
                  <TH>Boundary</TH>
                  <TH>Raised</TH>
                  <TH>&nbsp;</TH>
                </TR>
              </THead>
              <tbody>
                {rows.map((a) => (
                  <TR key={a.audit_id}>
                    <TD>
                      <Badge tone="warn">{a.alert_code.replace(/_/g, " ").toLowerCase()}</Badge>
                    </TD>
                    <TD>
                      <Badge tone={stateTone(a.state) as any}>{a.state.toLowerCase()}</Badge>
                    </TD>
                    <TD>{a.trigger.replace(/_/g, " ").toLowerCase()}</TD>
                    <TD>{a.reason_code ?? "—"}</TD>
                    <TD>
                      {formatRelative(a.boundary_at)}
                      {a.boundary_clock_suspect && (
                        <>
                          {" "}
                          <Badge tone="warn">clock suspect</Badge>
                        </>
                      )}
                    </TD>
                    <TD>{formatRelative(a.created_at)}</TD>
                    <TD>
                      {canAct && (
                        <span className="flex gap-2">
                          {a.state === "OPEN" && (
                            <Button
                              aria-label={"Acknowledge alert " + a.alert_code}
                              disabled={busy === a.audit_id + "acknowledge"}
                              onClick={() => act(a, "acknowledge")}
                            >
                              Acknowledge
                            </Button>
                          )}
                          <Button
                            aria-label={"Resolve alert " + a.alert_code}
                            disabled={busy === a.audit_id + "resolve"}
                            onClick={() => act(a, "resolve")}
                          >
                            Resolve
                          </Button>
                        </span>
                      )}
                    </TD>
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
