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

/** Enrolment is a dialog now, so every test opens it before it can touch the form. */
async function mount(user: ReturnType<typeof userEvent.setup>) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const result = render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <EnrollPanel server="http://127.0.0.1:18080" />
      </ToastHost>
    </QueryClientProvider>,
  );
  await user.click(screen.getByRole("button", { name: "Add a node" }));
  await screen.findByRole("dialog");
  return result;
}

describe("enrollCommand", () => {
  it("is one line that carries the token and leaves the dev token as a shell reference", () => {
    const cmd = enrollCommand("http://127.0.0.1:8080", "tok_abc");
    expect(cmd).not.toContain("\n");
    expect(cmd).toContain("PODIUM_NODE_SERVER=http://127.0.0.1:8080");
    expect(cmd).toContain("PODIUM_NODE_ENROLL_TOKEN=tok_abc");
    expect(cmd).toContain("PODIUM_NODE_LOCAL_TOKEN=$PODIUM_LOCAL_TOKEN");
    expect(cmd).toContain("PODIUM_NODE_TRANSPORT=local");
    expect(cmd.endsWith("podium-node")).toBe(true);
  });

  it("switches to the tailnet transport for an https control plane, and names no secret", () => {
    const cmd = enrollCommand("https://podium.tail0a1b2c.ts.net", "tok_abc");
    expect(cmd).toContain("PODIUM_NODE_TRANSPORT=tailnet");
    expect(cmd).toContain("PODIUM_NODE_TS_AUTHKEY=$TS_AUTHKEY");
    expect(cmd).not.toContain("PODIUM_NODE_LOCAL_TOKEN");
    // The Tailscale auth key stays a shell reference: Podium never sees it.
    expect(cmd).not.toContain("tskey-");
  });
});

describe("EnrollPanel", () => {
  beforeEach(() => {
    createEnrollmentToken.mockReset();
  });

  it("sends the typed labels and chosen TTL, then shows the command once", async () => {
    createEnrollmentToken.mockResolvedValue({
      token: "tok_live",
      expiresAt: timestampFromDate(new Date("2026-01-01T00:00:00Z")),
    });
    const user = userEvent.setup();
    await mount(user);

    expect(screen.queryByTestId("enroll-command")).not.toBeInTheDocument();

    await user.type(screen.getByLabelText("Labels"), "demo, linux/arm64");
    await user.click(screen.getByRole("radio", { name: "15 minutes" }));
    await user.click(screen.getByRole("button", { name: "Create enrollment token" }));

    await waitFor(() => expect(screen.getByTestId("enroll-command")).toBeInTheDocument());
    expect(createEnrollmentToken).toHaveBeenCalledWith({
      labels: ["demo", "linux/arm64"],
      ttl: { seconds: 900n, nanos: 0 },
    });
    expect(screen.getByTestId("enroll-command")).toHaveTextContent(
      "PODIUM_NODE_ENROLL_TOKEN=tok_live",
    );
    expect(screen.getByText(/Shown once, single use/)).toBeInTheDocument();
  });

  it("copies the command to the clipboard", async () => {
    createEnrollmentToken.mockResolvedValue({ token: "tok_copy" });
    const user = userEvent.setup();
    // userEvent.setup() installs its own clipboard stub, so replace it after that.
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    await mount(user);
    await user.click(screen.getByRole("button", { name: "Create enrollment token" }));
    await waitFor(() => expect(screen.getByTestId("enroll-command")).toBeInTheDocument());

    await user.click(screen.getByRole("button", { name: "Copy" }));
    expect(writeText).toHaveBeenCalledWith(enrollCommand("http://127.0.0.1:18080", "tok_copy"));
    expect(screen.getByRole("button", { name: "Copied" })).toBeInTheDocument();
  });

  it("surfaces a server error where the action was, and shows no command", async () => {
    createEnrollmentToken.mockRejectedValue(new Error("permission denied"));
    const user = userEvent.setup();
    await mount(user);
    await user.click(screen.getByRole("button", { name: "Create enrollment token" }));

    await waitFor(() =>
      expect(screen.getByRole("status")).toHaveTextContent(
        "Could not create an enrollment token",
      ),
    );
    expect(screen.getByRole("status")).toHaveTextContent("permission denied");
    expect(screen.queryByTestId("enroll-command")).not.toBeInTheDocument();
  });

  it("drops empty labels rather than sending blanks", async () => {
    createEnrollmentToken.mockResolvedValue({ token: "tok_blank" });
    const user = userEvent.setup();
    await mount(user);
    await user.type(screen.getByLabelText("Labels"), " , demo , ");
    await user.click(screen.getByRole("button", { name: "Create enrollment token" }));
    await waitFor(() => expect(createEnrollmentToken).toHaveBeenCalled());
    expect(createEnrollmentToken).toHaveBeenCalledWith({
      labels: ["demo"],
      ttl: { seconds: 3600n, nanos: 0 },
    });
  });
});
