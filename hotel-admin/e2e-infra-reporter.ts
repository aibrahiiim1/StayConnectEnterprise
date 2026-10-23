import type { Reporter, TestCase, TestResult, FullResult } from "@playwright/test/reporter";

/** Report a dead dev server once as infrastructure failure while preserving the failed exit status. */

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
