import { vi } from "vitest";
import { render } from "@testing-library/react";
import { SessionProvider } from "@/lib/session";
import { ToastProvider } from "@/components/ui/toast";
import { StepUpProvider } from "@/components/step-up";
import type { Whoami } from "@/lib/api";

type Route = { method?: string; match: RegExp | string; status?: number; body: unknown | ((n: number) => { status?: number; body: unknown }) };

/**
 * Replaces global fetch with a tiny router over the /api/* paths lib/api.ts calls. Each call is recorded so a
 * test can assert exactly what was sent. Unmatched requests answer 404 so a missing route fails loudly. A route
 * whose body is a function is called with how many times it has matched, so it can answer differently on a
 * retry (a step-up demand, then success).
 */
export function mockFetch(routes: Route[]) {
  const calls: { method: string; url: string; body: unknown }[] = [];
  const hits = new Map<Route, number>();
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method ?? "GET").toUpperCase();
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ method, url, body });
    const route = routes.find((r) =>
      (!r.method || r.method.toUpperCase() === method) &&
      (typeof r.match === "string" ? url === r.match : r.match.test(url)));
    let status = route ? route.status ?? 200 : 404;
    let payload: unknown = route ? route.body : { error: "not_found", message: `unmocked ${method} ${url}` };
    if (route && typeof route.body === "function") {
      const n = hits.get(route) ?? 0;
      hits.set(route, n + 1);
      const r = (route.body as (n: number) => { status?: number; body: unknown })(n);
      status = r.status ?? 200;
      payload = r.body;
    }
    return {
      ok: status >= 200 && status < 300,
      status,
      headers: { get: () => "application/json" },
      json: async () => payload,
      text: async () => JSON.stringify(payload),
    } as unknown as Response;
  });
  vi.stubGlobal("fetch", fn);
  return { fn, calls };
}

export const PLATFORM_ME: Whoami = {
  operator_id: "op-1",
  email: "admin@example.test",
  is_super_admin: true,
  roles: ["platform_admin"],
  customer_id: null,
  customer_name: null,
};

export const SUPPORT_ME: Whoami = {
  operator_id: "op-3",
  email: "support@example.test",
  is_super_admin: false,
  roles: ["platform_support"],
  customer_id: null,
  customer_name: null,
};

export const TENANT_ME: Whoami = {
  operator_id: "op-2",
  email: "ops@semantics.test",
  is_super_admin: false,
  roles: ["tenant_admin"],
  customer_id: "c-semantics",
  customer_name: "Semantics",
};

/** Renders a screen the way the app shell does: signed in, with toasts and the password dialog. */
export function renderAs(me: Whoami, ui: React.ReactNode) {
  return render(
    <SessionProvider me={me}>
      <ToastProvider>
        <StepUpProvider>{ui}</StepUpProvider>
      </ToastProvider>
    </SessionProvider>,
  );
}
