"use client";

// ACTIVE RESTRICTIONS — which devices are currently being asked to wait, and the one action that ends a wait.
//
// It sits beside Guest sign-in attempts rather than on a page of its own because it answers the second half of
// the same question. The attempts list says why a guest could not connect; this says whether they are being
// asked to wait before trying again, and lets an operator who can see the guest end that wait.
//
// THE ROOM SHOWN HERE IS UNVERIFIED. It is the last room this device TYPED — a string somebody chose, not a
// statement about where anyone is staying. The column heading, the detail label and the release dialog all say
// so, because an operator who read it as an identity would be identifying a person by an attacker's input.
//
// RELEASING ALLOWS ANOTHER ATTEMPT. IT DOES NOT SIGN ANYONE IN. That is stated on the button's dialog and in
// the confirmation, because "release this guest" is exactly the phrase a desk would expect to mean "let them
// in", and the difference matters when the person at the desk is not the person who was guessing.

import { useCallback, useEffect, useState } from "react";
import { ShieldCheck, Timer } from "lucide-react";
import { api, GuestSignInRestriction } from "@/lib/api";
import { Card } from "@/components/ui/card";
import { Table, TBody, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Field, Input } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { SkeletonRows } from "@/components/ui/misc";
import { DialogForm } from "@/components/ui/dialog";
import { KeyValueGrid } from "@/components/ui/data";
import { LiveStatus, formatCountdown, refreshingClass } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";
import { cn, formatDate } from "@/lib/utils";

// countdown renders the remaining wait. The number it counts from is the SERVER's, taken at the moment of the
// read; this only makes it tick so the screen is not stale between refreshes. When it reaches zero the row is
// gone on the next load rather than being declared over here.
function useTicker(): number {
  const [tick, setTick] = useState(0);
  useEffect(() => {
    const t = setInterval(() => setTick((n) => n + 1), 1000);
    return () => clearInterval(t);
  }, []);
  return tick;
}

function remaining(r: GuestSignInRestriction, loadedAt: number, _tick: number): number {
  const elapsed = Math.floor((Date.now() - loadedAt) / 1000);
  return Math.max(0, r.remaining_seconds - elapsed);
}

const POLL_SECONDS = 15;

export function ActiveRestrictions({
  canRelease,
  onShowAttempts,
}: {
  canRelease: boolean;
  onShowAttempts: (deviceMAC: string) => void;
}) {
  const toast = useToast();
  const [rows, setRows] = useState<GuestSignInRestriction[] | null>(null);
  const [loadedAt, setLoadedAt] = useState(Date.now());
  const [err, setErr] = useState<unknown>(null);
  const [refreshing, setRefreshing] = useState(false);

  const [target, setTarget] = useState<GuestSignInRestriction | null>(null);
  const [reason, setReason] = useState("");
  const [releasing, setReleasing] = useState(false);
  const [releaseErr, setReleaseErr] = useState<unknown>(null);

  const tick = useTicker();

  const load = useCallback(async () => {
    setRefreshing(true);
    try {
      const r = await api.get<{ restrictions: GuestSignInRestriction[] }>("/guest-signin-restrictions");
      setRows(r.restrictions ?? []);
      setLoadedAt(Date.now());
      setErr(null);
    } catch (e) {
      setErr(e);
      setRows((prev) => prev ?? []);
    } finally {
      setRefreshing(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  // A restriction is a minute long by default, so the list re-reads itself periodically. Without it an
  // operator watching the screen would see a countdown reach zero and the row stay there.
  useEffect(() => {
    const t = setInterval(() => { void load(); }, POLL_SECONDS * 1000);
    return () => clearInterval(t);
  }, [load]);

  async function release() {
    if (!target) return;
    setReleasing(true);
    setReleaseErr(null);
    try {
      await api.post(`/guest-signin-restrictions/${target.id}/release`, { reason: reason.trim() });
      toast.success(
        "Wait ended — the guest is not signed in",
        `${target.device_mac} may try to sign in again. It has not been given access.`,
      );
      setTarget(null);
      setReason("");
      await load();
    } catch (e2) {
      setReleaseErr(e2);
    } finally {
      setReleasing(false);
    }
  }

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <LiveStatus
          updatedAt={rows === null ? null : loadedAt}
          refreshing={refreshing && rows !== null}
          intervalSeconds={POLL_SECONDS}
          error={!!err && !!rows?.length}
          onRefresh={() => void load()}
        />
      </div>

      <ErrorBanner err={err} />

      <Card className={cn("overflow-hidden", refreshing && rows !== null && refreshingClass)}>
        {rows === null ? (
          <SkeletonRows rows={4} cols={6} />
        ) : rows.length === 0 ? (
          <EmptyState
            icon={<ShieldCheck />}
            title="No device is being asked to wait"
            hint="Devices appear here after repeated incorrect sign-in details, and leave on their own when the wait ends."
          />
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>Device</TH>
                <TH className="hidden md:table-cell">Guest network</TH>
                <TH className="hidden sm:table-cell">Last room typed (unverified)</TH>
                <TH className="hidden lg:table-cell">Why</TH>
                <TH className="hidden lg:table-cell">Started</TH>
                <TH>Time left</TH>
                <TH><span className="sr-only">Actions</span></TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((r) => {
                const left = remaining(r, loadedAt, tick);
                return (
                  <TR key={r.id}>
                    <TD className="font-mono text-xs">{r.device_mac}</TD>
                    <TD className="hidden text-sm text-muted-foreground md:table-cell">{r.guest_network || "—"}</TD>
                    <TD className="hidden text-sm sm:table-cell">
                      {r.last_submitted_room ? (
                        <span className="inline-flex flex-wrap items-center gap-1.5" title="What this device typed. Not a verified room.">
                          {r.last_submitted_room}
                          <Badge tone="neutral">unverified</Badge>
                        </span>
                      ) : "—"}
                    </TD>
                    <TD className="hidden text-sm lg:table-cell">
                      {r.failure_count} incorrect sign-in{r.failure_count === 1 ? "" : "s"}
                    </TD>
                    <TD className="hidden whitespace-nowrap text-sm text-muted-foreground lg:table-cell">
                      {formatDate(r.restricted_at)}
                    </TD>
                    <TD className="whitespace-nowrap">
                      <Badge tone={left > 0 ? "info" : "default"} dot>
                        <Timer size={11} aria-hidden />{" "}
                        <span className="tabular">{left > 0 ? formatCountdown(left) : "ending"}</span>
                      </Badge>
                      <div className="mt-0.5 text-caption text-muted-foreground">until {formatDate(r.expires_at)}</div>
                    </TD>
                    <TD className="whitespace-nowrap text-end">
                      <div className="flex flex-wrap justify-end gap-1.5">
                        <Button size="sm" variant="ghost" onClick={() => onShowAttempts(r.device_mac)}>
                          Sign-in attempts
                        </Button>
                        {canRelease && (
                          <Button size="sm" variant="secondary" onClick={() => { setTarget(r); setReason(""); setReleaseErr(null); }}>
                            Release
                          </Button>
                        )}
                      </div>
                    </TD>
                  </TR>
                );
              })}
            </TBody>
          </Table>
        )}
      </Card>

      <DialogForm
        open={target !== null}
        onOpenChange={(v) => { if (!v && !releasing) { setTarget(null); setReleaseErr(null); } }}
        title="Release this restriction"
        description="The device will be able to try signing in again. It is not being given access."
        size="sm"
        submitLabel="Release"
        busy={releasing}
        busyLabel="Releasing…"
        error={releaseErr}
        disabled={reason.trim().length < 3}
        onSubmit={release}
      >
        {target && (
          <>
            <KeyValueGrid
              items={[
                { label: "Device", value: <span className="font-mono text-xs">{target.device_mac}</span> },
                { label: "Guest network", value: target.guest_network || "—" },
                {
                  label: "Last room typed",
                  value: (
                    <>
                      {target.last_submitted_room || "—"}{" "}
                      <span className="text-caption text-muted-foreground">(unverified — what was typed)</span>
                    </>
                  ),
                },
                { label: "Wait ends", value: formatDate(target.expires_at) },
              ]}
            />
            <Field
              label="Reason"
              required
              hint="Recorded with your name. At least 3 characters. Who released the restriction, which device, when and why are all recorded."
            >
              <Input
                value={reason}
                onChange={(e) => setReason(e.target.value)}
                maxLength={200}
                placeholder="e.g. guest at the desk, identity confirmed from their passport"
              />
            </Field>
            <Callout tone="warning" title="Releasing does not sign the guest in">
              The guest still has to enter details the property accepts. If they do not know them, the answer is
              the PMS record — not this button.
            </Callout>
          </>
        )}
      </DialogForm>
    </div>
  );
}
