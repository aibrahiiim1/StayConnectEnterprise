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
import { Card, CardBody } from "@/components/ui/card";
import { Table, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner, Callout } from "@/components/ui/error-banner";
import { SkeletonRows } from "@/components/ui/misc";
import { DialogForm } from "@/components/ui/dialog";
import { formatDate } from "@/lib/utils";

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

export function ActiveRestrictions({
  canRelease,
  onShowAttempts,
}: {
  canRelease: boolean;
  onShowAttempts: (deviceMAC: string) => void;
}) {
  const [rows, setRows] = useState<GuestSignInRestriction[] | null>(null);
  const [loadedAt, setLoadedAt] = useState(Date.now());
  const [err, setErr] = useState<unknown>(null);
  const [note, setNote] = useState("");

  const [target, setTarget] = useState<GuestSignInRestriction | null>(null);
  const [reason, setReason] = useState("");
  const [releasing, setReleasing] = useState(false);
  const [releaseErr, setReleaseErr] = useState<unknown>(null);

  const tick = useTicker();

  const load = useCallback(async () => {
    try {
      const r = await api.get<{ restrictions: GuestSignInRestriction[] }>("/guest-signin-restrictions");
      setRows(r.restrictions ?? []);
      setLoadedAt(Date.now());
      setErr(null);
    } catch (e) {
      setErr(e);
      setRows([]);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  // A restriction is a minute long by default, so the list re-reads itself periodically. Without it an
  // operator watching the screen would see a countdown reach zero and the row stay there.
  useEffect(() => {
    const t = setInterval(() => { void load(); }, 15_000);
    return () => clearInterval(t);
  }, [load]);

  async function release(e: React.FormEvent) {
    e.preventDefault();
    if (!target) return;
    setReleasing(true);
    setReleaseErr(null);
    try {
      await api.post(`/guest-signin-restrictions/${target.id}/release`, { reason: reason.trim() });
      setNote(
        `The wait was ended for ${target.device_mac}. That device may try to sign in again — it has not been ` +
        `given access.`,
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
    <>
      <ErrorBanner err={err} />
      {note && <p className="text-sm text-success-subtle-foreground" role="status">{note}</p>}

      <Callout tone="neutral" title="What this list is">
        Devices currently being asked to wait after too many incorrect sign-in details. A device disappears from
        this list on its own when its wait ends. The room shown is the last room that device{" "}
        <strong>typed</strong> — it is not a statement about who is using it or where they are staying.
      </Callout>

      <Card>
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
                <TH>Guest network</TH>
                <TH>Last room typed (unverified)</TH>
                <TH>Why</TH>
                <TH>Started</TH>
                <TH>Time left</TH>
                <TH />
              </TR>
            </THead>
            <tbody>
              {rows.map((r) => {
                const left = remaining(r, loadedAt, tick);
                return (
                  <TR key={r.id}>
                    <TD className="font-mono text-xs">{r.device_mac}</TD>
                    <TD className="text-sm text-muted-foreground">{r.guest_network || "—"}</TD>
                    <TD className="text-sm">
                      {r.last_submitted_room ? (
                        <span title="What this device typed. Not a verified room.">{r.last_submitted_room}</span>
                      ) : "—"}
                    </TD>
                    <TD className="text-sm">
                      {r.failure_count} incorrect sign-in{r.failure_count === 1 ? "" : "s"}
                    </TD>
                    <TD className="whitespace-nowrap text-sm text-muted-foreground" title={formatDate(r.restricted_at)}>
                      {formatDate(r.restricted_at)}
                    </TD>
                    <TD className="whitespace-nowrap">
                      <Badge tone={left > 0 ? "info" : "default"} dot>
                        <Timer size={11} aria-hidden /> {left > 0 ? `${left}s` : "ending"}
                      </Badge>
                      <span className="ml-2 text-xs text-muted-foreground" title={formatDate(r.expires_at)}>
                        until {formatDate(r.expires_at)}
                      </span>
                    </TD>
                    <TD className="whitespace-nowrap">
                      <Button size="sm" variant="ghost" onClick={() => onShowAttempts(r.device_mac)}>
                        Sign-in attempts
                      </Button>
                      {canRelease && (
                        <Button size="sm" variant="secondary" onClick={() => { setTarget(r); setReason(""); setReleaseErr(null); }}>
                          Release
                        </Button>
                      )}
                    </TD>
                  </TR>
                );
              })}
            </tbody>
          </Table>
        )}
      </Card>

      <DialogForm
        open={target !== null}
        onOpenChange={(v) => { if (!v && !releasing) { setTarget(null); setReleaseErr(null); } }}
        title="Release this restriction"
        description="The device will be able to try signing in again. It is not being given access."
        submitLabel="Release"
        busy={releasing}
        busyLabel="Releasing…"
        error={releaseErr}
        disabled={reason.trim().length < 3}
        onSubmit={release}
      >
        {target && (
          <>
            <dl className="grid grid-cols-2 gap-2 text-sm">
              <dt className="text-muted-foreground">Device</dt>
              <dd className="font-mono text-xs">{target.device_mac}</dd>
              <dt className="text-muted-foreground">Guest network</dt>
              <dd>{target.guest_network || "—"}</dd>
              <dt className="text-muted-foreground">Last room typed</dt>
              <dd>
                {target.last_submitted_room || "—"}{" "}
                <span className="text-xs text-muted-foreground">(unverified — what was typed)</span>
              </dd>
              <dt className="text-muted-foreground">Wait ends</dt>
              <dd>{formatDate(target.expires_at)}</dd>
            </dl>
            <div className="space-y-1">
              <label htmlFor="release-reason" className="block text-sm font-medium">
                Reason <span className="font-normal text-muted-foreground">(recorded with your name)</span>
              </label>
              <Input
                id="release-reason"
                value={reason}
                onChange={(e) => setReason(e.target.value)}
                maxLength={200}
                placeholder="e.g. guest at the desk, identity confirmed from their passport"
                aria-describedby="release-reason-help"
              />
              <p id="release-reason-help" className="text-xs text-muted-foreground">
                At least 3 characters. Who released the restriction, which device, when and why are all recorded.
              </p>
            </div>
            <Callout tone="warning" title="Releasing does not sign the guest in">
              The guest still has to enter details the property accepts. If they do not know them, the answer is
              the PMS record — not this button.
            </Callout>
          </>
        )}
      </DialogForm>
    </>
  );
}
