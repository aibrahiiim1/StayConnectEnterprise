// The page-level lightbulb. Explanations moved off the screens and behind it, so the one thing that must hold is
// that the button is there, is named for the page, and opens the explanation when pressed.

import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import "@testing-library/jest-dom/vitest";
import { PageHeader } from "@/components/ui/page";
import { HelpSection } from "@/components/help";

describe("PageHeader help", () => {
  it("shows a lightbulb named for the page, and opens the tips in a dialog", async () => {
    const user = userEvent.setup();
    render(
      <PageHeader
        title="Service plans"
        description="Short line."
        help={
          <HelpSection title="What a service plan is">
            <p>A plan reaches guests only through the packages that hand it out.</p>
          </HelpSection>
        }
      />,
    );

    const button = screen.getByRole("button", { name: "Tips: Service plans" });
    expect(button).toBeInTheDocument();
    // Closed until asked for.
    expect(screen.queryByText(/reaches guests only through the packages/i)).toBeNull();

    await user.click(button);
    const dialog = await screen.findByRole("dialog");
    expect(within(dialog).getByText("What a service plan is")).toBeInTheDocument();
    expect(within(dialog).getByText(/reaches guests only through the packages/i)).toBeInTheDocument();
  });

  it("shows no lightbulb when a page has no help", () => {
    render(<PageHeader title="Plain" />);
    expect(screen.queryByRole("button", { name: /^Tips/ })).toBeNull();
  });
});
