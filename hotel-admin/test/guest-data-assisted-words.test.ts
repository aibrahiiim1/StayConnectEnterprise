import { describe, it, expect } from "vitest";
import fs from "node:fs";
import path from "node:path";
import {
  BLOCK_WORDS, CLASS_WORDS, KIND_WORDS, REASON_WORDS, REFUSAL_WORDS, describeDetail, isGroupAddress, isPrivateAddress, parseAddresses,
} from "@/lib/guest-data-assisted-words";

// Migration 0146 decides what a record is; this console only explains it. These tests keep the two in step: every
// class, reason, kind, block and refusal the database or edged can produce has words, and no word describes
// something they never produce.

const root = path.resolve(__dirname, "../..");
const migDir = path.join(root, "data-plane/migrations");
const up = fs.readFileSync(path.join(migDir, fs.readdirSync(migDir).find((f) => /^0146_.*\.up\.sql$/.test(f))!), "utf8");
const handler = fs.readFileSync(path.join(root, "data-plane/cmd/edged/guest_data_assisted.go"), "utf8");

function all(re: RegExp, text: string): string[] {
  return [...text.matchAll(re)].map((m) => m[1]);
}

describe("what the database can say about a record", () => {
  const classFn = up.slice(up.indexOf("FUNCTION iam_v2.assisted_class"), up.indexOf("FUNCTION iam_v2.assisted_device_state"));
  const stateFn = up.slice(up.indexOf("FUNCTION iam_v2.assisted_device_state"), up.indexOf("FUNCTION iam_v2.assisted_device_whole"));
  const reasons = new Set([
    ...all(/ARRAY\['[A-Z]+',\s*'([a-z_]+)'\]/g, classFn),
    ...all(/THEN '([a-z_]+)'/g, stateFn),
    ...all(/THEN 'UNVERIFIABLE'|'(device_has_records_outside_the_request)'/g, up).filter(Boolean),
  ]);
  const classes = new Set(all(/ARRAY\['([A-Z]+)',/g, classFn));

  it("finds the reasons the SQL emits", () => {
    expect(reasons.size).toBeGreaterThanOrEqual(11);
    expect(reasons.has("device_used_under_another_access")).toBe(true);
    expect(reasons.has("device_has_records_outside_the_request")).toBe(true);
  });
  it("has words for every reason, and none for a reason that is never given", () => {
    for (const r of reasons) expect(REASON_WORDS[r], `reason ${r}`).toBeTruthy();
    for (const r of Object.keys(REASON_WORDS)) expect(reasons.has(r), `unused words for ${r}`).toBe(true);
  });
  it("has words for every class, and only those", () => {
    expect([...classes].sort()).toEqual(Object.keys(CLASS_WORDS).sort());
  });
  it("names every kind of record the review lists", () => {
    const kinds = all(/'([a-z_]+)'::text, (?:se|sa|g|b|x|dv)\./g, up);
    expect(kinds.length).toBeGreaterThanOrEqual(7);
    for (const k of kinds) expect(KIND_WORDS[k], `kind ${k}`).toBeTruthy();
    expect(KIND_WORDS.device).toBeTruthy();
  });
  it("explains the two reasons an erasure waits", () => {
    for (const b of ["recent", "live"]) {
      expect(up).toContain(`'${b}'`);
      expect(BLOCK_WORDS[b]).toBeTruthy();
    }
  });
});

describe("what a refusal says", () => {
  it("explains every code edged can send, and every database detail it passes on", () => {
    const edged = all(/^\s*"([a-z_]+)":\s+"/gm, handler.slice(handler.indexOf("var assistedRefusals")));
    expect(edged.length).toBeGreaterThanOrEqual(10);
    for (const code of edged) expect(REFUSAL_WORDS[code], `refusal ${code}`).toBeTruthy();
    const details = new Set(all(/DETAIL = '([a-z_]+)'/g, up).filter((d) => !d.endsWith("_")));
    details.add("blocked_recent"); details.add("blocked_live"); // 'blocked_' || the record's reason
    for (const d of details) expect(edged, `edged does not word ${d}`).toContain(d);
  });
});

describe("device addresses", () => {
  it("reads one per line, comma or space in any common spelling, once each", () => {
    const r = parseAddresses("3C:22:FB:0A:11:7E, 3c-22-fb-0a-11-7e\n3c22fb0a117f  nonsense");
    expect(r.ok).toEqual(["3c:22:fb:0a:11:7e", "3c:22:fb:0a:11:7f"]);
    expect(r.bad).toEqual(["nonsense"]);
  });
  it("recognises a private (randomised) address and a group address", () => {
    expect(isPrivateAddress("2a:11:22:33:44:55")).toBe(true);
    expect(isPrivateAddress("3c:22:fb:0a:11:7e")).toBe(false);
    expect(isGroupAddress("01:00:5e:00:00:01")).toBe(true);
    expect(isGroupAddress("3c:22:fb:0a:11:7e")).toBe(false);
  });
});

describe("the line that says what a record would disclose", () => {
  it("shows the address, volume and the number of usage samples of a session", () => {
    const s = describeDetail("session", { ip: "10.1.2.3", mac_address: "3c:22:fb:0a:11:7e", bytes_down: 5 * 1024 ** 2, bytes_up: 2048, usage_samples: [{}, {}] });
    expect(s).toContain("10.1.2.3");
    expect(s).toContain("3c:22:fb:0a:11:7e");
    expect(s).toContain("5.0 MB");
    expect(s).toContain("2 usage samples");
  });
  it("is empty for a record whose detail is not shown", () => {
    expect(describeDetail("session", null)).toBe("");
    expect(describeDetail("unknown", { a: 1 })).toBe("");
  });
});
