import type { Reporter, TestCase, TestResult, FullResult } from "@playwright/test/reporter";

/**
 * Report a dead dev server ONCE, as infrastructure, instead of as N product regressions.
 *
 * THE FAILURE THIS EXISTS FOR. The suite runs `workers: 1`, `fullyParallel: false`, `retries: 0`, against a
 * `next dev` server Playwright starts. Playwright waits for that server once, at startup, and never checks
 * it again. If it dies mid-run -- an out-of-memory kill during an on-demand compile is the usual way -- every
 * remaining spec still executes, each navigation burns up to `navigationTimeout` (90s) and then fails with
 * ERR_CONNECTION_REFUSED, and the report is a wall of red assertions that is indistinguishable from a mass
 * product regression.
 *
 * That is not a hypothetical: it produced a 20-failed run on a workstation during the PR #108 delivery and
 * sent a developer looking for a UI bug that did not exist. The correct reading -- "the server died, none of
 * these results mean anything" -- was recoverable only by scrolling to the first failure and noticing the
 * error was a connection error rather than an assertion.
 *
 * MEASURED HONESTLY: this never caused a CI failure. Across the 34 gate attempts of PRs #108/#109 the
 * E2E-INFRASTRUCTURE category was ZERO. This is a local-time and diagnosis-quality fix, and a guard against
 * a class of failure that would be extremely expensive to misread on a runner.
 *
 * WHAT IT DOES NOT DO. It does not convert failures into passes, does not retry, and does not change the exit
 * status. A run with infrastructure death still FAILS. It only makes the report say the true thing.
 */

/** Signatures that mean "nothing was listening", never "the product is wrong". */
const INFRA_SIGNATURES = [
  "ERR_CONNECTION_REFUSED",
  "ECONNREFUSED",
  "ERR_CONNECTION_RESET",
  "ERR_EMPTY_RESPONSE",
  "ERR_SOCKET_NOT_CONNECTED",
  "ERR_CONNECTION_CLOSED",
  "socket hang up",
];

function isInfrastructure(result: TestResult): boolean {
  const text = [
    result.error?.message ?? "",
    result.error?.stack ?? "",
    ...result.errors.map((e) => `${e.message ?? ""}${e.stack ?? ""}`),
  ].join("\n");
  return INFRA_SIGNATURES.some((sig) => text.includes(sig));
}

export default class E2EInfraReporter implements Reporter {
  private infra: string[] = [];
  private product: string[] = [];

  onTestEnd(test: TestCase, result: TestResult) {
    if (result.status !== "failed" && result.status !== "timedOut") return;
    (isInfrastructure(result) ? this.infra : this.product).push(test.titlePath().slice(1).join(" › "));
  }

  onEnd(_result: FullResult) {
    if (this.infra.length === 0) return;

    const total = this.infra.length + this.product.length;
    console.log("");
    console.log("=".repeat(78));
    console.log("  INFRASTRUCTURE FAILURE — THE TEST SERVER WENT AWAY MID-RUN");
    console.log("=".repeat(78));
    console.log(
      `  ${this.infra.length} of ${total} failures are connection errors, not assertions. The dev server on`,
    );
    console.log("  the test port stopped answering; every spec after that point failed on navigation.");
    console.log("");
    console.log("  THESE RESULTS DO NOT MEAN THE PRODUCT IS BROKEN. Do not start debugging them. Restart");
    console.log("  the suite against a fresh server; only then are the failures evidence of anything.");
    console.log("");
    if (this.product.length > 0) {
      console.log(`  ${this.product.length} failure(s) are genuine assertions and DO need attention:`);
      for (const name of this.product.slice(0, 10)) console.log(`    - ${name}`);
      if (this.product.length > 10) console.log(`    ... and ${this.product.length - 10} more`);
    } else {
      console.log("  No genuine assertion failed before the server died, so this run proved nothing at all.");
    }
    console.log("=".repeat(78));
    console.log("");
  }
}
