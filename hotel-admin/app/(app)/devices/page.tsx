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
import { Card, CardBody, CardFooter } from "@/components/ui/card";
import { Table, TBody, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge, StatusBadge } from "@/components/ui/badge";
import { Field, Input } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { ResultsCount, SearchInput } from "@/components/ui/data";
import { SkeletonRows } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { Smartphone } from "lucide-react";
import { formatRelative } from "@/lib/utils";
import { DeviceDetailDialog } from "@/components/devices/device-detail";
import { SigninLimitsCard } from "@/components/devices/signin-limits-card";
import { REASON_HINT, REASON_MAX_CHARS, reasonOk } from "@/lib/reason";
import { useEditFocus } from "@/lib/use-edit-focus";

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

  useEffect(() => { setPage(1); }, [q]);
  const latest = useRef(0);
  const load = useCallback(async () => {
    const mine = ++latest.current;
    setErr(null);
    try {
      const needle = q.trim();
      const r = await api.get<{ data: ClientDeviceRow[]; meta: { has_more: boolean } }>(
        `/client-devices?page=${page}`,
        needle ? { "X-Device-Search": encodeURIComponent(needle) } : undefined,
      );
      if (mine !== latest.current) return;
      setRows(r?.data ?? []); setMore(!!r?.meta?.has_more);
    } catch (e) {
      if (mine !== latest.current) return;
      setErr(e); setRows([]);
    }
  }, [q, page]);
  useEffect(() => { void load(); }, [load]);

  return (
    <PageShell width="wide">
      <PageHeader
        title="Devices"
        description="Every device that has used the Wi-Fi, and the package places it holds."
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

      {maySeeSettings && <DeviceChangesCard canEdit={mayConfigure} />}
      {maySeeSettings && <SigninLimitsCard canEdit={mayConfigure} />}

      <Card className="overflow-hidden">
        <FilterBar
          inset
          search={<SearchInput value={q} onChange={setQ} delay={300} placeholder="Search MAC, address or account…" label="Search devices" />}
          actions={<ResultsCount count={rows?.length} noun="device" more={more} />}
        />
        <ErrorBanner err={err} className="mx-4 mt-3" />
        {rows === null ? (
          <SkeletonRows rows={5} />
        ) : rows.length === 0 ? (
          <EmptyState size="sm" icon={<Smartphone />} title={q ? "No device matches" : "No devices yet"} hint={q ? "Try part of the MAC address, the address or the account name." : "Devices appear here once they have used the Wi-Fi."} />
        ) : (
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
                    {/* The Last seen column needs the room of a 2xl screen; below it the time sits here. */}
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
        )}
        {rows && rows.length > 0 && (page > 1 || more) && (
          <CardFooter className="justify-end">
            <Button size="sm" variant="secondary" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>Previous</Button>
            <Button size="sm" variant="secondary" disabled={!more} onClick={() => setPage((p) => p + 1)}>Next</Button>
          </CardFooter>
        )}
      </Card>

      <DeviceDetailDialog deviceId={open} mayWrite={mayWrite} onClose={() => setOpen(null)} onChanged={() => void load()} />
    </PageShell>
  );
}

function DeviceChangesCard({ canEdit }: { canEdit: boolean }) {
  const toast = useToast();
  const [value, setValue] = useState<number | null>(null);
  const [draft, setDraft] = useState("");
  const [reason, setReason] = useState("");
  const [password, setPassword] = useState("");
  const [editing, setEditing] = useState(false);
  const { triggerRef, panelRef } = useEditFocus(editing);
  const [err, setErr] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(async () => {
    try {
      const r = await api.get<{ settings: DeviceSettings }>("/device-settings/");
      setValue(r.settings.client_device_changes_per_day);
    } catch (e) { setErr(e); }
  }, []);
  useEffect(() => { void load(); }, [load]);

  async function save() {
    setBusy(true); setErr(null);
    try {
      await api.put("/device-settings/", { client_device_changes_per_day: Number(draft), reason: reason.trim(), password });
      toast.success("Saved.");
      setEditing(false); setPassword(""); setReason("");
      await load();
    } catch (e) {
      setErr(e instanceof ApiError && e.code === "reauth_required" ? new Error("Your password was not accepted.") : e);
    } finally { setBusy(false); }
  }

  const n = Number(draft);
  const valid = Number.isInteger(n) && n >= 1 && n <= 10 && reasonOk(reason) && password !== "";
  return (
    <Card>
      <CardBody className="space-y-3">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="min-w-0 space-y-1">
            <div className="text-emphasis">Device changes a client may make per day</div>
            <p className="text-caption text-muted-foreground">
              Replacing or removing a device on a package from the portal. Default 3; from 1 to 10.
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Badge tone="neutral">{value === null ? "—" : `${value} per day`}</Badge>
            {canEdit && !editing && (
              <Button ref={triggerRef} size="sm" variant="secondary" onClick={() => { setDraft(String(value ?? 3)); setEditing(true); }}>Change</Button>
            )}
          </div>
        </div>
        <ErrorBanner err={err} />
        {editing && (
          <div ref={panelRef} className="grid gap-3 sm:grid-cols-3">
            <Field label="Changes per day" hint="1 to 10">
              <Input type="number" min={1} max={10} value={draft} onChange={(e) => setDraft(e.target.value)} />
            </Field>
            <Field label="Reason" required hint={REASON_HINT}>
              <Input value={reason} onChange={(e) => setReason(e.target.value)} maxLength={REASON_MAX_CHARS} />
            </Field>
            <Field label="Your password" required hint="Confirms the change.">
              <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
            </Field>
            <div className="flex justify-end gap-2 sm:col-span-3">
              <Button size="sm" variant="secondary" disabled={busy} onClick={() => setEditing(false)}>Cancel</Button>
              <Button size="sm" variant="primary" disabled={busy || !valid} onClick={() => void save()}>{busy ? "Saving…" : "Save"}</Button>
            </div>
          </div>
        )}
      </CardBody>
    </Card>
  );
}
