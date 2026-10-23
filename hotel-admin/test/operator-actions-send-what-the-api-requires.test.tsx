import { describe, it, expect } from "vitest";
import { readFileSync } from "node:fs";
import { join } from "node:path";

// Source assertions on what a screen does before an answer exists: it must send what the endpoint requires (Back up now
// needs a password step-up),
// and must not state a verdict it has not obtained (PMS health arrives in a separate request from the list).

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
    const dialog = SRC.slice(SRC.indexOf('title="Back up this site now?"'));
    const block = dialog.slice(0, dialog.indexOf("/>"));
    expect(block).toContain("requirePassword");
    expect(block).toContain("onConfirm={backupNow}");
  });
});

describe("a screen does not state a verdict it has not obtained", () => {
  const SRC = read("app/(app)/pms-interfaces/page.tsx");

  it("waits for health before summarising room sign-in", () => {
    expect(SRC, "nothing distinguishes 'not asked' from 'answered no'").toContain("healthLoaded");
    expect(SRC).toMatch(/list\.length > 0 && healthLoaded/);
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
    // Restores take db-safety-<stamp>.sql.gz; the name guard must accept it.
    const m = /var safeBackupName = regexp\.MustCompile\(`([^`]+)`\)/.exec(SRC);
    expect(m, "safeBackupName is gone or reshaped").toBeTruthy();
    const re = new RegExp(m![1]);
    expect(re.test("db-20260921T095526Z.sql.gz"), "an ordinary backup is refused").toBe(true);
    expect(re.test("db-safety-20260921T095526Z.sql.gz"), "a safety dump is refused").toBe(true);

    for (const bad of [
      "../etc/passwd", "db-../../x.sql.gz", "db-2026/09.sql.gz", "db-x.sql.gz",
      "db-20260921T095526Z.sql.gz.other", "safety-20260921Z.sql.gz",
    ]) {
      expect(re.test(bad), `${bad} is accepted as a backup name`).toBe(false);
    }
  });
});
