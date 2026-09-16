import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { TokenGate } from "./TokenGate";
import { Header } from "./Header";
import { IdentityKind, WhoAmIResponseSchema } from "../gen/podium/v1/identity_pb";
import { clearToken, type AuthStatus } from "../lib/auth";

const whoAmI = vi.fn();

vi.mock("../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../lib/client")>("../lib/client");
  return { ...actual, identity: { whoAmI: (...a: unknown[]) => whoAmI(...a) } };
});

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <TokenGate>
        <MemoryRouter>
          <Header />
          <div>the app</div>
        </MemoryRouter>
      </TokenGate>
    </QueryClientProvider>,
  );
}

const statusOff: AuthStatus = { google: false, claimed: false, hosted_domain: "" };
const statusOn: AuthStatus = { google: true, claimed: false, hosted_domain: "" };
const statusClaimed: AuthStatus = { google: true, claimed: true, hosted_domain: "acme.com" };

function stubStatus(status: AuthStatus) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => ({
      ok: true,
      json: async () => status,
    })),
  );
}

describe("TokenGate", () => {
  beforeEach(() => {
    whoAmI.mockReset();
    clearToken();
    vi.stubGlobal(
      "fetch",
      vi.fn(async (input: RequestInfo) => {
        const url = String(input);
        if (url.includes("/auth/status")) {
          return {
            ok: true,
            json: async () => statusOff,
          } as Response;
        }
        throw new Error(`unexpected fetch ${url}`);
      }),
    );
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("never prompts when WhoAmI succeeds unauthenticated, and shows the tailnet login", async () => {
    whoAmI.mockResolvedValue(
      create(WhoAmIResponseSchema, {
        login: "user@example.com",
        displayName: "Example User",
        kind: IdentityKind.USER,
      }),
    );
    mount();

    await waitFor(() => expect(screen.getByText("the app")).toBeTruthy());
    expect(screen.queryByLabelText("Dev token")).toBeNull();
    expect(screen.getByText("Example User")).toBeTruthy();
  });

  it("falls back to the login name when the profile has no display name", async () => {
    whoAmI.mockResolvedValue(
      create(WhoAmIResponseSchema, { login: "user@example.com", kind: IdentityKind.USER }),
    );
    mount();
    await waitFor(() => expect(screen.getAllByText("user@example.com").length).toBeGreaterThan(0));
  });

  it("shows local in the header under the local transport", async () => {
    whoAmI.mockResolvedValue(create(WhoAmIResponseSchema, { login: "local", kind: IdentityKind.LOCAL_TOKEN }));
    mount();

    await waitFor(() => expect(screen.getByText("the app")).toBeTruthy());
    expect(screen.getByText("local")).toBeTruthy();
  });

  it("asks for the dev token when the probe is Unauthenticated", async () => {
    whoAmI.mockRejectedValue(new ConnectError("unauthenticated", Code.Unauthenticated));
    mount();

    await waitFor(() => expect(screen.getByLabelText("Dev token")).toBeTruthy());
    expect(screen.queryByText("the app")).toBeNull();
    expect(screen.getByText(/shared bearer token/)).toBeTruthy();
  });

  it("re-probes with the token the operator typed", async () => {
    whoAmI.mockRejectedValueOnce(new ConnectError("unauthenticated", Code.Unauthenticated));
    whoAmI.mockResolvedValue(create(WhoAmIResponseSchema, { login: "local", kind: IdentityKind.LOCAL_TOKEN }));
    mount();

    const user = userEvent.setup();
    await user.type(await screen.findByLabelText("Dev token"), "devtoken");
    await user.click(screen.getByRole("button", { name: "Connect" }));

    await waitFor(() => expect(screen.getByText("the app")).toBeTruthy());
    expect(whoAmI).toHaveBeenCalledTimes(2);
  });

  it("shows a non-auth failure rather than blaming the token", async () => {
    whoAmI.mockRejectedValue(new ConnectError("postgres unreachable", Code.Unavailable));
    mount();
    await waitFor(() => expect(screen.getByText("postgres unreachable")).toBeTruthy());
  });

  it("offers Google Workspace sign-in and does not ask for a local token", async () => {
    whoAmI.mockRejectedValue(new ConnectError("unauthenticated", Code.Unauthenticated));
    stubStatus(statusOn);
    mount();
    await waitFor(() =>
      expect(screen.getByRole("link", { name: "Sign in with Google Workspace" })).toBeTruthy(),
    );
    expect(screen.getByRole("link", { name: "Sign in with Google Workspace" })).toHaveAttribute(
      "href",
      "/auth/google/start",
    );
    expect(screen.queryByLabelText("Dev token")).toBeNull();
  });

  it("does not name the Workspace on a claimed instance", async () => {
    whoAmI.mockRejectedValue(new ConnectError("unauthenticated", Code.Unauthenticated));
    stubStatus(statusClaimed);
    mount();
    await waitFor(() =>
      expect(screen.getByRole("link", { name: "Sign in with Google Workspace" })).toBeTruthy(),
    );
    expect(screen.queryByLabelText("Dev token")).toBeNull();
    expect(screen.getByText(/This instance of Podium is claimed/)).toBeTruthy();
    expect(screen.queryByText(/acme.com/)).toBeNull();
  });

  it("lets a local token into the app the same way the CLI does", async () => {
    whoAmI.mockResolvedValue(
      create(WhoAmIResponseSchema, {
        login: "local",
        kind: IdentityKind.LOCAL_TOKEN,
        googleAuthEnabled: true,
        claimed: true,
        hostedDomain: "acme.com",
      }),
    );
    stubStatus(statusClaimed);
    mount();
    await waitFor(() => expect(screen.getByText("the app")).toBeTruthy());
    expect(screen.getByText("local")).toBeTruthy();
    expect(screen.queryByLabelText("Dev token")).toBeNull();
  });

  it("keeps a failed Google callback on the sign-in screen even with a local token", async () => {
    whoAmI.mockResolvedValue(
      create(WhoAmIResponseSchema, {
        login: "local",
        kind: IdentityKind.LOCAL_TOKEN,
        googleAuthEnabled: true,
      }),
    );
    stubStatus(statusOn);
    window.history.replaceState({}, "", "/?auth_error=failed");
    mount();
    await waitFor(() =>
      expect(screen.getByRole("link", { name: "Sign in with Google Workspace" })).toBeTruthy(),
    );
    expect(screen.getByText("Google sign-in failed. Try again.")).toBeTruthy();
    expect(screen.queryByText("the app")).toBeNull();
    window.history.replaceState({}, "", "/");
  });

  it("asks the first Workspace user to type the domain before claiming", async () => {
    whoAmI.mockResolvedValue(
      create(WhoAmIResponseSchema, {
        login: "alice@acme.com",
        displayName: "Alice",
        kind: IdentityKind.USER,
        canClaim: true,
        claimDomain: "acme.com",
        googleAuthEnabled: true,
      }),
    );
    mount();
    await waitFor(() => expect(screen.getByText("Claim this instance")).toBeTruthy());
    expect(screen.queryByText("the app")).toBeNull();
    expect(screen.getByRole("button", { name: "Claim for acme.com" })).toBeDisabled();
  });
});
