import { Suspense } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import { MemoryPanel } from "./MemoryPanel";
import { ViewerContext, type Viewer } from "../../lib/identity";
import { IdentityKind } from "../../gen/podium/v1/identity_pb";
import { ToastHost } from "../Toast";

const listMemories = vi.fn();
const searchMemories = vi.fn();
const deleteMemory = vi.fn();
const getMemorySettings = vi.fn();
const setMemorySettings = vi.fn();
const listAgents = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      listMemories: (...a: unknown[]) => listMemories(...a),
      searchMemories: (...a: unknown[]) => searchMemories(...a),
      deleteMemory: (...a: unknown[]) => deleteMemory(...a),
      getMemorySettings: (...a: unknown[]) => getMemorySettings(...a),
      setMemorySettings: (...a: unknown[]) => setMemorySettings(...a),
      listAgents: (...a: unknown[]) => listAgents(...a),
    },
  };
});

function admin(): Viewer {
  return {
    login: "local",
    displayName: "",
    kind: IdentityKind.LOCAL_TOKEN,
    agentEnabled: true,
    serverVersion: "test",
    roles: [],
    claimed: false,
    hostedDomain: "",
    canClaim: false,
    googleAuthEnabled: false,
    claimDomain: "",
    pictureUrl: "",
  };
}

function member(): Viewer {
  return {
    ...admin(),
    login: "ada@acme.com",
    kind: IdentityKind.USER,
    roles: ["member"],
    claimed: true,
    googleAuthEnabled: true,
  };
}

function mount(viewer?: Viewer) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <ViewerContext.Provider value={viewer}>
      <QueryClientProvider client={qc}>
        <ToastHost>
          <MemoryRouter>
            <Suspense fallback={null}>
              <MemoryPanel />
            </Suspense>
          </MemoryRouter>
        </ToastHost>
      </QueryClientProvider>
    </ViewerContext.Provider>,
  );
}

const providers = [
  { provider: "anthropic", label: "Anthropic", available: true, reason: "" },
  { provider: "xai", label: "xAI", available: false, reason: "No xAI key is saved for everyone." },
  { provider: "openai", label: "OpenAI", available: true, reason: "" },
  { provider: "google", label: "Google", available: false, reason: "No Google key is saved for everyone." },
];

function settings(over: Record<string, unknown> = {}) {
  return {
    provider: "",
    model: "",
    setBy: "",
    proxyEnabled: true,
    providers,
    ...over,
  };
}

const memory = {
  id: "ed1bd235-bd25-483c-beff-4d54ff776e52",
  text: "Bob owns the Podium scheduler.",
  factType: "world",
  tags: ["source:slack", "playbook:general"],
  metadata: {
    session_id: "sess_01abc",
    turn_id: "turn_01abc",
    task_id: "task_01xyz",
    source_ref: "C0123/1725000000.000100/1725000000.000100",
    source_url: "https://example.slack.com/archives/C0123/p1725000000000100",
  },
  createdAt: timestampFromDate(new Date(Date.now() - 60_000)),
  entities: ["Bob", "scheduler"],
  context: "podium agent, playbook general",
  documentId: "turn_01abc",
};

describe("MemoryPanel", () => {
  beforeEach(() => {
    vi.useRealTimers();
    listMemories.mockReset();
    searchMemories.mockReset();
    deleteMemory.mockReset();
    getMemorySettings.mockReset();
    setMemorySettings.mockReset();
    listAgents.mockReset();
    listMemories.mockResolvedValue({ items: [memory], nextCursor: "", total: 1 });
    searchMemories.mockResolvedValue({ items: [] });
    deleteMemory.mockResolvedValue({});
    getMemorySettings.mockResolvedValue({
      settings: settings({ provider: "anthropic", model: "claude-haiku-5-5", setBy: "ada" }),
    });
    listAgents.mockResolvedValue({
      agents: [
        {
          provider: "anthropic",
          defaultModel: "claude-sonnet-5-5",
          models: [{ id: "claude-sonnet-5-5" }, { id: "claude-haiku-5-5" }],
        },
        { provider: "openai", defaultModel: "gpt-5-mini", models: [{ id: "gpt-5-mini" }] },
      ],
    });
  });

  it("shows a memory with the provenance a human needs to judge it", async () => {
    mount();
    const row = await screen.findByTestId("memory-row");

    expect(row).toHaveTextContent("Bob owns the Podium scheduler.");
    expect(row).toHaveTextContent("world");
    expect(row).toHaveTextContent("general");
    expect(row).toHaveTextContent("1m ago");
    expect(row).toHaveTextContent("about Bob, scheduler");

    // The source chip links back to the conversation that produced it.
    expect(screen.getByRole("link", { name: "slack" })).toHaveAttribute(
      "href",
      "https://example.slack.com/archives/C0123/p1725000000000100",
    );
    // And the task it came out of is one click away, so the logs are too.
    expect(screen.getByRole("link", { name: "task_01xyz" })).toHaveAttribute(
      "href",
      "/tasks/task_01xyz",
    );
  });

  it("does not link a source it has no url for", async () => {
    listMemories.mockResolvedValue({
      items: [{ ...memory, metadata: { turn_id: "turn_01abc" } }],
      nextCursor: "",
    });
    mount();
    const row = await screen.findByTestId("memory-row");
    expect(row).toHaveTextContent("slack");
    expect(screen.queryByRole("link", { name: "slack" })).toBeNull();
    expect(screen.queryByRole("link", { name: /task_/ })).toBeNull();
  });

  it("says so rather than showing 1970 when the memory has no timestamp", async () => {
    listMemories.mockResolvedValue({
      items: [{ ...memory, createdAt: undefined }],
      nextCursor: "",
    });
    mount();
    expect(await screen.findByText("learned at an unknown time")).toBeInTheDocument();
  });

  it("searches instead of listing once something is typed", async () => {
    searchMemories.mockResolvedValue({ items: [{ ...memory, text: "a searched fact" }] });
    mount();
    await screen.findByTestId("memory-row");

    await userEvent.type(screen.getByTestId("memory-search"), "who owns the scheduler");

    await waitFor(() =>
      expect(searchMemories).toHaveBeenCalledWith({ query: "who owns the scheduler", limit: 25 }),
    );
    expect(await screen.findByText("a searched fact")).toBeInTheDocument();
  });

  it("says a search matched nothing without claiming the memory is empty", async () => {
    mount();
    await userEvent.type(await screen.findByTestId("memory-search"), "nothing like this");
    expect(await screen.findByText("Nothing remembered matches that.")).toBeInTheDocument();
  });

  it("asks before forgetting, and the row goes when it is confirmed", async () => {
    mount();
    await userEvent.click(await screen.findByTestId("memory-delete"));

    expect(
      screen.getByText("Forget this? Every future agent turn stops seeing it."),
    ).toBeInTheDocument();
    expect(deleteMemory).not.toHaveBeenCalled();

    listMemories.mockResolvedValue({ items: [], nextCursor: "" });
    await userEvent.click(screen.getByTestId("memory-delete-confirm"));

    await waitFor(() =>
      expect(deleteMemory).toHaveBeenCalledWith({ id: "ed1bd235-bd25-483c-beff-4d54ff776e52" }),
    );
    expect(await screen.findByRole("status")).toHaveTextContent("Forgotten");
    await waitFor(() => expect(screen.queryByTestId("memory-row")).toBeNull());
  });

  it("keeps the memory when the confirm is declined", async () => {
    mount();
    await userEvent.click(await screen.findByTestId("memory-delete"));
    await userEvent.click(screen.getByRole("button", { name: "Keep" }));

    expect(deleteMemory).not.toHaveBeenCalled();
    expect(screen.getByTestId("memory-row")).toBeInTheDocument();
  });

  it("says the memory is not configured rather than showing an error", async () => {
    let rejectList: (err: unknown) => void = () => {};
    listMemories.mockReturnValue(
      new Promise((_, reject) => {
        rejectList = reject;
      }),
    );
    mount();
    // The empty bank and the search are other screens. Neither paints before
    // this request says the service is missing.
    expect(screen.queryByTestId("memory-search")).toBeNull();
    expect(screen.queryByText("Nothing remembered yet.")).toBeNull();
    rejectList(new ConnectError("memory is not configured on this host", Code.FailedPrecondition));
    expect(await screen.findByText("Memory isn't set up yet")).toBeInTheDocument();
    expect(screen.getByText(/Ask your administrator to turn it on/)).toBeInTheDocument();
    expect(screen.queryByText(/PODIUM_AGENT_MEMORY/)).toBeNull();
    expect(screen.queryByTestId("memory-search")).toBeNull();
    expect(screen.queryByText("Nothing remembered yet.")).toBeNull();
  });

  it("points a human at the empty memory rather than at nothing", async () => {
    let resolveList: (value: { items: never[]; nextCursor: string }) => void = () => {};
    listMemories.mockReturnValue(
      new Promise((resolve) => {
        resolveList = resolve;
      }),
    );
    mount();
    expect(screen.queryByTestId("memory-search")).toBeNull();
    expect(screen.queryByText("Nothing remembered yet.")).toBeNull();

    resolveList({ items: [], nextCursor: "" });
    expect(await screen.findByText("Nothing remembered yet.")).toBeInTheDocument();
  });

  it("keeps the list usable when the conductor is down", async () => {
    listMemories.mockRejectedValue(
      new ConnectError("podium-agent is not reachable", Code.Unavailable),
    );
    mount();
    expect(await screen.findByText(/podium-agent is not reachable/)).toBeInTheDocument();
    expect(screen.getByTestId("memory-search")).toBeInTheDocument();
  });

  it("pages through the memory by number", async () => {
    const page = (n: number) =>
      Array.from({ length: n === 3 ? 10 : 25 }, (_, i) => ({
        ...memory,
        id: `p${n}-${i}`,
        text: `fact ${n}.${i}`,
      }));
    listMemories.mockImplementation(({ offset }: { offset?: number }) => {
      const n = Math.floor((offset ?? 0) / 25) + 1;
      return Promise.resolve({ items: page(n), nextCursor: n < 3 ? String(n * 25) : "", total: 60 });
    });
    mount();
    expect(await screen.findByText("fact 1.0")).toBeInTheDocument();
    expect(
      screen.getByText((_, el) => el?.tagName === "P" && el.textContent === "60 memories, newest first"),
    ).toBeInTheDocument();
    expect(screen.getByText("Page 1 of 3")).toBeInTheDocument();
    expect(screen.getByTestId("memory-range")).toHaveTextContent("1–25 of 60");
    expect(screen.getByTestId("memory-prev")).toBeDisabled();

    await userEvent.click(screen.getByTestId("memory-next"));
    expect(await screen.findByText("fact 2.0")).toBeInTheDocument();
    expect(screen.queryByText("fact 1.0")).toBeNull();
    expect(listMemories).toHaveBeenLastCalledWith({ offset: 25, limit: 25 });
    expect(screen.getByText("Page 2 of 3")).toBeInTheDocument();

    await userEvent.click(screen.getByTestId("memory-next"));
    expect(await screen.findByText("fact 3.0")).toBeInTheDocument();
    expect(screen.getByTestId("memory-range")).toHaveTextContent("51–60 of 60");
    expect(screen.getByTestId("memory-next")).toBeDisabled();

    await userEvent.click(screen.getByTestId("memory-prev"));
    expect(await screen.findByText("fact 2.0")).toBeInTheDocument();
  });

  it("shows no pager when everything fits on one page", async () => {
    mount();
    await screen.findByTestId("memory-row");
    expect(screen.queryByTestId("memory-next")).toBeNull();
  });
});

describe("Memory model settings", () => {
  beforeEach(() => {
    listMemories.mockReset();
    getMemorySettings.mockReset();
    setMemorySettings.mockReset();
    listAgents.mockReset();
    listMemories.mockResolvedValue({ items: [memory], nextCursor: "", total: 1 });
    listAgents.mockResolvedValue({
      agents: [
        {
          provider: "anthropic",
          defaultModel: "claude-sonnet-5-5",
          models: [{ id: "claude-sonnet-5-5" }, { id: "claude-haiku-5-5" }],
        },
      ],
    });
  });

  it("shows a member what is in use, with nothing to change", async () => {
    getMemorySettings.mockResolvedValue({
      settings: settings({ provider: "anthropic", model: "claude-haiku-5-5", setBy: "ada" }),
    });
    mount(member());
    const card = await screen.findByTestId("memory-model");
    expect(await within(card).findByText("claude-haiku-5-5")).toBeInTheDocument();
    expect(within(card).getByText(/saved Anthropic key/)).toBeInTheDocument();
    expect(within(card).queryByRole("button", { name: "Change" })).toBeNull();
  });

  it("opens itself for an admin when nothing is chosen, and saves a saved key's model", async () => {
    getMemorySettings.mockResolvedValue({ settings: settings() });
    setMemorySettings.mockResolvedValue({
      settings: settings({ provider: "anthropic", model: "claude-haiku-5-5", setBy: "local" }),
    });
    mount(admin());
    const card = await screen.findByTestId("memory-model");
    expect(within(card).getByText("Not chosen")).toBeInTheDocument();
    expect(within(card).getByText(/Nothing new is being remembered/)).toBeInTheDocument();

    // The first usable key is preselected and its default model filled in.
    const input = await within(card).findByTestId("memory-model-input");
    await waitFor(() => expect(input).toHaveValue("claude-sonnet-5-5"));
    await userEvent.clear(input);
    await userEvent.type(input, "claude-haiku-5-5");
    await userEvent.click(within(card).getByTestId("memory-model-save"));

    await waitFor(() =>
      expect(setMemorySettings).toHaveBeenCalledWith({ provider: "anthropic", model: "claude-haiku-5-5" }),
    );
    expect(await within(card).findByText("claude-haiku-5-5")).toBeInTheDocument();
    expect(within(card).queryByTestId("memory-model-input")).toBeNull();
  });

  it("keeps the form open with the provider's own words when the test message fails", async () => {
    getMemorySettings.mockResolvedValue({ settings: settings() });
    setMemorySettings.mockRejectedValue(
      new ConnectError("anthropic did not answer with nope: model nope does not exist", Code.FailedPrecondition),
    );
    mount(admin());
    const card = await screen.findByTestId("memory-model");
    const input = await within(card).findByTestId("memory-model-input");
    await userEvent.clear(input);
    await userEvent.type(input, "nope");
    await userEvent.click(within(card).getByTestId("memory-model-save"));
    expect(await within(card).findByTestId("memory-model-error")).toHaveTextContent(
      "model nope does not exist",
    );
    expect(within(card).getByTestId("memory-model-input")).toBeInTheDocument();
  });

  it("points an admin at Models when no key is saved for everyone", async () => {
    getMemorySettings.mockResolvedValue({
      settings: settings({ providers: providers.map((p) => ({ ...p, available: false })) }),
    });
    mount(admin());
    const card = await screen.findByTestId("memory-model");
    expect(await within(card).findByRole("link", { name: "Add one under Models" })).toHaveAttribute(
      "href",
      "/agent/settings/models",
    );
  });

  it("says the host uses its own key when the conductor does not serve extraction", async () => {
    getMemorySettings.mockResolvedValue({ settings: settings({ proxyEnabled: false }) });
    mount(admin());
    const card = await screen.findByTestId("memory-model");
    expect(within(card).getByText(/uses the key it was started with/)).toBeInTheDocument();
    expect(within(card).queryByTestId("memory-model-input")).toBeNull();
  });
});
