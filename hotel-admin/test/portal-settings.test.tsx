import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { render, screen, fireEvent, waitFor, within } from "@testing-library/react";

// PORTAL SETTINGS — a designer for a hotel's sign-in page, not a revision manager.
//
// Much of what is asserted here is ABSENCE: the operator-facing vocabulary that was removed, and the password
// prompt on ordinary changes. A test that only checked the new controls would pass just as happily with the old
// draft/publish/version machinery still sitting beside them, which is exactly what the Product Owner rejected.

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

// The preview renders real browser frames and fetches the portal page; it has its own reasoning (and its own
// browser tests in e2e/) and is not what these assertions are about.
vi.mock("@/app/(app)/portal-branding/preview", () => ({
  PortalPreview: ({ design, sanitized }: any) => (
    <div data-testid="preview" data-hotel={design.hotel_name ?? ""} data-template={design.template_id ?? ""}
      data-html={sanitized?.custom_html ?? ""} />
  ),
  PortalFrame: ({ title }: any) => <div data-testid="thumb" data-title={title} />,
  buildSrcDoc: () => "<html></html>",
  usePortalHTML: () => ({ html: "<html></html>", err: null }),
  useInlinedDesign: (d: any) => d,
  useSettled: (v: any) => v,
}));

beforeEach(() => { get.mockReset(); put.mockReset(); post.mockReset(); del.mockReset(); upload.mockReset(); mockPost(); });
afterEach(() => vi.resetModules());

/** A slice of what the portal really ships, enough to prove the screen shows it rather than English. */
const SHIPPED = {
  languages: [
    { code: "en", label: "English" },
    { code: "ar", label: "العربية", rtl: true },
    { code: "de", label: "Deutsch" },
    { code: "fr", label: "Français" },
    { code: "it", label: "Italiano" },
    { code: "ru", label: "Русский" },
  ],
  strings: {
    en: { "pms.room": "Room Number", "btn.submit": "Submit", "tab.guest": "Guest Login" },
    ar: { "pms.room": "رقم الغرفة", "btn.submit": "إرسال", "tab.guest": "تسجيل دخول النزلاء" },
    de: { "pms.room": "Zimmernummer", "btn.submit": "Senden", "tab.guest": "Gäste-Anmeldung" },
    fr: { "pms.room": "Numéro de chambre", "btn.submit": "Envoyer", "tab.guest": "Connexion client" },
    it: { "pms.room": "Numero di camera", "btn.submit": "Invia", "tab.guest": "Accesso ospiti" },
    ru: { "pms.room": "Номер комнаты", "btn.submit": "Отправить", "tab.guest": "Вход для гостей" },
  },
};

function mock(design: Record<string, any> = {}, draft: Record<string, any> = {}, assets: any[] = [], revisions: any[] = []) {
  get.mockImplementation((p: string) => {
    if (p === "/auth/whoami") return Promise.resolve({ roles: ["site_admin"] });
    if (p === "/portal-branding") return Promise.resolve({ design, draft, revisions, published: true });
    if (p === "/portal-branding/languages") return Promise.resolve(SHIPPED);
    if (p === "/portal-assets") return Promise.resolve({ data: assets, meta: { has_more: false } });
    return Promise.resolve({});
  });
}

type Verdict = { ok: boolean; issues: any[]; sanitized: { custom_css: string; custom_html: string } };

/** POST answers: /validate from `verdict` (clean by default), everything else succeeds. */
function mockPost(verdict?: (design: any) => Verdict, onSave?: (path: string, body: any) => any) {
  post.mockImplementation((p: string, body: any) => {
    if (p === "/portal-branding/validate") {
      const d = body?.design ?? {};
      return Promise.resolve(verdict ? verdict(d) : {
        ok: true, issues: [], sanitized: { custom_css: d.custom_css ?? "", custom_html: d.custom_html ?? "" },
      });
    }
    if (onSave) return onSave(p, body);
    return Promise.resolve({ saved: true });
  });
}

const saves = () => post.mock.calls.filter((c) => c[0] === "/portal-branding/settings");

async function renderPage() {
  const Page = (await import("@/app/(app)/portal-branding/page")).default;
  render(<Page />);
  return screen.findByRole("tab", { name: /Template/ });
}

const open = (name: RegExp) => fireEvent.click(screen.getByRole("tab", { name }));

describe("the page speaks a hotel's language, not a release manager's", () => {
  it("offers the designer's sections", async () => {
    mock({ hotel_name: "Semantics Demo" });
    await renderPage();
    for (const t of ["Template", "Brand", "Content", "Sign-in page text", "Languages", "Advanced HTML & CSS", "History"]) {
      expect(screen.getByRole("tab", { name: new RegExp(t) })).toBeTruthy();
    }
  });

  it("never mentions drafts, publishing, versions or rollback", async () => {
    mock({ hotel_name: "Semantics Demo", custom_css: ".a{}" }, {}, [], [
      { version: 3, published_at: "2026-09-20T10:00:00Z", published_by: "a@x", note: "rolled back to version 1" },
      { version: 2, published_at: "2026-09-19T10:00:00Z", published_by: "a@x" },
    ]);
    await renderPage();
    for (const s of [/Template/, /Content/, /History/]) {
      open(s);
      const body = document.body.textContent ?? "";
      for (const word of [/\bdraft\b/i, /\bpublish/i, /\bversion\b/i, /\broll ?back\b/i, /\blive version\b/i]) {
        expect(body).not.toMatch(word);
      }
    }
  });

  it("saves through one button, and offers nothing to save until something changed", async () => {
    mock({ hotel_name: "Semantics Demo" });
    await renderPage();
    expect((screen.getByRole("button", { name: /Save changes/i }) as HTMLButtonElement).disabled).toBe(true);

    open(/Content/);
    fireEvent.change(screen.getByLabelText(/Hotel name/i), { target: { value: "Semantics Demo Hotel" } });
    await waitFor(() => expect((screen.getByRole("button", { name: /Save changes/i }) as HTMLButtonElement).disabled).toBe(false));

    fireEvent.click(screen.getByRole("button", { name: /Save changes/i }));
    await waitFor(() => expect(saves()).toHaveLength(1));
    const body = saves()[0][1];
    expect(body.design.hotel_name).toBe("Semantics Demo Hotel");
    // No password for an ordinary settings change.
    expect(body.password).toBeUndefined();
  });

  it("discards back to what guests are seeing", async () => {
    mock({ hotel_name: "Semantics Demo" });
    await renderPage();
    open(/Content/);
    fireEvent.change(screen.getByLabelText(/Hotel name/i), { target: { value: "Something else" } });
    fireEvent.click(screen.getByRole("button", { name: /Discard/i }));
    await waitFor(() =>
      expect((screen.getByLabelText(/Hotel name/i) as HTMLInputElement).value).toBe("Semantics Demo"));
  });
});

describe("templates", () => {
  it("offers six layouts, with the original as the default", async () => {
    mock({ hotel_name: "Semantics Demo" });
    await renderPage();
    const group = screen.getByRole("radiogroup", { name: /Page template/ });
    const radios = within(group).getAllByRole("radio");
    expect(radios.length).toBeGreaterThanOrEqual(5);
    expect((within(group).getByRole("radio", { name: /Classic/ }) as HTMLInputElement).checked).toBe(true);
    // Every card previews the REAL page, not an illustration.
    expect(screen.getAllByTestId("thumb").length).toBe(radios.length);
  });

  it("choosing a layout reaches the preview and saves with no password", async () => {
    mock({ hotel_name: "Semantics Demo" });
    await renderPage();
    fireEvent.click(screen.getByRole("radio", { name: /Split/ }));
    expect(screen.getByTestId("preview").getAttribute("data-template")).toBe("split");
    // The options follow the layout: a split layout has a photograph and a side to put the sign-in on.
    expect(screen.getByRole("radiogroup", { name: /Sign-in panel position/ })).toBeTruthy();
    expect(screen.getByLabelText(/Upload hero photograph/i)).toBeTruthy();

    await waitFor(() => expect((screen.getByRole("button", { name: /Save changes/i }) as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByRole("button", { name: /Save changes/i }));
    await waitFor(() => expect(saves()).toHaveLength(1));
    expect(saves()[0][1].design.template_id).toBe("split");
    expect(saves()[0][1].password).toBeUndefined();
    expect(screen.queryByLabelText(/Confirm your password/i)).toBeNull();
  });

  it("layout options are closed choices", async () => {
    mock({ template_id: "immersive" });
    await renderPage();
    const pos = screen.getByRole("radiogroup", { name: /Sign-in panel position/ });
    fireEvent.click(within(pos).getByRole("radio", { name: "Centre" }));
    fireEvent.click(within(screen.getByRole("radiogroup", { name: /Panel surface/ })).getByRole("radio", { name: "Solid" }));
    await waitFor(() => expect((screen.getByRole("button", { name: /Save changes/i }) as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByRole("button", { name: /Save changes/i }));
    await waitFor(() => expect(saves()).toHaveLength(1));
    expect(saves()[0][1].design.template_options).toEqual({ panel_position: "center", surface: "solid" });
  });
});

describe("the password step-up is asked for where it matters", () => {
  it("does not ask when only ordinary settings changed", async () => {
    mock({ hotel_name: "Semantics Demo" });
    await renderPage();
    open(/Content/);
    fireEvent.change(screen.getByLabelText(/Hotel name/i), { target: { value: "X" } });
    await waitFor(() => expect((screen.getByRole("button", { name: /Save changes/i }) as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByRole("button", { name: /Save changes/i }));
    await waitFor(() => expect(saves()).toHaveLength(1));
    expect(screen.queryByLabelText(/Confirm your password/i)).toBeNull();
  });

  it("asks, and sends it, when the custom CSS or HTML changed", async () => {
    mock({ hotel_name: "Semantics Demo" });
    await renderPage();
    open(/Advanced/);
    fireEvent.change(screen.getByLabelText(/Custom CSS/i), { target: { value: ".card { border: 0 }" } });
    expect(await screen.findByText(/Saving will ask for your password/i)).toBeTruthy();

    await waitFor(() => expect((screen.getAllByRole("button", { name: /Save changes/i })[0] as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getAllByRole("button", { name: /Save changes/i })[0]);
    // Nothing is saved until the password is given, and it is given in a masked field.
    const pw = await screen.findByLabelText(/Confirm your password/i) as HTMLInputElement;
    expect(pw.type).toBe("password");
    expect(saves()).toHaveLength(0);
    fireEvent.change(pw, { target: { value: "hunter2" } });
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: /Save changes/i }));
    await waitFor(() => expect(saves()).toHaveLength(1));
    expect(saves()[0][1].password).toBe("hunter2");
  });

  it("asks when the custom CSS is CLEARED, too", async () => {
    // Emptying the stylesheet is still a change to what the page carries; the rule holds in both directions.
    mock({ hotel_name: "Semantics Demo", custom_css: ".card { border: 0 }" });
    await renderPage();
    open(/Advanced/);
    fireEvent.change(screen.getByLabelText(/Custom CSS/i), { target: { value: "" } });
    await waitFor(() => expect((screen.getAllByRole("button", { name: /Save changes/i })[0] as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getAllByRole("button", { name: /Save changes/i })[0]);
    expect(await screen.findByLabelText(/Confirm your password/i)).toBeTruthy();
    expect(saves()).toHaveLength(0);
  });

  it("opens the password prompt when the server asks for it", async () => {
    mock({ hotel_name: "Semantics Demo" });
    const { ApiError } = await import("@/lib/api");
    mockPost(undefined, () => Promise.reject(new (ApiError as any)(401, { error: "reauth_required", message: "confirm your password" })));
    await renderPage();
    open(/Content/);
    fireEvent.change(screen.getByLabelText(/Hotel name/i), { target: { value: "X" } });
    await waitFor(() => expect((screen.getByRole("button", { name: /Save changes/i }) as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByRole("button", { name: /Save changes/i }));
    expect(await screen.findByLabelText(/Confirm your password/i)).toBeTruthy();
  });
});

describe("the server's verdict on the Advanced fields", () => {
  const hostile = '<p>Pool</p><img src=x onerror="steal()">';
  const verdict = (d: any): Verdict => d.custom_html === hostile
    ? {
      ok: false,
      issues: [{ field: "custom_html", severity: "error", message: "removed the onerror attribute on <img>: inline script is not allowed" }],
      sanitized: { custom_css: "", custom_html: '<p>Pool</p><img src="x">' },
    }
    : { ok: true, issues: [], sanitized: { custom_css: d.custom_css ?? "", custom_html: d.custom_html ?? "" } };

  it("names what the portal would remove, previews the safe version, and offers it", async () => {
    mockPost(verdict);
    mock({ hotel_name: "Semantics Demo" });
    await renderPage();
    open(/Advanced/);
    fireEvent.change(screen.getByLabelText(/Custom HTML/i), { target: { value: hostile } });

    expect(await screen.findAllByText(/removed the onerror attribute on <img>/)).not.toHaveLength(0);
    // The preview renders what a guest would actually get.
    expect(screen.getByTestId("preview").getAttribute("data-html")).toBe('<p>Pool</p><img src="x">');
    // A design the portal would change cannot be saved as written.
    expect((screen.getAllByRole("button", { name: /Save changes/i })[0] as HTMLButtonElement).disabled).toBe(true);

    fireEvent.click(screen.getByRole("button", { name: /Use the cleaned version/i }));
    expect((screen.getByLabelText(/Custom HTML/i) as HTMLTextAreaElement).value).toBe('<p>Pool</p><img src="x">');
    await waitFor(() => expect(screen.queryByText(/removed the onerror attribute/)).toBeNull());
  });

  it("shows a field problem beside the field it belongs to", async () => {
    mockPost((d) => ({
      ok: !d.terms_url?.startsWith("javascript"),
      issues: d.terms_url?.startsWith("javascript")
        ? [{ field: "terms_url", severity: "error", message: "must be an https:// address or a path on this appliance starting with /" }]
        : [],
      sanitized: { custom_css: "", custom_html: "" },
    }));
    mock({});
    await renderPage();
    open(/Content/);
    fireEvent.change(screen.getByLabelText(/Terms of use link/i), { target: { value: "javascript:alert(1)" } });
    await waitFor(() => expect(screen.getByLabelText(/Terms of use link/i).getAttribute("aria-invalid")).toBe("true"));
    expect(screen.getAllByText(/must be an https:\/\/ address/).length).toBeGreaterThan(0);
  });
});

describe("brand", () => {
  it("uploads through the API client rather than a hand-written URL", async () => {
    mock({});
    upload.mockResolvedValue({ name: "abc.png", url: "/assets/abc.png", size_bytes: 10 });
    await renderPage();
    open(/Brand/);
    const file = new File([new Uint8Array([0x89, 0x50, 0x4e, 0x47])], "logo.png", { type: "image/png" });
    fireEvent.change(screen.getByLabelText(/Upload logo/i), { target: { files: [file] } });
    await waitFor(() => expect(upload).toHaveBeenCalledTimes(1));
    expect(upload.mock.calls[0][0]).toBe("/portal-assets");
    expect(upload.mock.calls[0][1]).toBe(file);
  });

  it("shows what is currently set, through the operator API", async () => {
    mock({ logo_url: "/assets/abc.png" });
    await renderPage();
    open(/Brand/);
    const img = await screen.findByAltText(/Logo currently set/i);
    expect(img.getAttribute("src")).toBe("/api/edge/v1/portal-assets/abc.png/raw");
  });

  it("offers replace and remove once an image is set", async () => {
    mock({ background_url: "/assets/bg.jpg" });
    await renderPage();
    open(/Brand/);
    fireEvent.click(screen.getByRole("button", { name: /Remove background photograph/i }));
    await waitFor(() => expect(screen.queryByAltText(/Background photograph currently set/i)).toBeNull());
  });

  it("says why SVG is refused, before an operator tries it", async () => {
    mock({});
    await renderPage();
    open(/Brand/);
    expect(screen.getByText(/SVG is refused: it can carry script/i)).toBeTruthy();
  });

  it("warns when the brand colour is too light to read white button text on", async () => {
    mock({ brand_color: "#f5e663" });
    await renderPage();
    open(/Brand/);
    expect(screen.getByText(/White button text on your brand colour: .*below the 4\.5:1/)).toBeTruthy();
    expect(screen.getByText(/Your text colour on the white card: .*:1$/)).toBeTruthy();
  });
});

describe("languages and wording", () => {
  it("separates who sees a language from whose words are being edited", async () => {
    mock({});
    await renderPage();
    open(/^Languages/);
    for (const l of ["English", "العربية", "Deutsch", "Français", "Italiano", "Русский"]) {
      expect(screen.getByLabelText(new RegExp(`Offer ${l} to guests`))).toBeTruthy();
    }
    expect((screen.getByLabelText(/Offer English to guests/) as HTMLInputElement).disabled).toBe(true);
    // The wording is its own section.
    expect(screen.queryByLabelText(/Room Number in English/)).toBeNull();
  });

  it("SHOWS the shipped wording for the language being edited", async () => {
    mock({});
    await renderPage();
    open(/Sign-in page text/);
    fireEvent.click(screen.getByRole("tab", { name: /Italiano/ }));
    const room = await screen.findByLabelText(/Room Number in Italiano/) as HTMLInputElement;
    expect(room.value).toBe("Numero di camera");
    expect(room.placeholder).toBe("Room Number");
    fireEvent.click(screen.getByRole("tab", { name: /Deutsch/ }));
    expect((await screen.findByLabelText(/Room Number in Deutsch/) as HTMLInputElement).value).toBe("Zimmernummer");
    expect(screen.queryByLabelText(/Room Number in Italiano/)).toBeNull();
  });

  it("stores an override only when the wording actually differs", async () => {
    mock({ hotel_name: "Semantics Demo" });
    await renderPage();
    open(/Sign-in page text/);
    fireEvent.click(screen.getByRole("tab", { name: /Italiano/ }));
    const room = await screen.findByLabelText(/Room Number in Italiano/);
    fireEvent.change(room, { target: { value: "Numero di camera" } });
    expect((screen.getByRole("button", { name: /Save changes/i }) as HTMLButtonElement).disabled).toBe(true);

    fireEvent.change(room, { target: { value: "Camera n." } });
    await waitFor(() => expect((screen.getByRole("button", { name: /Save changes/i }) as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByRole("button", { name: /Save changes/i }));
    await waitFor(() => expect(saves()).toHaveLength(1));
    const tr = saves()[0][1].design.translations;
    expect(tr.it["pms.room"]).toBe("Camera n.");
    expect(Object.keys(tr.it)).toEqual(["pms.room"]);
  });

  it("removes the override when the wording is put back", async () => {
    mock({ translations: { it: { "pms.room": "Camera n." } } });
    await renderPage();
    open(/Sign-in page text/);
    fireEvent.click(screen.getByRole("tab", { name: /Italiano/ }));
    const room = await screen.findByRole("textbox", { name: "Room Number in Italiano" }) as HTMLInputElement;
    expect(room.value).toBe("Camera n.");
    fireEvent.click(screen.getByRole("button", { name: /Reset Room Number in Italiano/ }));
    await waitFor(() =>
      expect((screen.getByRole("textbox", { name: "Room Number in Italiano" }) as HTMLInputElement).value).toBe("Numero di camera"));
    await waitFor(() => expect((screen.getByRole("button", { name: /Save changes/i }) as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByRole("button", { name: /Save changes/i }));
    await waitFor(() => expect(saves()).toHaveLength(1));
    expect(saves()[0][1].design.translations.it).toBeUndefined();
  });

  it("says which languages are untouched and which the hotel has changed", async () => {
    mock({ translations: { de: { "btn.submit": "Los" } } });
    await renderPage();
    open(/Sign-in page text/);
    expect(within(screen.getByRole("tab", { name: /Italiano/ })).getByText("Built-in")).toBeTruthy();
    expect(within(screen.getByRole("tab", { name: /Deutsch/ })).getByText(/1 customised/)).toBeTruthy();
  });

  it("adds a language and goes straight to its wording", async () => {
    mock({});
    await renderPage();
    open(/^Languages/);
    fireEvent.change(screen.getByLabelText(/Language code/i), { target: { value: "es" } });
    fireEvent.change(screen.getByLabelText(/Shown as/i), { target: { value: "Español" } });
    fireEvent.click(screen.getByRole("button", { name: /^Add$/ }));

    const tab = await screen.findByRole("tab", { name: /Español/ });
    expect(tab.getAttribute("aria-selected")).toBe("true");
    expect(within(tab).getByText(/\d+ in English/)).toBeTruthy();
    expect(within(screen.getByRole("tab", { name: /Italiano/ })).queryByText(/in English/)).toBeNull();
  });

  it("types Arabic right to left", async () => {
    mock({});
    await renderPage();
    open(/Sign-in page text/);
    fireEvent.click(screen.getByRole("tab", { name: /العربية/ }));
    const field = await screen.findByLabelText(/Room Number in العربية/) as HTMLInputElement;
    expect(field.getAttribute("dir")).toBe("rtl");
    expect(field.value).toBe("رقم الغرفة");
  });

  it("finds a string without scrolling through fifty", async () => {
    mock({});
    await renderPage();
    open(/Sign-in page text/);
    fireEvent.change(screen.getByLabelText(/Find a string/i), { target: { value: "voucher code" } });
    expect(await screen.findByLabelText(/Voucher Code in English/)).toBeTruthy();
    expect(screen.queryByLabelText(/Room Number in English/)).toBeNull();
  });

  it("only sends the languages the operator ticked", async () => {
    mock({ hotel_name: "Semantics Demo" });
    await renderPage();
    open(/^Languages/);
    fireEvent.click(screen.getByLabelText(/Offer Русский to guests/));
    fireEvent.click(screen.getByLabelText(/Offer Deutsch to guests/));
    await waitFor(() => expect((screen.getByRole("button", { name: /Save changes/i }) as HTMLButtonElement).disabled).toBe(false));
    fireEvent.click(screen.getByRole("button", { name: /Save changes/i }));
    await waitFor(() => expect(saves()).toHaveLength(1));
    const codes = saves()[0][1].design.languages.map((l: any) => l.code);
    expect(codes).toContain("en");
    expect(codes).not.toContain("ru");
    expect(codes).not.toContain("de");
  });
});

describe("the hotel's own words reach the portal", () => {
  it("offers the content fields the portal renders, as single fields (not per language)", async () => {
    mock({});
    await renderPage();
    open(/Content/);
    expect(screen.getByLabelText(/Welcome line/i)).toBeTruthy();
    expect(screen.getByLabelText(/Help line/i)).toBeTruthy();
    expect(screen.getByLabelText(/Terms of use link/i)).toBeTruthy();
    expect(screen.getAllByLabelText(/Welcome line/i)).toHaveLength(1);
  });
});

describe("history", () => {
  it("lists earlier saves and restores one only with the password", async () => {
    mock({ hotel_name: "Semantics Demo" }, {}, [], [
      { version: 4, published_at: "2026-09-23T10:00:00Z", published_by: "front@hotel" },
      { version: 3, published_at: "2026-09-20T10:00:00Z", published_by: "gm@hotel", note: "rolled back to version 1" },
    ]);
    await renderPage();
    open(/History/);
    expect(screen.getByText("Save #4")).toBeTruthy();
    expect(screen.getByText(/Guests see this/)).toBeTruthy();
    expect(screen.getByText(/Restored save #1/)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: /Restore/ }));
    const dialog = await screen.findByRole("dialog");
    const pw = within(dialog).getByLabelText(/Confirm your password/i) as HTMLInputElement;
    expect(pw.type).toBe("password");
    const confirm = within(dialog).getByRole("button", { name: /^Restore$/ }) as HTMLButtonElement;
    expect(confirm.disabled).toBe(true);
    fireEvent.change(pw, { target: { value: "hunter2" } });
    fireEvent.click(confirm);
    await waitFor(() => expect(post.mock.calls.some((c) => c[0] === "/portal-branding/rollback/3")).toBe(true));
    expect(post.mock.calls.find((c) => c[0] === "/portal-branding/rollback/3")![1]).toEqual({ password: "hunter2" });
  });
});

describe("unsaved work is kept only when the server would keep it (found on PRE-LIVE)", () => {
  const drafts = () => put.mock.calls.filter((c) => c[0] === "/portal-branding/draft");
  const refuseBase = (d: any): Verdict => ({
    ok: !/<base/i.test(d.custom_html ?? ""),
    issues: /<base/i.test(d.custom_html ?? "")
      ? [{ field: "custom_html", severity: "error", message: "removed <base> and its content" }]
      : [],
    sanitized: { custom_css: d.custom_css ?? "", custom_html: (d.custom_html ?? "").replace(/<base[^>]*>/gi, "") },
  });

  it("never sends a design the server has refused, and says the work is not being kept", async () => {
    put.mockResolvedValue({});
    mockPost(refuseBase);
    mock({ hotel_name: "Semantics Demo" });
    await renderPage();
    open(/Advanced HTML/);
    fireEvent.change(await screen.findByLabelText(/^custom html$/i), { target: { value: '<base href="https://evil.example/">' } });
    expect(await screen.findByText(/your changes are not kept if you close this page/i)).toBeTruthy();
    await new Promise((r) => setTimeout(r, 2300));
    expect(drafts().length).toBe(0);
  });

  it("still keeps a clean design as the operator types", async () => {
    put.mockResolvedValue({});
    mockPost(refuseBase);
    mock({ hotel_name: "Semantics Demo" });
    await renderPage();
    open(/Advanced HTML/);
    fireEvent.change(await screen.findByLabelText(/^custom html$/i), { target: { value: "<p>Breakfast 7-10</p>" } });
    await waitFor(() => expect(drafts().length).toBeGreaterThan(0), { timeout: 4000 });
    expect(drafts().at(-1)?.[1]?.design?.custom_html).toBe("<p>Breakfast 7-10</p>");
  });
});
