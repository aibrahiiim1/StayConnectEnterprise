import { vi } from "vitest";

/**
 * A controllable next/navigation for tests: set `nav.path` / `nav.search` before rendering, and read what the
 * page asked the router to do from the spies. Each test file mocks the module with:
 *
 *   vi.mock("next/navigation", async () => (await import("./navigation")).navigationModule);
 */
const router = { replace: vi.fn(), push: vi.fn(), refresh: vi.fn() };

export const nav = {
  path: "/overview",
  search: new URLSearchParams(),
  router,
  reset(path = "/overview", search = "") {
    nav.path = path;
    nav.search = new URLSearchParams(search);
    router.replace.mockReset();
    router.push.mockReset();
    router.refresh.mockReset();
  },
};

export const navigationModule = {
  usePathname: () => nav.path,
  useRouter: () => router,
  useSearchParams: () => nav.search,
  useParams: () => ({}),
  redirect: vi.fn(),
};
