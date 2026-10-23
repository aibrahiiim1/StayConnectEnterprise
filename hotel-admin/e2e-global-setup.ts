import type { FullConfig } from "@playwright/test";

/** Playwright's URL check may pass before Next compiles; verify login markup before running specs. */
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
        if (body.includes("StayConnect") || body.includes("Sign in")) return;
        lastProblem = `${target} answered ${res.status} but did not look like the Admin Console login page`;
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
