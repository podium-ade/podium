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
    expect(screen.getByRole("link", { name: "Tasks" })).toHaveAttribute("href", "/tasks");
    for (const label of ["Nodes", "Secrets", "Registries"]) {
      expect(screen.getByRole("link", { name: label })).toBeInTheDocument();
    }
  });

  // A control plane with no conductor has no Agent screen worth reaching, and WhoAmI is what
  // says so. A server built before agent_enabled existed sends nothing and reads as false.
  it("hides the Assistant tab when the server has no conductor", () => {
    mount(base);
    expect(screen.queryByRole("link", { name: "Assistant" })).toBeNull();
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
    expect(screen.queryByRole("link", { name: "Assistant" })).toBeNull();
  });

  it("hides it before WhoAmI has answered at all", () => {
    mount(undefined);
    expect(screen.queryByRole("link", { name: "Assistant" })).toBeNull();
    expect(screen.queryByRole("link", { name: "Settings" })).toBeNull();
  });

  it("shows the assistant screens, Playbooks and Skills when the server proxies a conductor", () => {
    mount({ ...base, agentEnabled: true });
    expect(screen.getByRole("link", { name: "Chat" })).toHaveAttribute("href", "/agent/chat");
    expect(screen.getByRole("link", { name: "Sessions" })).toHaveAttribute(
      "href",
      "/agent/sessions",
    );
    expect(screen.getByRole("link", { name: "Memory" })).toHaveAttribute("href", "/agent/memory");
    expect(screen.getByRole("link", { name: "Assistant" })).toHaveAttribute(
      "href",
      "/agent/profile",
    );
    expect(screen.getByRole("link", { name: "Playbooks" })).toHaveAttribute(
      "href",
      "/agent/playbooks",
    );
    expect(screen.getByRole("link", { name: "Skills" })).toHaveAttribute("href", "/agent/skills");
    expect(screen.getByRole("link", { name: "MCP" })).toHaveAttribute("href", "/agent/mcp");
    expect(screen.getByRole("link", { name: "Channels" })).toHaveAttribute(
      "href",
      "/agent/channels",
    );
  });

  // MCP is a sibling of Assistant, not one of its talk screens, so it lights itself and leaves
  // Assistant alone — the same rule Playbooks and Skills follow.
  it("lights MCP on its own route without lighting Chat", () => {
    mount({ ...base, agentEnabled: true }, "/agent/mcp");
    expect(screen.getByRole("link", { name: "MCP" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Chat" })).not.toHaveAttribute("aria-current");
  });

  it("lights Channels on its own route without lighting Chat", () => {
    mount({ ...base, agentEnabled: true }, "/agent/channels");
    expect(screen.getByRole("link", { name: "Channels" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Chat" })).not.toHaveAttribute("aria-current");
  });

  it("puts Assistant above Run, and Run above Administer", () => {
    mount({ ...base, agentEnabled: true });
    const assistant = screen.getByText("Assistant", { selector: "p" });
    const run = screen.getByText("Run", { selector: "p" });
    const administer = screen.getByText("Administer", { selector: "p" });
    expect(assistant.compareDocumentPosition(run) & Node.DOCUMENT_POSITION_FOLLOWING).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    );
    expect(run.compareDocumentPosition(administer) & Node.DOCUMENT_POSITION_FOLLOWING).toBe(
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

  it("puts Settings in Administer, after the wordmark", () => {
    mount({ ...base, agentEnabled: true });
    const settings = screen.getByRole("link", { name: "Settings" });
    expect(settings).toHaveAttribute("href", "/agent/settings");
    const administer = screen.getByText("Administer", { selector: "p" });
    expect(administer.compareDocumentPosition(settings) & Node.DOCUMENT_POSITION_FOLLOWING).toBe(
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

  it("lights Chat on the chat route and not on Playbooks", () => {
    const who = { ...base, agentEnabled: true };
    const first = mount(who, "/agent/chat");
    expect(screen.getByRole("link", { name: "Chat" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Assistant" })).not.toHaveAttribute("aria-current");
    expect(screen.getByRole("link", { name: "Playbooks" })).not.toHaveAttribute("aria-current");
    first.unmount();

    const deep = mount(who, "/agent/chat/chat_01abc");
    expect(screen.getByRole("link", { name: "Chat" })).toHaveAttribute("aria-current", "page");
    deep.unmount();

    mount(who, "/agent/playbooks");
    expect(screen.getByRole("link", { name: "Playbooks" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Chat" })).not.toHaveAttribute("aria-current");
  });

  it("lights Settings on its own route without lighting Chat", () => {
    mount({ ...base, agentEnabled: true }, "/agent/settings/models");
    expect(screen.getByRole("link", { name: "Settings" })).toHaveAttribute("aria-current", "page");
    expect(screen.getByRole("link", { name: "Chat" })).not.toHaveAttribute("aria-current");
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
