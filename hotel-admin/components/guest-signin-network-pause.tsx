"use client";

// THE NETWORK PAUSE (migration 0136; GDPR audit A4).
//
// The device rule (GuestSignInProtectionCard) is defeated by a device that keeps changing its address, so many
// wrong room sign-ins on one client network pause Room sign-in there. Two parts here:
//
//   - NetworkPauseSettingsCard: the three numbers, changed with a reason and the password, like the device rule;
//   - NetworkPausesNotice: the alert while a pause runs, with the two things staff can do about it — let a
//     genuine client's device try again, or end the pause when it is a false alarm. Neither signs anyone in.

import { useCallback, useEffect, useState } from "react";
import { ShieldAlert } from "lucide-react";
import { api, ApiError, inBackground } from "@/lib/api";
import { ConfirmDialog } from "@/components/ui/dialog";
import { REASON_MAX_CHARS, REASON_MIN_CHARS } from "@/lib/reason";
import { Card, CardBody, CardDescription, CardFooter, CardHeader, CardTitle } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Callout, ErrorBanner } from "@/components/ui/error-banner";
import { Skeleton } from "@/components/ui/misc";
import { ReadOnlyNotice, SettingField } from "@/components/ui/patterns";
import { useToast } from "@/components/ui/toast";

export type NetworkPausePolicy = {
  network_max_failures: number;
  network_window_seconds: number;
  network_pause_seconds: number;
  is_default: boolean;
  limits: {
    min_network_max_failures: number; max_network_max_failures: number;
    min_network_window_seconds: number; max_network_window_seconds: number;
    min_network_pause_seconds: number; max_network_pause_seconds: number;
  };
};

export type NetworkPause = {
  id: string;
  guest_network: string;
  started_at: string;
  expires_at: string;
  remaining_seconds: number;
  failure_count: number;
  confirmed_devices: number;
};

export type PausedDevice = {
  device_mac: string;
  last_submitted_room_unverified: string;
  last_attempt_at: string;
  attempts: number;
  confirmed: boolean;
};

const STANDARD = { max: 30, windowMin: 10, pauseMin: 15 } as const;

const at = (iso: string) => new Date(iso).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });

function invalid(value: string, min: number, max: number): string | undefined {
  const n = Number(value);
  return value.trim() === "" || !Number.isInteger(n) || n < min || n > max
    ? `Enter a whole number between ${min} and ${max}.` : undefined;
}

/** The three numbers. Minutes on screen; seconds on the wire, like the server's bounds. */
export function NetworkPauseSettingsCard({ canWrite }: { canWrite: boolean }) {
  const toast = useToast();
  const [p, setP] = useState<NetworkPausePolicy | null>(null);
  const [draft, setDraft] = useState({ max: "", window: "", pause: "" });
  const [err, setErr] = useState<unknown>(null);
  const [confirming, setConfirming] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saveErr, setSaveErr] = useState<unknown>(null);

  const fill = (v: NetworkPausePolicy) => {
    setP(v);
    setDraft({ max: String(v.network_max_failures), window: String(v.network_window_seconds / 60),
      pause: String(v.network_pause_seconds / 60) });
  };
  useEffect(() => {
    api.get<NetworkPausePolicy>("/guest-signin-protection/network")
      .then((v) => (v?.limits ? fill(v) : setErr(new Error("The network pause settings could not be read."))))
      .catch(setErr);
  }, []);

  if (err && !p) return <ErrorBanner err={err} />;
  if (!p) return <Skeleton className="h-48 w-full" />;

  const l = p.limits;
  const errors = {
    max: invalid(draft.max, l.min_network_max_failures, l.max_network_max_failures),
    window: invalid(draft.window, l.min_network_window_seconds / 60, l.max_network_window_seconds / 60),
    pause: invalid(draft.pause, l.min_network_pause_seconds / 60, l.max_network_pause_seconds / 60),
  };
  const dirty = draft.max !== String(p.network_max_failures) || draft.window !== String(p.network_window_seconds / 60) ||
    draft.pause !== String(p.network_pause_seconds / 60);
  const readOnly = !canWrite || saving;

  async function save({ reason, password }: { reason: string; password: string }) {
    setSaving(true);
    setSaveErr(null);
    try {
      const next = await api.put<NetworkPausePolicy>("/guest-signin-protection/network", {
        network_max_failures: Number(draft.max),
        network_window_seconds: Number(draft.window) * 60,
        network_pause_seconds: Number(draft.pause) * 60,
        reason: reason.trim(),
        password,
      });
      fill(next);
      setConfirming(false);
      toast.success("Network pause settings saved", "They apply to the next wrong room sign-in.");
    } catch (e) {
      setSaveErr(e instanceof ApiError && e.code === "reauth_required" ? new Error("Your password was not accepted.") : e);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Card>
      <CardHeader className="items-start">
        <div className="min-w-0 space-y-1">
          <CardTitle className="flex flex-wrap items-center gap-2">
            <ShieldAlert className="size-4 text-muted-foreground" aria-hidden /> Many wrong room sign-ins on one network
            {p.is_default && <Badge tone="default">Using the standard settings</Badge>}
          </CardTitle>
          <CardDescription className="max-w-2xl">
            Someone can get round the device limit above by making their device look like a new one each time. So
            when a client network gets this many wrong room sign-ins, from any devices, Room sign-in pauses on that
            network for a while. Other sign-in methods keep working. Staff see the pause here and on Guest sign-in
            attempts, and can let a genuine client try again or end the pause.
          </CardDescription>
        </div>
      </CardHeader>
      <CardBody className="space-y-5">
        {!canWrite && <ReadOnlyNotice>Your role can see these settings but not change them.</ReadOnlyNotice>}
        <div className="grid gap-5 sm:grid-cols-3">
          <SettingField id="gnp-max" label="Wrong room sign-ins" unit="sign-ins" value={draft.max}
            min={l.min_network_max_failures} max={l.max_network_max_failures} defaultValue={STANDARD.max}
            readOnly={readOnly} error={canWrite ? errors.max : undefined}
            onChange={(v) => setDraft({ ...draft, max: v })}
            explanation="On one client network, from any devices, before Room sign-in pauses there." />
          <SettingField id="gnp-window" label="Counted over" unit="minutes" value={draft.window}
            min={l.min_network_window_seconds / 60} max={l.max_network_window_seconds / 60} defaultValue={STANDARD.windowMin}
            readOnly={readOnly} error={canWrite ? errors.window : undefined}
            onChange={(v) => setDraft({ ...draft, window: v })}
            explanation="Wrong room sign-ins older than this stop counting." />
          <SettingField id="gnp-pause" label="Pause for" unit="minutes" value={draft.pause}
            min={l.min_network_pause_seconds / 60} max={l.max_network_pause_seconds / 60} defaultValue={STANDARD.pauseMin}
            readOnly={readOnly} error={canWrite ? errors.pause : undefined}
            onChange={(v) => setDraft({ ...draft, pause: v })}
            explanation="How long Room sign-in stays paused on that network, unless staff end it sooner." />
        </div>
      </CardBody>
      {canWrite && (
        <CardFooter className="justify-end">
          {dirty && !saving && <Button variant="ghost" onClick={() => fill(p)}>Discard</Button>}
          <Button onClick={() => { setSaveErr(null); setConfirming(true); }}
            disabled={!dirty || saving || !!(errors.max || errors.window || errors.pause)}>
            {saving ? "Saving…" : "Save network pause settings"}
          </Button>
        </CardFooter>
      )}
      <ConfirmDialog
        open={confirming}
        onOpenChange={(v) => !v && !saving && setConfirming(false)}
        title="Change the network pause?"
        description={`From the next wrong room sign-in: ${draft.max} within ${draft.window} minutes on one network pause Room sign-in there for ${draft.pause} minutes.`}
        confirmLabel="Save network pause settings"
        requireReason
        reasonMinLength={REASON_MIN_CHARS}
        reasonMaxLength={REASON_MAX_CHARS}
        reasonPlaceholder="For example: large conference, many guests mistyping"
        requirePassword
        busy={saving}
        error={saveErr}
        onConfirm={save}
      />
    </Card>
  );
}

/** The alert while a pause runs, with the staff actions. Renders nothing when no network is paused. */
export function NetworkPausesNotice({ canAct }: { canAct: boolean }) {
  const toast = useToast();
  const [pauses, setPauses] = useState<NetworkPause[]>([]);
  const [ending, setEnding] = useState<NetworkPause | null>(null);
  const [letting, setLetting] = useState<NetworkPause | null>(null);
  const [devices, setDevices] = useState<PausedDevice[] | null>(null);
  const [picked, setPicked] = useState("");
  const [busy, setBusy] = useState(false);
  const [actionErr, setActionErr] = useState<unknown>(null);

  const load = useCallback(async () => {
    try {
      const r = await api.get<{ pauses?: NetworkPause[] }>("/guest-signin-restrictions/network-pauses");
      setPauses(Array.isArray(r?.pauses) ? r.pauses : []);
    } catch { /* the notice is advisory: no pause shown rather than an error on an unrelated screen */ }
  }, []);
  useEffect(() => {
    void load();
    const t = setInterval(() => void inBackground(load), 30_000);
    return () => clearInterval(t);
  }, [load]);

  async function openLetThrough(p: NetworkPause) {
    setLetting(p); setPicked(""); setDevices(null); setActionErr(null);
    try {
      const r = await api.get<{ devices: PausedDevice[] }>(`/guest-signin-restrictions/network-pauses/${p.id}/devices`);
      setDevices(r.devices ?? []);
    } catch (e) {
      setActionErr(e);
      setDevices([]);
    }
  }

  async function letThrough({ reason }: { reason: string }) {
    if (!letting) return;
    if (!picked) { setActionErr(new Error("Pick the client's device from the list first.")); return; }
    setBusy(true); setActionErr(null);
    try {
      await api.post(`/guest-signin-restrictions/network-pauses/${letting.id}/confirm`, { device_mac: picked, reason: reason.trim() });
      setLetting(null);
      toast.success("The client can try again", "Ask them to sign in again now. They have not been signed in.");
      void load();
    } catch (e) {
      setActionErr(e);
    } finally {
      setBusy(false);
    }
  }

  async function endPause({ reason }: { reason: string }) {
    if (!ending) return;
    setBusy(true); setActionErr(null);
    try {
      await api.post(`/guest-signin-restrictions/network-pauses/${ending.id}/end`, { reason: reason.trim() });
      setEnding(null);
      toast.success("Room sign-in works again", `On ${ending.guest_network || "that network"}. Nobody has been signed in.`);
      void load();
    } catch (e) {
      setActionErr(e);
    } finally {
      setBusy(false);
    }
  }

  if (pauses.length === 0) return null;
  return (
    <>
      {pauses.map((p) => (
        <Callout key={p.id} tone="warning" title={`Room sign-in is paused on ${p.guest_network || "a client network"} until ${at(p.expires_at)}`}>
          <p>
            {p.failure_count} wrong room sign-ins came from this network in a short time, which is what someone
            guessing rooms looks like. Other sign-in methods still work.
            {p.confirmed_devices > 0 && ` ${p.confirmed_devices} client device${p.confirmed_devices === 1 ? " has" : "s have"} been let through.`}
          </p>
          {canAct && (
            <div className="mt-2 flex flex-wrap gap-2">
              <Button size="sm" onClick={() => void openLetThrough(p)}>Let a client try again</Button>
              <Button size="sm" variant="secondary" onClick={() => { setActionErr(null); setEnding(p); }}>End the pause</Button>
            </div>
          )}
        </Callout>
      ))}

      <ConfirmDialog
        open={!!letting}
        onOpenChange={(v) => !v && !busy && setLetting(null)}
        title="Let a client try Room sign-in again"
        description="Ask the client which room they typed, and pick their device. That device may sign in again now; everyone else still waits. This does not sign them in."
        confirmLabel="Let this device try again"
        requireReason
        reasonMinLength={REASON_MIN_CHARS}
        reasonMaxLength={REASON_MAX_CHARS}
        reasonPlaceholder="For example: client at reception, room confirmed"
        busy={busy}
        error={actionErr}
        onConfirm={letThrough}
      >
        {devices === null ? (
          <Skeleton className="h-24 w-full" />
        ) : devices.length === 0 ? (
          <p className="text-sm text-muted-foreground">No device has tried Room sign-in on this network recently.</p>
        ) : (
          <fieldset className="max-h-64 space-y-1 overflow-y-auto" aria-label="Devices that tried Room sign-in">
            {devices.map((d) => (
              <label key={d.device_mac} className="flex cursor-pointer items-start gap-2 rounded-md border border-border px-3 py-2 text-sm">
                <input type="radio" name="paused-device" value={d.device_mac} checked={picked === d.device_mac}
                  disabled={d.confirmed} onChange={() => setPicked(d.device_mac)} className="mt-1" />
                <span className="min-w-0">
                  <span className="font-medium">Room typed: {d.last_submitted_room_unverified || "—"}</span>
                  <span className="text-muted-foreground"> (as typed, not checked) · {d.attempts} tr{d.attempts === 1 ? "y" : "ies"} · last {at(d.last_attempt_at)}</span>
                  <span className="block font-mono text-xs text-muted-foreground">{d.device_mac}</span>
                  {d.confirmed && <Badge tone="ok">Already let through</Badge>}
                </span>
              </label>
            ))}
          </fieldset>
        )}
      </ConfirmDialog>

      <ConfirmDialog
        open={!!ending}
        onOpenChange={(v) => !v && !busy && setEnding(null)}
        title={`End the pause on ${ending?.guest_network || "this network"}?`}
        description="Use this when the wrong sign-ins were genuine clients, not guessing. Room sign-in works again for every device on this network. Nobody is signed in."
        confirmLabel="End the pause"
        requireReason
        reasonMinLength={REASON_MIN_CHARS}
        reasonMaxLength={REASON_MAX_CHARS}
        reasonPlaceholder="For example: tour group mistyping their surname"
        busy={busy}
        error={actionErr}
        onConfirm={endPause}
      />
    </>
  );
}
