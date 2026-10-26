import { describe, it, expect } from "vitest";
import { effectiveMethods, type SiteFacts, type RoomBrief } from "@/lib/sign-in-effective";

const facts = (over: Partial<SiteFacts> = {}): SiteFacts => ({
  cfg: { voucher: { enabled: true }, guest_account: { enabled: true }, email_account: { enabled: true }, pms: { enabled: true }, open: { enabled: false }, portal: { primary_method: "pms" } },
  modules: { hospitality: true, email_otp: true, sms_otp: false, whatsapp_otp: false, social_login: false },
  senders: { email: true, sms: null, whatsapp: null },
  socialProviders: null,
  ...over,
});
const room = (over: Partial<RoomBrief> = {}): RoomBrief => ({ policy: "DEDICATED", state: "READY", summary: "", interfaces: ["Protel"], ...over });
const state = (r: ReturnType<typeof effectiveMethods>, k: string) => r.cells.find((c) => c.key === k)?.state;

describe("what a network actually offers", () => {
  it("offers what the site offers when the network narrows nothing", () => {
    const r = effectiveMethods(facts(), undefined, room());
    expect(state(r, "voucher")).toBe("offered");
    expect(state(r, "pms")).toBe("offered");
    expect(state(r, "open")).toBe("off_site");
    expect(r.lead).toBe("pms");
  });

  it("a network can switch a method off but never on", () => {
    const r = effectiveMethods(facts({ cfg: { voucher: { enabled: false } } }), { disabled_methods: ["guest_account"] }, undefined);
    expect(state(r, "voucher")).toBe("off_site");
    expect(state(r, "guest_account")).toBe("off_site");
    const s = effectiveMethods(facts(), { disabled_methods: ["voucher", "open"] }, room());
    expect(state(s, "voucher")).toBe("off_here");
    expect(state(s, "open")).toBe("off_site");
  });

  it("room sign-in follows the network's PMS routing, not only the site switch", () => {
    expect(state(effectiveMethods(facts(), undefined, room({ policy: "NONE", state: "OFF" })), "pms")).toBe("off_here");
    expect(state(effectiveMethods(facts(), undefined, undefined), "pms")).toBe("off_here");
    const blocked = effectiveMethods(facts(), undefined, room({ state: "BLOCKED", summary: "Protel is not connected." }));
    expect(state(blocked, "pms")).toBe("needs_setup");
    expect(blocked.cells.find((c) => c.key === "pms")?.why).toBe("Protel is not connected.");
    expect(blocked.lead).toBeNull();
  });

  it("names the PMS that answers a dedicated or shared network", () => {
    const shared = effectiveMethods(facts(), undefined, room({ policy: "SHARED_STRICT", interfaces: ["Protel", "Aqua"] }));
    expect(shared.cells.find((c) => c.key === "pms")?.why).toBe("Answered by Protel, Aqua.");
  });

  it("does not offer a method whose module is not licensed, and flags a missing sender", () => {
    const r = effectiveMethods(facts({ modules: { hospitality: false, email_otp: true, sms_otp: false, whatsapp_otp: false, social_login: false }, senders: { email: false, sms: null, whatsapp: null } }), undefined, undefined);
    expect(r.cells.find((c) => c.key === "pms")).toBeUndefined();
    expect(state(r, "email_account")).toBe("needs_setup");
  });

  it("the network's own lead method wins over the site's, and only an offered method can lead", () => {
    const r = effectiveMethods(facts(), { disabled_methods: [], primary_method: "voucher" }, room());
    expect(r.lead).toBe("voucher");
    const none = effectiveMethods(facts(), { disabled_methods: ["pms"], primary_method: "" }, room());
    expect(none.lead).toBeNull();
  });
});
