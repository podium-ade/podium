import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { IdentityKind } from "../gen/podium/v1/identity_pb";
import { ProviderSettingsSchema } from "../gen/podium/agent/v1/agent_pb";
import { Header } from "../components/Header";
import { NotificationsProvider } from "../hooks/useNotifications";
import { ViewerContext, type Viewer } from "../lib/identity";
import { resetNotificationsForTests, writeNotifications, type AppNotification } from "../lib/notifications";
import { NotificationsPage } from "./NotificationsPage";

const getSettings = vi.fn();

vi.mock("../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../lib/client")>("../lib/client");
  return {
    ...actual,
    agent: { getSettings: (...args: unknown[]) => getSettings(...args) },
  };
});

const viewer: Viewer = {
  login: "local",
  displayName: "",
  kind: IdentityKind.LOCAL_TOKEN,
  agentEnabled: true,
  serverVersion: "dev",
  roles: [],
  claimed: false,
  hostedDomain: "",
  canClaim: false,
  googleAuthEnabled: false,
  claimDomain: "",
  pictureUrl: "",
};

function expired() {
  return create(ProviderSettingsSchema, {
    provider: "xai",
    keySet: true,
    authKind: "oauth",
    expiresAt: timestampFromDate(new Date(Date.now() - 86_400_000)),
  });
}

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = render(
    <QueryClientProvider client={qc}>
      <ViewerContext value={viewer}>
        <MemoryRouter>
          <NotificationsProvider>
            <Header />
            <NotificationsPage />
          </NotificationsProvider>
        </MemoryRouter>
      </ViewerContext>
    </QueryClientProvider>,
  );
  return { ...view, qc };
}

describe("NotificationsPage", () => {
  beforeEach(() => {
    resetNotificationsForTests();
    getSettings.mockReset();
    getSettings.mockResolvedValue({ providers: [] });
  });

  it("records an expired sign-in, links to Models, and keeps it after it clears", async () => {
    getSettings.mockResolvedValue({ providers: [expired()] });
    const { qc } = mount();

    const open = await screen.findByTestId("notification");
    expect(open).toHaveAttribute("data-state", "open");
    expect(open).toHaveTextContent("Sign in to Grok again");
    expect(screen.getByRole("link", { name: "Sign in again" })).toHaveAttribute(
      "href",
      "/agent/settings/models",
    );

    getSettings.mockResolvedValue({ providers: [] });
    await qc.invalidateQueries({ queryKey: ["agent", "settings"] });

    await waitFor(() =>
      expect(screen.getByTestId("notification")).toHaveAttribute("data-state", "resolved"),
    );
    expect(screen.getByTestId("notification")).toHaveTextContent("resolved");
  });

  it("clears resolved rows and leaves an open one", async () => {
    const open: AppNotification = {
      id: "open",
      key: "provider-oauth-expired:xai",
      level: "alert",
      title: "Sign in to Grok again",
      body: "still expired",
      href: "/agent/settings/models",
      action: "Sign in again",
      createdAt: Date.now() - 1000,
      readAt: Date.now(),
    };
    const past: AppNotification = {
      ...open,
      id: "past",
      key: "provider-oauth-expired:openai",
      level: "alert",
      title: "Sign in to GPT again",
      createdAt: Date.now() - 5000,
      resolvedAt: Date.now() - 1000,
    };
    writeNotifications([open, past]);
    getSettings.mockResolvedValue({ providers: [expired()] });
    mount();

    expect(await screen.findByText("Sign in to GPT again")).toBeInTheDocument();
    await userEvent.click(screen.getByTestId("notifications-clear-history"));
    expect(screen.queryByText("Sign in to GPT again")).toBeNull();
    expect(screen.getByText("Sign in to Grok again")).toBeInTheDocument();
    expect(screen.getByTestId("notifications-history-empty")).toBeInTheDocument();
  });
});
