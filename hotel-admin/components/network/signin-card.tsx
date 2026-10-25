"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { api } from "@/lib/api";
import { Button } from "@/components/ui/button";
import { SectionCard } from "@/components/ui/card";
import { Callout } from "@/components/ui/error-banner";
import { Field, Input, Select } from "@/components/ui/input";
import { useToast } from "@/components/ui/toast";
import { errMsg } from "@/lib/utils";
import { SwitchRow } from "@/components/network/shared";
import type { NetworkOverview } from "@/lib/api/network-overview";

type SiteMethods = {
  voucher?: { enabled?: boolean };
  email_account?: { enabled?: boolean };
  guest_account?: { enabled?: boolean };
  sms?: { enabled?: boolean };
  whatsapp?: { enabled?: boolean };
  social?: Record<string, { enabled?: boolean }>;
  pms?: { enabled?: boolean };
  open?: { enabled?: boolean };
  portal?: { remember_device_days?: number | null };
};

type Override = { disabled_methods: string[]; primary_method: string; remember_days: number | null };

export const SIGNIN_METHODS: { key: string; label: string; offered: (m: SiteMethods) => boolean }[] = [
  { key: "pms", label: "Room sign-in", offered: (m) => !!m.pms?.enabled },
  { key: "voucher", label: "Voucher", offered: (m) => !!m.voucher?.enabled },
  { key: "guest_account", label: "Staff-created account", offered: (m) => !!m.guest_account?.enabled },
  { key: "email_account", label: "Email account", offered: (m) => !!m.email_account?.enabled },
  { key: "sms", label: "Text message code", offered: (m) => !!m.sms?.enabled },
  { key: "whatsapp", label: "WhatsApp code", offered: (m) => !!m.whatsapp?.enabled },
  { key: "social", label: "Social sign-in", offered: (m) => Object.values(m.social ?? {}).some((p) => p?.enabled) },
  { key: "open", label: "Choose a package without signing in", offered: (m) => !!m.open?.enabled },
];

/** A network can only narrow what the site offers: it cannot switch on a method the site, the licence or the deployment does not. */
export function SignInCard({ networkId, writable }: { networkId: string; writable: boolean }) {
  const toast = useToast();
  const [site, setSite] = useState<SiteMethods | null>(null);
  const [saved, setSaved] = useState<Override | null>(null);
  const [off, setOff] = useState<string[]>([]);
  const [primary, setPrimary] = useState("");
  const [remember, setRemember] = useState("");
  const [rooms, setRooms] = useState<string[] | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    Promise.all([
      api.get<SiteMethods>("/auth-methods"),
      api.get<Override>(`/network/guest-networks/${networkId}/sign-in`),
    ]).then(([m, o]) => {
      if (!live) return;
      setSite(m ?? {});
      setSaved(o);
      setOff(o.disabled_methods ?? []);
      setPrimary(o.primary_method ?? "");
      setRemember(o.remember_days == null ? "" : String(o.remember_days));
    }).catch(() => { if (live) setSite(null); });
    api.get<NetworkOverview>("/network/overview")
      .then((ov) => { if (live) setRooms(ov.networks.find((n) => n.id === networkId)?.pms_interfaces ?? []); })
      .catch(() => { if (live) setRooms(null); });
    return () => { live = false; };
  }, [networkId]);

  if (!site || !saved) return null;
  const offered = SIGNIN_METHODS.filter((m) => m.offered(site));
  const siteDays = site.portal?.remember_device_days;
  const rememberNum = remember.trim() === "" ? null : Number(remember);
  const invalid = rememberNum !== null && (!Number.isInteger(rememberNum) || rememberNum < 0 || rememberNum > 365);
  const dirty = JSON.stringify([[...off].sort(), primary, rememberNum]) !==
    JSON.stringify([[...saved.disabled_methods].sort(), saved.primary_method ?? "", saved.remember_days ?? null]);

  async function save() {
    setBusy(true);
    setErr(null);
    try {
      const o = await api.put<Override>(`/network/guest-networks/${networkId}/sign-in`,
        { disabled_methods: off, primary_method: primary, remember_days: rememberNum });
      setSaved(o);
      toast.success("Sign-in options saved", "Clients on this network see them straight away.");
    } catch (e) {
      setErr(errMsg(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <SectionCard
      title="Sign-in on this network"
      description="Start from what the site offers and switch methods off here. A network cannot offer a method the site does not."
      bodyClassName="space-y-4"
      footer={writable ? <Button disabled={!dirty || invalid || busy} onClick={() => void save()}>{busy ? "Saving…" : "Save sign-in options"}</Button> : undefined}
    >
      {offered.length === 0 && <p className="text-sm text-muted-foreground">The site offers no sign-in methods yet. Set them up under Sign-in methods.</p>}
      {offered.map((m) => (
        <SwitchRow key={m.key} label={m.label} checked={!off.includes(m.key)} disabled={!writable}
          hint={m.key === "pms" && rooms !== null
            ? (rooms.length ? <>Checks rooms on {rooms.join(", ")}. <Link className="underline" href="/pms-routing">Change PMS routing</Link></>
              : <>No PMS interface is routed to this network, so room sign-in is not available here. <Link className="underline" href="/pms-routing">PMS routing</Link></>)
            : undefined}
          onChange={(v) => { setOff(v ? off.filter((k) => k !== m.key) : [...off, m.key]); if (!v && primary === m.key) setPrimary(""); }} />
      ))}
      {offered.length > 0 && (
        <>
          <Field label="Method shown first" hint="Clients see this one before the others.">
            <Select value={primary} disabled={!writable} onChange={(e) => setPrimary(e.target.value)}>
              <option value="">Same as the site</option>
              {offered.filter((m) => !off.includes(m.key)).map((m) => <option key={m.key} value={m.key}>{m.label}</option>)}
            </Select>
          </Field>
          <Field label="Remember devices for (days)" optional error={invalid ? "Use a whole number from 0 to 365." : undefined}
            hint={`Blank follows the site${siteDays != null ? ` (${siteDays} days)` : ""}. A number here can only be shorter than the site's.`}>
            <Input inputMode="numeric" value={remember} disabled={!writable} onChange={(e) => setRemember(e.target.value)} />
          </Field>
        </>
      )}
      {err && <Callout tone="warning" title="Could not save">{err}</Callout>}
    </SectionCard>
  );
}
