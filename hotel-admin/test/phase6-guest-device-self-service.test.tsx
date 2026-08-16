// PHASE 6 (DARK) — the Guest Device Self-Service setting screen.
//
// Three things could go wrong here in a way that matters, and each has its own group below:
//
//   1. the screen could imply that switching the setting ON deploys the capability. An operator who
//      believes that will tell guests a feature exists, and the guests will find nothing;
//   2. the screen could offer a control to an operator whose role cannot use it, producing a refusal they
//      cannot interpret -- the UI must hide exactly what edged would refuse;
//   3. the screen could leak the internals it sits on (flag names, appliance ids, operator ids), or invent
//      an affordance the API does not have.
//
// The UI matrix is asserted against the SAME resource key edged mounts, because a screen that hides the
// control for the wrong reason is indistinguishable from one that hides it for the right one.

import { describe, expect, it, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import "@testing-library/jest-dom/vitest";
import { GuestDeviceSelfServiceView } from "@/components/phase6/guest-device-self-service-view";
import { canRead, canWrite } from "@/lib/roles";

const get = vi.fn();
const put = vi.fn();
vi.mock("@/lib/api", async (orig) => {
  const actual = await (orig() as Promise<any>);
  return { ...actual, api: { get: (...a: any[]) => get(...a), put: (...a: any[]) => put(...a) } };
});

function setting(enabled: boolean, gate: boolean) {
  get.mockImplementation(async (path: string) => {
    if (path !== "/guest-device-self-service/") throw new Error(`unexpected GET ${path}`);
    return { enabled, phase_gate_enabled: gate };
  });
}

beforeEach(() => {
  get.mockReset();
  put.mockReset();
});
afterEach(() => vi.restoreAllMocks());

describe("the two controls are never conflated", () => {
  it("shows the product setting and the deployment state as two separate facts", async () => {
    setting(true, false);
    render(<GuestDeviceSelfServiceView canAct />);
    expect(await screen.findByTestId("setting-state")).toHaveTextContent("On");
    expect(screen.getByTestId("gate-state")).toHaveTextContent("Not yet");
  });

  it("says plainly that switching it on does not install it", async () => {
    setting(true, false);
    render(<GuestDeviceSelfServiceView canAct />);
    const effect = await screen.findByTestId("effect");
    expect(effect).toHaveTextContent(/Guests cannot use device self-service yet/i);
    expect(effect).toHaveTextContent(/Saving this setting does not install it/i);
  });

  it("only claims guests can use it when BOTH are true", async () => {
    for (const [on, gate, expected] of [
      [false, false, /Guests cannot use device self-service:/i],
      [true, false, /Guests cannot use device self-service yet/i],
      [false, true, /because this property has it switched off/i],
      [true, true, /Guests can use device self-service on this property now/i],
    ] as const) {
      setting(on, gate);
      const { unmount } = render(<GuestDeviceSelfServiceView canAct />);
      const effect = await screen.findByTestId("effect");
      expect(effect).toHaveTextContent(expected);
      if (!(on && gate)) {
        expect(effect).not.toHaveTextContent(/Guests can use device self-service on this property now/i);
      }
      unmount();
    }
  });

  it("never describes the setting as enabling, activating or deploying the feature", async () => {
    setting(false, false);
    render(<GuestDeviceSelfServiceView canAct />);
    await screen.findByTestId("effect");
    const body = (document.body.textContent ?? "").toLowerCase();
    for (const phrase of ["activate", "deploy", "roll out", "enables the feature", "turns on the feature"]) {
      expect(body).not.toContain(phrase);
    }
  });
});

describe("authorization matches the API", () => {
  it("offers the switch to a role that holds write", async () => {
    setting(false, true);
    render(<GuestDeviceSelfServiceView canAct={canWrite("guest-device-self-service", ["hotel_it_manager"])} />);
    expect(await screen.findByRole("button", { name: /switch on/i })).toBeInTheDocument();
  });

  it("shows a read-only role the state and no control", async () => {
    setting(true, true);
    render(
      <GuestDeviceSelfServiceView
        canAct={canWrite("guest-device-self-service", ["front_office_operator"])}
      />
    );
    expect(await screen.findByTestId("setting-state")).toHaveTextContent("On");
    expect(screen.queryByRole("button", { name: /switch (on|off)/i })).not.toBeInTheDocument();
    expect(screen.getByTestId("readonly-note")).toHaveTextContent(/can see this setting but not change it/i);
  });

  // The UI matrix must agree with edged's, resource key for resource key. edged's own test asserts the
  // matrix against docs/ROLE_AND_SCOPE_MATRIX.md; this asserts the UI against the same documented shape, so
  // all three agree by test rather than by inspection.
  it("mirrors the documented role matrix exactly", () => {
    const res = "guest-device-self-service";
    for (const role of ["site_admin", "hotel_it_manager"]) {
      expect(canWrite(res, [role])).toBe(true);
    }
    for (const role of ["front_office_operator", "guest_relations_operator", "payments_operator", "site_viewer"]) {
      expect(canRead(res, [role])).toBe(true);
      expect(canWrite(res, [role])).toBe(false);
    }
    expect(canRead(res, ["voucher_operator"])).toBe(false);
    expect(canWrite(res, ["voucher_operator"])).toBe(false);
  });
});

describe("changing the setting", () => {
  it("confirms, records a reason, and reports what actually changed", async () => {
    setting(false, true);
    put.mockResolvedValue({ enabled: true, changed: true, phase_gate_enabled: true });
    const user = userEvent.setup();
    render(<GuestDeviceSelfServiceView canAct />);

    await user.click(await screen.findByRole("button", { name: /switch on/i }));
    // A confirmation step, so the switch is not a single stray click on a guest-facing capability.
    expect(screen.getByText(/Offer guest device self-service at this property\?/i)).toBeInTheDocument();
    await user.type(screen.getByPlaceholderText(/why are you making this change/i), "guests asked");
    await user.click(screen.getByRole("button", { name: "Switch on" }));

    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    expect(put).toHaveBeenCalledWith("/guest-device-self-service/", { enabled: true, reason: "guests asked" });
    expect(await screen.findByRole("status")).toHaveTextContent(/now offers guest device self-service/i);
  });

  it("sends no identity of any kind — the server derives all four", async () => {
    setting(false, true);
    put.mockResolvedValue({ enabled: true, changed: true, phase_gate_enabled: true });
    const user = userEvent.setup();
    render(<GuestDeviceSelfServiceView canAct />);
    await user.click(await screen.findByRole("button", { name: /switch on/i }));
    await user.click(screen.getByRole("button", { name: "Switch on" }));
    await waitFor(() => expect(put).toHaveBeenCalled());
    const body = put.mock.calls[0][1];
    for (const forbidden of ["tenant_id", "site_id", "appliance_id", "operator_id", "changed_by", "phase_gate_enabled"]) {
      expect(body).not.toHaveProperty(forbidden);
    }
    expect(Object.keys(body).sort()).toEqual(["enabled", "reason"]);
  });

  it("distinguishes a real change from a no-op", async () => {
    setting(true, true);
    put.mockResolvedValue({ enabled: true, changed: false, phase_gate_enabled: true });
    const user = userEvent.setup();
    render(<GuestDeviceSelfServiceView canAct />);
    await user.click(await screen.findByRole("button", { name: /switch off/i }));
    await user.click(screen.getByRole("button", { name: "Switch off" }));
    await waitFor(() => expect(put).toHaveBeenCalled());
    expect(await screen.findByRole("status")).toHaveTextContent(/already in that state/i);
  });

  it("surfaces a refusal instead of pretending it saved", async () => {
    setting(false, true);
    put.mockRejectedValue(Object.assign(new Error("forbidden"), { status: 403 }));
    const user = userEvent.setup();
    render(<GuestDeviceSelfServiceView canAct />);
    await user.click(await screen.findByRole("button", { name: /switch on/i }));
    await user.click(screen.getByRole("button", { name: "Switch on" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(/forbidden/i);
    expect(screen.queryByRole("status")).not.toBeInTheDocument();
    expect(screen.getByTestId("setting-state")).toHaveTextContent("Off");
  });
});

describe("what the screen does not show", () => {
  it("names no flag, appliance id or operator id", async () => {
    setting(true, false);
    render(<GuestDeviceSelfServiceView canAct />);
    await screen.findByTestId("effect");
    const body = document.body.textContent ?? "";
    for (const leak of ["STAYCONNECT_PHASE6", "phase_gate_enabled", "appliance_id", "operator_id", "iam_v2"]) {
      expect(body).not.toContain(leak);
    }
  });

  it("offers no way to see or manage an individual guest's devices", async () => {
    setting(true, true);
    render(<GuestDeviceSelfServiceView canAct />);
    await screen.findByTestId("effect");
    const body = (document.body.textContent ?? "").toLowerCase();
    // This screen is a property-level switch. Operator-side device removal is a different capability that
    // was never authorized, and an affordance here would be the beginning of one.
    for (const phrase of ["mac address", "remove device", "release device", "search guest"]) {
      expect(body).not.toContain(phrase);
    }
  });
});
