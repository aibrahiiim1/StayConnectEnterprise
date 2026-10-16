"use client";

// DEVICES: find a client device -- online or not -- and act on it (edged /client-devices, migration 0118).
// The search text travels in a header, not the URL (it is a MAC address or a client's account name, and edged logs
// request lines).

import { useCallback, useEffect, useRef, useState } from "react";
import { api, ApiError, type ClientDeviceRow, type DeviceSettings } from "@/lib/api";
import { canRead, canWrite } from "@/lib/roles";
import { useOperatorRoles } from "@/lib/whoami-context";
import { PageShell, PageHeader, Toolbar } from "@/components/ui/page";
import { HelpList, HelpSection } from "@/components/help";
import { Card, CardBody } from "@/components/ui/card";
import { Table, TBody, THead, TR, TH, TD } from "@/components/ui/table";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Field, Input } from "@/components/ui/input";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorBanner } from "@/components/ui/error-banner";
import { SearchInput } from "@/components/ui/data";
import { SkeletonRows } from "@/components/ui/misc";
import { useToast } from "@/components/ui/toast";
import { Smartphone, Settings2 } from "lucide-react";
import { formatRelative } from "@/lib/utils";
import { DeviceDetailDialog } from "@/components/devices/device-detail";
import { REASON_HINT, REASON_MAX_CHARS, reasonOk } from "@/lib/reason";

// THREE STATES, NOT TWO. "Online" is signed in with a live Internet session. A device can be on the Wi-Fi --
// holding a DHCP lease, sitting at the sign-in page -- without that, and used to read as "Offline".
function DeviceStatus({ r }: { r: ClientDeviceRow }) {
  if (r.online) return <Badge tone="ok" dot>Online</Badge>;
  if (r.on_network) return <Badge tone="warn" dot>On the Wi-Fi, not signed in</Badge>;
  return <Badge tone="neutral" dot>Not connected</Badge>;
}

function holder(r: ClientDeviceRow): string {
  const p = r.current;
  if (!p) return "—";
  switch (p.subject_kind) {
    case "ACCOUNT": return p.subject_label ?? "Account";
    case "VOUCHER": return "Voucher";
    case "ROOM": return p.subject_label ? `Room ${p.subject_label}` : "Room";
    case "CLIENT": return "Client sign-in";
    default: return "Open selection";
  }
}

export default function DevicesPage() {
  const roles = useOperatorRoles();
  const mayWrite = roles === null ? false : canWrite("sessions", roles);
  const mayConfigure = roles === null ? false : canWrite("auth-methods", roles);
  // The daily device-change limit is an auth-methods setting. A role that cannot read it (the voucher station, the
  // payments role) is not shown the card at all -- it used to be, and the page opened on a 403 banner.
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
        eyebrow="Clients"
        title="Devices"
        icon={<Smartphone />}
        description="Every device that has used the Wi-Fi: what it holds, and what you can do about it."
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

      <Card className="overflow-hidden">
        <CardBody className="border-b border-border py-3">
          <Toolbar>
            <SearchInput value={q} onChange={setQ} delay={300} placeholder="Search MAC, address or account…" label="Search devices" />
          </Toolbar>
        </CardBody>
        <ErrorBanner err={err} />
        {rows === null ? (
          <CardBody><SkeletonRows rows={5} /></CardBody>
        ) : rows.length === 0 ? (
          <EmptyState title={q ? "No device matches" : "No devices yet"} hint={q ? "Try part of the MAC address, the address or the account name." : "Devices appear here once they have used the Wi-Fi."} />
        ) : (
          <Table>
            <THead>
              <TR>
                <TH>Device</TH>
                <TH>Status</TH>
                <TH className="hidden md:table-cell">Package</TH>
                <TH className="hidden md:table-cell">Held by</TH>
                <TH className="hidden lg:table-cell">Last seen</TH>
                <TH><span className="sr-only">Actions</span></TH>
              </TR>
            </THead>
            <TBody>
              {rows.map((r) => (
                <TR key={r.id || r.mac}>
                  <TD>
                    <div className="font-mono text-sm">{r.mac}</div>
                    <div className="text-caption text-muted-foreground">
                      {[r.hostname, r.online_ip || r.lease_ip || r.last_ip].filter(Boolean).join(" · ")}
                    </div>
                    {/* The package on narrow screens, where its column is hidden. */}
                    {r.current && (
                      <div className="text-caption text-muted-foreground md:hidden">
                        {r.current.package_name || "Package"} · {holder(r)}
                      </div>
                    )}
                  </TD>
                  <TD>
                    <DeviceStatus r={r} />
                    {!r.id && <div className="mt-1 text-caption text-muted-foreground">Has not signed in yet</div>}
                    {r.current && (
                      <div className="mt-1">
                        <Badge tone={r.current.slot === "AUTHORIZED" ? "info" : "neutral"}>
                          {r.current.slot === "AUTHORIZED" ? "Holds a place" : "Place released"}
                        </Badge>
                      </div>
                    )}
                  </TD>
                  <TD className="hidden text-sm md:table-cell">
                    {r.current ? `${r.current.package_name || "Package"}${r.current.max_devices ? ` · ${r.current.places_in_use}/${r.current.max_devices}` : ""}` : "—"}
                  </TD>
                  <TD className="hidden text-sm md:table-cell">{holder(r)}</TD>
                  <TD className="hidden text-sm text-muted-foreground lg:table-cell">{r.last_seen ? formatRelative(r.last_seen) : "—"}</TD>
                  <TD className="text-end">
                    {r.id && <Button size="sm" variant="ghost" onClick={() => setOpen(r.id)}><Settings2 /> Manage</Button>}
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
        )}
        {rows && rows.length > 0 && (page > 1 || more) && (
          <CardBody className="flex justify-end gap-2 border-t border-border py-3">
            <Button size="sm" variant="outline" disabled={page <= 1} onClick={() => setPage((p) => p - 1)}>Previous</Button>
            <Button size="sm" variant="outline" disabled={!more} onClick={() => setPage((p) => p + 1)}>Next</Button>
          </CardBody>
        )}
      </Card>

      <DeviceDetailDialog deviceId={open} mayWrite={mayWrite} onClose={() => setOpen(null)} onChanged={() => void load()} />
    </PageShell>
  );
}

// The site setting: how many device changes a client may make on the portal per package per day.
function DeviceChangesCard({ canEdit }: { canEdit: boolean }) {
  const toast = useToast();
  const [value, setValue] = useState<number | null>(null);
  const [draft, setDraft] = useState("");
  const [reason, setReason] = useState("");
  const [password, setPassword] = useState("");
  const [editing, setEditing] = useState(false);
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
              How many times a day a client may replace or remove one of their devices on a package from the portal.
              Default 3; from 1 to 10. It limits passing one package around between devices.
            </p>
          </div>
          <div className="flex items-center gap-2">
            <Badge tone="neutral">{value === null ? "—" : `${value} per day`}</Badge>
            {canEdit && !editing && (
              <Button size="sm" variant="outline" onClick={() => { setDraft(String(value ?? 3)); setEditing(true); }}>Change</Button>
            )}
          </div>
        </div>
        <ErrorBanner err={err} />
        {editing && (
          <div className="grid gap-3 sm:grid-cols-3">
            <Field label="Changes per day" hint="1 to 10">
              <Input type="number" min={1} max={10} value={draft} onChange={(e) => setDraft(e.target.value)} />
            </Field>
            <Field label="Reason" required hint={REASON_HINT}>
              <Input value={reason} onChange={(e) => setReason(e.target.value)} maxLength={REASON_MAX_CHARS} />
            </Field>
            <Field label="Your password" required hint="Confirms the change.">
              <Input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
            </Field>
            <div className="flex gap-2 sm:col-span-3">
              <Button size="sm" variant="primary" disabled={busy || !valid} onClick={() => void save()}>{busy ? "Saving…" : "Save"}</Button>
              <Button size="sm" variant="ghost" disabled={busy} onClick={() => setEditing(false)}>Cancel</Button>
            </div>
          </div>
        )}
      </CardBody>
    </Card>
  );
}
