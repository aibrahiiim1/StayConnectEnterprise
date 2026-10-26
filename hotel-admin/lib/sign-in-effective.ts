// What a client network actually offers at sign-in, worked out from the four places that decide it. Nothing here grants a method: a network can only narrow what the site
// offers (cmd/scd/signin_network.go), so a method is offered on a network only when ALL of these agree.
//
//   site-wide      the switches on Sign-in methods, the module licence and, for code and social methods, a working sender or provider
//   this network   the methods switched off for the network (Client networks > the network > Sign-in)
//   PMS routing    room sign-in is offered only where the network's routing says so and a PMS connection can answer
//   packages       which package a client gets after signing in is a separate rule (Internet packages); it never hides a method
//
// This mirrors the server's decision for display; the server remains the authority at every sign-in.

import type { AssessmentState, RoutingPolicy } from "@/lib/api/pms-routing";

export type MethodKey = "pms" | "voucher" | "guest_account" | "email_account" | "sms" | "whatsapp" | "social" | "open";

export const METHOD_ORDER: MethodKey[] = ["pms", "voucher", "guest_account", "email_account", "sms", "whatsapp", "social", "open"];

export const METHOD_LABEL: Record<MethodKey, string> = {
  pms: "Room sign-in",
  voucher: "Voucher code",
  guest_account: "Client account",
  email_account: "Email account",
  sms: "SMS code",
  whatsapp: "WhatsApp code",
  social: "Social login",
  open: "Package without sign-in",
};

export type SiteFacts = {
  cfg: {
    voucher?: { enabled?: boolean };
    guest_account?: { enabled?: boolean };
    open?: { enabled?: boolean };
    email_account?: { enabled?: boolean };
    sms?: { enabled?: boolean };
    whatsapp?: { enabled?: boolean };
    social?: Record<string, { enabled?: boolean }>;
    pms?: { enabled?: boolean };
    portal?: { primary_method?: string };
  };
  modules: { hospitality: boolean; email_otp: boolean; sms_otp: boolean; whatsapp_otp: boolean; social_login: boolean };
  /** null = could not be read: shown as not confirmed rather than as working or broken. */
  senders: { email: boolean | null; sms: boolean | null; whatsapp: boolean | null };
  socialProviders: string[] | null;
};

export type NetworkOverride = { disabled_methods: string[]; primary_method?: string };

export type RoomBrief = { policy: RoutingPolicy; state: AssessmentState; summary: string; interfaces: string[] };

export type CellState =
  | "offered"       // clients on this network can use it
  | "off_here"      // the site offers it, this network (or its routing) switches it off
  | "off_site"      // switched off for the whole site
  | "needs_setup";  // switched on, but cannot work yet (no sender, no provider, no PMS answer)

export type Cell = { key: MethodKey; label: string; state: CellState; why: string; where: "site" | "network" | "pms" | "setup" };

function site(key: MethodKey, f: SiteFacts): { on: boolean; setup?: string } | "hidden" {
  switch (key) {
    case "pms": return f.modules.hospitality ? { on: f.cfg.pms?.enabled === true } : "hidden";
    case "voucher": return { on: f.cfg.voucher?.enabled === true };
    case "guest_account": return { on: f.cfg.guest_account?.enabled === true };
    case "open": return { on: f.cfg.open?.enabled === true };
    case "email_account":
      if (!f.modules.email_otp) return "hidden";
      return { on: f.cfg.email_account?.enabled === true, setup: f.senders.email === false ? "No email sender is set up." : undefined };
    case "sms":
      if (!f.modules.sms_otp) return "hidden";
      return { on: f.cfg.sms?.enabled === true, setup: f.senders.sms === false ? "No SMS sender is set up." : undefined };
    case "whatsapp":
      if (!f.modules.whatsapp_otp) return "hidden";
      return { on: f.cfg.whatsapp?.enabled === true, setup: f.senders.whatsapp === false ? "No WhatsApp sender is set up." : undefined };
    case "social": {
      if (!f.modules.social_login) return "hidden";
      const on = Object.values(f.cfg.social ?? {}).some((m) => m?.enabled === true);
      const ready = f.socialProviders === null ? true : f.socialProviders.length > 0;
      return { on, setup: ready ? undefined : "No social login provider is set up." };
    }
  }
}

export function effectiveMethods(f: SiteFacts, override: NetworkOverride | undefined, room: RoomBrief | undefined): { cells: Cell[]; lead: MethodKey | null } {
  const narrowed = new Set(override?.disabled_methods ?? []);
  const cells: Cell[] = [];
  for (const key of METHOD_ORDER) {
    const s = site(key, f);
    if (s === "hidden") continue;
    const label = METHOD_LABEL[key];
    if (!s.on) {
      cells.push({ key, label, state: "off_site", why: "Switched off for the whole site on Sign-in methods.", where: "site" });
      continue;
    }
    if (key === "pms") {
      if (!room || room.policy === "NONE" || room.state === "OFF") {
        cells.push({ key, label, state: "off_here", why: room?.policy === "NONE" ? "This network is set to offer no room sign-in (PMS routing)." : "Room sign-in is not offered on this network.", where: "pms" });
        continue;
      }
      if (narrowed.has(key)) {
        cells.push({ key, label, state: "off_here", why: "Switched off for this network.", where: "network" });
        continue;
      }
      if (room.state === "READY") {
        cells.push({ key, label, state: "offered", why: room.interfaces.length ? `Answered by ${room.interfaces.join(", ")}.` : "A PMS connection answers it.", where: "pms" });
      } else {
        cells.push({ key, label, state: "needs_setup", why: room.summary || "Room sign-in cannot answer on this network yet.", where: "pms" });
      }
      continue;
    }
    if (narrowed.has(key)) {
      cells.push({ key, label, state: "off_here", why: "Switched off for this network.", where: "network" });
      continue;
    }
    if (s.setup) {
      cells.push({ key, label, state: "needs_setup", why: s.setup, where: "setup" });
      continue;
    }
    cells.push({ key, label, state: "offered", why: "Offered on the sign-in page.", where: "site" });
  }
  const wanted = (override?.primary_method || f.cfg.portal?.primary_method || "") as MethodKey | "";
  const leadCell = wanted ? cells.find((c) => c.key === wanted && c.state === "offered") : undefined;
  return { cells, lead: leadCell ? leadCell.key : null };
}

export const CELL_WORDS: Record<CellState, { label: string; tone: "ok" | "neutral" | "warn" }> = {
  offered: { label: "Offered", tone: "ok" },
  off_here: { label: "Off on this network", tone: "neutral" },
  off_site: { label: "Off site-wide", tone: "neutral" },
  needs_setup: { label: "Needs setup", tone: "warn" },
};
