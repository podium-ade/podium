import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter, Route, Routes, useLocation } from "react-router";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { LinearIssueSchema } from "../../../gen/podium/agent/v1/agent_pb";
import { LinearIssueChip } from "./LinearIssueChip";

const getLinearIssues = vi.fn();

vi.mock("../../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../../lib/client")>("../../../lib/client");
  return { ...actual, agent: { getLinearIssues: (...a: unknown[]) => getLinearIssues(...a) } };
});

function Where() {
  return <p data-testid="where">{useLocation().pathname}</p>;
}

function mount(issueRef = "EME-6") {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={["/agent/chat/c1"]}>
        <Routes>
          <Route
            path="/agent/chat/c1"
            element={
              <p>
                look at <LinearIssueChip issueRef={issueRef}>{issueRef}</LinearIssueChip> please
              </p>
            }
          />
          <Route path="*" element={<Where />} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  );
}

const issue = (over: Record<string, unknown> = {}) =>
  create(LinearIssueSchema, {
    ref: "EME-6",
    id: "issue-6",
    identifier: "EME-6",
    title: "Fix the login page",
    url: "https://linear.app/acme/issue/EME-6",
    teamKey: "EME",
    teamName: "Emergere",
    stateName: "In Progress",
    stateType: "started",
    updatedAt: timestampFromDate(new Date(Date.now() - 3_600_000)),
    ...over,
  });

describe("LinearIssueChip", () => {
  beforeEach(() => {
    getLinearIssues.mockReset();
  });

  it("stays the text it was for an issue the viewer may not see", async () => {
    getLinearIssues.mockResolvedValue({ issues: [] });
    mount();
    await waitFor(() => expect(getLinearIssues).toHaveBeenCalled());
    expect(screen.queryByTestId("linear-chip")).toBeNull();
    expect(screen.getByText(/look at EME-6 please/)).toBeInTheDocument();
  });

  it("draws the issue's state, identifier and title once the conductor describes it", async () => {
    getLinearIssues.mockResolvedValue({ issues: [issue()] });
    mount();
    const chip = await screen.findByTestId("linear-chip");
    expect(chip).toHaveTextContent("EME-6");
    expect(chip).toHaveTextContent("Fix the login page");
    expect(chip).toHaveAccessibleName("EME-6: Fix the login page, In Progress");
    expect(getLinearIssues).toHaveBeenCalledWith({ refs: ["EME-6"], detail: false });
  });

  it("opens a card with Linear's details, and continues in the ticket's conversation", async () => {
    getLinearIssues.mockImplementation(({ detail }: { detail: boolean }) =>
      Promise.resolve({ issues: [issue(detail ? { assignee: "Ada Lovelace", chatId: "chat-eme6" } : {})] }),
    );
    mount();
    await userEvent.click(await screen.findByTestId("linear-chip"));
    const card = await screen.findByTestId("linear-card");
    await waitFor(() => expect(card).toHaveTextContent("Ada Lovelace"));
    expect(card).toHaveTextContent("Emergere");
    expect(card).toHaveTextContent("In Progress");
    expect(card).toHaveTextContent("1h ago");
    expect(screen.getByRole("link", { name: /open in linear/i })).toHaveAttribute("href", "https://linear.app/acme/issue/EME-6");
    await userEvent.click(screen.getByTestId("linear-card-continue"));
    expect(screen.getByTestId("where")).toHaveTextContent("/agent/chat/chat-eme6");
  });

  it("offers only Linear when the viewer may not read the ticket's conversation", async () => {
    getLinearIssues.mockResolvedValue({ issues: [issue({ assignee: "" })] });
    mount();
    await userEvent.click(await screen.findByTestId("linear-chip"));
    const card = await screen.findByTestId("linear-card");
    await waitFor(() => expect(card).toHaveTextContent("Unassigned"));
    expect(screen.queryByTestId("linear-card-continue")).toBeNull();
    expect(screen.getByRole("link", { name: /open in linear/i })).toBeInTheDocument();
  });
});
