import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";

// THE PMS CONNECTIONS SCREEN, AS AN OPERATOR MEETS IT — and, above all, WHAT IT SENDS.
//
//   * Adding a Protel connection produces exactly the two requests the screen has always produced, and nothing
//     else: no provider_config, no credential, no publish and no activation.
//   * Adding a web-API provider sends its settings as provider_config and its credential as a structured secret,
//     with the operator's password, and still neither publishes nor activates.
//   * A connection that was created and never configured is kept apart from the live ones.
//   * The verification badge says exactly how far a provider has been proven.
//   * "Test connection" is never offered for a socket link.
//   * No internal or development vocabulary reaches the operator.

const get = vi.fn();
const post = vi.fn();
const put = vi.fn();
const del = vi.fn();
vi.mock("@/lib/api", () => ({
  api: {
    get: (...a: any[]) => get(...a),
    post: (...a: any[]) => post(...a),
    put: (...a: any[]) => put(...a),
    del: (...a: any[]) => del(...a),
  },
  ApiError: class ApiError extends Error {},
}));

beforeEach(() => { get.mockReset(); post.mockReset(); put.mockReset(); del.mockReset(); });
afterEach(() => vi.resetModules());

const protel = {
  id: "i1", connector_kind: "protel-fias", display_label: "Main PMS", lifecycle_state: "ACTIVE",
  current_revision_id: "r1", current_revision_no: 1, revision_count: 1, published: true,
  endpoint: "10.0.0.1:5003",
};
const healthOk = {
  pms_interface_id: "i1", room_auth_ready: true, transport_status: "CONNECTED", continuity_status: "CONTINUOUS",
  sync_status: "IN_SYNC", materialization_ready: true, in_house_stays: 41, pending_events: 0, review_events: 0,
  last_heartbeat_at: new Date(Date.now() - 60_000).toISOString(),
};

const REST_PROVIDER = {
  kind: "apaleo", label: "apaleo", vendor: "apaleo GmbH", integration: "REST API", transport: "REST_POLL",
  verification: "AUTOMATED_CONTRACT_VERIFIED",
  verification_note: "Checked against the provider's published API contract in automated tests.",
  credential: {
    mode: "AUTH_KEY",
    fields: [
      { key: "client_id", label: "Client ID", secret: false, required: true },
      { key: "client_secret", label: "Client secret", secret: true, required: true },
    ],
  },
  fields: [
    { key: "base_url", label: "API address", type: "url", required: true, placeholder: "https://api.apaleo.com" },
    { key: "property_id", label: "Property code", type: "string", required: true },
    { key: "poll_interval_s", label: "Check for changes every", type: "int", default: 60, min: 30, max: 3600, unit: "seconds" },
  ],
  capabilities: { full_resync: false, live_events: false, test_connection: true, arrivals: true, departures: true },
  setup_steps: ["Create an API client for OneGate in the provider's console."],
};
const PROTEL_PROVIDER = {
  kind: "protel-fias", label: "Protel (FIAS)", vendor: "Protel", integration: "FIAS interface", transport: "SOCKET",
  verification: "LIVE_PROVIDER_VERIFIED", credential: { mode: "NONE", fields: [] },
  // Even if a catalogue ever claimed it, a socket link must not get a "test connection" button.
  capabilities: { full_resync: true, test_connection: true },
};

function mockBackend(over: {
  interfaces?: any[];
  health?: Record<string, any>;
  providers?: any[] | null;
  revisions?: any[];
  roles?: string[];
} = {}) {
  get.mockImplementation((path: string) => {
    if (path === "/auth/whoami") return Promise.resolve({ roles: over.roles ?? ["site_admin"] });
    if (path === "/pms-interfaces") return Promise.resolve({ interfaces: over.interfaces ?? [protel] });
    if (path === "/pms-providers") {
      return over.providers
        ? Promise.resolve({ providers: over.providers })
        : Promise.reject(Object.assign(new Error("page not found"), { status: 404 }));
    }
    if (path === "/pms-routing") {
      return Promise.resolve({
        routes: [{ guest_network_id: "gn1", guest_network_name: "Guest Wi-Fi", pms_interface_id: "i1",
          is_default: true, routing_mode: "MAPPED" }],
        unmapped_guest_networks: [],
      });
    }
    const m = /^\/pms-interfaces\/([^/]+)\/health$/.exec(path);
    if (m) return Promise.resolve({ health: over.health?.[m[1]] ?? { ...healthOk, pms_interface_id: m[1] } });
    if (path.endsWith("/revisions")) {
      return Promise.resolve({
        revisions: over.revisions ?? [{ id: "r1", revision_no: 1, source_timezone: "Europe/Berlin",
          folio_identity_strategy: "UNSET", normalization_version: 1, config: { endpoint: "10.0.0.1:5003" }, published: true }],
      });
    }
    if (path.endsWith("/connection-settings")) return Promise.reject(new Error("none"));
    return Promise.resolve({ interface: protel, guest_networks: [] });
  });
}

async function renderPage() {
  const Page = (await import("@/app/(app)/pms-interfaces/page")).default;
  return render(<Page />);
}

async function openWizard() {
  await userEvent.click(await screen.findByRole("button", { name: /add connection/i }));
  return screen.findByRole("dialog", { name: /Add a PMS connection/ });
}

describe("adding a connection", () => {
  it("sends exactly today's Protel requests — create, then a draft — and nothing else", async () => {
    mockBackend();
    post.mockImplementation((path: string) => Promise.resolve(
      path === "/pms-interfaces" ? { id: "new1", lifecycle_state: "AUTH_DISABLED" } : { revision_id: "rv1", revision_no: 1 }));
    await renderPage();
    const dialog = await openWizard();

    // Only one provider without a catalogue, and it is already chosen.
    await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    await userEvent.type(within(dialog).getByLabelText(/^Name/), "Front office");
    await userEvent.type(within(dialog).getByLabelText(/PMS address and port/), "10.0.0.5:5010");
    await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    // No credential step for a link that needs none, and no password asked for.
    expect(within(dialog).queryByLabelText(/Confirm your password/)).toBeNull();
    await userEvent.click(within(dialog).getByRole("button", { name: "Create connection" }));

    await waitFor(() => expect(post).toHaveBeenCalledTimes(2));
    expect(post.mock.calls[0]).toEqual(["/pms-interfaces", { connector_kind: "protel-fias", display_label: "Front office" }]);
    const [path, body] = post.mock.calls[1];
    expect(path).toBe("/pms-interfaces/new1/revisions");
    expect(JSON.parse(JSON.stringify(body))).toEqual({
      endpoint: "10.0.0.5:5010", source_timezone: "Africa/Cairo",
      dial_timeout_ms: 5000, read_timeout_ms: 15000, write_timeout_ms: 15000,
      heartbeat_interval_ms: 30000, heartbeat_timeout_ms: 90000, feed_freshness_ms: 120000, complete_sync_ms: 600000,
      read_only: true,
    });
    // It stops at a draft. Publishing and activating are the operator's next, separate, confirmed steps.
    expect(await within(dialog).findByRole("button", { name: "Publish and activate" })).toBeTruthy();
    expect(post.mock.calls.some(([p]) => /publish|lifecycle|secret/.test(String(p)))).toBe(false);
  });

  it("sends a web-API provider's settings as provider_config and its credential as a structured secret", async () => {
    mockBackend({ providers: [PROTEL_PROVIDER, REST_PROVIDER] });
    post.mockImplementation((path: string) => Promise.resolve(
      path === "/pms-interfaces" ? { id: "new2" } : path.endsWith("/revisions") ? { revision_id: "rv9", revision_no: 1 } : { generation_no: 1 }));
    await renderPage();
    const dialog = await openWizard();

    await userEvent.click(await within(dialog).findByRole("radio", { name: /apaleo/ }));
    expect(within(dialog).getByText("Automated contract tests only — not yet live-verified")).toBeTruthy();
    await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));

    await userEvent.type(within(dialog).getByLabelText(/^Name/), "Cloud PMS");
    await userEvent.type(within(dialog).getByLabelText(/API address/), "not a url");
    await userEvent.type(within(dialog).getByLabelText(/Property code/), "BER");
    await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    // The shape check stops a malformed address before anything is sent.
    expect(await within(dialog).findByText(/Enter a full web address/)).toBeTruthy();
    expect(post).not.toHaveBeenCalled();
    await userEvent.clear(within(dialog).getByLabelText(/API address/));
    await userEvent.type(within(dialog).getByLabelText(/API address/), "https://api.example.test");
    await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));

    // Credentials: the secret is masked, the client id is not.
    const secretField = within(dialog).getByLabelText(/Client secret/) as HTMLInputElement;
    expect(secretField.type).toBe("password");
    await userEvent.type(within(dialog).getByLabelText(/Client ID/), "sc-client");
    await userEvent.type(secretField, "s3cr3t");
    await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));

    // The review never repeats the secret.
    expect(dialog.textContent).not.toContain("s3cr3t");
    await userEvent.type(within(dialog).getByLabelText(/Confirm your password/), "operator-pw");
    await userEvent.click(within(dialog).getByRole("button", { name: "Create connection" }));

    await waitFor(() => expect(post).toHaveBeenCalledTimes(3));
    expect(post.mock.calls[0]).toEqual(["/pms-interfaces", { connector_kind: "apaleo", display_label: "Cloud PMS" }]);
    expect(post.mock.calls[1]).toEqual(["/pms-interfaces/new2/revisions", {
      source_timezone: "Africa/Cairo", read_only: true,
      provider_config: { base_url: "https://api.example.test", property_id: "BER", poll_interval_s: 60 },
    }]);
    expect(post.mock.calls[2]).toEqual(["/pms-interfaces/new2/secret", {
      secret: { client_id: "sc-client", client_secret: "s3cr3t" },
      reason_code: "INITIAL_COMMISSIONING", password: "operator-pw",
    }]);
  });

  it("resumes after a failed step instead of creating a second connection", async () => {
    mockBackend();
    let revisionAttempts = 0;
    post.mockImplementation((path: string) => {
      if (path === "/pms-interfaces") return Promise.resolve({ id: "new3" });
      revisionAttempts += 1;
      return revisionAttempts === 1
        ? Promise.reject(Object.assign(new Error("source_timezone \"Mars/Base\" is not a known IANA time zone"), { code: "validation" }))
        : Promise.resolve({ revision_id: "rv3", revision_no: 1 });
    });
    await renderPage();
    const dialog = await openWizard();
    await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    await userEvent.type(within(dialog).getByLabelText(/^Name/), "Spa");
    await userEvent.type(within(dialog).getByLabelText(/PMS address and port/), "10.0.0.9:5010");
    await userEvent.click(within(dialog).getByRole("button", { name: "Next" }));
    await userEvent.click(within(dialog).getByRole("button", { name: "Create connection" }));

    // The refusal is in words, and says what did and did not happen.
    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toMatch(/connection was created, but its configuration was not saved/i);
    expect(alert.textContent).toMatch(/not a recognised time zone/i);

    await userEvent.click(within(dialog).getByRole("button", { name: "Try again" }));
    await within(dialog).findByRole("button", { name: "Publish and activate" });
    expect(post.mock.calls.filter(([p]) => p === "/pms-interfaces")).toHaveLength(1);
  });

  it("is not offered to a role that may only read PMS connections", async () => {
    mockBackend({ roles: ["front_office_operator"] });
    await renderPage();
    await screen.findByText("Main PMS");
    expect(screen.queryByRole("button", { name: /add connection/i })).toBeNull();
  });
});

describe("the overview", () => {
  it("keeps never-configured connections apart from the live ones", async () => {
    mockBackend({
      interfaces: [
        protel,
        { id: "i9", connector_kind: "protel-fias", display_label: "Test link", lifecycle_state: "AUTH_DISABLED",
          revision_count: 0, published: false },
      ],
    });
    await renderPage();
    await screen.findByText("Main PMS");

    // Folded away by default, and never rendered as a connection card.
    expect(screen.queryByText("Test link")).toBeNull();
    expect(screen.getAllByRole("button", { name: "Manage" })).toHaveLength(1);
    const toggle = screen.getByRole("button", { name: /Inactive \/ not configured/ });
    expect(toggle).toHaveAttribute("aria-expanded", "false");
    await userEvent.click(toggle);
    expect(screen.getByText("Test link")).toBeTruthy();
    expect(screen.getByText(/Never configured — not connected to any PMS/)).toBeTruthy();
  });

  it("states how far each provider has been verified", async () => {
    mockBackend({
      providers: [PROTEL_PROVIDER, REST_PROVIDER],
      interfaces: [
        protel,
        { id: "i2", connector_kind: "apaleo", display_label: "Cloud PMS", lifecycle_state: "ACTIVE",
          current_revision_id: "r7", current_revision_no: 1, revision_count: 1, published: true,
          provider_label: "apaleo", transport: "REST_POLL", verification: "AUTOMATED_CONTRACT_VERIFIED" },
      ],
    });
    await renderPage();
    await screen.findByText("Cloud PMS");
    expect(await screen.findByText("Live-verified")).toBeTruthy();
    expect(screen.getByText("Automated contract tests only — not yet live-verified")).toBeTruthy();
  });

  it("summarises room sign-in, guests in house and when a PMS was last heard from", async () => {
    mockBackend();
    await renderPage();
    expect(await screen.findByText("Working")).toBeTruthy();
    expect(screen.getAllByText("41").length).toBeGreaterThan(0);
    expect(screen.getAllByText("1m ago").length).toBeGreaterThan(0);
    // The guest network that uses the connection is named on its card.
    expect(screen.getByText("Guest Wi-Fi")).toBeTruthy();
  });
});

describe("the connection sheet", () => {
  it("never offers a connection test for a socket link", async () => {
    mockBackend({ providers: [PROTEL_PROVIDER, REST_PROVIDER] });
    await renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "Manage" }));
    await userEvent.click(await screen.findByRole("tab", { name: "Actions" }));
    await screen.findByRole("heading", { name: "Room sign-in through this connection" });
    expect(screen.queryByRole("button", { name: /test connection/i })).toBeNull();
  });

  it("tests a web-API connection with a password and shows the result in words", async () => {
    mockBackend({
      providers: [PROTEL_PROVIDER, REST_PROVIDER],
      interfaces: [{ id: "i2", connector_kind: "apaleo", display_label: "Cloud PMS", lifecycle_state: "ACTIVE",
        current_revision_id: "r7", current_revision_no: 1, revision_count: 1, published: true, secret_generation: 1 }],
    });
    post.mockResolvedValue({ ok: false, stage: "AUTH", code: "AUTH_REJECTED", message: "The provider rejected the client credentials.", latency_ms: 210 });
    await renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "Manage" }));
    await userEvent.click(await screen.findByRole("tab", { name: "Actions" }));
    await userEvent.click(await screen.findByRole("button", { name: "Test connection" }));
    const dialog = await screen.findByRole("dialog", { name: /Test the connection/ });
    await userEvent.type(within(dialog).getByLabelText(/Confirm your password/), "pw");
    await userEvent.click(within(dialog).getByRole("button", { name: "Run test" }));

    await waitFor(() => expect(post).toHaveBeenCalled());
    expect(post.mock.calls[0]).toEqual(["/pms-interfaces/i2/test-connection", { password: "pw" }]);
    expect(await screen.findByText(/The test failed while signing in to the provider/)).toBeTruthy();
    expect(screen.getByText(/rejected the client credentials/)).toBeTruthy();
    // A provider with a key gets a Credentials tab; the credential itself is never shown or fetched.
    expect(screen.getByRole("tab", { name: "Credentials" })).toBeTruthy();
    for (const [p] of get.mock.calls) expect(String(p)).not.toMatch(/secret/);
  });

  it("activates a paused connection with a reason and the operator's password", async () => {
    mockBackend({ interfaces: [{ ...protel, lifecycle_state: "AUTH_DISABLED" }] });
    post.mockResolvedValue({ changed: true });
    await renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "Manage" }));
    await userEvent.click(await screen.findByRole("tab", { name: "Actions" }));
    await userEvent.click(await screen.findByRole("button", { name: "Activate" }));
    const dialog = await screen.findByRole("dialog", { name: /Activate this connection/ });
    expect(dialog.textContent).toMatch(/Nothing is written to the PMS/);
    await userEvent.type(within(dialog).getByLabelText(/Confirm your password/), "pw");
    await userEvent.click(within(dialog).getByRole("button", { name: "Activate" }));

    await waitFor(() => expect(post).toHaveBeenCalled());
    expect(post.mock.calls[0]).toEqual(["/pms-interfaces/i1/lifecycle", { state: "ACTIVE", reason_code: "COMMISSIONING", password: "pw" }]);
  });

  it("explains a refused password instead of printing the server's code", async () => {
    mockBackend({ interfaces: [{ ...protel, lifecycle_state: "AUTH_DISABLED" }] });
    post.mockRejectedValue(Object.assign(new Error("password confirmation required"), { code: "reauth_required", status: 401 }));
    await renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "Manage" }));
    await userEvent.click(await screen.findByRole("tab", { name: "Actions" }));
    await userEvent.click(await screen.findByRole("button", { name: "Activate" }));
    const dialog = await screen.findByRole("dialog", { name: /Activate this connection/ });
    await userEvent.type(within(dialog).getByLabelText(/Confirm your password/), "wrong");
    await userEvent.click(within(dialog).getByRole("button", { name: "Activate" }));

    const alert = await within(dialog).findByRole("alert");
    expect(alert.textContent).toMatch(/Your password was not accepted/);
    expect(alert.textContent).not.toMatch(/reauth_required/);
  });

  it("does not offer to retire a connection, because the server does not allow it here", async () => {
    mockBackend();
    await renderPage();
    await userEvent.click(await screen.findByRole("button", { name: "Manage" }));
    await userEvent.click(await screen.findByRole("tab", { name: "Actions" }));
    await screen.findByRole("button", { name: "Pause room sign-in" });
    expect(screen.queryByRole("button", { name: /decommission|retire/i })).toBeNull();
  });
});

describe("operator wording", () => {
  it("renders no internal or development vocabulary on the overview or in any tab of the sheet", async () => {
    mockBackend({
      providers: [PROTEL_PROVIDER, REST_PROVIDER],
      health: { i1: { ...healthOk, room_auth_ready: false, room_auth_reason: "CONTINUITY_GAP", continuity_status: "GAP_DETECTED",
        sync_status: "RESYNC_REQUIRED", review_events: 2, pending_events: 3, oldest_pending_at: new Date(Date.now() - 7200_000).toISOString() } },
    });
    const { baseElement } = await renderPage();
    await screen.findByText("Main PMS");
    const texts = [baseElement.textContent ?? ""];
    await userEvent.click(screen.getByRole("button", { name: "Manage" }));
    for (const tab of ["Overview", "Configuration", "Guest networks", "History", "Actions"]) {
      await userEvent.click(await screen.findByRole("tab", { name: tab }));
      await waitFor(() => expect(screen.getByRole("tabpanel")).toBeTruthy());
      texts.push(baseElement.textContent ?? "");
    }
    for (const t of texts) {
      // Match case-sensitively for the enum shape, case-insensitively for the words.
      expect(t.match(/\b[A-Z]{2,}_[A-Z_]{2,}\b/)).toBeNull();
      expect(t.match(/\bphase\b|phase-\d|controlled validation|\(no PMS\)|iam_v2|\bT0\d{3}\b/i)).toBeNull();
    }
  });
});

// EDITING A SAVED WEB-API CONNECTION STARTS FROM WHAT WAS SAVED. edged stores a REST provider's settings under
// config.provider (pmsprovider.RESTConfig.StoredConfig); `provider_config` is only the request field. Reading
// the request name started every Mews/Apaleo/OPERA edit from defaults and blanks -- dropping required property
// ids and silently resetting optional scoping. The revision below has the exact shape StoredConfig writes.
describe("editing a saved web-API connection", () => {
  const storedRevision = {
    source_timezone: "Europe/Berlin",
    config: {
      endpoint: "https://api.example.test",
      resync_supported: true,
      auth: { credential_mode: "AUTH_KEY", read_only: true },
      provider: { base_url: "https://api.example.test", property_id: "BER", poll_interval_s: 300 },
      heartbeat_interval_ms: 300000,
    },
  };

  it("reloads every persisted provider value instead of defaults or blanks", async () => {
    const { valuesFromRevision } = await import("@/lib/api/pms-connections");
    const { values, timezone } = valuesFromRevision(REST_PROVIDER as any, storedRevision);
    expect(values.base_url).toBe("https://api.example.test");
    expect(values.property_id).toBe("BER");
    expect(String(values.poll_interval_s)).toBe("300"); // not the catalogue default of 60
    expect(timezone).toBe("Europe/Berlin");
  });

  it("shows the persisted values in the configuration summary", async () => {
    const { ConfigSummary } = await import("@/app/(app)/pms-interfaces/provider-fields");
    render(<ConfigSummary provider={REST_PROVIDER as any} rev={storedRevision} />);
    expect(screen.getByText("BER")).toBeInTheDocument();
    expect(screen.getByText("300 seconds")).toBeInTheDocument();
  });
});
