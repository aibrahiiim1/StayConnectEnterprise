import { vi } from "vitest";

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
