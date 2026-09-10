import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";
import { create } from "@bufbuild/protobuf";
import { TokenGate } from "./TokenGate";
import { Header } from "./Header";
import { IdentityKind, WhoAmIResponseSchema } from "../gen/podium/v1/identity_pb";
import { clearToken } from "../lib/auth";

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

describe("TokenGate", () => {
  beforeEach(() => {
    whoAmI.mockReset();
    clearToken();
  });

  it("never prompts when WhoAmI succeeds unauthenticated, and shows the tailnet login", async () => {
    whoAmI.mockResolvedValue(
      create(WhoAmIResponseSchema, {
        login: "alvaro@example.com",
        displayName: "Alvaro Ibarguen",
        kind: IdentityKind.USER,
      }),
    );
    mount();

    await waitFor(() => expect(screen.getByText("the app")).toBeTruthy());
    expect(screen.queryByLabelText("Dev token")).toBeNull();
    expect(screen.getByText("Alvaro Ibarguen")).toBeTruthy();
  });

  it("falls back to the login name when the profile has no display name", async () => {
    whoAmI.mockResolvedValue(
      create(WhoAmIResponseSchema, { login: "alvaro@example.com", kind: IdentityKind.USER }),
    );
    mount();
    await waitFor(() => expect(screen.getByText("alvaro@example.com")).toBeTruthy());
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
});
