import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes } from "react-router";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";
import { IdentityKind } from "../gen/podium/v1/identity_pb";
import { ViewerContext, type Viewer } from "../lib/identity";
import { RoleAdmin, RoleMember } from "../lib/rbac";
import { ToastHost } from "../components/Toast";
import { OrgSettingsPage } from "./OrgSettingsPage";

const getOrganization = vi.fn();
const updateOrganization = vi.fn();

vi.mock("../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../lib/client")>("../lib/client");
  return {
    ...actual,
    organization: {
      getOrganization: (...a: unknown[]) => getOrganization(...a),
      updateOrganization: (...a: unknown[]) => updateOrganization(...a),
    },
    users: { listUsers: () => Promise.resolve({ users: [] }), setUserRole: vi.fn() },
  };
});

const admin: Viewer = {
  login: "cara@acme.com",
  displayName: "Cara",
  kind: IdentityKind.USER,
  agentEnabled: true,
  serverVersion: "dev",
  roles: [RoleAdmin],
  claimed: true,
  hostedDomain: "acme.com",
  canClaim: false,
  googleAuthEnabled: true,
  claimDomain: "acme.com",
  pictureUrl: "",
};
const member: Viewer = { ...admin, login: "bob@acme.com", roles: [RoleMember] };

function mount(path: string, who: Viewer) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <ViewerContext value={who}>
          <MemoryRouter initialEntries={[path]}>
            <Routes>
              <Route path="/org/*" element={<OrgSettingsPage />} />
            </Routes>
          </MemoryRouter>
        </ViewerContext>
      </ToastHost>
    </QueryClientProvider>,
  );
}

function sections() {
  return within(screen.getByRole("navigation", { name: "Organization settings" }))
    .getAllByRole("link")
    .map((l) => l.textContent);
}

describe("OrgSettingsPage", () => {
  beforeEach(() => {
    getOrganization.mockReset().mockResolvedValue({ organization: { organizationName: "", deploymentName: "" } });
    updateOrganization.mockReset();
  });

  it("opens General and lists every section an admin shares with the organization", async () => {
    mount("/org/settings", admin);
    expect(await screen.findByRole("region", { name: "General" })).toBeInTheDocument();
    expect(sections()).toEqual(["General", "Members", "Connections", "Model keys", "Backend", "Routines policy"]);
  });

  it("gives a member General and Members, read-only", async () => {
    getOrganization.mockResolvedValue({ organization: { organizationName: "Acme Corp", deploymentName: "Acme AI" } });
    mount("/org/settings/connections", member);
    expect(await screen.findByRole("region", { name: "General" })).toBeInTheDocument();
    expect(sections()).toEqual(["General", "Members"]);
    expect(await screen.findByDisplayValue("Acme AI")).toBeDisabled();
    expect(screen.getByLabelText("Organization name")).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Save" })).toBeNull();
    expect(screen.getByText(/Only an admin or owner can change these/)).toBeInTheDocument();
  });

  it("renames the deployment and the organization", async () => {
    updateOrganization.mockResolvedValue({ organization: { organizationName: "Acme Corp", deploymentName: "Acme AI" } });
    mount("/org/settings/general", admin);
    const deployment = await screen.findByLabelText("Deployment name");
    // The fields wait for the saved names, so typing never races the read.
    await waitFor(() => expect(deployment).toBeEnabled());
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    expect(screen.getByTestId("org-preview")).toHaveTextContent("People will see Podium.");

    await userEvent.type(screen.getByLabelText("Organization name"), " Acme Corp ");
    await userEvent.type(deployment, "Acme AI");
    expect(screen.getByTestId("org-preview")).toHaveTextContent(
      "People will see Acme AI. The assistant calls itself Acme AI and knows it works for Acme Corp.",
    );
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(updateOrganization).toHaveBeenCalledWith({ organizationName: "Acme Corp", deploymentName: "Acme AI" }),
    );
  });

  it("says why the server refused a name", async () => {
    updateOrganization.mockRejectedValue(
      new ConnectError("the deployment name cannot hold a line break, a tab, < or >", Code.InvalidArgument),
    );
    mount("/org/settings/general", admin);
    const deployment = await screen.findByLabelText("Deployment name");
    await waitFor(() => expect(deployment).toBeEnabled());
    await userEvent.type(deployment, "Acme <AI>");
    await userEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/cannot hold a line break/)).toBeInTheDocument();
  });
});
