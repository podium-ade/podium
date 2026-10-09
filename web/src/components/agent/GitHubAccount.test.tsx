import { StrictMode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { rememberGitHubPending } from "../../lib/github";
import { GitHubAccountCard } from "./GitHubAccountCard";
import { GitHubCallback } from "./GitHubCallback";

const getGitHubAccount = vi.fn();
const startGitHubOAuth = vi.fn();
const completeGitHubOAuth = vi.fn();
const disconnectGitHubAccount = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      getGitHubAccount: (...a: unknown[]) => getGitHubAccount(...a),
      startGitHubOAuth: (...a: unknown[]) => startGitHubOAuth(...a),
      completeGitHubOAuth: (...a: unknown[]) => completeGitHubOAuth(...a),
      disconnectGitHubAccount: (...a: unknown[]) => disconnectGitHubAccount(...a),
    },
  };
});

function mount(node: React.ReactNode, path = "/agent/settings/account", strict = false) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const tree = (
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={[path]}>{node}</MemoryRouter>
    </QueryClientProvider>
  );
  return render(strict ? <StrictMode>{tree}</StrictMode> : tree);
}

describe("GitHubAccountCard", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    sessionStorage.clear();
  });

  it("starts the connection with this app's callback and parks the flow", async () => {
    getGitHubAccount.mockResolvedValue({ account: { available: true, connected: false } });
    startGitHubOAuth.mockResolvedValue({ flowId: "f1", state: "s1", authorizeUrl: "https://github.example/authorize" });
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, origin: "https://podium.test", assign });
    mount(<GitHubAccountCard />);

    fireEvent.click(await screen.findByRole("button", { name: /connect github/i }));
    await waitFor(() => expect(assign).toHaveBeenCalledWith("https://github.example/authorize"));
    expect(startGitHubOAuth).toHaveBeenCalledWith({ redirectUri: "https://podium.test/agent/github/callback" });
    expect(sessionStorage.getItem("podium.github.oauth.pending")).toContain("f1");
    vi.unstubAllGlobals();
  });

  it("shows the connected account and disconnects it", async () => {
    getGitHubAccount.mockResolvedValue({
      account: { available: true, connected: true, githubLogin: "ada-gh", name: "Ada L" },
    });
    disconnectGitHubAccount.mockResolvedValue({ account: { available: true, connected: false } });
    mount(<GitHubAccountCard />);

    expect(await screen.findByText("@ada-gh")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /disconnect github/i }));
    await waitFor(() => expect(disconnectGitHubAccount).toHaveBeenCalled());
  });

  it("tells the person to contact an admin when connecting is unavailable", async () => {
    getGitHubAccount.mockResolvedValue({ account: { available: false, connected: false } });
    mount(<GitHubAccountCard />);
    expect(await screen.findByText(/not enabled for your organization yet/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /connect github/i })).not.toBeInTheDocument();
  });
});

describe("GitHubCallback", () => {
  beforeEach(() => {
    vi.resetAllMocks();
    sessionStorage.clear();
  });

  it("exchanges the code exactly once and names the account", async () => {
    rememberGitHubPending({ flowId: "f1", state: "s1" });
    completeGitHubOAuth.mockResolvedValue({ account: { connected: true, githubLogin: "ada-gh" } });
    mount(<GitHubCallback />, "/agent/github/callback?code=c1&state=s1", true);

    expect(await screen.findByText("Connected as @ada-gh")).toBeInTheDocument();
    expect(completeGitHubOAuth).toHaveBeenCalledTimes(1);
    expect(completeGitHubOAuth).toHaveBeenCalledWith({ flowId: "f1", code: "c1", state: "s1" });
  });

  it("explains a denied consent without calling the conductor", async () => {
    rememberGitHubPending({ flowId: "f1", state: "s1" });
    mount(<GitHubCallback />, "/agent/github/callback?error=access_denied&error_description=The+user+denied");
    expect(await screen.findByText("GitHub was not connected")).toBeInTheDocument();
    expect(completeGitHubOAuth).not.toHaveBeenCalled();
  });
});
