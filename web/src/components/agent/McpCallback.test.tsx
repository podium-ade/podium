import { StrictMode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { render, screen, waitFor } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";
import { ToastHost } from "../Toast";
import { PENDING_KEY, type PendingMcpOAuth } from "../../lib/mcp";
import { McpCallback } from "./McpCallback";

const completeMcpOAuth = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: { completeMcpOAuth: (...a: unknown[]) => completeMcpOAuth(...a) },
  };
});

function pending(over: Partial<PendingMcpOAuth> = {}) {
  sessionStorage.setItem(
    PENDING_KEY,
    JSON.stringify({
      flowId: "flow-1",
      state: "st-1",
      name: "linear",
      issuer: "https://auth.linear.app",
      ...over,
    }),
  );
}

function mount(search: string, strict = false) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const tree = (
    <QueryClientProvider client={qc}>
      <ToastHost>
        <MemoryRouter initialEntries={[`/agent/mcp/callback${search}`]}>
          <McpCallback />
        </MemoryRouter>
      </ToastHost>
    </QueryClientProvider>
  );
  // StrictMode is how the double-invoke that would spend the code twice is actually
  // reproduced. It is opt-in per test because it doubles every render in the file otherwise.
  return render(strict ? <StrictMode>{tree}</StrictMode> : tree);
}

describe("McpCallback", () => {
  beforeEach(() => {
    completeMcpOAuth.mockReset();
    sessionStorage.clear();
  });

  // The code reaches the conductor over the authenticated API rather than on a redirect the
  // server would have to serve unauthenticated. This is that hop.
  it("hands the code and the state to the conductor and reports who signed in", async () => {
    pending();
    completeMcpOAuth.mockResolvedValue({
      server: { name: "linear", account: "alice@example.test" },
    });
    mount("?code=code-1&state=st-1");

    await waitFor(() =>
      expect(completeMcpOAuth).toHaveBeenCalledWith({
        flowId: "flow-1",
        code: "code-1",
        state: "st-1",
      }),
    );
    expect(await screen.findByText("Signed in to linear")).toBeInTheDocument();
    expect(screen.getByText(/alice@example.test/)).toBeInTheDocument();
  });

  // An authorization code is single-use at the authorization server, so spending it twice
  // would turn a sign-in that worked into an error an operator cannot explain. React's
  // development double-invoke is exactly the thing that would do it.
  it("exchanges the code exactly once, even under StrictMode's double-invoke", async () => {
    pending();
    completeMcpOAuth.mockResolvedValue({ server: { name: "linear" } });
    mount("?code=code-1&state=st-1", true);

    await waitFor(() => expect(completeMcpOAuth).toHaveBeenCalled());
    expect(completeMcpOAuth).toHaveBeenCalledTimes(1);
    expect(await screen.findByText("Signed in to linear")).toBeInTheDocument();
  });

  // The parked flow is taken, not read: a reload of this page must not look like a second
  // callback for the same sign-in.
  it("consumes the parked sign-in", async () => {
    pending();
    completeMcpOAuth.mockResolvedValue({ server: { name: "linear" } });
    mount("?code=code-1&state=st-1");

    await waitFor(() => expect(completeMcpOAuth).toHaveBeenCalled());
    expect(sessionStorage.getItem(PENDING_KEY)).toBeNull();
  });

  // A denied consent screen comes back here rather than to the conductor, so this page is
  // what has to explain it.
  it("explains a refusal from the authorization server without calling the conductor", async () => {
    pending();
    mount("?error=access_denied&error_description=The+user+said+no");

    expect(await screen.findByText("The sign-in was not granted")).toBeInTheDocument();
    expect(screen.getByText("The user said no")).toBeInTheDocument();
    expect(completeMcpOAuth).not.toHaveBeenCalled();
  });

  it("says there is nothing in progress for a stale tab", async () => {
    mount("?code=code-1&state=st-1");
    expect(await screen.findByText("There is no sign-in in progress")).toBeInTheDocument();
    expect(completeMcpOAuth).not.toHaveBeenCalled();
  });

  it("says the same when the address carries no code at all", async () => {
    pending();
    mount("");
    expect(await screen.findByText("There is no sign-in in progress")).toBeInTheDocument();
    expect(completeMcpOAuth).not.toHaveBeenCalled();
  });

  // A state that does not match is refused by the conductor, which is the comparison that
  // counts. What this page owes the operator is the reason and the way back.
  it("shows the conductor's refusal and says the sign-in has to be restarted", async () => {
    pending();
    completeMcpOAuth.mockRejectedValue(
      new ConnectError("this callback does not belong to that sign-in", Code.PermissionDenied),
    );
    mount("?code=code-1&state=wrong");

    expect(
      await screen.findByText("this callback does not belong to that sign-in"),
    ).toBeInTheDocument();
    expect(screen.getByText(/can only be used once/)).toBeInTheDocument();
  });
});
