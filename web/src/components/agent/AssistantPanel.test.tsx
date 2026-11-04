import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { Server } from "lucide-react";
import { SaveBar, SectionTabs } from "./AssistantPanel";
import { listSections } from "../../lib/assistantProfile";

describe("SectionTabs", () => {
  const tabs = [
    { id: "identity", label: "Identity", icon: Server },
    { id: "model", label: "Model", icon: Server, unsaved: true },
  ];

  it("is a strip of links, so a section has an address", () => {
    render(
      <MemoryRouter>
        <SectionTabs tabs={tabs} current="identity" href={(id) => `/agent/assistants/podium/${id}`} />
      </MemoryRouter>,
    );
    expect(screen.getByRole("navigation", { name: "Assistant" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Identity" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Model, unsaved" })).toHaveAttribute(
      "href",
      "/agent/assistants/podium/model",
    );
    expect(screen.getByRole("link", { name: "Model, unsaved" })).not.toHaveAttribute("aria-current");
    expect(screen.queryByRole("tab")).toBeNull();
  });
});

describe("SaveBar", () => {
  it("names the sections waiting to be saved", () => {
    render(
      <SaveBar dirty status={`Unsaved changes in ${listSections(["Identity", "Model", "Limits"])}`}>
        <button type="button">Save</button>
      </SaveBar>,
    );
    expect(screen.getByText("Unsaved changes in Identity, Model and Limits")).toBeInTheDocument();
  });

  it("lists one or two sections without a comma", () => {
    expect(listSections(["Identity"])).toBe("Identity");
    expect(listSections(["Identity", "Model"])).toBe("Identity and Model");
  });
});
