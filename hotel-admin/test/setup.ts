import "@testing-library/jest-dom/vitest";
import { afterEach } from "vitest";
import { cleanup, configure } from "@testing-library/react";

// A longer wait ceiling absorbs parallel-worker load without delaying tests that settle sooner.
configure({ asyncUtilTimeout: 5000 });

// The shared /health answer is module state; each test starts without one. Imported lazily, so a test file's
// vi.mock("@/lib/api") still applies to it.
afterEach(async () => {
  cleanup();
  (await import("@/lib/shared-health")).resetSharedHealth();
});
