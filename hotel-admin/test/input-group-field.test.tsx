import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Field, Input, InputGroup } from "@/components/ui/input";

describe("InputGroup inside a Field", () => {
  it("gives the grouped input the label, the hint and the invalid state", () => {
    render(
      <Field label="Lease time" hint="From 60 to 86400 seconds." error="Too short.">
        <InputGroup suffix="seconds">
          <Input defaultValue="30" />
        </InputGroup>
      </Field>,
    );
    const input = screen.getByLabelText("Lease time");
    expect(input.tagName).toBe("INPUT");
    expect(input).toHaveAttribute("aria-invalid", "true");
    const describedBy = input.getAttribute("aria-describedby") ?? "";
    expect(describedBy).not.toBe("");
    expect(document.getElementById(describedBy.split(" ")[0])).toHaveTextContent("Too short.");
  });

  it("keeps an id the input already has, and the label names it", () => {
    render(
      <Field label="Renewal">
        <InputGroup suffix="seconds">
          <Input id="renewal-input" defaultValue="900" />
        </InputGroup>
      </Field>,
    );
    expect(screen.getByLabelText("Renewal")).toHaveAttribute("id", "renewal-input");
  });
});
