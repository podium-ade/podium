import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Server } from "lucide-react";
import { SettingsSectionNav } from "./SettingsSections";
import { sectionsOverflow } from "./sectionsOverflow";

describe("sectionsOverflow", () => {
  it("stays tabs until the row is wider than its frame", () => {
    expect(sectionsOverflow(0, 0)).toBe(false);
    expect(sectionsOverflow(200, 0)).toBe(false);
    expect(sectionsOverflow(200, 240)).toBe(false);
    expect(sectionsOverflow(241, 240)).toBe(false);
    expect(sectionsOverflow(242, 240)).toBe(true);
  });
});

describe("SettingsSectionNav", () => {
  const sections = [
    { id: "account", label: "Account", icon: Server },
    { id: "backend", label: "Backend", icon: Server },
    { id: "models", label: "Models", icon: Server },
  ];

  it("uses tabs while the names fit", () => {
    render(<SettingsSectionNav sections={sections} current="models" onChange={() => {}} />);
    expect(screen.getByRole("tab", { name: "Models" })).toHaveAttribute("aria-selected", "true");
    expect(screen.queryByTestId("settings-section-menu")).toBeNull();
  });

  it("collapses to a menu of the open section", async () => {
    const onChange = vi.fn();
    render(
      <SettingsSectionNav sections={sections} current="backend" onChange={onChange} collapsed />,
    );
    expect(screen.queryByRole("tab", { name: "Backend" })).toBeNull();
    await userEvent.click(screen.getByTestId("settings-section-menu"));
    await userEvent.click(await screen.findByRole("menuitem", { name: "Models" }));
    expect(onChange).toHaveBeenCalledWith("models");
  });
});
