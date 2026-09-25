import { vi } from "vitest";

type Route = { method?: string; match: RegExp | string; status?: number; body: unknown };

/**
 * Replaces global fetch with a tiny router over the /api/* paths lib/api.ts calls. Each call is recorded so a
 * test can assert exactly what was sent. Unmatched requests answer 404 so a missing route fails loudly.
 */
export function mockFetch(routes: Route[]) {
  const calls: { method: string; url: string; body: unknown }[] = [];
  const fn = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const method = (init?.method ?? "GET").toUpperCase();
    const body = init?.body ? JSON.parse(String(init.body)) : undefined;
    calls.push({ method, url, body });
    const route = routes.find((r) =>
      (!r.method || r.method.toUpperCase() === method) &&
      (typeof r.match === "string" ? url === r.match : r.match.test(url)));
    const status = route ? route.status ?? 200 : 404;
    const payload = route ? route.body : { error: "not_found", message: `unmocked ${method} ${url}` };
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

export const PLATFORM_ME = {
  operator_id: "op-1",
  email: "admin@example.test",
  is_super_admin: true,
  roles: ["platform_admin"],
  expires_at: "2099-01-01T00:00:00Z",
};

export const TENANT_ME = {
  operator_id: "op-2",
  email: "ops@acme.test",
  is_super_admin: false,
  default_tenant_id: "t-acme",
  roles: ["tenant_admin"],
  expires_at: "2099-01-01T00:00:00Z",
};
