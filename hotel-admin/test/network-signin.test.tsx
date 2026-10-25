import React from "react";
import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, waitFor, fireEvent } from "@testing-library/react";

const get = vi.fn();
const put = vi.fn();
vi.mock("@/lib/api", () => ({
  ApiError: class ApiError extends Error {},
  api: { get: (...a: unknown[]) => get(...a), post: vi.fn(), put: (...a: unknown[]) => put(...a), patch: vi.fn(), del: vi.fn() },
}));
vi.mock("@/components/ui/toast", () => ({ useToast: () => ({ success: vi.fn(), error: vi.fn(), info: vi.fn() }) }));
vi.mock("next/link", () => ({ default: ({ href, children }: { href: string; children: React.ReactNode }) => <a href={href}>{children}</a> }));

import { SignInCard } from "@/components/network/signin-card";
import { NetworkPicker } from "@/components/network/network-picker";

const site = { pms: { enabled: true }, voucher: { enabled: true }, sms: { enabled: false }, social: { google: { enabled: false } }, portal: { remember_device_days: 30 } };
const none = { disabled_methods: [], primary_method: "", remember_days: null };

function serve(override = none, rooms: string[] = ["Protel"]) {
  get.mockImplementation((path: string) => {
    if (path === "/auth-methods") return Promise.resolve(site);
    if (path.endsWith("/sign-in")) return Promise.resolve(override);
    if (path === "/network/overview") return Promise.resolve({ networks: [{ id: "n1", pms_interfaces: rooms }] });
    if (path === "/network/guest-networks") return Promise.resolve({ data: [{ id: "n1", name: "Rooms" }, { id: "n2", name: "Pool" }] });
    return Promise.reject(new Error(path));
  });
}

beforeEach(() => { get.mockReset(); put.mockReset(); });

describe("sign-in on a network", () => {
  it("lists only what the site offers, and saves a narrowing", async () => {
    serve();
    put.mockResolvedValue({ disabled_methods: ["voucher"], primary_method: "pms", remember_days: 7 });
    render(<SignInCard networkId="n1" writable />);
    await screen.findByLabelText("Room sign-in");
    expect(screen.queryByText("Text message code")).toBeNull();
    expect(screen.queryByText("Social sign-in")).toBeNull();
    expect(screen.getByText(/Checks rooms on Protel/)).toBeTruthy();

    fireEvent.click(screen.getByLabelText("Voucher"));
    fireEvent.change(screen.getByLabelText("Method shown first"), { target: { value: "pms" } });
    fireEvent.change(screen.getByLabelText(/Remember devices/), { target: { value: "7" } });
    fireEvent.click(screen.getByRole("button", { name: "Save sign-in options" }));
    await waitFor(() => expect(put).toHaveBeenCalledWith("/network/guest-networks/n1/sign-in",
      { disabled_methods: ["voucher"], primary_method: "pms", remember_days: 7 }));
  });

  it("says plainly when no PMS interface reaches the network, and refuses a bad remember period", async () => {
    serve(none, []);
    render(<SignInCard networkId="n1" writable />);
    await screen.findByText(/room sign-in is not available here/);
    fireEvent.change(screen.getByLabelText(/Remember devices/), { target: { value: "400" } });
    expect((screen.getByRole("button", { name: "Save sign-in options" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("does not offer a switched-off method as the one shown first, and is read-only without write access", async () => {
    serve({ disabled_methods: ["voucher"], primary_method: "", remember_days: null });
    const { unmount } = render(<SignInCard networkId="n1" writable />);
    await screen.findByText("Voucher");
    const select = screen.getByLabelText("Method shown first") as HTMLSelectElement;
    expect([...select.options].map((o) => o.value)).toEqual(["", "pms"]);
    unmount();
    render(<SignInCard networkId="n1" writable={false} />);
    await screen.findByText("Voucher");
    expect(screen.queryByRole("button", { name: "Save sign-in options" })).toBeNull();
  });
});

describe("network picker", () => {
  it("picks networks by name and keeps an id whose network is gone so it can be removed", async () => {
    serve();
    const onChange = vi.fn();
    render(<NetworkPicker value="n1,gone" onChange={onChange} label="Networks" />);
    await screen.findByText("Pool");
    expect(screen.getByText("A network that no longer exists")).toBeTruthy();
    fireEvent.click(screen.getByLabelText("Pool"));
    expect(onChange).toHaveBeenCalledWith("n1,gone,n2");
    fireEvent.click(screen.getByLabelText("A network that no longer exists"));
    expect(onChange).toHaveBeenLastCalledWith("n1");
  });

  it("falls back to typing ids when the networks cannot be read", async () => {
    get.mockRejectedValue(new Error("down"));
    render(<NetworkPicker value="" onChange={vi.fn()} label="Networks" />);
    await screen.findByPlaceholderText("uuid,uuid");
  });
});
