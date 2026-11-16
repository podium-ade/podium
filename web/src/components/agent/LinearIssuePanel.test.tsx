import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import {
  ChatPullRequestSchema,
  GetLinearIssueConversationResponseSchema,
  LinearIssueSchema,
} from "../../gen/podium/agent/v1/agent_pb";
import { LinearIssuePanel } from "./LinearIssuePanel";

const getLinearIssueConversation = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: { getLinearIssueConversation: (...a: unknown[]) => getLinearIssueConversation(...a) },
  };
});

const ago = (ms: number) => timestampFromDate(new Date(Date.now() - ms));

function mount(refreshKey = 0, pullRequests = [] as ReturnType<typeof chatPR>[]) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const ui = (key: number) => (
    <QueryClientProvider client={qc}>
      <MemoryRouter>
        <LinearIssuePanel chatId="chat-1" pullRequests={pullRequests} refreshKey={key} />
      </MemoryRouter>
    </QueryClientProvider>
  );
  const r = render(ui(refreshKey));
  return { ...r, refresh: (key: number) => r.rerender(ui(key)) };
}

const chatPR = () =>
  create(ChatPullRequestSchema, { url: "https://github.com/acme/app/pull/12", owner: "acme", repo: "app", number: 12 });

const response = () =>
  create(GetLinearIssueConversationResponseSchema, {
    issue: create(LinearIssueSchema, {
      id: "issue-6",
      identifier: "EME-6",
      title: "Fix the login page",
      url: "https://linear.app/acme/issue/EME-6",
      teamKey: "EME",
      teamName: "Emergere",
      stateName: "In Review",
      stateType: "started",
      assignee: "Ada Lovelace",
      labels: ["Bug", "Auth"],
      updatedAt: ago(120_000),
      links: [{ title: "API", url: "https://github.com/acme/api/pull/3", source: "github" }],
      chatId: "chat-1",
    }),
    tasks: [
      { taskId: "task-1", playbook: "coder", status: "running", createdAt: ago(60_000) },
      { taskId: "task-0", playbook: "reviewer", status: "succeeded", createdAt: ago(3_600_000) },
    ],
    routineRuns: [{ id: "run-1", routineId: "rtn-1", routineName: "Triage", status: "done", summary: "Labelled it", createdAt: ago(7_200_000) }],
  });

describe("LinearIssuePanel", () => {
  beforeEach(() => {
    getLinearIssueConversation.mockReset();
  });

  it("heads the conversation with the issue as Linear has it", async () => {
    getLinearIssueConversation.mockResolvedValue(response());
    mount(0, [chatPR()]);
    const panel = await screen.findByTestId("linear-issue-panel");
    expect(panel).toHaveAccessibleName("Linear issue EME-6");
    expect(panel).toHaveTextContent("Fix the login page");
    const facts = screen.getByTestId("linear-issue-facts");
    for (const t of ["In Review", "Emergere", "Ada Lovelace", "updated 2m ago", "Bug", "Auth"]) {
      expect(facts).toHaveTextContent(t);
    }
    expect(screen.getByRole("link", { name: /open in linear/i })).toHaveAttribute("href", "https://linear.app/acme/issue/EME-6");
    expect(panel).toHaveTextContent("2 pull requests · 2 tasks, 1 running · 1 routine run");
  });

  it("lists its pull requests, tasks and routine runs when opened", async () => {
    getLinearIssueConversation.mockResolvedValue(response());
    mount(0, [chatPR()]);
    await userEvent.click(await screen.findByRole("button", { name: "Show details" }));
    const details = await screen.findByTestId("linear-issue-details");
    expect(details).toHaveTextContent("acme/app#12");
    expect(details).toHaveTextContent("from this conversation");
    expect(details).toHaveTextContent("acme/api#3");
    expect(details).toHaveTextContent("linked in Linear");
    expect(screen.getByRole("link", { name: "coder" })).toHaveAttribute("href", "/tasks/task-1");
    expect(screen.getByRole("link", { name: "Triage" })).toHaveAttribute("href", "/agent/routines/rtn-1");
    expect(details).toHaveTextContent("Labelled it");
  });

  it("draws nothing for a conversation that is no issue's", async () => {
    getLinearIssueConversation.mockRejectedValue(new ConnectError("no such Linear conversation", Code.NotFound));
    const { container } = mount();
    await waitFor(() => expect(getLinearIssueConversation).toHaveBeenCalled());
    expect(container.querySelector("[data-testid=linear-issue-panel]")).toBeNull();
  });

  it("asks again when something is said in the conversation", async () => {
    getLinearIssueConversation.mockResolvedValue(response());
    const { refresh } = mount(3);
    await screen.findByTestId("linear-issue-panel");
    expect(getLinearIssueConversation).toHaveBeenCalledTimes(1);
    refresh(4);
    await waitFor(() => expect(getLinearIssueConversation).toHaveBeenCalledTimes(2));
  });
});
