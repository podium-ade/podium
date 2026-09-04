import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router";
import { Code, ConnectError } from "@connectrpc/connect";
import { SpecForm } from "./SpecForm";
import { ToastHost } from "./Toast";

const createTask = vi.fn();
const navigate = vi.fn();

vi.mock("../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../lib/client")>("../lib/client");
  return { ...actual, tasks: { createTask: (...a: unknown[]) => createTask(...a) } };
});

vi.mock("react-router", async () => {
  const actual = await vi.importActual<typeof import("react-router")>("react-router");
  return { ...actual, useNavigate: () => navigate };
});

function mount(props: Parameters<typeof SpecForm>[0] = {}) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <MemoryRouter>
          <SpecForm {...props} />
        </MemoryRouter>
      </ToastHost>
    </QueryClientProvider>,
  );
}

function problems(): string[] {
  const list = screen.queryByTestId("spec-problems");
  if (!list) return [];
  return within(list)
    .getAllByRole("listitem")
    .map((li) => li.textContent ?? "");
}

describe("SpecForm", () => {
  beforeEach(() => {
    createTask.mockReset();
    navigate.mockReset();
  });

  it("submits the form fields as a spec and goes to the new task", async () => {
    createTask.mockResolvedValue({ task: { id: "task_01new" } });
    mount();

    await userEvent.type(screen.getByLabelText("Image"), "alpine:3");
    await userEvent.type(screen.getByLabelText("Command"), "sh\n-c\necho hi");
    await userEvent.type(screen.getByLabelText("Labels"), "linux/arm64");
    await userEvent.click(screen.getByRole("button", { name: "Submit task" }));

    await waitFor(() => expect(createTask).toHaveBeenCalledTimes(1));
    expect(createTask.mock.calls[0][0]).toMatchObject({
      spec: { image: "alpine:3", command: ["sh", "-c", "echo hi"], labels: ["linux/arm64"] },
    });
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("/tasks/task_01new"));
  });

  it("lists every client-side problem at once and never calls the server", async () => {
    mount();
    await userEvent.type(screen.getByLabelText("Environment"), "NOTANASSIGNMENT");
    await userEvent.type(screen.getByLabelText("Timeout"), "soon");
    await userEvent.click(screen.getByRole("button", { name: "Submit task" }));

    const found = problems();
    expect(found).toHaveLength(3);
    expect(found.join("\n")).toContain("NOTANASSIGNMENT");
    expect(found.join("\n")).toContain("timeout");
    expect(found.join("\n")).toContain("image: is required");
    expect(createTask).not.toHaveBeenCalled();
  });

  it("renders every problem the server reports, not just the first", async () => {
    // pkg/spec.Validate joins its errors with errors.Join, so one InvalidArgument carries a
    // whole list. Showing it as one blob would hide all but the first line.
    createTask.mockRejectedValue(
      new ConnectError(
        "invalid task spec: timeout must be positive, got 0s\nmax_attempts must be at least 1, got 0\nenv key \"MY-VAR\" is not a valid shell identifier",
        Code.InvalidArgument,
      ),
    );
    mount();
    await userEvent.type(screen.getByLabelText("Image"), "alpine:3");
    await userEvent.click(screen.getByRole("button", { name: "Submit task" }));

    await waitFor(() => expect(problems()).toHaveLength(3));
    expect(problems()).toEqual([
      "timeout must be positive, got 0s",
      "max_attempts must be at least 1, got 0",
      'env key "MY-VAR" is not a valid shell identifier',
    ]);
    expect(navigate).not.toHaveBeenCalled();
  });

  it("clears the previous problems on the next submit", async () => {
    createTask.mockResolvedValue({ task: { id: "task_ok" } });
    mount();
    await userEvent.click(screen.getByRole("button", { name: "Submit task" }));
    expect(problems().length).toBeGreaterThan(0);

    await userEvent.type(screen.getByLabelText("Image"), "alpine:3");
    await userEvent.click(screen.getByRole("button", { name: "Submit task" }));
    await waitFor(() => expect(problems()).toHaveLength(0));
  });

  it("rejects a YAML field the schema does not have instead of dropping it", async () => {
    mount({ initialMode: "yaml", initialYaml: "image: alpine:3\nprivileged: true\n" });
    await userEvent.click(screen.getByRole("button", { name: "Submit task" }));

    expect(problems()).toHaveLength(1);
    expect(problems()[0]).toContain("privileged");
    expect(createTask).not.toHaveBeenCalled();
  });

  it("submits a YAML spec with sidecars the simple form cannot express", async () => {
    createTask.mockResolvedValue({ task: { id: "task_sc" } });
    mount({
      initialMode: "yaml",
      initialYaml:
        "image: alpine:3\nsidecars:\n  db:\n    image: pgvector/pgvector:pg16\n    readiness:\n      tcp_port: 5432\n",
    });
    await userEvent.click(screen.getByRole("button", { name: "Submit task" }));

    await waitFor(() => expect(createTask).toHaveBeenCalledTimes(1));
    expect(createTask.mock.calls[0][0].spec.sidecars.db).toMatchObject({
      image: "pgvector/pgvector:pg16",
      readiness: { tcpPort: 5432 },
    });
  });

  it("seeds the YAML tab from the form the first time, and never overwrites typed YAML", async () => {
    mount();
    await userEvent.type(screen.getByLabelText("Image"), "alpine:3");
    await userEvent.click(screen.getByRole("button", { name: "YAML spec" }));

    const editor = screen.getByLabelText("Task spec YAML");
    expect(editor).toHaveValue("image: alpine:3\n");

    await userEvent.clear(editor);
    await userEvent.type(editor, "image: redis:7-alpine");
    await userEvent.click(screen.getByRole("button", { name: "Form" }));
    await userEvent.click(screen.getByRole("button", { name: "YAML spec" }));
    expect(screen.getByLabelText("Task spec YAML")).toHaveValue("image: redis:7-alpine");
  });

  it("says which task it was pre-filled from", () => {
    mount({ rerunOf: "task_01old", initialFields: undefined });
    expect(screen.getByText("task_01old")).toBeInTheDocument();
  });
});
