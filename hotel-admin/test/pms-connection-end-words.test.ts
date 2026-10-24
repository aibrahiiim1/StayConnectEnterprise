import { describe, expect, it } from "vitest";
import { connectionEndWords } from "@/lib/api/pms-connections";

describe("connectionEndWords", () => {
  it("puts pmsd's transport endings into plain words", () => {
    expect(connectionEndWords("TRANSPORT_REMOTE_CLOSED")).toBe("The PMS closed the connection");
    expect(connectionEndWords("TRANSPORT_RESET")).toBe("The connection was reset");
    expect(connectionEndWords("TRANSPORT_TIMEOUT")).toBe("No reply within the time limit");
  });

  it("shows an unknown code as sent and an empty string for no code", () => {
    expect(connectionEndWords("PUBLISH_FAILED")).toBe("PUBLISH_FAILED");
    expect(connectionEndWords("")).toBe("");
    expect(connectionEndWords(undefined)).toBe("");
  });
});
