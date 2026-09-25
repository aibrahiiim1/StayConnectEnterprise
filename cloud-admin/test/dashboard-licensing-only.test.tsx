import { describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import { mockFetch, TENANT_ME } from "./helpers";

vi.mock("next/navigation", () => ({
  usePathname: () => "/dashboard",
  useRouter: () => ({ replace: () => {}, push: () => {}, refresh: () => {} }),
  useSearchParams: () => new URLSearchParams(),
}));

import { CustomerProvider } from "@/lib/customer-context";
import { ToastProvider } from "@/components/ui/toast";
import type { Whoami } from "@/lib/api";
import DashboardPage from "@/app/(app)/dashboard/page";

// CENTRAL IS USED FOR LICENSING ONLY. The dashboard must ask ctrlapi only for what it serves -- licenses, sites
// and appliances -- and never for guest usage, which is not reported to Central and has no route there.
describe("the Central dashboard", () => {
  it("reads only licensing data and shows what needs attention", async () => {
    const soon = new Date(Date.now() + 10 * 86400000).toISOString();
    const { calls } = mockFetch([
      { match: /\/api\/v1\/tenants$/, body: { data: [{ id: "t-acme", slug: "acme", name: "Acme Hotels" }] } },
      { match: /\/api\/cloud\/v1\/licenses\?/, body: { data: [{
        id: "l1", tenant_id: "t-acme", site_id: "s1", commercial_plan_code: "", status: "active",
        issued_at: soon, valid_until: soon, offline_grace_days: 0, appliance_ids: ["a1"], key_id: "k",
        created_at: soon, grace_period_days: 30, max_concurrent_online_guests: 500,
      }], meta: { has_more: false } } },
      { match: /\/api\/v1\/sites\?/, body: { data: [{ id: "s1", tenant_id: "t-acme", code: "CSR", name: "Coral Sea Resort", timezone: "UTC", created_at: soon, updated_at: soon }], meta: { has_more: false } } },
      { match: /\/api\/v1\/appliances\?/, body: { data: [{ id: "a1", tenant_id: "t-acme", site_id: "s1", serial: "SN1", name: "gw", status: "online" }], meta: { has_more: false } } },
    ]);
    render(
      <CustomerProvider me={TENANT_ME as Whoami}>
        <ToastProvider><DashboardPage /></ToastProvider>
      </CustomerProvider>,
    );
    await waitFor(() => expect(screen.getByText("Coral Sea Resort")).toBeInTheDocument());
    expect(screen.getByText("Expiring soon")).toBeInTheDocument();
    expect(screen.getByText(/Central is used for licensing only/i)).toBeInTheDocument();
    expect(calls.some((c) => /usage|\/cloud\/v1\/fleet\b/.test(c.url))).toBe(false);
    expect(calls.every((c) => !/404/.test(c.url))).toBe(true);
  });
});
