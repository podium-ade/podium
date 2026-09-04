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

function mount(who: Viewer | undefined) {
  return render(
    <ViewerContext value={who}>
      <MemoryRouter>
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
  });

  it("hides it before WhoAmI has answered at all", () => {
    mount(undefined);
    expect(screen.queryByRole("link", { name: "Agent" })).toBeNull();
  });

  it("shows the Agent tab when the server proxies a conductor", () => {
    mount({ ...base, agentEnabled: true });
    expect(screen.getByRole("link", { name: "Agent" })).toHaveAttribute("href", "/agent");
  });
});
