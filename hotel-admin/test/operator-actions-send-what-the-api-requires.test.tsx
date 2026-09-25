import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

// TWO WAYS A SCREEN CAN LIE ABOUT THE APPLIANCE, BOTH REPORTED BY AN OPERATOR USING IT.
//
//   1. It can send a request the endpoint cannot accept, and show the parse failure as if the appliance were
//      broken. "Back up now" posted no body to an endpoint that requires a password step-up, so every click
//      returned "malformed request body" -- which reads like a fault, not like a prompt.
//
//   2. It can state a verdict it has not obtained yet. The PMS list renders from one request and its health
//      from another, so between them "Room sign-in" announced "Not working" in an error tone about a
//      connection that was fine. Not asked and answered-no are different facts.
//
// Both are asserted on the source because both are about what the screen DOES before an answer exists, which
// a render test with mocked data cannot reach: mock the data and the race is gone.

const read = (p: string) => readFileSync(join(process.cwd(), p), "utf8");

describe("an operator action sends what the endpoint requires", () => {
  const SRC = read("app/(app)/backups/page.tsx");

  it("Back up now carries the password the step-up demands", () => {
    const start = SRC.indexOf("async function backupNow(");
    expect(start, "backupNow is gone").toBeGreaterThan(-1);
    const fn = SRC.slice(start);
    const body = fn.slice(0, fn.indexOf("\n  }"));
    expect(body, "the request posts no body, so the endpoint cannot parse it").toContain("password");
    expect(body).toMatch(/api\.post\("\/backups\/run",\s*\{\s*password/);
  });

  it("asks for the password rather than failing the click", () => {
    // The endpoint is unchanged and still enforces. What changed is that the screen takes part: Back up now
    // opens a confirmation that requires the password (a masked field inside ConfirmDialog) and hands it on.
    const dialog = SRC.slice(SRC.indexOf('title="Back up this property now?"'));
    const block = dialog.slice(0, dialog.indexOf("/>"));
    expect(block).toContain("requirePassword");
    expect(block).toContain("onConfirm={backupNow}");
  });
});

describe("a screen does not state a verdict it has not obtained", () => {
  const SRC = read("app/(app)/pms-interfaces/page.tsx");

  it("waits for health before summarising room sign-in", () => {
    expect(SRC, "nothing distinguishes 'not asked' from 'answered no'").toContain("healthLoaded");
    // The verdict block must be gated on the answer having arrived.
    expect(SRC).toMatch(/list\.length > 0 && healthLoaded/);
    // ...and there must be a not-yet-asked rendering, or gating just makes the summary vanish.
    expect(SRC).toMatch(/list\.length > 0 && !healthLoaded/);
  });

  it("marks health as loaded only once the requests have resolved", () => {
    const load = SRC.slice(SRC.indexOf("const load = useCallback"));
    const body = load.slice(0, load.indexOf("}, []);"));
    const setHealth = body.indexOf("setHealth(");
    const setLoaded = body.indexOf("setHealthLoaded(true)");
    expect(setLoaded, "healthLoaded is never set, so the summary would never appear").toBeGreaterThan(-1);
    expect(setLoaded, "healthLoaded is set before the health it describes").toBeGreaterThan(setHealth);
  });
});

describe("the appliance can name the backups it takes", () => {
  const SRC = readFileSync(join(process.cwd(), "../data-plane/cmd/scd/backup.go"), "utf8");

  it("accepts the safety dumps a restore writes", () => {
    // Every restore takes db-safety-<stamp>.sql.gz before touching anything. The pattern matched only
    // db-<stamp>.sql.gz, so the one copy an operator would most want back was the one the guard refused --
    // and the refusal said "a database backup name is required" about a file the screen had just listed.
    const m = /var safeBackupName = regexp\.MustCompile\(`([^`]+)`\)/.exec(SRC);
    expect(m, "safeBackupName is gone or reshaped").toBeTruthy();
    const re = new RegExp(m![1]);
    expect(re.test("db-20260921T095526Z.sql.gz"), "an ordinary backup is refused").toBe(true);
    expect(re.test("db-safety-20260921T095526Z.sql.gz"), "a safety dump is refused").toBe(true);

    // And it is still a path guard, which is why it exists at all.
    for (const bad of [
      "../etc/passwd", "db-../../x.sql.gz", "db-2026/09.sql.gz", "db-x.sql.gz",
      "db-20260921T095526Z.sql.gz.other", "safety-20260921Z.sql.gz",
    ]) {
      expect(re.test(bad), `${bad} is accepted as a backup name`).toBe(false);
    }
  });
});
