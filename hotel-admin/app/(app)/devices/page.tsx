"use client";

// The search text travels in a header, not the URL: it is a MAC address or account name, and edged logs request lines.

import { usePmsConnections } from "@/lib/pms-scope";
import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, type ClientDeviceRow, type DeviceSettings } from "@/lib/api";
import { canRead, canWrite } from "@/lib/roles";
import { useOperatorRoles } from "@/lib/whoami-context";
import { PageShell, PageHeader } from "@/components/ui/page";
import { FilterBar } from "@/components/ui/toolbar";
import { RowActions } from "@/components/ui/row-actions";
import { HelpList, HelpSection } from "@/components/help";
import { Card, CardFooter } from "@/components/ui/card";
import { Table, TBody, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { Field, Input, QuietHints } from "@/components/ui/input";
import { ConfirmDialog, Dialog, DialogBody, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { InfoHint } from "@/components/ui/quiet";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { ResultsCount, SearchInput } from "@/components/ui/data";
import { SkeletonRows } from "@/components/ui/misc";
import { LiveStatus, refreshingClass } from "@/components/ui/patterns";
import { usePoll } from "@/lib/use-poll";
import { cn } from "@/lib/utils";
import { useToast } from "@/components/ui/toast";
import { AlertTriangle, RefreshCw, Settings2, Smartphone } from "lucide-react";
import { formatRelative } from "@/lib/utils";
import { DeviceDetailDialog } from "@/components/devices/device-detail";
import { SIGNIN_SETTING_GROUPS, SigninSettingsGroup } from "@/components/devices/signin-limits-card";

// Online means signed in with a live session; a device on Wi-Fi without one is not "Offline".
function DeviceStatus({ r }: { r: ClientDeviceRow }) {
  if (r.online) return <StatusBadge tone="ok">Online</StatusBadge>;
  if (r.on_network) return <StatusBadge tone="warn">On the Wi-Fi, not signed in</StatusBadge>;
  return <StatusBadge>Not connected</StatusBadge>;
}

function holder(r: ClientDeviceRow, multi = false): string {
  const p = r.current;
  if (!p) return "—";
  switch (p.subject_kind) {
    case "ACCOUNT": return p.subject_label ?? "Account";
    case "VOUCHER": return "Voucher";
    case "ROOM": return p.subject_label ? `Room ${p.subject_label}${multi && p.pms_interface_label ? ` · ${p.pms_interface_label}` : ""}` : "Room";
    case "CLIENT": return "Client sign-in";
    default: return "Open selection";
  }
}

export default function DevicesPage() {
  const { multi } = usePmsConnections();
  const roles = useOperatorRoles();
  const mayWrite = roles === null ? false : canWrite("sessions", roles);
  const mayConfigure = roles === null ? false : canWrite("auth-methods", roles);
  // A role that cannot read this setting is not shown the card (it would open on a 403).
  const maySeeSettings = roles === null ? false : canRead("auth-methods", roles);
  const [q, setQ] = useState("");
  const [page, setPage] = useState(1);
  const [rows, setRows] = useState<ClientDeviceRow[] | null>(null);
  const [more, setMore] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [open, setOpen] = useState<string | null>(null);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [loading, setLoading] = useState(false);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);
  const [refreshFailed, setRefreshFailed] = useState(false);

  useEffect(() => { setPage(1); }, [q]);
  const latest = useRef(0);
  const hasRows = useRef(false);
  const load = useCallback(async () => {
    const mine = ++latest.current;
    setErr(null);
    setLoading(true);
    try {
      const needle = q.trim();
      const r = await api.get<{ data: ClientDeviceRow[]; meta: { has_more: boolean } }>(
        `/client-devices?page=${page}`,
        needle ? { "X-Device-Search": encodeURIComponent(needle) } : undefined,
      );
      if (mine !== latest.current) return;
      setRows(r?.data ?? []); setMore(!!r?.meta?.has_more);
      setUpdatedAt(Date.now());
      setRefreshFailed(false);
      hasRows.current = true;
    } catch (e) {
      if (mine !== latest.current) return;
      if (hasRows.current) setRefreshFailed(true);
      else setErr(e);
    } finally {
      if (mine === latest.current) setLoading(false);
    }
  }, [q, page]);
  useEffect(() => { void load(); }, [load]);
  usePoll(() => load(), 30_000, { enabled: open === null && !settingsOpen && !err });

  return (
    <PageShell width="wide">
      <PageHeader
        title="Devices"
        description="Every device that has used the Wi-Fi, and the package places it holds."
        meta={rows !== null ? (
          <LiveStatus updatedAt={updatedAt} refreshing={loading} error={refreshFailed} onRefresh={() => void load()} intervalSeconds={30} />
        ) : undefined}
        actions={maySeeSettings ? (
          <Button variant="secondary" onClick={() => setSettingsOpen(true)}>
            <Settings2 /> Device settings
          </Button>
        ) : undefined}
        help={
          <>
            <HelpSection title="Places on a package">
              <p>
                A package allows a number of devices. Each device that has used it holds a place, and keeps it after
                signing out — otherwise one package could be passed from device to device. Clients can replace one of
                their devices themselves on the portal, a limited number of times a day.
              </p>
            </HelpSection>
            <HelpSection title="What each action does">
              <HelpList items={[
                "Online: signed in, with Internet. On the Wi-Fi, not signed in: the device has an address from the Wi-Fi and is at the sign-in page (or has not opened it). Not connected: neither.",
                "Disconnect ends the device's sessions. It keeps its places and can sign in again.",
                "Release place frees the device's place on one package, so another device can join. The package's time, data and owner do not change.",
                "Require fresh sign-in forgets the device's remembered sign-in, so it sees the full sign-in page.",
                "Prepare for a sign-in test does all three. It deletes nothing and resets no quota: a free package that may be taken once per device is still refused on a device that has had it.",
              ]} />
            </HelpSection>
          </>
        }
      />

      <Card className="overflow-hidden">
        <FilterBar
          inset
          search={<SearchInput value={q} onChange={setQ} delay={300} placeholder="Search MAC, address or account…" label="Search devices" />}
          actions={<ResultsCount count={rows?.length} noun="device" more={more} />}
        />
        {err && rows === null ? (
          <EmptyState
            icon={<AlertTriangle />}
            title="The devices could not be loaded"
            hint={err instanceof Error ? err.message : undefined}
            action={<Button variant="secondary" disabled={loading} onClick={() => void load()}><RefreshCw /> Try again</Button>}
          />
        ) : rows === null ? (
          <SkeletonRows rows={5} />
        ) : rows.length === 0 ? (
          <EmptyState size="sm" icon={<Smartphone />} title={q ? "No device matches" : "No devices yet"} hint={q ? "Try part of the MAC address, the address or the account name." : "Devices appear here once they have used the Wi-Fi."}
            action={q ? <Button variant="secondary" size="sm" onClick={() => setQ("")}>Clear search</Button> : undefined} />
        ) : (
          <div className={cn(loading && refreshingClass)}>
          <Table label="Devices">
            <THead>
              <TR>
                <TH>Device</TH>
                <TH>Status</TH>
                <TH className="hidden md:table-cell">Package</TH>
                <TH className="hidden md:table-cell">Held by</TH>
                <TH className="hidden 2xl:table-cell">Last seen</TH>
                <TH><span className="sr-only">Actions</span></TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((r) => {
                const where = [r.hostname, r.online_ip || r.lease_ip || r.last_ip].filter(Boolean).join(" · ");
                const pkg = r.current ? r.current.package_name || "Package" : "";
                const held = holder(r, multi);
                return (
                <TR key={r.id || r.mac} onClick={r.id ? () => setOpen(r.id) : undefined}>
                  <TD nowrap className="max-w-[13rem]">
                    <div className="font-mono text-sm">{r.mac}</div>
                    <div className="truncate text-caption text-muted-foreground" title={where || undefined}>{where}</div>
                    {r.current && (
                      <div className="truncate text-caption text-muted-foreground md:hidden" title={`${pkg} · ${held}`}>
                        {pkg} · {held}
                      </div>
                    )}
                  </TD>
                  <TD nowrap>
                    <DeviceStatus r={r} />
                    {!r.id && <div className="mt-1 text-caption text-muted-foreground">Has not signed in yet</div>}
                    {r.current && (
                      <div className="mt-1">
                        <Badge tone={r.current.slot === "AUTHORIZED" ? "info" : "neutral"}>
                          {r.current.slot === "AUTHORIZED" ? "Holds a place" : "Place released"}
                        </Badge>
                      </div>
                    )}
                    {r.last_seen && (
                      <div className="mt-1 text-caption text-muted-foreground 2xl:hidden">Seen {formatRelative(r.last_seen)}</div>
                    )}
                  </TD>
                  <TD className="hidden max-w-[12rem] md:table-cell">
                    {r.current ? (
                      <>
                        <div className="truncate" title={pkg}>{pkg}</div>
                        {r.current.max_devices ? (
                          <div className="tabular text-caption text-muted-foreground">
                            {r.current.places_in_use}/{r.current.max_devices} places
                          </div>
                        ) : null}
                      </>
                    ) : "—"}
                  </TD>
                  <TD truncate title={held} className="hidden max-w-[12rem] md:table-cell">{held}</TD>
                  <TD nowrap muted className="hidden 2xl:table-cell">{r.last_seen ? formatRelative(r.last_seen) : "—"}</TD>
                  <TD className="w-0">
                    {r.id && <RowActions subject={r.mac} primary={{ label: "Manage", onSelect: () => setOpen(r.id) }} />}
                  </TD>
                </TR>
                );
              })}
            </TBody>
          </Table>
          </div>
        )}
        {rows && rows.length > 0 && (page > 1 || more) && (
          <CardFooter className="justify-end">
            <span className="me-auto text-caption text-muted-foreground tabular" aria-live="polite">Page {page}</span>
            <Button size="sm" variant="secondary" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>Previous</Button>
            <Button size="sm" variant="secondary" disabled={!more} onClick={() => setPage((p) => p + 1)}>Next</Button>
          </CardFooter>
        )}
      </Card>

      <DeviceDetailDialog deviceId={open} mayWrite={mayWrite} onClose={() => setOpen(null)} onChanged={() => void load()} />

      {maySeeSettings && (
        <Dialog open={settingsOpen} onOpenChange={setSettingsOpen}>
          <DialogContent size="lg">
            <DialogHeader>
              <DialogTitle>Device settings</DialogTitle>
              <DialogDescription className="sr-only">How often clients may change their devices, and how long unused devices are kept.</DialogDescription>
            </DialogHeader>
            <DialogBody className="space-y-5 divide-y divide-border [&>*+*]:pt-5">
              <DeviceChangesSetting canEdit={mayConfigure} />
              <SigninSettingsGroup bare title="Device retention" keys={SIGNIN_SETTING_GROUPS.devices} canEdit={mayConfigure}
                hint="Devices that have not been seen for this long are removed from the list." />
            </DialogBody>
          </DialogContent>
        </Dialog>
      )}
    </PageShell>
  );
}

/**
 * How many times a day a client may replace or remove a device on a package from the portal. Confirm-only by the Product
 * Owner's decision of 2026-11-02: Save opens a warning with the old and new values; Confirm applies it. No reason or
 * password is asked; the appliance records the administrator, the old and new values and the time, and the role check
 * (auth-methods write) is unchanged.
 */
function DeviceChangesSetting({ canEdit }: { canEdit: boolean }) {
  const toast = useToast();
  const [value, setValue] = useState<number | null>(null);
  const [unavailable, setUnavailable] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [editing, setEditing] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [err, setErr] = useState<unknown>(null);
  const [saveErr, setSaveErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    setErr(null);
    setUnavailable(null);
    try {
      const r = await api.get<{ settings?: DeviceSettings; awaiting_assignment?: boolean; reason?: string }>("/device-settings/");
      const v = r?.settings?.client_device_changes_per_day;
      if (typeof v === "number") setValue(v);
      else setUnavailable(r?.reason || "This appliance did not report the setting.");
    } catch (e) { setErr(e); }
  }, []);
  useEffect(() => { void load(); }, [load]);

  async function confirm() {
    setBusy(true); setSaveErr(null);
    try {
      await api.put("/device-settings/", { client_device_changes_per_day: Number(draft) });
      toast.success("Daily device changes saved.");
      setConfirming(false); setEditing(false);
      await load();
    } catch (e) {
      setSaveErr(e instanceof ApiError && e.code === "reauth_required" ? new Error("Your session needs you to sign in again.") : e);
    } finally { setBusy(false); }
  }

  const n = Number(draft);
  const inRange = draft.trim() !== "" && Number.isInteger(n) && n >= 1 && n <= 10;
  const valid = inRange && n !== value;
  const whyNot = !inRange ? "Enter a whole number from 1 to 10." : n === value ? "That is the current value." : null;
  return (
    <QuietHints>
      <div className="space-y-3" data-testid="device-changes-setting">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 items-center gap-1">
            <span className="text-emphasis">Daily device changes</span>
            <InfoHint>
              How many times a day a client may replace or remove a device on a package from the portal. Default 3; from 1 to 10.
            </InfoHint>
          </div>
          <div className="flex items-center gap-2">
            {value !== null && <Badge tone="neutral">{`${value} per day`}</Badge>}
            {canEdit && !editing && value !== null && (
              <Button size="sm" variant="secondary" onClick={() => { setDraft(String(value ?? 3)); setEditing(true); }}>Change</Button>
            )}
          </div>
        </div>
        {unavailable && <p className="text-caption text-muted-foreground">{unavailable}</p>}
        {err !== null && (
          <div className="flex flex-wrap items-center gap-2">
            <ErrorBanner err={err} className="mb-0 flex-1" />
            <Button size="sm" variant="secondary" onClick={() => void load()}><RefreshCw /> Try again</Button>
          </div>
        )}
        {editing && (
          <form
            className="flex flex-wrap items-end gap-3 animate-in fade-in-0 duration-base ease-onegate motion-reduce:animate-none"
            onSubmit={(e) => { e.preventDefault(); if (valid && !busy) { setSaveErr(null); setConfirming(true); } }}
          >
            <Field label="Changes per day" hint="1 to 10" className="w-40">
              <Input type="number" min={1} max={10} value={draft} onChange={(e) => setDraft(e.target.value)} aria-describedby={whyNot ? "device-changes-why" : undefined} />
            </Field>
            <div className="ms-auto flex flex-wrap items-center justify-end gap-2">
              {whyNot && <span id="device-changes-why" className="text-caption text-muted-foreground">{whyNot}</span>}
              <Button type="button" size="sm" variant="secondary" disabled={busy} onClick={() => setEditing(false)}>Cancel</Button>
              <Button type="submit" size="sm" variant="primary" disabled={busy || !valid}>Save</Button>
            </div>
          </form>
        )}
        <ConfirmDialog
          open={confirming}
          onOpenChange={(v) => !busy && setConfirming(v)}
          title="Change the daily device limit?"
          description={`Every client will be able to replace or remove ${n} device${n === 1 ? "" : "s"} a day from the portal (now ${value ?? "—"}). It applies immediately and is recorded with your name.`}
          confirmLabel="Confirm"
          busy={busy}
          error={saveErr}
          onConfirm={confirm}
        />
      </div>
    </QuietHints>
  );
}
