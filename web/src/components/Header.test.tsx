import { describe, expect, it } from "vitest";
import { MemoryRouter } from "react-router";
import { render, screen } from "@testing-library/react";
import { IdentityKind } from "../gen/podium/v1/identity_pb";
import { ViewerContext, type Viewer } from "../lib/identity";
import { Header } from "./Header";

const base: Viewer = {
  login: "dev",
  displayName: "",
  kind: IdentityKind.DEV_TOKEN,
  agentEnabled: false,
  serverVersion: "dev",
};

function mount(who: Viewer | undefined, path = "/") {
  return render(
    <ViewerContext value={who}>
      <MemoryRouter initialEntries={[path]}>
        <Header />
      </MemoryRouter>
    </ViewerContext>,
  );
}

describe("Header", () => {
  it("always offers the screens every control plane has", () => {
    mount(base);
    for (const label of ["Tasks", "Nodes", "Secrets"]) {
      expect(screen.getByRole("link", { name: label })).toBeInTheDocument();
    }
  });

  // A control plane with no conductor has no Agent screen worth reaching, and WhoAmI is what
  // says so. A server built before agent_enabled existed sends nothing and reads as false.
  it("hides the Agent tab when the server has no conductor", () => {
    mount(base);
    expect(screen.queryByRole("link", { name: "Agent" })).toBeNull();
    expect(screen.queryByRole("link", { name: "Playbooks" })).toBeNull();
    expect(screen.queryByRole("link", { name: "Skills" })).toBeNull();
    expect(screen.queryByRole("link", { name: "Settings" })).toBeNull();
  });

  it("hides it before WhoAmI has answered at all", () => {
    mount(undefined);
    expect(screen.queryByRole("link", { name: "Agent" })).toBeNull();
    expect(screen.queryByRole("link", { name: "Settings" })).toBeNull();
  });

  it("shows Agent, Playbooks and Skills when the server proxies a conductor", () => {
    mount({ ...base, agentEnabled: true });
    expect(screen.getByRole("link", { name: "Agent" })).toHaveAttribute("href", "/agent");
    expect(screen.getByRole("link", { name: "Playbooks" })).toHaveAttribute(
      "href",
      "/agent/playbooks",
    );
    expect(screen.getByRole("link", { name: "Skills" })).toHaveAttribute("href", "/agent/skills");
  });

  it("puts Settings under the profile picture, not under Agent", () => {
    mount({ ...base, agentEnabled: true });
    const settings = screen.getByRole("link", { name: "Settings" });
    expect(settings).toHaveAttribute("href", "/agent/settings");
    expect(settings.closest("nav")?.getAttribute("aria-label")).toBe("Profile");
    const identity = screen.getByText("dev");
    expect(identity.compareDocumentPosition(settings) & Node.DOCUMENT_POSITION_FOLLOWING).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    );
  });

  // Footer chrome, not a primary dest: text-xs against the workspace items' text-sm. Hit
  // target stays h-8 with the rest of the rail.
  it("sets Settings in the smaller type scale", () => {
    mount({ ...base, agentEnabled: true });
    expect(screen.getByRole("link", { name: "Settings" })).toHaveClass("text-xs");
    expect(screen.getByRole("link", { name: "Settings" })).not.toHaveClass("text-sm");
    expect(screen.getByRole("link", { name: "Tasks" })).toHaveClass("text-sm");
    expect(screen.getByRole("link", { name: "Agent" })).toHaveClass("text-sm");
  });

  it("lights Agent on the talk screens and not on Playbooks", () => {
    const who = { ...base, agentEnabled: true };
    const { unmount } = mount(who, "/agent/chat");
    expect(screen.getByRole("link", { name: "Agent" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Playbooks" })).not.toHaveAttribute("aria-current");
    unmount();

    mount(who, "/agent/playbooks");
    expect(screen.getByRole("link", { name: "Playbooks" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Agent" })).not.toHaveAttribute("aria-current");
  });

  it("lights Settings on its own route without lighting Agent", () => {
    mount({ ...base, agentEnabled: true }, "/agent/settings");
    expect(screen.getByRole("link", { name: "Settings" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Agent" })).not.toHaveAttribute("aria-current");
  });

  // Cost is recorded per agent turn, so on a control plane with no conductor the Usage
  // screen has nothing to report and is hidden on the same signal.
  it("gates Usage on the conductor, like Agent", () => {
    const { unmount } = mount(base);
    expect(screen.queryByRole("link", { name: "Usage" })).toBeNull();
    unmount();

    mount({ ...base, agentEnabled: true });
    expect(screen.getByRole("link", { name: "Usage" })).toHaveAttribute("href", "/usage");
  });
});
