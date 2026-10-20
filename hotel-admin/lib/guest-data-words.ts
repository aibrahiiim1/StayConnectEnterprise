/**
 * What a request covers depends on whose record it is (migration 0145, decision D-R5). A reservation's own data
 * belongs to the guest the PMS names; an account or identity belongs to the person who registered it. The
 * internet access used through a room's credentials, a voucher or a staff-issued account, and any device used with
 * someone else, is shared by several people: no request about one person exports or changes it.
 */
export function exportDescription(kind: string | undefined): string {
  if (kind === "stay" || kind === "stay_guest") {
    return "Downloads what the PMS reservation holds about this guest: the reservation, the guest’s details, PMS messages, room charges and post-stay access. Internet access used through the room’s credentials is shared by everyone in the room, so it is not included; a request about it is yours to handle by hand. When other guests share the stay, the file holds only this guest’s details.";
  }
  return "Downloads this person’s own data: the account or sign-in methods, their internet access and purchases, and the devices only they used. The reservation an account is linked to, internet access shared with others (a room, a voucher, a staff-issued account) and devices used with someone else are not included; a request about those is yours to handle by hand.";
}

export function eraseConsequences(kind: string | undefined): string[] {
  const common = [
    "Names, email addresses, phone numbers, room and reservation numbers, MAC and IP addresses are replaced or cleared. This cannot be undone.",
    "Totals, reports and financial records stay, without anything that identifies the guest.",
    "Refused while the guest is in-house, has internet access running, or has a device online.",
  ];
  if (kind === "stay" || kind === "stay_guest") {
    return [
      "Only the reservation goes: its guest details, PMS messages and room number. Other guests on a shared stay are left alone: only this guest’s name and PMS guest number are removed from it.",
      "Internet access used through the room’s credentials is shared by everyone in the room, so it is not changed here. A request about it is yours to handle by hand: waiting for the retention period is not an answer. Accounts linked to the stay stay too: they belong to the people who registered them.",
      ...common,
    ];
  }
  return [
    "Only this person’s own records go: their account or sign-in methods, and the devices only they used.",
    "The reservation an account is linked to, the other people in the room and any device also used with someone else are left alone. A request about them is yours to handle by hand: the retention period ending is not an answer.",
    ...common,
  ];
}

/** Why a request was refused, in plain words (the database's reason codes). */
export const LIVE_REASONS: Record<string, string> = {
  co_guests: "other guests are named on this stay; search again and choose the guest who asked, so the others are left alone",
  pms_open: "the PMS still shows this stay as reserved or in-house and its departure date has not passed; the PMS would copy the guest straight back",
  pms_pending: "a PMS message about this stay has not been processed yet; try again once it has (see PMS activity)",
  access_live: "internet access linked to this record has not ended yet (for a reservation, the room's access, which other people in the room may be using)",
  online: "a device is online right now on this record's access (for a reservation, anyone in the room)",
  signin_restricted: "one of the guest's devices is under a sign-in restriction; release it on Guest sign-in attempts first",
  other_site: "this guest identity was also used at another site, so it cannot be erased here",
  busy: "the guest's records changed while this ran; try again",
};
