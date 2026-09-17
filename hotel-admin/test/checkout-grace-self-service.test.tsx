import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, within, fireEvent, waitFor } from "@testing-library/react";

// THE OPERATOR JOURNEY, PERFORMED RATHER THAN DESCRIBED.
//
// The screen used to ask the operator to choose a PACKAGE, and on a site with none it told them to "publish
// one through the commercial catalog first". That instruction could not be followed: the grace package is a
// reserved system package the operator publisher refuses to create, and the checkout validator only ever
// accepts one derived from the policy itself. PRE-LIVE sat on the emergency fallback with zero selectable
// packages and no route out that did not involve SQL.
//
// Each test below starts where a real operator starts and drives the real controls. None of them asserts a
// string the component would print regardless of whether the workflow works -- the publish tests read what
// was actually SENT, because "the button was enabled" is not "the right policy was published".

const get = vi.fn();
const put = vi.fn();
vi.mock("@/lib/api", () => ({
  api: {
    get: (...a: any[]) => get(...a),
    post: vi.fn(),
    put: (...a: any[]) => put(...a),
    del: vi.fn(),
  },
  ApiError: class ApiError extends Error {},
}));

beforeEach(() => {
  get.mockReset();
  put.mockReset();
});
afterEach(() => vi.resetModules());

/** The built-in emergency terms, as data-plane/internal/grace reports them. */
const emergency = {
  source: "EMERGENCY_FALLBACK",
  duration_seconds: 3600,
  down_kbps: 5000,
  up_kbps: 2000,
  data_quota_bytes: 500 * 1024 * 1024,
  // 0 is what the appliance actually reports, and it means "no extra devices". The fixture said 1, which is
  // why the bare-zero rendering was never exercised.
  device_limit: 0,
  device_limit_policy: "REJECT_NEW_DEVICE",
  policy_version: "EMERGENCY_GRACE_V1",
};

const unconfigured = {
  published: false,
  config_version: 0,
  supported_device_policies: ["REJECT_NEW_DEVICE"],
  effective: emergency,
  emergency_history: { count: 6, last_at: "2026-09-14T07:47:27Z" },
};

/** states may hold successive GET responses, so a test can assert what the screen shows AFTER publishing. */
function mockGrace(states: Record<string, any>[], history: any[] = []) {
  let i = 0;
  get.mockImplementation((path: string) => {
    if (path === "/checkout-grace/history") return Promise.resolve({ data: history, meta: { has_more: false } });
    if (path === "/checkout-grace") {
      const s = states[Math.min(i, states.length - 1)];
      i += 1;
      return Promise.resolve(s);
    }
    return Promise.resolve({});
  });
}

async function renderForm() {
  const { CheckoutGraceForm } = await import("@/components/phase3/checkout-grace-form");
  render(<CheckoutGraceForm canWrite />);
  await screen.findByText("In force right now");
}

/** Fill one numeric field by its visible label. */
function setField(label: RegExp, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("an operator can author the hotel's checkout grace policy without leaving this page", () => {
  it("offers to CREATE a policy when none exists, and never sends the operator to the catalog", async () => {
    mockGrace([unconfigured]);
    await renderForm();

    // The old dead end, asserted as absent. This is the actual regression: a page that tells an operator to
    // do something impossible is worse than one that says nothing.
    expect(screen.queryByText(/commercial catalog/i)).toBeNull();
    expect(screen.queryByText(/No checkout-grace package is available/i)).toBeNull();
    expect(screen.queryByLabelText(/Grace package/i)).toBeNull();

    // And the way out is on the page, named for what it does.
    expect(screen.getByRole("button", { name: /Create hotel policy/i })).toBeTruthy();
  });

  it("publishes the typed policy itself — no package, correct units, the version the operator read", async () => {
    mockGrace([unconfigured, { ...unconfigured, published: true, config_version: 1 }]);
    put.mockResolvedValue({ config_version: 1 });
    await renderForm();

    fireEvent.click(screen.getByRole("button", { name: /Create hotel policy/i }));
    setField(/Grace duration \(minutes\)/i, "45");
    setField(/Download speed \(Mbps\)/i, "8");
    setField(/Upload speed \(Mbps\)/i, "3");
    setField(/Data allowance \(MB\)/i, "750");
    setField(/Device limit/i, "2");
    setField(/Eligibility window \(minutes\)/i, "120");

    fireEvent.click(screen.getByRole("button", { name: /Review before publishing/i }));
    fireEvent.change(screen.getByLabelText(/Confirm your password/i), { target: { value: "s3cret" } });
    fireEvent.click(screen.getByRole("button", { name: /^Publish policy$/i }));

    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    const [path, body] = put.mock.calls[0];
    expect(path).toBe("/checkout-grace");

    // THE UNITS ARE THE POINT. The operator types minutes, Mbps and MB; the contract is seconds, kbps and
    // bytes. A conversion that silently lost a factor of a thousand would publish a policy nobody chose.
    expect(body.grace_duration_seconds).toBe(45 * 60);
    expect(body.grace_down_kbps).toBe(8000);
    expect(body.grace_up_kbps).toBe(3000);
    expect(body.grace_data_quota_bytes).toBe(750 * 1024 * 1024);
    expect(body.grace_device_limit).toBe(2);
    expect(body.eligibility_window_seconds).toBe(120 * 60);

    // No package revision is sent at all: the server derives it. Sending one is now an explicit 400.
    expect(body.grace_package_revision_id).toBeUndefined();

    // Optimistic concurrency and step-up travel with it.
    expect(body.expected_config_version).toBe(0);
    expect(body.password).toBe("s3cret");
    expect(body.reason_code).toMatch(/^[A-Z][A-Z0-9_]{0,63}$/);
  });

  it("shows the operator the exact terms BEFORE publishing, and publishes nothing until they confirm", async () => {
    mockGrace([unconfigured]);
    await renderForm();

    fireEvent.click(screen.getByRole("button", { name: /Create hotel policy/i }));
    setField(/Grace duration \(minutes\)/i, "90");
    setField(/Download speed \(Mbps\)/i, "12");
    setField(/Data allowance \(MB\)/i, "2048");
    fireEvent.click(screen.getByRole("button", { name: /Review before publishing/i }));

    // The review panel restates the terms in the units a guest experiences, from the SAME conversion the
    // request uses -- so what is confirmed and what is sent cannot drift.
    const dl = screen.getByLabelText("Policy to publish");
    expect(within(dl).getByText("90 min")).toBeTruthy();
    expect(within(dl).getByText("12 Mbps")).toBeTruthy();
    expect(within(dl).getByText("2.0 GB")).toBeTruthy();

    // Reviewing is not publishing.
    expect(put).not.toHaveBeenCalled();
  });

  it("refuses an out-of-range policy in the page, without widening what the server accepts", async () => {
    mockGrace([unconfigured]);
    await renderForm();

    fireEvent.click(screen.getByRole("button", { name: /Create hotel policy/i }));
    // Above 1 TB. The input's own min/max cannot express this bound, so it is the page guard that must catch
    // it -- a duration of 0 would be refused by the browser's constraint validation first, which would make
    // this test pass without the guard existing at all.
    setField(/Data allowance \(MB\)/i, "2000000");
    fireEvent.click(screen.getByRole("button", { name: /Review before publishing/i }));

    expect(screen.getByRole("alert").textContent).toMatch(/Data allowance must be between/i);
    expect(screen.queryByLabelText("Policy to publish")).toBeNull();
    expect(put).not.toHaveBeenCalled();
  });

  it("a second version starts from what is in force, not from a blank form", async () => {
    // THE DETAIL THAT MAKES VERSION 2 SAFE. An operator changing one number must not have to retype the other
    // five from memory -- a blank form is how a policy loses its data allowance by accident.
    const live = {
      published: true,
      config_version: 3,
      supported_device_policies: ["REJECT_NEW_DEVICE"],
      effective: {
        source: "PUBLISHED",
        duration_seconds: 1800,
        down_kbps: 10000,
        up_kbps: 4000,
        data_quota_bytes: 1024 * 1024 * 1024,
        device_limit: 2,
        device_limit_policy: "REJECT_NEW_DEVICE",
        eligibility_window_seconds: 7200,
        config_version: 3,
      },
      emergency_history: { count: 0 },
    };
    mockGrace([live]);
    put.mockResolvedValue({ config_version: 4 });
    await renderForm();

    fireEvent.click(screen.getByRole("button", { name: /Change policy/i }));
    expect((screen.getByLabelText(/Grace duration \(minutes\)/i) as HTMLInputElement).value).toBe("30");
    expect((screen.getByLabelText(/Download speed \(Mbps\)/i) as HTMLInputElement).value).toBe("10");
    expect((screen.getByLabelText(/Data allowance \(MB\)/i) as HTMLInputElement).value).toBe("1024");
    expect((screen.getByLabelText(/Eligibility window \(minutes\)/i) as HTMLInputElement).value).toBe("120");

    // Change ONE number and publish; everything else must survive unchanged.
    setField(/Grace duration \(minutes\)/i, "20");
    fireEvent.click(screen.getByRole("button", { name: /Review before publishing/i }));
    fireEvent.change(screen.getByLabelText(/Confirm your password/i), { target: { value: "pw" } });
    fireEvent.click(screen.getByRole("button", { name: /^Publish policy$/i }));

    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    const body = put.mock.calls[0][1];
    expect(body.grace_duration_seconds).toBe(20 * 60);
    expect(body.grace_data_quota_bytes).toBe(1024 * 1024 * 1024);
    expect(body.grace_down_kbps).toBe(10000);
    // Published against the version actually read, so a concurrent publication conflicts instead of vanishing.
    expect(body.expected_config_version).toBe(3);
  });

  it("a concurrent publication reloads rather than overwrites", async () => {
    mockGrace([unconfigured, { ...unconfigured, published: true, config_version: 7 }]);
    put.mockRejectedValue(Object.assign(new Error("conflict"), { status: 409 }));
    await renderForm();

    fireEvent.click(screen.getByRole("button", { name: /Create hotel policy/i }));
    fireEvent.click(screen.getByRole("button", { name: /Review before publishing/i }));
    fireEvent.change(screen.getByLabelText(/Confirm your password/i), { target: { value: "pw" } });
    fireEvent.click(screen.getByRole("button", { name: /^Publish policy$/i }));

    await waitFor(() => expect(screen.getByRole("alert").textContent).toMatch(/newer policy/i));
    // Reloaded, so the operator is looking at what is actually in force before they try again.
    expect(get).toHaveBeenCalledWith("/checkout-grace");
    expect(screen.queryByLabelText("Policy to publish")).toBeNull();
  });

  it("shows version history with who published it and why", async () => {
    mockGrace(
      [{ ...unconfigured, published: true, config_version: 2 }],
      [
        {
          config_version: 2,
          published_at: "2026-09-17T09:00:00Z",
          actor: "Dana Whitfield",
          reason_code: "SHORTER_GRACE",
          policy: {
            grace_duration_seconds: 1800,
            grace_down_kbps: 10000,
            grace_up_kbps: 4000,
            grace_data_quota_bytes: 1024 * 1024 * 1024,
            grace_device_limit: 2,
          },
        },
        {
          config_version: 1,
          published_at: "2026-09-16T09:00:00Z",
          actor: "Dana Whitfield",
          reason_code: "HOTEL_ADMIN_UPDATE",
          policy: {
            grace_duration_seconds: 3600,
            grace_down_kbps: 5000,
            grace_up_kbps: 2000,
            grace_data_quota_bytes: 500 * 1024 * 1024,
            grace_device_limit: 1,
          },
        },
      ],
    );
    await renderForm();

    const table = await screen.findByLabelText("Checkout grace policy history");
    const rows = within(table).getAllByRole("row");
    // Header plus two versions, newest first.
    expect(rows).toHaveLength(3);
    expect(within(rows[1]).getByText("2")).toBeTruthy();
    expect(within(rows[1]).getByText("SHORTER_GRACE")).toBeTruthy();
    // The EARLIER version still shows its own terms, not today's. An append-only ledger that re-derived its
    // numbers from the current config would agree with itself and describe nothing.
    expect(within(rows[2]).getByText(/1 h/)).toBeTruthy();
    expect(within(rows[2]).getByText(/500 MB/)).toBeTruthy();
  });

  it("says it CANNOT SEE the history rather than claiming there is none", async () => {
    // FOUND ON PRE-LIVE, NOT BY A TEST. The publication ledger is written by a SECURITY DEFINER function, so
    // svc_edged holds no SELECT on it and the endpoint answered 500 on every page load. A fixture connecting
    // as the schema owner can read everything and could never have caught it.
    //
    // The distinction being asserted is the one that matters: "no policy has ever been published" and "I
    // cannot read the record" are different claims, and showing the first while the second is true would
    // misinform an operator auditing who changed the hotel's grace terms.
    get.mockImplementation((path: string) => {
      if (path === "/checkout-grace/history")
        return Promise.resolve({ data: [], meta: { has_more: false }, available: false });
      if (path === "/checkout-grace") return Promise.resolve({ ...unconfigured, published: true, config_version: 2 });
      return Promise.resolve({});
    });
    await renderForm();

    const note = await screen.findByText(/cannot be read on this appliance/i);
    expect(note.textContent).toMatch(/does not mean no policy has been published/i);
    // and it must not render an empty table that reads as "nothing was ever published"
    expect(screen.queryByLabelText("Checkout grace policy history")).toBeNull();
  });

  it("calls the fallback's zero device limit 'no extra devices', not '0'", async () => {
    // The built-in fallback carries device_limit 0. A bare "0" reads as "no limit" -- the opposite of what it
    // means. The previous screen said so and the rewrite lost it; PRE-LIVE showed the bare 0.
    mockGrace([unconfigured]);
    await renderForm();
    const dl = screen.getByLabelText("Effective checkout grace");
    expect(within(dl).getByText(/no extra devices/i)).toBeTruthy();
  });

  it("a read-only role sees the terms but cannot open the editor", async () => {
    mockGrace([unconfigured]);
    const { CheckoutGraceForm } = await import("@/components/phase3/checkout-grace-form");
    render(<CheckoutGraceForm canWrite={false} />);
    await screen.findByText("In force right now");

    expect((screen.getByRole("button", { name: /Create hotel policy/i }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.getByText(/can view this policy but not change it/i)).toBeTruthy();
  });
});
