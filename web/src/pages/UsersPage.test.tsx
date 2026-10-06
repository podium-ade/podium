import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { IdentityKind } from "../gen/podium/v1/identity_pb";
import { ViewerContext, type Viewer } from "../lib/identity";
import { RoleAdmin, RoleMember, RoleOwner } from "../lib/rbac";
import { UsersPage } from "./UsersPage";
import { ToastHost } from "../components/Toast";

const listUsers = vi.fn();
const setUserRole = vi.fn();

vi.mock("../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../lib/client")>("../lib/client");
  return {
    ...actual,
    users: {
      listUsers: (...a: unknown[]) => listUsers(...a),
      setUserRole: (...a: unknown[]) => setUserRole(...a),
    },
  };
});

function viewer(partial: Partial<Viewer> = {}): Viewer {
  return {
    login: "alice@acme.com",
    displayName: "Alice",
    kind: IdentityKind.USER,
    agentEnabled: true,
    serverVersion: "dev",
    roles: [RoleOwner],
    claimed: true,
    hostedDomain: "acme.com",
    canClaim: false,
    googleAuthEnabled: true,
    claimDomain: "acme.com",
    pictureUrl: "",
    ...partial,
  };
}

function mount(who: Viewer) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <ViewerContext value={who}>
          <UsersPage />
        </ViewerContext>
      </ToastHost>
    </QueryClientProvider>,
  );
}

const alice = {
  login: "alice@acme.com",
  displayName: "Alice",
  roles: [RoleOwner],
  hostedDomain: "acme.com",
  pictureUrl: "",
  lastSeenAt: timestampFromDate(new Date(Date.now() - 86_400_000)),
};
const bob = {
  login: "bob@acme.com",
  displayName: "Bob",
  roles: [RoleMember],
  hostedDomain: "acme.com",
  pictureUrl: "",
  lastSeenAt: timestampFromDate(new Date(Date.now() - 3_600_000)),
};

describe("UsersPage", () => {
  beforeEach(() => {
    listUsers.mockReset();
    setUserRole.mockReset();
    listUsers.mockResolvedValue({ users: [alice, bob] });
  });

  it("lists people and says what each role can do", async () => {
    mount(viewer());
    expect(await screen.findByText("Alice")).toBeInTheDocument();
    expect(screen.getByText("bob@acme.com")).toBeInTheDocument();
    expect(screen.getByText("Permission")).toBeInTheDocument();
    expect(screen.getByText(/Change anyone's role/i)).toBeInTheDocument();
    expect(screen.getByLabelText("Owner: Change anyone's role")).toBeInTheDocument();
  });

  it("lets an owner change a member's role", async () => {
    setUserRole.mockResolvedValue({ user: { ...bob, roles: [RoleAdmin] } });
    mount(viewer());
    const select = await screen.findByLabelText("Role for bob@acme.com");
    await userEvent.click(select);
    await userEvent.click(await screen.findByRole("option", { name: "Admin" }));
    expect(setUserRole).toHaveBeenCalledWith({ login: "bob@acme.com", role: RoleAdmin });
  });

  it("does not let a member change roles", async () => {
    mount(viewer({ login: "bob@acme.com", displayName: "Bob", roles: [RoleMember] }));
    await screen.findByText("Alice");
    expect(screen.queryByLabelText("Role for bob@acme.com")).not.toBeInTheDocument();
    expect(screen.getByText(/Changing a role takes the owner role/i)).toBeInTheDocument();
  });

  it("does not let the last owner demote themselves", async () => {
    mount(viewer());
    const select = await screen.findByLabelText("Role for alice@acme.com");
    expect(select).toBeDisabled();
  });
});
