import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import { SecretsPage } from "./SecretsPage";
import { ToastHost } from "../components/Toast";

const listSecrets = vi.fn();
const setSecret = vi.fn();
const deleteSecret = vi.fn();

vi.mock("../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../lib/client")>("../lib/client");
  return {
    ...actual,
    secrets: {
      listSecrets: (...a: unknown[]) => listSecrets(...a),
      setSecret: (...a: unknown[]) => setSecret(...a),
      deleteSecret: (...a: unknown[]) => deleteSecret(...a),
    },
  };
});

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <SecretsPage />
      </ToastHost>
    </QueryClientProvider>,
  );
}

const row = {
  name: "DB_PASSWORD",
  version: 3,
  keyId: "k_01abc",
  createdBy: "alvaro@example.com",
  updatedAt: timestampFromDate(new Date(Date.now() - 60_000)),
};

describe("SecretsPage", () => {
  beforeEach(() => {
    listSecrets.mockReset();
    setSecret.mockReset();
    deleteSecret.mockReset();
    listSecrets.mockResolvedValue({ secrets: [row] });
  });

  it("shows the metadata and says plainly that a value cannot be viewed", async () => {
    mount();
    expect(await screen.findByText("DB_PASSWORD")).toBeInTheDocument();
    expect(screen.getByText("3")).toBeInTheDocument();
    expect(screen.getByText("alvaro@example.com")).toBeInTheDocument();
    expect(screen.getByText("k_01abc")).toBeInTheDocument();
    expect(
      screen.getByText(/value cannot be viewed after it is saved/i),
    ).toBeInTheDocument();
  });

  it("offers no way at all to reveal a value", async () => {
    mount();
    await screen.findByText("DB_PASSWORD");
    for (const label of [/reveal/i, /show value/i, /view value/i, /copy value/i]) {
      expect(screen.queryByRole("button", { name: label })).toBeNull();
    }
    // ListSecrets carries no value field; nothing rendered can have come from one.
    expect(document.body.textContent).not.toContain("hunter2");
  });

  it("sends the value as bytes and clears the field afterwards", async () => {
    setSecret.mockResolvedValue({ secret: { name: "TOKEN", version: 1 } });
    mount();
    await screen.findByText("DB_PASSWORD");

    await userEvent.type(screen.getByLabelText("Secret name"), "TOKEN");
    await userEvent.type(screen.getByLabelText("Secret value"), "hunter2");
    await userEvent.click(screen.getByRole("button", { name: "Save secret" }));

    await waitFor(() => expect(setSecret).toHaveBeenCalledTimes(1));
    const sent = setSecret.mock.calls[0][0] as { name: string; value: Uint8Array };
    expect(sent.name).toBe("TOKEN");
    expect(new TextDecoder().decode(sent.value)).toBe("hunter2");
    await waitFor(() => expect(screen.getByLabelText("Secret value")).toHaveValue(""));
    expect(document.body.textContent).not.toContain("hunter2");
  });

  it("will not save without both a name and a value", async () => {
    mount();
    await screen.findByText("DB_PASSWORD");
    expect(screen.getByRole("button", { name: "Save secret" })).toBeDisabled();
    await userEvent.type(screen.getByLabelText("Secret name"), "TOKEN");
    expect(screen.getByRole("button", { name: "Save secret" })).toBeDisabled();
  });

  it("confirms a delete by name before sending it", async () => {
    deleteSecret.mockResolvedValue({});
    mount();
    await screen.findByText("DB_PASSWORD");

    await userEvent.click(screen.getByRole("button", { name: "Delete DB_PASSWORD" }));
    expect(deleteSecret).not.toHaveBeenCalled();
    expect(screen.getByText("Delete DB_PASSWORD?")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Keep" }));
    expect(deleteSecret).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: "Delete DB_PASSWORD" }));
    await userEvent.click(screen.getByRole("button", { name: "Yes, delete" }));
    await waitFor(() => expect(deleteSecret).toHaveBeenCalledWith({ name: "DB_PASSWORD" }));
  });

  it("says the server has no master key when it answers FailedPrecondition", async () => {
    listSecrets.mockRejectedValue(new ConnectError("no master key", Code.FailedPrecondition));
    mount();
    expect(await screen.findByText(/no master key configured/i)).toBeInTheDocument();
  });

  it("flags a half-finished key rotation", async () => {
    listSecrets.mockResolvedValue({
      secrets: [row, { ...row, name: "OTHER", keyId: "k_02def" }],
    });
    mount();
    expect(await screen.findByText(/rotation stopped part way/i)).toBeInTheDocument();
  });
});
