import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { EnrollPanel } from "./EnrollPanel";
import { enrollCommand } from "../lib/enroll";
import { ToastHost } from "./Toast";

const createEnrollmentToken = vi.fn();

vi.mock("../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../lib/client")>("../lib/client");
  return { ...actual, admin: { createEnrollmentToken: (...a: unknown[]) => createEnrollmentToken(...a) } };
});

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <EnrollPanel server="http://127.0.0.1:18080" />
      </ToastHost>
    </QueryClientProvider>,
  );
}

describe("enrollCommand", () => {
  it("is one line that carries the token and leaves the dev token as a shell reference", () => {
    const cmd = enrollCommand("http://127.0.0.1:8080", "tok_abc");
    expect(cmd).not.toContain("\n");
    expect(cmd).toContain("PODIUM_NODE_SERVER=http://127.0.0.1:8080");
    expect(cmd).toContain("PODIUM_NODE_ENROLL_TOKEN=tok_abc");
    expect(cmd).toContain("PODIUM_NODE_DEV_TOKEN=$PODIUM_DEV_TOKEN");
    expect(cmd.endsWith("podium-node")).toBe(true);
  });
});

describe("EnrollPanel", () => {
  beforeEach(() => {
    createEnrollmentToken.mockReset();
  });

  it("sends the typed labels and TTL, then shows the command once", async () => {
    createEnrollmentToken.mockResolvedValue({
      token: "tok_live",
      expiresAt: timestampFromDate(new Date("2026-01-01T00:00:00Z")),
    });
    const user = userEvent.setup();
    mount();

    expect(screen.queryByTestId("enroll-command")).not.toBeInTheDocument();

    await user.type(screen.getByLabelText("Labels"), "demo, linux/arm64");
    await user.clear(screen.getByLabelText("TTL"));
    await user.type(screen.getByLabelText("TTL"), "30");
    await user.selectOptions(screen.getByLabelText("TTL unit"), "minutes");
    await user.click(screen.getByRole("button", { name: "Create enrollment token" }));

    await waitFor(() => expect(screen.getByTestId("enroll-command")).toBeInTheDocument());
    expect(createEnrollmentToken).toHaveBeenCalledWith({
      labels: ["demo", "linux/arm64"],
      ttl: { seconds: 1800n, nanos: 0 },
    });
    expect(screen.getByTestId("enroll-command")).toHaveTextContent(
      "PODIUM_NODE_ENROLL_TOKEN=tok_live",
    );
    expect(screen.getByText(/Shown once/)).toBeInTheDocument();
  });

  it("copies the command to the clipboard", async () => {
    createEnrollmentToken.mockResolvedValue({ token: "tok_copy" });
    const user = userEvent.setup();
    // userEvent.setup() installs its own clipboard stub, so replace it after that.
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    mount();
    await user.click(screen.getByRole("button", { name: "Create enrollment token" }));
    await waitFor(() => expect(screen.getByTestId("enroll-command")).toBeInTheDocument());

    await user.click(screen.getByRole("button", { name: "Copy" }));
    expect(writeText).toHaveBeenCalledWith(enrollCommand("http://127.0.0.1:18080", "tok_copy"));
    expect(screen.getByRole("button", { name: "Copied" })).toBeInTheDocument();
  });

  it("surfaces a server error and shows no command", async () => {
    createEnrollmentToken.mockRejectedValue(new Error("permission denied"));
    const user = userEvent.setup();
    mount();
    await user.click(screen.getByRole("button", { name: "Create enrollment token" }));

    await waitFor(() =>
      expect(screen.getByRole("status")).toHaveTextContent(
        "CreateEnrollmentToken: permission denied",
      ),
    );
    expect(screen.queryByTestId("enroll-command")).not.toBeInTheDocument();
  });

  it("drops empty labels rather than sending blanks", async () => {
    createEnrollmentToken.mockResolvedValue({ token: "tok_blank" });
    const user = userEvent.setup();
    mount();
    await user.type(screen.getByLabelText("Labels"), " , demo , ");
    await user.click(screen.getByRole("button", { name: "Create enrollment token" }));
    await waitFor(() => expect(createEnrollmentToken).toHaveBeenCalled());
    expect(createEnrollmentToken).toHaveBeenCalledWith({
      labels: ["demo"],
      ttl: { seconds: 3600n, nanos: 0 },
    });
  });
});
