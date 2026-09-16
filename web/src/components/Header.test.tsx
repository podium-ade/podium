import { describe, expect, it } from "vitest";
import { MemoryRouter } from "react-router";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { IdentityKind } from "../gen/podium/v1/identity_pb";
import { ViewerContext, type Viewer } from "../lib/identity";
import { Header } from "./Header";

const base: Viewer = {
  login: "local",
  displayName: "",
  kind: IdentityKind.LOCAL_TOKEN,
  agentEnabled: false,
  serverVersion: "dev",
  roles: [],
  claimed: false,
  hostedDomain: "",
  canClaim: false,
  googleAuthEnabled: false,
  claimDomain: "",
  pictureUrl: "",
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

  it("still offers Settings when Google sign-in is on and there is no conductor", () => {
    mount({ ...base, googleAuthEnabled: true });
    expect(screen.getByRole("link", { name: "Settings" })).toHaveAttribute(
      "href",
      "/agent/settings",
    );
    expect(screen.queryByRole("link", { name: "Agent" })).toBeNull();
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
    expect(screen.getByRole("link", { name: "MCP" })).toHaveAttribute("href", "/agent/mcp");
  });

  // MCP is a sibling of Agent, not one of its talk screens, so it lights itself and leaves
  // Agent alone — the same rule Playbooks and Skills follow.
  it("lights MCP on its own route without lighting Agent", () => {
    mount({ ...base, agentEnabled: true }, "/agent/mcp");
    expect(screen.getByRole("link", { name: "MCP" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Agent" })).not.toHaveAttribute("aria-current");
  });

  it("puts the Agent section above Workspace", () => {
    mount({ ...base, agentEnabled: true });
    const agent = screen.getByText("Agent", { selector: "p" });
    const workspace = screen.getByText("Workspace", { selector: "p" });
    expect(agent.compareDocumentPosition(workspace) & Node.DOCUMENT_POSITION_FOLLOWING).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    );
  });

  it("offers Sign out for a Google Workspace session", async () => {
    mount({
      ...base,
      login: "alice@acme.com",
      displayName: "Alice",
      kind: IdentityKind.USER,
      googleAuthEnabled: true,
      hostedDomain: "acme.com",
    });
    await userEvent.click(screen.getByRole("button", { name: "Alice" }));
    expect(screen.getByRole("menuitem", { name: "Sign out" })).toHaveAttribute(
      "href",
      "/auth/logout",
    );
  });

  it("does not offer Sign out under the local token", () => {
    mount({ ...base, agentEnabled: true });
    expect(screen.queryByRole("button", { name: "local" })).toBeNull();
    expect(screen.queryByRole("menuitem", { name: "Sign out" })).toBeNull();
  });

  it("puts Settings next to the wordmark, not under Agent", () => {
    mount({ ...base, agentEnabled: true });
    const settings = screen.getByRole("link", { name: "Settings" });
    expect(settings).toHaveAttribute("href", "/agent/settings");
    const title = screen.getByText("podium");
    expect(title.compareDocumentPosition(settings) & Node.DOCUMENT_POSITION_FOLLOWING).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    );
  });

  it("shows the Google profile photo when WhoAmI has one", () => {
    mount({
      ...base,
      login: "alice@acme.com",
      displayName: "Alice",
      kind: IdentityKind.USER,
      googleAuthEnabled: true,
      pictureUrl: "https://lh3.googleusercontent.com/a/alice",
    });
    const img = screen.getByRole("presentation");
    expect(img).toHaveAttribute("src", "/auth/picture");
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
    mount({ ...base, agentEnabled: true }, "/agent/settings/models");
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
