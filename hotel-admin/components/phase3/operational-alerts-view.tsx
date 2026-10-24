"use client";

// Send the viewed alert state with each action so concurrent decisions cannot silently overwrite each other.

import { useEffect, useState } from "react";
import { BellRing, CheckCheck } from "lucide-react";
import { api, ListResp, OperationalAlert } from "@/lib/api";
import { PageShell, PageHeader } from "@/components/ui/page";
import { Card } from "@/components/ui/card";
import { Table, THead, TBody, TR, TH, TD } from "@/components/ui/table";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { RowActions } from "@/components/ui/row-actions";
import { EmptyState } from "@/components/ui/empty-state";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { SkeletonRows } from "@/components/ui/misc";
import { ReadOnlyNotice } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { formatDate, formatRelative } from "@/lib/utils";
import { HelpList, HelpSection } from "@/components/help";

const stateTone = (s: string) => (s === "ACKNOWLEDGED" ? "info" : "warn");
const stateWord = (s: string) => (s === "ACKNOWLEDGED" ? "Acknowledged" : s === "OPEN" ? "Open" : s.toLowerCase());

const words = (code?: string | null) => (code ? code.replace(/_/g, " ").toLowerCase() : "—");
const sentence = (code?: string | null) => {
  const w = words(code);
  return w === "—" ? w : w.charAt(0).toUpperCase() + w.slice(1);
};

export function OperationalAlertsView({
  canAct = true,
  readOnly = false,
}: {
  canAct?: boolean;
  /** Wait for role loading before declaring the operator read-only. */
  readOnly?: boolean;
}) {
  const toast = useToast();
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
      // Keep the last known queue if a refresh fails.
      setErr(e?.message ?? "Failed to load alerts");
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
        expected_state: a.state,
        reason_code: action === "resolve" ? "OPERATOR_RESOLVED" : "OPERATOR_ACKNOWLEDGED",
      });
      toast.success(action === "resolve" ? "Alert resolved" : "Alert acknowledged");
      await load();
    } catch (e: any) {
      if (e?.status === 409) {
        // Reload on conflict to show the other operator's decision.
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
    <PageShell width="wide">
      <PageHeader
        title="Alerts"
        description="Checkouts the configured policy could not handle on its own."
        help={
          <>
            <HelpSection title="What raises an alert">
              <p>
                A checkout situation the configured policy could not handle on its own — for example when the
                emergency Grace Period terms were used.
              </p>
            </HelpSection>
            <HelpSection title="Working the queue">
              <HelpList
                items={[
                  <><strong>Acknowledge</strong> — you have seen it and are dealing with it. It stays in the queue.</>,
                  <><strong>Resolve</strong> — it is handled. Resolved alerts leave the queue.</>,
                  "If someone else changed an alert while you were looking at it, your action is refused and the queue refreshes so you see the current state.",
                ]}
              />
            </HelpSection>
            <HelpSection title="Clock suspect">
              <p>
                The appliance&rsquo;s clock may not have been trustworthy at the boundary time, so treat that time with
                care.
              </p>
            </HelpSection>
          </>
        }
      />

      {readOnly && <ReadOnlyNotice>Your role can view alerts but not acknowledge or resolve them.</ReadOnlyNotice>}
      <ErrorBanner err={err} className="mb-0" />
      {note && <Callout tone="warning">{note}</Callout>}

      <Card className="overflow-hidden">
        {rows === null ? (
          err ? (
            <EmptyState size="sm" icon={<BellRing />} title="Alerts could not be read" hint="The queue appears here once the appliance answers." />
          ) : (
            <SkeletonRows rows={3} cols={5} />
          )
        ) : rows.length === 0 ? (
          <EmptyState
            size="sm"
            icon={<CheckCheck />}
            title="No open alerts"
            hint="Every checkout was handled with the configured policy."
          />
        ) : (
          <Table label="Alerts">
            <THead>
              <TR>
                <TH>Alert</TH>
                <TH>State</TH>
                <TH className="hidden md:table-cell">Trigger</TH>
                <TH className="hidden lg:table-cell">Reason</TH>
                <TH className="hidden sm:table-cell">Boundary time</TH>
                <TH className="hidden sm:table-cell">Raised</TH>
                {canAct && <TH><span className="sr-only">Actions</span></TH>}
              </TR>
            </THead>
            <TBody>
              {rows.map((a) => (
                <TR key={a.audit_id}>
                  <TD>
                    <div className="font-medium">{sentence(a.alert_code)}</div>
                    <div className="text-caption text-muted-foreground sm:hidden">
                      Raised {formatRelative(a.created_at)}
                    </div>
                  </TD>
                  <TD nowrap>
                    <StatusBadge tone={stateTone(a.state)}>{stateWord(a.state)}</StatusBadge>
                  </TD>
                  <TD className="hidden md:table-cell">{sentence(a.trigger)}</TD>
                  <TD muted className="hidden lg:table-cell">{sentence(a.reason_code)}</TD>
                  <TD nowrap className="hidden sm:table-cell">
                    <span title={formatDate(a.boundary_at)}>{formatRelative(a.boundary_at)}</span>
                    {a.boundary_clock_suspect && (
                      <>
                        {" "}
                        <Badge tone="warn">Clock suspect</Badge>
                      </>
                    )}
                  </TD>
                  <TD nowrap muted className="hidden sm:table-cell">
                    <span title={formatDate(a.created_at)}>{formatRelative(a.created_at)}</span>
                  </TD>
                  {canAct && (
                    <TD className="w-0">
                      <RowActions
                        subject={"alert " + a.alert_code}
                        primary={a.state === "OPEN"
                          ? { label: "Acknowledge", disabled: busy === a.audit_id + "acknowledge", onSelect: () => void act(a, "acknowledge") }
                          : { label: "Resolve", disabled: busy === a.audit_id + "resolve", onSelect: () => void act(a, "resolve") }}
                        items={[{
                          label: "Resolve",
                          hidden: a.state !== "OPEN",
                          disabled: busy === a.audit_id + "resolve",
                          onSelect: () => void act(a, "resolve"),
                        }]}
                      />
                    </TD>
                  )}
                </TR>
              ))}
            </TBody>
          </Table>
        )}
      </Card>
    </PageShell>
  );
}
