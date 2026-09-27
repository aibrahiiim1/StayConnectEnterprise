"use client";

import { useEffect, useMemo, useState } from "react";
import { api, itemsOf, qs, type Customer, type Items, type Site } from "@/lib/api";
import { Field, Input, Select } from "@/components/ui/input";

const NEW = "__new";

/** Where a waiting appliance goes on activation: an existing customer and site, or new ones created in the same step. */
export type Placement = {
  customerId: string;       // "" | id | NEW
  newCustomerName: string;
  siteId: string;           // "" | id | NEW
  newSiteName: string;
  newSiteTimezone: string;
  newSiteCountry: string;
};

export function browserTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

export function emptyPlacement(customerId = "", siteId = ""): Placement {
  return {
    customerId,
    newCustomerName: "",
    siteId,
    newSiteName: "",
    newSiteTimezone: browserTimezone(),
    newSiteCountry: "",
  };
}

const isNewCustomer = (p: Placement) => p.customerId === NEW;
const isNewSite = (p: Placement) => p.siteId === NEW || p.customerId === NEW;

export function placementProblem(p: Placement): string | null {
  if (!p.customerId) return "Choose a customer.";
  if (isNewCustomer(p) && !p.newCustomerName.trim()) return "Enter the new customer's name.";
  if (!isNewSite(p) && !p.siteId) return "Choose a site.";
  if (isNewSite(p)) {
    if (!p.newSiteName.trim()) return "Enter the new site's name.";
    if (!p.newSiteTimezone.trim()) return "Choose the site's time zone.";
    if (p.newSiteCountry && !/^[A-Za-z]{2}$/.test(p.newSiteCountry.trim())) return "Country is a two-letter code, such as EG.";
  }
  return null;
}

/** The customer/site half of the §6 activate body. */
export function placementBody(p: Placement): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  if (isNewCustomer(p)) out.new_customer = { name: p.newCustomerName.trim() };
  else out.customer_id = p.customerId;
  if (isNewSite(p)) {
    const site: Record<string, string> = { name: p.newSiteName.trim(), timezone: p.newSiteTimezone.trim() };
    if (p.newSiteCountry.trim()) site.country = p.newSiteCountry.trim().toUpperCase();
    out.new_site = site;
  } else out.site_id = p.siteId;
  return out;
}

function timezones(): string[] {
  try {
    const f = (Intl as unknown as { supportedValuesOf?: (k: string) => string[] }).supportedValuesOf;
    return f ? f("timeZone") : [];
  } catch {
    return [];
  }
}

/** A site's time zone: the browser's list when it has one, free text on an older browser. */
export function TimezoneSelect({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const zones = useMemo(timezones, []);
  if (zones.length === 0) return <Input value={value} onChange={(e) => onChange(e.target.value)} />;
  return (
    <Select value={value} onChange={(e) => onChange(e.target.value)}>
      {!zones.includes(value) && <option value={value}>{value}</option>}
      {zones.map((z) => <option key={z} value={z}>{z}</option>)}
    </Select>
  );
}

/** Activate's customer and site. Move stays within the customer and picks its site itself (appliance-dialogs.tsx).
 *  `lockedCustomer` pins the customer (an appliance that still holds that customer's data): the customer cannot
 *  be changed and no new customer can be created; only the site is chosen. */
export function PlacementFields({
  value,
  onChange,
  lockedCustomer,
}: {
  value: Placement;
  onChange: (next: Placement) => void;
  lockedCustomer?: { id: string; name: string };
}) {
  const [customers, setCustomers] = useState<Customer[] | null>(null);
  const [sites, setSites] = useState<Site[] | null>(null);
  const set = (patch: Partial<Placement>) => onChange({ ...value, ...patch });

  useEffect(() => {
    api.get<Items<Customer>>(`/cloud/v1/customers${qs({ status: "active" })}`)
      .then((r) => setCustomers(itemsOf(r)))
      .catch(() => setCustomers([]));
  }, []);

  const cid = value.customerId;
  useEffect(() => {
    setSites(null);
    if (!cid || cid === NEW) return;
    api.get<Items<Site>>(`/cloud/v1/customers/${cid}/sites`)
      .then((r) => setSites(itemsOf(r).filter((s) => s.status !== "archived")))
      .catch(() => setSites([]));
  }, [cid]);

  const newCustomer = isNewCustomer(value);
  const newSite = isNewSite(value);

  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Field label="Customer" required>
        {lockedCustomer ? (
          <Select value={lockedCustomer.id} disabled onChange={() => undefined}>
            <option value={lockedCustomer.id}>{lockedCustomer.name}</option>
          </Select>
        ) : (
          <Select
            value={value.customerId}
            disabled={customers === null}
            onChange={(e) => set({ customerId: e.target.value, siteId: e.target.value === NEW ? NEW : "" })}
          >
            <option value="">{customers === null ? "Loading…" : "Choose a customer…"}</option>
            {(customers ?? []).map((c) => (
              <option key={c.id} value={c.id}>{c.name}</option>
            ))}
            <option value={NEW}>New customer…</option>
          </Select>
        )}
      </Field>

      {newCustomer ? (
        <Field label="New customer's name" required>
          <Input value={value.newCustomerName} onChange={(e) => set({ newCustomerName: e.target.value })} autoComplete="off" />
        </Field>
      ) : (
        <Field label="Site" required>
          <Select
            value={value.siteId}
            disabled={!value.customerId || sites === null}
            onChange={(e) => set({ siteId: e.target.value })}
          >
            <option value="">{!value.customerId ? "Choose a customer first" : sites === null ? "Loading…" : "Choose a site…"}</option>
            {(sites ?? []).map((s) => (
              <option key={s.id} value={s.id}>{s.name}</option>
            ))}
            {value.customerId && <option value={NEW}>New site…</option>}
          </Select>
        </Field>
      )}

      {newSite && (
        <>
          <Field label="New site's name" required hint="One physical location, such as a hotel, office or campus.">
            <Input value={value.newSiteName} onChange={(e) => set({ newSiteName: e.target.value })} autoComplete="off" />
          </Field>
          <Field label="Time zone" required>
            <TimezoneSelect value={value.newSiteTimezone} onChange={(v) => set({ newSiteTimezone: v })} />
          </Field>
          <Field label="Country" hint="Two letters, such as EG. Optional.">
            <Input
              value={value.newSiteCountry}
              maxLength={2}
              onChange={(e) => set({ newSiteCountry: e.target.value })}
              autoComplete="off"
              className="uppercase"
            />
          </Field>
        </>
      )}
    </div>
  );
}
