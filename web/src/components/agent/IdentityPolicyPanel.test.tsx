import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { IdentityPolicySchema } from "../../gen/podium/agent/v1/agent_pb";
import { ToastHost } from "../Toast";
import { IdentityPolicyPanel } from "./IdentityPolicyPanel";

const getIdentityPolicy = vi.fn();
const setIdentityPolicy = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      getIdentityPolicy: (...a: unknown[]) => getIdentityPolicy(...a),
      setIdentityPolicy: (...a: unknown[]) => setIdentityPolicy(...a),
    },
  };
});

const defaults = {
  linear: "person_then_app",
  githubWrite: "person_then_app",
  githubRead: "person_only",
  modelKey: "person_then_app",
  sharedModelKey: "app",
  slackOutsiders: "refuse",
  linearOutsiders: "refuse",
  githubOutsiders: "review",
};

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <IdentityPolicyPanel />
      </ToastHost>
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  getIdentityPolicy.mockReset();
  setIdentityPolicy.mockReset();
  getIdentityPolicy.mockResolvedValue({ policy: create(IdentityPolicySchema, defaults) });
  setIdentityPolicy.mockImplementation(({ policy }) =>
    Promise.resolve({ policy: create(IdentityPolicySchema, { ...defaults, ...policy }) }),
  );
});

describe("IdentityPolicyPanel", () => {
  it("shows the policy and saves only a change", async () => {
    mount();
    expect(await screen.findByTestId("identity-linear")).toHaveTextContent("Theirs, else Podium");
    expect(screen.getByTestId("identity-githubOutsiders")).toHaveTextContent("Review command only");
    const save = screen.getByTestId("identity-save");
    expect(save).toBeDisabled();

    await userEvent.click(screen.getByTestId("identity-linearOutsiders"));
    await userEvent.click(await screen.findByRole("option", { name: "Delegated issues only" }));
    expect(save).toBeEnabled();
    await userEvent.click(save);

    await waitFor(() =>
      expect(setIdentityPolicy).toHaveBeenCalledWith({
        policy: { linearOutsiders: "fixed" },
      }),
    );
    await waitFor(() => expect(screen.getByTestId("identity-save")).toBeDisabled());
  });

  it("sends one save for a double click", async () => {
    mount();
    await userEvent.click(await screen.findByTestId("identity-slackOutsiders"));
    await userEvent.click(await screen.findByRole("option", { name: "Answer as Podium" }));
    await userEvent.dblClick(screen.getByTestId("identity-save"));
    await waitFor(() => expect(screen.getByTestId("identity-save")).toBeDisabled());
    expect(setIdentityPolicy).toHaveBeenCalledTimes(1);
  });

  it("names each setting so the two Linear ones differ", async () => {
    mount();
    expect(await screen.findByRole("combobox", { name: "Linear" })).toBeInTheDocument();
    expect(screen.getByRole("combobox", { name: "Linear members" })).toBeInTheDocument();
  });

  it("opens a dropdown only from the dropdown, not from its setting's name", async () => {
    mount();
    await userEvent.click(await screen.findByText("Model key in private chats"));
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(screen.getByRole("combobox", { name: "Model key in private chats" })).toHaveAccessibleDescription(
      /only one person is in/,
    );
  });

  it("lets an admin have shared conversations spend the asker's own plan", async () => {
    mount();
    const shared = await screen.findByRole("combobox", { name: "Model key in shared conversations" });
    expect(shared).toHaveTextContent("Always Podium's");
    expect(shared).toHaveAccessibleDescription(/never a personal API key/);
    await userEvent.click(shared);
    await userEvent.click(await screen.findByRole("option", { name: "Theirs, else Podium's" }));
    await userEvent.click(screen.getByTestId("identity-save"));
    await waitFor(() =>
      expect(setIdentityPolicy).toHaveBeenCalledWith({ policy: { sharedModelKey: "plan_then_app" } }),
    );
  });

  it("offers GitHub reads no Always option", async () => {
    mount();
    await userEvent.click(await screen.findByTestId("identity-githubRead"));
    const names = (await screen.findAllByRole("option")).map((o) => o.textContent);
    expect(names).toEqual(["Theirs, else Podium", "Theirs only"]);
  });
});
