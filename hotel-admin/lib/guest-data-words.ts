/**
 * A reservation's data belongs to the guest the PMS names; an account to who registered it. Shared access (room
 * credentials, vouchers, staff accounts, shared devices) is never exported or changed for one person.
 */
export function exportDescription(kind: string | undefined): string {
  if (kind === "stay" || kind === "stay_guest") {
    return "Downloads what the PMS reservation holds about this guest: the reservation, the guest’s details, PMS messages, room charges and post-stay access. Internet access used through the room’s credentials is shared by everyone in the room, so it is not included; a request about it is yours to handle by hand. When other guests share the stay, the file holds only this guest’s details.";
  }
  return "Downloads this person’s own data: the account or sign-in methods, their internet access and purchases, and the devices only they used. The reservation an account is linked to, internet access shared with others (a room, a voucher, a staff-issued account) and devices used with someone else are not included; a request about those is yours to handle by hand.";
}

export function eraseConsequences(kind: string | undefined): string[] {
  const common = [
    "Names, addresses, phone numbers, room and reservation numbers, MAC and IP addresses are removed. This cannot be undone.",
    "Totals and reports stay. Financial records are kept for the legal period with their PMS and payment references.",
  ];
  if (kind === "stay" || kind === "stay_guest") {
    return [
      "Only the reservation goes: this guest’s details, PMS messages and room number. Other guests on the stay are left alone. Accounts linked to the stay stay too.",
      "The room’s internet access is shared, so it is not changed: a request about it is yours to handle by hand; the retention period ending is not an answer.",
      ...common,
      "Refused while the guest is in-house or the room’s internet access is still running.",
    ];
  }
  return [
    "Only this person’s own records go: their account or sign-in methods and the devices only they used. Their own internet access ends now and those devices are disconnected.",
    "The reservation an account is linked to and anything shared (a room, a device used by others) are left alone: a request about them is yours to handle by hand; the retention period ending is not an answer.",
    ...common,
    "Refused while one of their devices is under a sign-in restriction.",
  ];
}

/** What an erasure did to the person's access; a disconnection is claimed only where the appliance confirmed it. */
export function erasedAccessWords(packages: number | undefined, sessions: number | undefined, withdrawn: number | undefined): string {
  if (!packages) return "";
  const plural = (n: number, w: string) => `${n} ${w}${n === 1 ? "" : "s"}`;
  const ended = `Their access was ended (${plural(packages, "package")}${sessions ? `, ${plural(sessions, "device session")}` : ""}).`;
  if (!sessions) return ended;
  const w = withdrawn ?? 0;
  if (w >= sessions) return `${ended} The devices were disconnected.`;
  const rest = "the appliance takes the rest off the network on its next enforcement pass";
  return w > 0 ? `${ended} ${w} disconnected at once; ${rest}.` : `${ended} The disconnection is not confirmed yet: ${rest.replace("the rest", "them")}.`;
}

export const LIVE_REASONS: Record<string, string> = {
  co_guests: "other guests are named on this stay; search again and choose the guest who asked, so the others are left alone",
  pms_open: "the PMS still shows this stay as reserved or in-house and its departure date has not passed; the PMS would copy the guest straight back",
  pms_pending: "a PMS message about this stay has not been processed yet; try again once it has (see PMS activity)",
  access_live: "the room’s internet access, which other people in the room may be using, has not ended yet; erase once it ends",
  online: "a device is online on access that is not only this guest’s (a room or a verification still running); try again once it ends",
  signin_restricted: "one of the guest's devices is under a sign-in restriction; release it on Guest sign-in attempts first",
  other_site: "this guest identity was also used at another site, so it cannot be erased here",
  busy: "the guest's records changed while this ran; try again",
};
