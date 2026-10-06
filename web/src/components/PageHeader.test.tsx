import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { IdentityKind } from "../gen/podium/v1/identity_pb";
import { ViewerContext, type Viewer } from "../lib/identity";
import { resetNotificationsForTests } from "../lib/notifications";
import { PageFrame } from "./PageHeader";

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

function mount(who: Viewer) {
  return render(
    <ViewerContext value={who}>
      <PageFrame title="Tasks" actions={<button type="button">New task</button>}>
        <p>body</p>
      </PageFrame>
    </ViewerContext>,
  );
}

describe("PageFrame", () => {
  it("hides the bell when there is no conductor", () => {
    resetNotificationsForTests();
    mount(base);
    expect(screen.getByRole("heading", { name: "Tasks" })).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^Notifications/ })).toBeNull();
  });

  it("puts the bell after the page actions, at the end of the toolbar", () => {
    resetNotificationsForTests();
    mount({ ...base, agentEnabled: true });
    const action = screen.getByRole("button", { name: "New task" });
    const bell = screen.getByRole("button", { name: "Notifications" });
    expect(action.compareDocumentPosition(bell) & Node.DOCUMENT_POSITION_FOLLOWING).toBe(
      Node.DOCUMENT_POSITION_FOLLOWING,
    );
    const bar = screen.getByRole("banner");
    expect(bar.lastElementChild?.lastElementChild).toBe(bell);
  });
});
