import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { Server } from "lucide-react";
import { SettingsSectionNav } from "./SettingsSections";

describe("SettingsSectionNav", () => {
  const sections = [
    { id: "account", label: "Account", icon: Server },
    { id: "backend", label: "Backend", icon: Server },
    { id: "models", label: "Models", icon: Server },
  ];

  it("lists the sections and marks the open one", () => {
    render(
      <MemoryRouter>
        <SettingsSectionNav sections={sections} current="models" />
      </MemoryRouter>,
    );
    expect(screen.getByRole("navigation", { name: "Settings" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Models" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Models" })).toHaveAttribute(
      "href",
      "/agent/settings/models",
    );
    expect(screen.getByRole("link", { name: "Backend" })).not.toHaveAttribute("aria-current");
    expect(screen.queryByRole("tab")).toBeNull();
  });
});
