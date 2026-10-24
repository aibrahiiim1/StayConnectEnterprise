/**
 * Words for assisted guest-data classes, reasons and refusals; kept in step with the database by
 * test/guest-data-assisted-words.
 */

export type RecordClass = "ATTRIBUTABLE" | "CLAIMABLE" | "SHARED" | "CONFLICTING" | "UNVERIFIABLE";

export const CLASS_WORDS: Record<RecordClass, { label: string; tone: "ok" | "info" | "warn" | "err" | "neutral"; help: string }> = {
  ATTRIBUTABLE: {
    label: "Theirs by their own account",
    tone: "ok",
    help: "Made with the requester’s own account, sign-in identity or one-time code. It can be selected.",
  },
  CLAIMABLE: {
    label: "Established on your statements",
    tone: "info",
    help: "The access of the reservation or account the requester named, on an address nobody else has used, and you made the three statements. It can be selected.",
  },
  SHARED: {
    label: "Shared — cannot be attributed",
    tone: "warn",
    help: "A voucher or open access, or a statement is missing. It is never selected here.",
  },
  CONFLICTING: {
    label: "Conflict — left alone",
    tone: "err",
    help: "The same address was used under another reservation, account or person. It is never selected here.",
  },
  UNVERIFIABLE: {
    label: "Cannot be verified — left alone",
    tone: "neutral",
    help: "Not fully inside the range, or nothing says whose it is. It is never selected here.",
  },
};

export const REASON_WORDS: Record<string, string> = {
  own_account_or_identity: "Made with the requester’s own account or sign-in identity.",
  access_of_the_stated_context: "The access of the reservation or account the requester named, on an address no one else has used.",
  used_on_the_stated_device: "Typed on the stated device, which no one else has used.",
  statements_missing: "You have not stated that the device was shown and that the requester alone used it, so this stays shared.",
  private_address_not_confirmed: "This is a private (randomised) address and you have not confirmed that the device shows it.",
  voucher_or_open_access: "A voucher or open access: whoever held the code used it.",
  room_shared_by_named_guests: "The reservation names more than one guest, so the room’s access is shared by identified people and a statement cannot say whose device this is.",
  another_person_or_access: "Belongs to another reservation, account or person.",
  device_used_by_another_person: "Another person’s sign-in identity, account or verified address was also used on this device.",
  device_used_under_another_access: "The same device was also used under another reservation, voucher or account. If the same person made both stays, add the other reservation to the request.",
  straddles_range: "It begins or ends outside the range, or is still running.",
  no_owner_record: "Nothing says whose it is.",
  not_tied_to_the_stated_context: "Nothing on this device ties it to the reservation or account the requester named, so the named context says nothing about this record.",
  device_has_records_outside_the_request: "The device has other records outside this range or address list, so its identity row cannot be anonymised.",
};

export const BLOCK_WORDS: Record<string, string> = {
  recent: "Less than a day old: the sign-in protection may still count it. Try again tomorrow.",
  live: "Still in use (a live entitlement, session or restriction). Try again when it has ended.",
};

export const KIND_WORDS: Record<string, string> = {
  session: "Internet session",
  sign_in_attempt: "Sign-in attempt",
  verification_access: "Verification access",
  code_device_binding: "One-time code binding",
  sign_in_restriction: "Sign-in restriction",
  sign_in_pass: "Sign-in pass",
  sign_in_redirect: "Social sign-in redirect",
  device: "Device identity",
};

export const REFUSAL_WORDS: Record<string, string> = {
  closed: "This request is already closed.",
  changed: "The records changed after you reviewed them. They have been reloaded: review them again before confirming.",
  not_listed: "A selected record is not part of this request. Review the records again.",
  not_eligible: "A selected record cannot be processed: only records attributable to the person, or established as theirs, can be selected.",
  blocked_recent: "A selected record is less than a day old, so it cannot be erased yet.",
  blocked_live: "A selected record is still in use, so it cannot be erased now.",
  device_incomplete: "A device can be selected only together with every record it carries.",
  confirmation: "The confirmation does not match the request reference.",
  wrong_purpose: "This request was opened for the other purpose.",
  nothing_done: "Nothing was handed over yet, so the request cannot be completed.",
  other_site: "This guest identity was also used at another site, so it cannot be processed here.",
};

export const METHOD_OPTIONS: { value: string; label: string }[] = [
  { value: "ID_CHECKED_IN_PERSON", label: "Identity document checked in person" },
  { value: "ACCOUNT_CONTROL_SHOWN", label: "Requester showed control of the account" },
  { value: "CALLBACK_TO_KNOWN_CONTACT", label: "Called back on a contact the site already holds" },
  { value: "OTHER_DOCUMENTED", label: "Other — described in the evidence note" },
];

export const PURPOSE_WORDS: Record<string, string> = {
  ACCESS: "Hand over a copy of the records",
  ERASURE: "Anonymise the records",
};

export const STATUS_TONE: Record<string, "ok" | "neutral" | "info"> = { OPEN: "info", COMPLETED: "ok", CANCELLED: "neutral" };

export function parseAddresses(text: string): { ok: string[]; bad: string[] } {
  const ok: string[] = [];
  const bad: string[] = [];
  for (const raw of text.split(/[\s,;]+/).map((x) => x.trim()).filter(Boolean)) {
    const v = raw.toLowerCase().replace(/-/g, ":");
    const compact = v.replace(/:/g, "");
    if (/^[0-9a-f]{12}$/.test(compact) && (/^([0-9a-f]{2}:){5}[0-9a-f]{2}$/.test(v) || /^[0-9a-f]{12}$/.test(v))) {
      const m = compact.match(/.{2}/g)!.join(":");
      if (!ok.includes(m)) ok.push(m);
    } else bad.push(raw);
  }
  return { ok, bad };
}

/** A private (randomised, locally administered) address: the second hex digit of the first byte is 2, 6, a or e. */
export function isPrivateAddress(mac: string): boolean {
  const d = mac.replace(/[^0-9a-f]/gi, "").toLowerCase()[1];
  return d === "2" || d === "6" || d === "a" || d === "e";
}

/** Group multicast addresses (low bit of the first byte) name no single device. */
export function isGroupAddress(mac: string): boolean {
  const d = mac.replace(/[^0-9a-f]/gi, "").toLowerCase()[1];
  return d !== undefined && parseInt(d, 16) % 2 === 1;
}

function bytes(n: unknown): string {
  const v = typeof n === "number" ? n : Number(n);
  if (!Number.isFinite(v) || v < 0) return "";
  if (v < 1024) return `${v} B`;
  if (v < 1024 ** 2) return `${(v / 1024).toFixed(1)} KB`;
  if (v < 1024 ** 3) return `${(v / 1024 ** 2).toFixed(1)} MB`;
  return `${(v / 1024 ** 3).toFixed(2)} GB`;
}

export function describeDetail(kind: string, d: Record<string, unknown> | null | undefined): string {
  if (!d) return "";
  const parts: (string | undefined | null | false)[] = [];
  const s = (x: unknown) => (x === null || x === undefined || x === "" ? undefined : String(x));
  switch (kind) {
    case "session":
      parts.push(s(d.ip), s(d.mac_address), d.bytes_down !== undefined && `↓ ${bytes(d.bytes_down)}`, d.bytes_up !== undefined && `↑ ${bytes(d.bytes_up)}`,
        Array.isArray(d.usage_samples) && d.usage_samples.length > 0 && `${d.usage_samples.length} usage samples`);
      break;
    case "sign_in_attempt":
      parts.push(s(d.result), s(d.room_entered) && `room ${d.room_entered}`, s(d.device_ip), s(d.device_mac_address), s(d.guest_network));
      break;
    case "verification_access":
      parts.push(s(d.ip), s(d.device_mac_address), d.used_bytes !== undefined && `used ${bytes(d.used_bytes)}`, s(d.end_reason));
      break;
    case "code_device_binding":
    case "sign_in_pass":
      parts.push(s(d.device_mac_address));
      break;
    case "sign_in_restriction":
      parts.push(s(d.device_mac_address), s(d.guest_network), s(d.reason), s(d.room_entered) && `room ${d.room_entered}`);
      break;
    case "sign_in_redirect":
      parts.push(s(d.provider), s(d.from_ip), s(d.device_mac_address));
      break;
    case "device":
      parts.push(s(d.mac_address), s(d.last_ip) && `last ${d.last_ip}`);
      break;
  }
  return parts.filter(Boolean).join(" · ");
}

export const ASSISTED_EXPORT_NOTE =
  "Only the selected records are written to the file, with the request’s reference. Records of other people, shared access and anything not selected are not included. The request stays open until you close it.";

export const ASSISTED_ERASE_CONSEQUENCES: string[] = [
  "Only the selected records lose their device address, IP address and typed room. This cannot be undone.",
  "Usage totals, accounting, the reservation, entitlements, quotas, PMS data and financial records are not changed. A session stays as a record without an address.",
  "A device identity is anonymised only when every record it carries is inside the request and selected.",
  "Records you did not select, and everything shared with or used by anyone else, stay exactly as they are.",
  "The request closes and its device addresses and notes are removed from the database. Save the request file shown afterwards: it is the hotel’s record.",
];
