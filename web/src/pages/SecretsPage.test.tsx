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

/**
 * A File the component can actually read. jsdom's Blob has no `arrayBuffer()` — every browser
 * has had it since 2020 — and it is stubbed on the instance rather than on Blob.prototype
 * because undici's Response duck-types a blob by looking for exactly that method, and a global
 * stub sends it down a branch that then wants `stream()` as well.
 */
function pickable(text: string, name: string): File {
  const file = new File([text], name);
  Object.defineProperty(file, "arrayBuffer", {
    value: () => Promise.resolve(new TextEncoder().encode(text).buffer),
  });
  return file;
}

const row = {
  name: "DB_PASSWORD",
  version: 3,
  keyId: "k_01abc",
  createdBy: "user@example.com",
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
    expect(screen.getByText("user@example.com")).toBeInTheDocument();
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

  it("does not prefill a value when rotating an existing secret", async () => {
    mount();
    await userEvent.click(await screen.findByRole("button", { name: "Rotate DB_PASSWORD" }));
    // The rotate dialog masks its own draft, but there is nothing stored for it to start from.
    expect(screen.getByLabelText("Name")).toHaveValue("DB_PASSWORD");
    expect(screen.getByLabelText("Value")).toHaveValue("");
  });

  it("sends the value as bytes and does not leave it in the DOM", async () => {
    setSecret.mockResolvedValue({ secret: { name: "TOKEN", version: 1 } });
    mount();
    await screen.findByText("DB_PASSWORD");

    await userEvent.click(screen.getByRole("button", { name: "New secret" }));
    await userEvent.type(screen.getByLabelText("Name"), "TOKEN");
    await userEvent.type(screen.getByLabelText("Value"), "hunter2");
    await userEvent.click(screen.getByRole("button", { name: "Save secret" }));

    await waitFor(() => expect(setSecret).toHaveBeenCalledTimes(1));
    const sent = setSecret.mock.calls[0][0] as { name: string; value: Uint8Array };
    expect(sent.name).toBe("TOKEN");
    expect(new TextDecoder().decode(sent.value)).toBe("hunter2");
    // The dialog closes on success, which takes the field and its plaintext with it.
    await waitFor(() => expect(screen.queryByLabelText("Value")).toBeNull());
    expect(document.body.textContent).not.toContain("hunter2");
  });

  // A PEM does not survive being pasted into a single-line field, which is the whole reason
  // this exists. The bytes have to arrive exactly as the file holds them, newline included.
  it("sends a file byte for byte, trailing newline and all", async () => {
    setSecret.mockResolvedValue({ secret: { name: "DEPLOY_KEY", version: 1 } });
    mount();
    await screen.findByText("DB_PASSWORD");

    await userEvent.click(screen.getByRole("button", { name: "New secret" }));
    await userEvent.type(screen.getByLabelText("Name"), "DEPLOY_KEY");
    const pem = "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNza\n-----END OPENSSH PRIVATE KEY-----\n";
    await userEvent.upload(screen.getByLabelText("Or from a file"), pickable(pem, "id_ed25519"));
    expect(screen.getByText("id_ed25519")).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: "Save secret" }));
    await waitFor(() => expect(setSecret).toHaveBeenCalledTimes(1));
    const sent = setSecret.mock.calls[0][0] as { name: string; value: Uint8Array };
    expect(sent.name).toBe("DEPLOY_KEY");
    expect(new TextDecoder().decode(sent.value)).toBe(pem);
  });

  // Two sources for one value is only ever a way to store the wrong one.
  it("lets a file stand in for the value, and locks the field once one is picked", async () => {
    mount();
    await screen.findByText("DB_PASSWORD");
    await userEvent.click(screen.getByRole("button", { name: "New secret" }));
    await userEvent.type(screen.getByLabelText("Name"), "DEPLOY_KEY");
    expect(screen.getByRole("button", { name: "Save secret" })).toBeDisabled();

    await userEvent.upload(
      screen.getByLabelText("Or from a file"),
      new File(["hunter2"], "token.txt"),
    );
    expect(screen.getByRole("button", { name: "Save secret" })).toBeEnabled();
    expect(screen.getByLabelText("Value")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Show value" })).toBeDisabled();
  });

  it("refuses an empty file rather than letting the server do it", async () => {
    mount();
    await screen.findByText("DB_PASSWORD");
    await userEvent.click(screen.getByRole("button", { name: "New secret" }));
    await userEvent.type(screen.getByLabelText("Name"), "DEPLOY_KEY");

    await userEvent.upload(screen.getByLabelText("Or from a file"), new File([], "empty.pem"));
    expect(screen.getByText(/empty.pem is empty/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save secret" })).toBeDisabled();
    // The value field is usable again: nothing is selected, whatever the picker looks like.
    expect(screen.getByLabelText("Value")).toBeEnabled();
  });

  it("refuses a file far too big to be a credential", async () => {
    mount();
    await screen.findByText("DB_PASSWORD");
    await userEvent.click(screen.getByRole("button", { name: "New secret" }));
    await userEvent.type(screen.getByLabelText("Name"), "DEPLOY_KEY");

    const huge = new File([new Uint8Array(1024 * 1024 + 1)], "backup.img");
    await userEvent.upload(screen.getByLabelText("Or from a file"), huge);
    expect(screen.getByText(/not a payload/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save secret" })).toBeDisabled();
  });

  it("will not save without both a name and a value", async () => {
    mount();
    await screen.findByText("DB_PASSWORD");
    await userEvent.click(screen.getByRole("button", { name: "New secret" }));
    expect(screen.getByRole("button", { name: "Save secret" })).toBeDisabled();
    await userEvent.type(screen.getByLabelText("Name"), "TOKEN");
    expect(screen.getByRole("button", { name: "Save secret" })).toBeDisabled();
  });

  it("warns that setting an existing name rotates it rather than editing it", async () => {
    mount();
    await screen.findByText("DB_PASSWORD");
    await userEvent.click(screen.getByRole("button", { name: "New secret" }));
    await userEvent.type(screen.getByLabelText("Name"), "DB_PASSWORD");

    expect(screen.getByText("DB_PASSWORD already exists")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Rotate to version 4" })).toBeInTheDocument();
  });

  it("confirms a delete by name before sending it", async () => {
    deleteSecret.mockResolvedValue({});
    mount();
    await screen.findByText("DB_PASSWORD");

    await userEvent.click(screen.getByRole("button", { name: "Delete DB_PASSWORD" }));
    expect(deleteSecret).not.toHaveBeenCalled();
    expect(screen.getByText("Delete DB_PASSWORD?")).toBeInTheDocument();
    // Typing the name back is the confirmation; the button is inert until it matches.
    expect(screen.getByRole("button", { name: "Delete secret" })).toBeDisabled();

    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(deleteSecret).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: "Delete DB_PASSWORD" }));
    await userEvent.type(screen.getByLabelText(/to confirm/i), "DB_PASSWORD");
    await userEvent.click(screen.getByRole("button", { name: "Delete secret" }));
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
