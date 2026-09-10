import type { FullConfig } from "@playwright/test";

/**
 * Prove the test server is genuinely serving the app before a single spec runs.
 *
 * Playwright's own `webServer.url` wait is satisfied by the FIRST response on that URL. For `next dev` that
 * can arrive while the route is still compiling, and it says nothing about whether the flag-ON build the
 * specs assume is the one being served. If the server is wrong or half-up, the first spec fails on an
 * assertion and the run reads as a product failure.
 *
 * This asks one question the startup wait does not: does the login page actually come back, with the markup
 * the suite is written against? A failure here aborts the run immediately with a message about the
 * ENVIRONMENT, which is far cheaper to read than a spec failing 90 seconds later on a missing element.
 */
export default async function globalSetup(config: FullConfig) {
  const baseURL =
    (config.projects[0]?.use as { baseURL?: string } | undefined)?.baseURL ?? "http://127.0.0.1:3123";
  const target = `${baseURL}/login`;
  const deadline = Date.now() + 60_000;
  let lastProblem = "no attempt completed";

  while (Date.now() < deadline) {
    try {
      const res = await fetch(target, { redirect: "manual" });
      if (res.status >= 500) {
        lastProblem = `the server answered ${res.status}`;
      } else {
        const body = await res.text();
        // The login page is the one surface every spec reaches first.
        if (body.includes("StayConnect") || body.includes("Sign in")) return;
        lastProblem = `${target} answered ${res.status} but did not look like the Hotel Admin login page`;
      }
    } catch (err) {
      lastProblem = `${target} refused the connection (${(err as Error).message})`;
    }
    await new Promise((r) => setTimeout(r, 1_000));
  }

  throw new Error(
    [
      "",
      "E2E ENVIRONMENT IS NOT READY — no spec was run.",
      `  ${lastProblem}`,
      "",
      "  This is an INFRASTRUCTURE failure, not a product failure. Nothing about the application has been",
      "  demonstrated either way. Check that the test server started, that nothing else holds the port, and",
      "  that the build carries the NEXT_PUBLIC_* flags the suite assumes.",
      "",
    ].join("\n"),
  );
}
