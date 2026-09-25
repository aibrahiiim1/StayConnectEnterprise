import { describe, expect, it } from "vitest";
import { cn } from "@/lib/utils";

describe("cn keeps the Velonet type roles", () => {
  it("a type role and a text colour coexist", () => {
    expect(cn("text-caption", "text-muted-foreground")).toBe("text-caption text-muted-foreground");
    expect(cn("text-nano uppercase", "text-sidebar-muted")).toBe("text-nano uppercase text-sidebar-muted");
  });
  it("two sizes resolve to the last one", () => {
    expect(cn("text-sm", "text-label")).toBe("text-label");
    expect(cn("text-title", "text-xs")).toBe("text-xs");
  });
});
