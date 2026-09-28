"use client";

// PRICE AND HOW CLIENTS GET IT — one section of the package form.
//
// A package has a price (0 is Free) and the ways a client may acquire it: Free, Voucher, Card payment, Room
// charge. Free and Voucher are the core product. A price above zero, Card payment and Room charge each belong
// to a module the site must have licensed and switched on; an option the site cannot use is shown disabled
// with the reason, not hidden, so the operator learns what exists and where it is switched on.
//
// The section is controlled: the form owns the value and validates it with buildAcquisition on save. The
// server stays authoritative and re-checks every rule, the module state included.

import { useId } from "react";
import { Trash2, Plus } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Field, Input, Select } from "@/components/ui/input";
import {
  ACQUISITION_METHODS, ACQUISITION_METHOD_LABELS, OFFERED_CURRENCIES, PAID_ACCESS_MODULE,
  currencyExponent, isPaidPrice, methodAvailability, methodsForPriceChange, moduleAvailability,
  roomChargeNotReadyText,
  type AcquisitionForm, type AcquisitionMethod, type ModulesReport, type RoomChargeInterface,
  type RoomChargeMappingForm,
} from "@/lib/commerce-form";

const emptyMapping = (pms_interface_id = ""): RoomChargeMappingForm =>
  ({ pms_interface_id, posting_code: "", tax_code: "", tax_rate_pct: "" });

export function AcquisitionSection({
  value, onChange, modules, interfaces,
}: {
  value: AcquisitionForm;
  onChange: (v: AcquisitionForm) => void;
  /** GET /modules: null while loading, "error" when it could not be read. Both fail closed. */
  modules: ModulesReport | "error" | null;
  /** GET /pms-financial-onboarding interfaces: null while loading, "error" when they could not be listed. */
  interfaces: RoomChargeInterface[] | "error" | null;
}) {
  const ids = useId();
  const headingID = `${ids}-acq`;
  const methodsLegendID = `${ids}-methods`;
  const mappingsHeadingID = `${ids}-mappings`;

  const paid = isPaidPrice(value.price, value.currency, value.currency_exponent);
  const exponent = currencyExponent(value.currency, value.currency_exponent);
  const paidAccess = moduleAvailability(PAID_ACCESS_MODULE, modules);
  const set = (patch: Partial<AcquisitionForm>) => onChange({ ...value, ...patch });

  // A currency this build does not list stays selectable when the package already uses it; otherwise saving
  // an unrelated change would silently move the package to another currency.
  const currencies: string[] = (OFFERED_CURRENCIES as readonly string[]).includes(value.currency)
    ? [...OFFERED_CURRENCIES] : [value.currency, ...OFFERED_CURRENCIES];

  function setPrice(price: string) {
    const nowPaid = isPaidPrice(price, value.currency, value.currency_exponent);
    set({ price, methods: methodsForPriceChange(value.methods, paid, nowPaid) });
  }

  function toggle(m: AcquisitionMethod, on: boolean) {
    const methods = on ? [...value.methods.filter((x) => x !== m), m] : value.methods.filter((x) => x !== m);
    // Ticking Room charge with nothing mapped opens one row to fill in, so the next thing needed is on screen.
    const room_charge = on && m === "PMS_POSTING" && value.room_charge.length === 0
      ? [emptyMapping(firstFreeInterface([]))] : value.room_charge;
    set({ methods, room_charge });
  }

  const ifaceList = Array.isArray(interfaces) ? interfaces : [];
  function firstFreeInterface(used: string[]): string {
    return ifaceList.find((i) => !used.includes(i.pms_interface_id))?.pms_interface_id ?? "";
  }
  const setRow = (i: number, patch: Partial<RoomChargeMappingForm>) =>
    set({ room_charge: value.room_charge.map((r, j) => (j === i ? { ...r, ...patch } : r)) });

  const roomOn = value.methods.includes("PMS_POSTING");
  const usedIDs = value.room_charge.map((r) => r.pms_interface_id).filter(Boolean);
  const canAddRow = ifaceList.some((i) => !usedIDs.includes(i.pms_interface_id));

  return (
    <div role="group" aria-labelledby={headingID} data-testid="acquisition-section">
      <h3 id={headingID} className="text-sm font-medium mb-2">Price and how clients get it</h3>

      <div className="grid gap-3 sm:grid-cols-2">
        <Field label="Price" hint={exponent === null ? undefined
          : exponent === 0 ? "In whole units. 0 means free." : `Up to ${exponent} decimal places. 0 means free.`}>
          <Input data-testid="price" inputMode="decimal" value={value.price}
            onChange={(e) => setPrice(e.target.value)} placeholder="0" />
        </Field>
        <Field label="Currency" hint="The decimal places follow the currency.">
          <Select data-testid="currency" value={value.currency}
            onChange={(e) => set({ currency: e.target.value })}>
            {currencies.map((c) => <option key={c} value={c}>{c}</option>)}
          </Select>
        </Field>
      </div>

      {/* A PRICE NEEDS PAID ACCESS. The price input is not locked -- a package that already has one must stay
          editable -- but the operator is told before saving that the server will refuse it. */}
      {paid && !paidAccess.selectable && (
        <p className="mt-2 text-xs text-warning-subtle-foreground" role="note" data-testid="paid-access-note">
          Charging for a package needs the Paid access module ({paidAccess.reason?.toLowerCase()}). Saving a price
          above 0 will be refused until it is licensed and switched on in System › Modules.
        </p>
      )}
      {modules === "error" && (
        <p className="mt-2 text-xs text-warning-subtle-foreground" role="note" data-testid="modules-error-note">
          The site&rsquo;s module state could not be read, so Card payment and Room charge cannot be chosen right
          now. Free and Voucher are always available.
        </p>
      )}

      <fieldset className="mt-3" aria-labelledby={methodsLegendID}>
        <legend id={methodsLegendID} className="mb-1.5 text-sm">How clients get it</legend>
        <div className="space-y-1.5">
          {ACQUISITION_METHODS.map((m) => {
            const checked = value.methods.includes(m);
            const avail = methodAvailability(m, modules);
            // The price decides which methods can apply at all, independently of any module.
            const priceBlock = m === "NOT_REQUIRED" && paid ? "Not for a package with a price"
              : (m === "ONLINE_PAYMENT" || m === "PMS_POSTING") && !paid ? "Needs a price above 0"
              : undefined;
            const why = !avail.selectable ? avail.reason : priceBlock;
            // Unticking is always allowed: an option that is on and no longer usable must be removable, or the
            // package could never be saved again.
            const disabled = !checked && !!why;
            return (
              <div key={m} data-testid={`method-${m}`}>
                <label className="flex items-center gap-2 text-sm">
                  <input type="checkbox" className="size-4 accent-primary" data-testid={`method-check-${m}`}
                    checked={checked} disabled={disabled}
                    onChange={(e) => toggle(m, e.target.checked)} />
                  <span>{ACQUISITION_METHOD_LABELS[m]}</span>
                  {why && <span className="text-xs text-muted-foreground">— {why}</span>}
                </label>
                {avail.selectable && avail.notReady && (
                  <p className="ms-6 text-xs text-muted-foreground" data-testid={`method-not-ready-${m}`}>
                    Clients are not offered {ACQUISITION_METHOD_LABELS[m].toLowerCase()} until it is ready:{" "}
                    {avail.notReady.join("; ")}.
                  </p>
                )}
              </div>
            );
          })}
        </div>
      </fieldset>

      {/* ROOM CHARGE: which posting code the charge lands on, per PMS interface. One row per interface; a
          client is only offered room charge through an interface that has a row here, is approved for room
          charge, and posts in the package's currency (there is no currency conversion). */}
      {roomOn && (
        <div className="mt-3" role="group" aria-labelledby={mappingsHeadingID} data-testid="room-charge-mappings">
          <div className="flex items-center justify-between mb-1">
            <span id={mappingsHeadingID} className="text-sm">Room charge posting codes</span>
            <Button type="button" variant="ghost" disabled={!canAddRow} data-testid="add-room-charge-mapping"
              onClick={() => set({ room_charge: [...value.room_charge, emptyMapping(firstFreeInterface(usedIDs))] })}>
              <Plus size={14} /> Add PMS interface
            </Button>
          </div>
          {interfaces === "error" && (
            <p className="text-xs text-warning-subtle-foreground mb-2" role="note" data-testid="interfaces-error-note">
              The PMS interfaces for room charge could not be listed. Mappings already saved are kept as they are.
            </p>
          )}
          {interfaces === null && <p className="text-xs text-muted-foreground mb-2">Loading PMS interfaces…</p>}
          {Array.isArray(interfaces) && interfaces.length === 0 && (
            <p className="text-xs text-muted-foreground mb-2" data-testid="no-interfaces-note">
              There is no PMS interface to charge to yet.{" "}
              <a href="/room-charge" className="underline">Approve the interface for room charge</a> first.
            </p>
          )}
          {value.room_charge.map((r, i) => {
            const n = `Room charge mapping ${i + 1}`;
            const iface = ifaceList.find((x) => x.pms_interface_id === r.pms_interface_id);
            const others = value.room_charge.filter((_, j) => j !== i).map((x) => x.pms_interface_id);
            const options = ifaceList.filter((x) => !others.includes(x.pms_interface_id));
            const mismatch = iface?.financial_base_currency && iface.financial_base_currency !== value.currency
              ? iface.financial_base_currency : null;
            return (
              <div key={i} className="mb-2" data-testid={`room-charge-${i}`}>
                <div className="flex gap-2 items-center">
                  <Select data-testid={`room-charge-interface-${i}`} aria-label={`${n}: PMS interface`}
                    className="h-9 w-auto shrink-0 ps-2.5" value={r.pms_interface_id}
                    onChange={(e) => setRow(i, { pms_interface_id: e.target.value })}>
                    <option value="">Choose a PMS interface…</option>
                    {/* A saved mapping whose interface is not in the list is kept, not silently dropped. */}
                    {r.pms_interface_id && !iface && <option value={r.pms_interface_id}>{r.pms_interface_id}</option>}
                    {options.map((x) => (
                      <option key={x.pms_interface_id} value={x.pms_interface_id}>
                        {x.display_label || x.pms_interface_id}{x.ready ? "" : " (not ready)"}
                      </option>
                    ))}
                  </Select>
                  <Input data-testid={`room-charge-posting-${i}`} aria-label={`${n}: posting code`}
                    placeholder="posting code" maxLength={20} value={r.posting_code}
                    onChange={(e) => setRow(i, { posting_code: e.target.value })} />
                  <Input data-testid={`room-charge-tax-code-${i}`} aria-label={`${n}: tax code (optional)`}
                    placeholder="tax code (optional)" maxLength={20} value={r.tax_code}
                    onChange={(e) => setRow(i, { tax_code: e.target.value })} />
                  <Input data-testid={`room-charge-tax-rate-${i}`} aria-label={`${n}: tax rate percent (optional)`}
                    placeholder="tax %" inputMode="decimal" className="w-24" value={r.tax_rate_pct}
                    onChange={(e) => setRow(i, { tax_rate_pct: e.target.value })} />
                  <Button type="button" variant="ghost" data-testid={`remove-room-charge-${i}`}
                    aria-label={`Remove room charge mapping ${i + 1}`}
                    onClick={() => set({ room_charge: value.room_charge.filter((_, j) => j !== i) })}>
                    <Trash2 size={14} />
                  </Button>
                </div>
                {iface && !iface.ready && (
                  <p className="mt-1 text-xs text-muted-foreground" data-testid={`room-charge-not-ready-${i}`}>
                    {roomChargeNotReadyText(iface.reason)}.{" "}
                    <a href="/room-charge" className="underline">Approve the interface for room charge</a>.
                  </p>
                )}
                {mismatch && (
                  <p className="mt-1 text-xs text-warning-subtle-foreground" role="note"
                    data-testid={`room-charge-currency-${i}`}>
                    This interface posts in {mismatch} and the package is priced in {value.currency}. Room charge
                    will not be offered through it: the currencies must match, and no conversion is made.
                  </p>
                )}
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}
