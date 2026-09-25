import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, within, fireEvent, waitFor } from "@testing-library/react";

// THE OPERATOR JOURNEY, PERFORMED RATHER THAN DESCRIBED.
//
// The operator authors the POLICY — time, speeds, allowance, devices, stay rules — and the server derives the
// package that expresses it. Each test drives the real controls and reads what was actually SENT, because "the
// button was enabled" is not "the right policy was published".

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

const emergency = {
  source: "EMERGENCY_FALLBACK",
  duration_seconds: 3600,
  down_kbps: 5000,
  up_kbps: 2000,
  data_quota_bytes: 500 * 1024 * 1024,
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

async function renderScreen(canWrite = true) {
  const { CheckoutGraceScreen } = await import("@/components/checkout-grace/checkout-grace-screen");
  render(<CheckoutGraceScreen canWrite={canWrite} />);
  await screen.findByText("What a departing guest receives");
}

function setField(label: string | RegExp, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

function openEditor(name: RegExp = /Create hotel policy|Edit policy/) {
  // The header action; the empty history may offer a second "Create" button, so take the first.
  fireEvent.click(screen.getAllByRole("button", { name })[0]);
}

async function toReview() {
  fireEvent.click(screen.getByRole("button", { name: /Review changes/i }));
  await screen.findByLabelText("Policy changes");
}

async function confirmAndPublish(password = "s3cret") {
  setField("Confirm your password", password);
  fireEvent.click(screen.getByRole("button", { name: /^Publish policy$/i }));
}

describe("an operator authors the hotel's checkout grace policy", () => {
  it("offers to CREATE a policy when none exists, and never sends the operator to the catalog", async () => {
    mockGrace([unconfigured]);
    await renderScreen();
    expect(screen.queryByText(/commercial catalog/i)).toBeNull();
    expect(screen.queryByLabelText(/Grace package/i)).toBeNull();
    expect(screen.getAllByRole("button", { name: /Create hotel policy/i }).length).toBeGreaterThan(0);
  });

  it("publishes the typed policy itself — no package, correct units, the version the operator read", async () => {
    mockGrace([unconfigured, { ...unconfigured, published: true, config_version: 1 }]);
    put.mockResolvedValue({ config_version: 1 });
    await renderScreen();

    openEditor();
    setField("Grace time", "45");
    fireEvent.change(screen.getByLabelText("Grace time unit"), { target: { value: "min" } });
    setField("Download speed (Mbps)", "8");
    setField("Upload speed (Mbps)", "3");
    setField("Data allowance (MB)", "750");
    setField("Device limit", "2");
    setField("Stay rules after checkout", "2");
    fireEvent.change(screen.getByLabelText("Stay rules after checkout unit"), { target: { value: "h" } });

    await toReview();
    await confirmAndPublish();

    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    const [path, body] = put.mock.calls[0];
    expect(path).toBe("/checkout-grace");
    // THE UNITS ARE THE POINT: minutes/hours, Mbps and MB in; seconds, kbps and bytes out.
    expect(body.grace_duration_seconds).toBe(45 * 60);
    expect(body.grace_down_kbps).toBe(8000);
    expect(body.grace_up_kbps).toBe(3000);
    expect(body.grace_data_quota_bytes).toBe(750 * 1024 * 1024);
    expect(body.grace_device_limit).toBe(2);
    expect(body.grace_device_limit_policy).toBe("REJECT_NEW_DEVICE");
    expect(body.eligibility_window_seconds).toBe(2 * 3600);
    // No package revision is sent at all: the server derives it.
    expect(body.grace_package_revision_id).toBeUndefined();
    // Optimistic concurrency and step-up travel with it.
    expect(body.expected_config_version).toBe(0);
    expect(body.password).toBe("s3cret");
    expect(body.reason_code).toBe("INITIAL_SETUP");

    // The page re-reads and says what happened.
    expect(await screen.findByText("Version 1 published")).toBeTruthy();
  });

  it("shows a live 'guest will receive' preview built from the fields", async () => {
    mockGrace([unconfigured]);
    await renderScreen();
    openEditor();
    setField("Grace time", "90");
    fireEvent.change(screen.getByLabelText("Grace time unit"), { target: { value: "min" } });
    setField("Download speed (Mbps)", "12");
    setField("Data allowance (MB)", "2048");
    const preview = screen.getByTestId("grace-preview").textContent!;
    expect(preview).toMatch(/1 hour 30 minutes after checkout/);
    expect(preview).toMatch(/up to 12 Mbps down/);
    expect(preview).toMatch(/with 2 GB of data/);
  });

  it("shows old → new before publishing, and publishes nothing until confirmed", async () => {
    mockGrace([live]);
    await renderScreen();
    openEditor(/Edit policy/);

    // A second version starts from what is in force, not from a blank form.
    expect((screen.getByLabelText("Grace time") as HTMLInputElement).value).toBe("30");
    expect((screen.getByLabelText("Grace time unit") as HTMLSelectElement).value).toBe("min");
    expect((screen.getByLabelText("Download speed (Mbps)") as HTMLInputElement).value).toBe("10");
    expect((screen.getByLabelText("Data allowance (MB)") as HTMLInputElement).value).toBe("1024");
    expect((screen.getByLabelText("Stay rules after checkout") as HTMLInputElement).value).toBe("2");
    expect((screen.getByLabelText("Stay rules after checkout unit") as HTMLSelectElement).value).toBe("h");

    setField("Grace time", "20");
    await toReview();
    const changes = screen.getByLabelText("Policy changes");
    const row = within(changes).getByText("Grace time").closest("li")!;
    expect(row.textContent).toMatch(/30 min/);
    expect(row.textContent).toMatch(/20 min/);
    expect(screen.getByText("1 change")).toBeTruthy();
    expect(put).not.toHaveBeenCalled();

    await confirmAndPublish("pw");
    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    const body = put.mock.calls[0][1];
    expect(body.grace_duration_seconds).toBe(20 * 60);
    expect(body.grace_data_quota_bytes).toBe(1024 * 1024 * 1024);
    expect(body.grace_down_kbps).toBe(10000);
    expect(body.expected_config_version).toBe(3);
    expect(body.reason_code).toBe("POLICY_CHANGE");
  });

  it("does not offer to publish identical terms (the server would not move the version)", async () => {
    mockGrace([live]);
    await renderScreen();
    openEditor(/Edit policy/);
    await toReview();
    expect(screen.getByText("Nothing to publish")).toBeTruthy();
    setField("Confirm your password", "pw");
    expect((screen.getByRole("button", { name: /^Publish policy$/i }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("refuses an out-of-range policy inline, without widening what the server accepts", async () => {
    mockGrace([unconfigured]);
    await renderScreen();
    openEditor();
    setField("Data allowance (MB)", "2000000"); // above 1 TB
    setField("Grace time", "8");
    fireEvent.change(screen.getByLabelText("Grace time unit"), { target: { value: "d" } }); // above 7 days
    fireEvent.click(screen.getByRole("button", { name: /Review changes/i }));

    expect(screen.getByText(/data allowance between 1 MB and 1,048,576 MB/i)).toBeTruthy();
    expect(screen.getByText(/grace time between 1 minute and 7 days/i)).toBeTruthy();
    expect(screen.getByLabelText("Data allowance (MB)").getAttribute("aria-invalid")).toBe("true");
    expect(screen.queryByLabelText("Policy changes")).toBeNull();
    expect(screen.getByTestId("grace-preview").textContent).toMatch(/Correct the highlighted fields/);
    expect(put).not.toHaveBeenCalled();
  });

  it("a reason typed with spaces is normalised to a valid code — never sent invalid", async () => {
    mockGrace([live]);
    put.mockResolvedValue({ config_version: 4 });
    await renderScreen();
    openEditor(/Edit policy/);
    setField("Grace time", "40");
    await toReview();

    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "__OTHER__" } });
    setField("Describe the reason", "late checkout");
    expect(screen.getByText("Recorded as LATE_CHECKOUT")).toBeTruthy();
    await confirmAndPublish("pw");
    await waitFor(() => expect(put).toHaveBeenCalledTimes(1));
    expect(put.mock.calls[0][1].reason_code).toBe("LATE_CHECKOUT");
  });

  it("an 'Other' reason with nothing usable blocks publishing instead of sending it", async () => {
    mockGrace([live]);
    await renderScreen();
    openEditor(/Edit policy/);
    setField("Grace time", "40");
    await toReview();
    fireEvent.change(screen.getByLabelText("Reason"), { target: { value: "__OTHER__" } });
    setField("Describe the reason", "123 !!!");
    setField("Confirm your password", "pw");
    expect(screen.getByText(/starting with a letter/i)).toBeTruthy();
    const publish = screen.getByRole("button", { name: /^Publish policy$/i }) as HTMLButtonElement;
    expect(publish.disabled).toBe(true);
    fireEvent.click(publish);
    expect(put).not.toHaveBeenCalled();
  });

  it("a concurrent publication reloads, explains, and keeps the operator's draft", async () => {
    const newer = {
      ...live,
      config_version: 7,
      effective: { ...live.effective, duration_seconds: 3600, config_version: 7 },
    };
    mockGrace([live, newer]);
    put.mockRejectedValue(Object.assign(new Error("conflict"), { status: 409, code: "version_conflict" }));
    await renderScreen();
    openEditor(/Edit policy/);
    setField("Grace time", "20");
    await toReview();
    await confirmAndPublish("pw");

    await waitFor(() => expect(screen.getByRole("alert").textContent).toMatch(/Someone else published a newer policy/i));
    expect(put).toHaveBeenCalledTimes(1);
    // Reloaded, and back on the terms step with the draft intact.
    expect(get.mock.calls.filter((c) => c[0] === "/checkout-grace").length).toBe(2);
    expect((screen.getByLabelText("Grace time") as HTMLInputElement).value).toBe("20");
    // The review now compares against the NEW version and would publish against it.
    put.mockResolvedValue({ config_version: 8 });
    await toReview();
    const row = within(screen.getByLabelText("Policy changes")).getByText("Grace time").closest("li")!;
    expect(row.textContent).toMatch(/1 h/);
    await confirmAndPublish("pw");
    await waitFor(() => expect(put).toHaveBeenCalledTimes(2));
    expect(put.mock.calls[1][1].expected_config_version).toBe(7);
  });

  it("a wrong password is reported on the password field, and nothing claims success", async () => {
    mockGrace([live]);
    put.mockRejectedValue(Object.assign(new Error("password confirmation required"), { status: 401, code: "reauth_required" }));
    await renderScreen();
    openEditor(/Edit policy/);
    setField("Grace time", "25");
    await toReview();
    await confirmAndPublish("wrong");
    await waitFor(() => expect(screen.getByRole("alert").textContent).toMatch(/password was not accepted/i));
    expect(screen.getByText("Password not accepted.")).toBeTruthy();
    expect(screen.queryByText(/^Version \d+ published$/)).toBeNull();
    // Still on the review step, so the operator can retype the password.
    expect(screen.getByLabelText("Policy changes")).toBeTruthy();
  });

  it("a derived-package refusal is explained in plain words", async () => {
    mockGrace([live]);
    put.mockRejectedValue(Object.assign(new Error("the checkout-grace policy was refused"), { status: 400, code: "package_invalid" }));
    await renderScreen();
    openEditor(/Edit policy/);
    setField("Grace time", "25");
    await toReview();
    await confirmAndPublish("pw");
    await waitFor(() =>
      expect(screen.getByRole("alert").textContent).toMatch(/could not build a grace package that matches these terms/i),
    );
    expect(screen.getByRole("alert").textContent).toMatch(/policy in force is unchanged/i);
  });

  it("shows version history as a timeline with who, why and what changed", async () => {
    const snap = (d: number, down: number, q: number) => ({
      grace_duration_seconds: d,
      grace_down_kbps: down,
      grace_up_kbps: 4000,
      grace_data_quota_bytes: q,
      grace_device_limit: 2,
      grace_device_limit_policy: "REJECT_NEW_DEVICE",
      eligibility_window_seconds: 86400,
    });
    mockGrace(
      [{ ...live, config_version: 2 }],
      [
        { config_version: 2, published_at: "2026-09-17T09:00:00Z", actor: "Dana Whitfield", reason_code: "GUEST_FEEDBACK", policy: snap(1800, 10000, 1024 * 1024 * 1024) },
        { config_version: 1, published_at: "2026-09-16T09:00:00Z", actor: "Sam Ortega", reason_code: "SHORTER_GRACE", policy: snap(3600, 10000, 500 * 1024 * 1024) },
      ],
    );
    await renderScreen();

    const list = screen.getByLabelText("Checkout grace policy history");
    expect(within(list).getByText("Version 2")).toBeTruthy();
    expect(within(list).getByText("Version 1")).toBeTruthy();
    expect(within(list).getByText(/Guest feedback/)).toBeTruthy();
    // An unknown code is humanised, not shown raw.
    expect(within(list).getByText(/Shorter grace/)).toBeTruthy();

    // What changed in v2, computed from consecutive snapshots.
    const changes = within(list).getByLabelText("Changes in version 2");
    expect(changes.textContent).toMatch(/Grace time: 1 h → 30 min/);
    expect(changes.textContent).toMatch(/Data allowance: 500 MB → 1 GB/);
    expect(changes.textContent).not.toMatch(/Download speed/);

    // Opening a version shows ITS OWN terms.
    fireEvent.click(within(list).getByRole("button", { name: "View terms of version 1" }));
    const terms = await screen.findByLabelText("Version 1 terms");
    expect(within(terms).getByText("1 h")).toBeTruthy();
    expect(within(terms).getByText("500 MB")).toBeTruthy();
    expect(within(terms).queryByText("30 min")).toBeNull();
  });

  it("collapses long history behind 'show more' without dropping versions", async () => {
    const hist = Array.from({ length: 8 }, (_, i) => ({
      config_version: 8 - i,
      published_at: `2026-09-${String(20 - i).padStart(2, "0")}T09:00:00Z`,
      actor: "Dana Whitfield",
      reason_code: "POLICY_CHANGE",
      policy: { grace_duration_seconds: 600 * (8 - i), grace_down_kbps: 5000, grace_up_kbps: 2000, grace_data_quota_bytes: 500 * 1024 * 1024 },
    }));
    mockGrace([{ ...live, config_version: 8 }], hist);
    await renderScreen();
    const list = screen.getByLabelText("Checkout grace policy history");
    expect(within(list).queryByText("Version 3")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: /Show 3 older versions/ }));
    expect(within(list).getByText("Version 1")).toBeTruthy();
  });
});
