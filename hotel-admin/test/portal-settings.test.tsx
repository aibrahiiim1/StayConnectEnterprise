import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";

// PORTAL SETTINGS — a hotel's settings page, not a revision manager.
//
// What is asserted here is mostly ABSENCE: the operator-facing vocabulary that was removed. A test that only
// checked the new controls would pass just as happily with the old draft/publish/version machinery still
// sitting beside them, which is exactly what the Product Owner rejected.

const get = vi.fn();
const put = vi.fn();
const post = vi.fn();
const del = vi.fn();
const upload = vi.fn();
vi.mock("@/lib/api", () => ({
  api: {
    get: (...a: any[]) => get(...a),
    put: (...a: any[]) => put(...a),
    post: (...a: any[]) => post(...a),
    del: (...a: any[]) => del(...a),
    upload: (...a: any[]) => upload(...a),
  },
  ApiError: class ApiError extends Error {
    status: number; code: string;
    constructor(status: number, body: any) { super(body?.message ?? "err"); this.status = status; this.code = body?.error ?? "http_error"; }
  },
}));

// The preview renders a real browser frame and fetches the portal page; it has its own reasoning and is not
// what these assertions are about.
vi.mock("@/app/(app)/portal-branding/preview", () => ({
  PortalPreview: ({ design }: any) => <div data-testid="preview" data-hotel={design.hotel_name ?? ""} />,
}));

beforeEach(() => { get.mockReset(); put.mockReset(); post.mockReset(); del.mockReset(); upload.mockReset(); });
afterEach(() => vi.resetModules());

function mock(design: Record<string, any> = {}, draft: Record<string, any> = {}, assets: any[] = []) {
  get.mockImplementation((p: string) => {
    if (p === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
    if (p === "/portal-branding") return Promise.resolve({ design, draft, revisions: [], published: true });
    if (p === "/portal-assets") return Promise.resolve({ data: assets, meta: { has_more: false } });
    return Promise.resolve({});
  });
}

async function renderPage() {
  const Page = (await import("@/app/(app)/portal-branding/page")).default;
  render(<Page />);
  return screen.findByRole("tab", { name: /General/ });
}

describe("the page speaks a hotel's language, not a release manager's", () => {
  it("offers four sections an operator arrives with", async () => {
    mock({ hotel_name: "Coral Sea" });
    await renderPage();
    for (const t of ["General", "Branding", "Languages", "Advanced"]) {
      expect(screen.getByRole("tab", { name: new RegExp(t) })).toBeTruthy();
    }
  });

  it("never mentions drafts, publishing, versions or rollback", async () => {
    mock({ hotel_name: "Coral Sea", custom_css: ".a{}" });
    await renderPage();
    const body = document.body.textContent ?? "";
    for (const word of [/\bdraft\b/i, /\bpublish/i, /\bversion\b/i, /\broll ?back\b/i, /\blive version\b/i]) {
      expect(body).not.toMatch(word);
    }
  });

  it("saves through one button, and offers nothing to save until something changed", async () => {
    mock({ hotel_name: "Coral Sea" });
    await renderPage();
    const save = screen.getByRole("button", { name: /Save changes/i }) as HTMLButtonElement;
    expect(save.disabled).toBe(true);

    fireEvent.change(screen.getByLabelText(/Hotel name/i), { target: { value: "Coral Sea Resort" } });
    expect((screen.getByRole("button", { name: /Save changes/i }) as HTMLButtonElement).disabled).toBe(false);

    post.mockResolvedValue({ saved: true });
    fireEvent.click(screen.getByRole("button", { name: /Save changes/i }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    const [path, body] = post.mock.calls[0];
    expect(path).toBe("/portal-branding/settings");
    expect(body.design.hotel_name).toBe("Coral Sea Resort");
    // No password for an ordinary settings change.
    expect(body.password).toBeUndefined();
  });

  it("discards back to what guests are seeing", async () => {
    mock({ hotel_name: "Coral Sea" });
    await renderPage();
    fireEvent.change(screen.getByLabelText(/Hotel name/i), { target: { value: "Something else" } });
    fireEvent.click(screen.getByRole("button", { name: /Discard/i }));
    await waitFor(() =>
      expect((screen.getByLabelText(/Hotel name/i) as HTMLInputElement).value).toBe("Coral Sea"));
  });
});

describe("the password step-up is asked for where it matters", () => {
  it("does not ask when only ordinary settings changed", async () => {
    mock({ hotel_name: "Coral Sea" });
    await renderPage();
    fireEvent.change(screen.getByLabelText(/Hotel name/i), { target: { value: "X" } });
    expect(screen.queryByLabelText(/Confirm your password/i)).toBeNull();
  });

  it("asks, and sends it, when the custom CSS or HTML changed", async () => {
    mock({ hotel_name: "Coral Sea" });
    await renderPage();
    fireEvent.click(screen.getByRole("tab", { name: /Advanced/ }));
    fireEvent.change(screen.getByLabelText(/Custom CSS/i), { target: { value: ".card { border: 0 }" } });

    const pw = await screen.findByLabelText(/Confirm your password/i);
    fireEvent.change(pw, { target: { value: "hunter2" } });
    post.mockResolvedValue({ saved: true });
    fireEvent.click(screen.getByRole("button", { name: /Save changes/i }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    expect(post.mock.calls[0][1].password).toBe("hunter2");
  });
});

describe("logo and background are really uploadable", () => {
  it("uploads through the API client rather than a hand-written URL", async () => {
    // The defect this replaces: the only call in the admin that wrote its own URL dropped the /api prefix,
    // so Next redirected the POST to the login page and the operator was told the image was refused.
    mock({});
    upload.mockResolvedValue({ name: "abc.png", url: "/assets/abc.png", size_bytes: 10 });
    await renderPage();
    fireEvent.click(screen.getByRole("tab", { name: /Branding/ }));

    const file = new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], "logo.png", { type: "image/png" });
    fireEvent.change(screen.getByLabelText(/Upload logo/i), { target: { files: [file] } });

    await waitFor(() => expect(upload).toHaveBeenCalledTimes(1));
    expect(upload.mock.calls[0][0]).toBe("/portal-assets");
    expect(upload.mock.calls[0][1]).toBe(file);
  });

  it("shows what is currently set, through the operator API", async () => {
    // /assets/<name> is a guest-network path and resolves to nothing from the admin origin, which is why the
    // old screen's thumbnail was a broken image.
    mock({ logo_url: "/assets/abc.png" });
    await renderPage();
    fireEvent.click(screen.getByRole("tab", { name: /Branding/ }));
    const img = await screen.findByAltText(/Logo currently set/i);
    expect(img.getAttribute("src")).toBe("/api/edge/v1/portal-assets/abc.png/raw");
  });

  it("offers replace and remove once an image is set", async () => {
    mock({ background_url: "/assets/bg.jpg" });
    await renderPage();
    fireEvent.click(screen.getByRole("tab", { name: /Branding/ }));
    expect(screen.getByRole("button", { name: /Remove background photograph/i })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /Remove background photograph/i }));
    await waitFor(() =>
      expect(screen.queryByAltText(/Background photograph currently set/i)).toBeNull());
  });

  it("says why SVG is refused, before an operator tries it", async () => {
    mock({});
    await renderPage();
    fireEvent.click(screen.getByRole("tab", { name: /Branding/ }));
    expect(screen.getByText(/SVG is refused: it can carry script/i)).toBeTruthy();
  });
});

describe("languages", () => {
  it("separates who sees a language from who edits its words", async () => {
    mock({});
    await renderPage();
    fireEvent.click(screen.getByRole("tab", { name: /Languages/ }));

    // Availability: a checkbox per shipped language, English fixed.
    for (const l of ["English", "العربية", "Deutsch", "Français", "Italiano", "Русский"]) {
      expect(screen.getByLabelText(new RegExp(`Offer ${l} to guests`))).toBeTruthy();
    }
    expect((screen.getByLabelText(/Offer English to guests/) as HTMLInputElement).disabled).toBe(true);

    // Editing: one language at a time.
    fireEvent.click(screen.getByRole("tab", { name: /Italiano/ }));
    expect(await screen.findByLabelText(/Room Number in Italiano/)).toBeTruthy();
    expect(screen.queryByLabelText(/Room Number in Deutsch/)).toBeNull();
  });

  it("only sends the languages the operator ticked", async () => {
    mock({ hotel_name: "Coral Sea" });
    post.mockResolvedValue({ saved: true });
    await renderPage();
    fireEvent.click(screen.getByRole("tab", { name: /Languages/ }));
    fireEvent.click(screen.getByLabelText(/Offer Русский to guests/));
    fireEvent.click(screen.getByLabelText(/Offer Deutsch to guests/));

    fireEvent.click(screen.getByRole("button", { name: /Save changes/i }));
    await waitFor(() => expect(post).toHaveBeenCalledTimes(1));
    const codes = post.mock.calls[0][1].design.languages.map((l: any) => l.code);
    expect(codes).toContain("en");
    expect(codes).not.toContain("ru");
    expect(codes).not.toContain("de");
  });

  it("counts what an added language will show in English", async () => {
    mock({});
    await renderPage();
    fireEvent.click(screen.getByRole("tab", { name: /Languages/ }));
    fireEvent.change(screen.getByLabelText(/Language code/i), { target: { value: "es" } });
    fireEvent.change(screen.getByLabelText(/Shown as/i), { target: { value: "Español" } });
    fireEvent.click(screen.getByRole("button", { name: /^Add$/ }));

    const tab = await screen.findByRole("tab", { name: /Español/ });
    expect(within(tab).getByText(/\d+ in English/)).toBeTruthy();
    // A shipped language is never missing anything, so it never carries that badge.
    expect(within(screen.getByRole("tab", { name: /Italiano/ })).queryByText(/in English/)).toBeNull();
  });

  it("types Arabic right to left", async () => {
    mock({});
    await renderPage();
    fireEvent.click(screen.getByRole("tab", { name: /Languages/ }));
    fireEvent.click(screen.getByRole("tab", { name: /العربية/ }));
    const field = await screen.findByLabelText(/Room Number in العربية/);
    expect(field.getAttribute("dir")).toBe("rtl");
  });
});

describe("the hotel's own words reach the portal", () => {
  it("offers the content fields the portal now renders", async () => {
    mock({});
    await renderPage();
    expect(screen.getByLabelText(/Welcome line/i)).toBeTruthy();
    expect(screen.getByLabelText(/Help line/i)).toBeTruthy();
    expect(screen.getByLabelText(/Terms of use link/i)).toBeTruthy();
  });
});
