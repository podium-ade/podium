import { beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { Code, ConnectError } from "@connectrpc/connect";
import { RegistriesPage } from "./RegistriesPage";
import { ToastHost } from "../components/Toast";

const listRegistries = vi.fn();
const setRegistry = vi.fn();
const deleteRegistry = vi.fn();

vi.mock("../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../lib/client")>("../lib/client");
  return {
    ...actual,
    registries: {
      listRegistries: (...a: unknown[]) => listRegistries(...a),
      setRegistry: (...a: unknown[]) => setRegistry(...a),
      deleteRegistry: (...a: unknown[]) => deleteRegistry(...a),
    },
  };
});

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <RegistriesPage />
      </ToastHost>
    </QueryClientProvider>,
  );
}

const row = {
  host: "us-docker.pkg.dev",
  username: "_json_key",
  keyId: "k_01abc",
  createdBy: "alvaro@example.com",
  updatedAt: timestampFromDate(new Date(Date.now() - 60_000)),
};

const KEY = '{"type":"service_account","project_id":"acme"}';

describe("RegistriesPage", () => {
  beforeEach(() => {
    listRegistries.mockReset();
    setRegistry.mockReset();
    deleteRegistry.mockReset();
    listRegistries.mockResolvedValue({ registries: [row] });
  });

  it("lists hosts and usernames and never a password", async () => {
    mount();
    expect(await screen.findByText("us-docker.pkg.dev")).toBeInTheDocument();
    expect(screen.getByText("_json_key")).toBeInTheDocument();
    expect(screen.getByText("alvaro@example.com")).toBeInTheDocument();
    expect(document.body.textContent).not.toContain("service_account");
  });

  it("saves a Google Artifact Registry login as _json_key with the key file as the password", async () => {
    setRegistry.mockResolvedValue({ registry: { host: "europe-docker.pkg.dev" } });
    mount();
    await screen.findByText("us-docker.pkg.dev");

    await userEvent.click(screen.getByRole("button", { name: "New registry" }));
    // Google Artifact Registry is the default kind, and it fixes the username.
    expect(screen.getByLabelText("Username")).toHaveValue("_json_key");
    expect(screen.getByLabelText("Username")).toHaveAttribute("readonly");
    expect(screen.getByRole("button", { name: "Save registry" })).toBeDisabled();

    await userEvent.type(screen.getByLabelText("Host"), "https://europe-docker.pkg.dev/acme/images");
    expect(screen.getByText("Saved as europe-docker.pkg.dev.")).toBeInTheDocument();
    await userEvent.click(screen.getByLabelText("Service account key (JSON)"));
    await userEvent.paste(KEY);
    await userEvent.click(screen.getByRole("button", { name: "Save registry" }));

    await waitFor(() => expect(setRegistry).toHaveBeenCalledTimes(1));
    const sent = setRegistry.mock.calls[0][0] as { host: string; username: string; password: Uint8Array };
    expect(sent.host).toBe("europe-docker.pkg.dev");
    expect(sent.username).toBe("_json_key");
    expect(new TextDecoder().decode(sent.password)).toBe(KEY);
    await waitFor(() => expect(screen.queryByLabelText("Service account key (JSON)")).toBeNull());
    expect(document.body.textContent).not.toContain("service_account");
  });

  it("frees the username for another kind of registry", async () => {
    mount();
    await screen.findByText("us-docker.pkg.dev");
    await userEvent.click(screen.getByRole("button", { name: "New registry" }));
    await userEvent.click(screen.getByRole("radio", { name: "GitHub Container Registry" }));
    expect(screen.getByLabelText("Username")).toHaveValue("");
    expect(screen.getByLabelText("Username")).not.toHaveAttribute("readonly");
    expect(screen.getByLabelText("Personal access token")).toHaveAttribute("type", "password");
  });

  it("warns that saving a host that is already there replaces its login", async () => {
    mount();
    await screen.findByText("us-docker.pkg.dev");
    await userEvent.click(screen.getByRole("button", { name: "New registry" }));
    await userEvent.type(screen.getByLabelText("Host"), "us-docker.pkg.dev");
    expect(screen.getByText("us-docker.pkg.dev already has a login")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Replace login" })).toBeInTheDocument();
  });

  it("rejects a host that is not a hostname", async () => {
    mount();
    await screen.findByText("us-docker.pkg.dev");
    await userEvent.click(screen.getByRole("button", { name: "New registry" }));
    await userEvent.type(screen.getByLabelText("Host"), "not a host");
    expect(screen.getByLabelText("Host")).toHaveAttribute("aria-invalid", "true");
    await userEvent.click(screen.getByLabelText("Service account key (JSON)"));
    await userEvent.paste(KEY);
    expect(screen.getByRole("button", { name: "Save registry" })).toBeDisabled();
  });

  it("replaces a login with the host locked and the password empty", async () => {
    mount();
    await userEvent.click(
      await screen.findByRole("button", { name: "Replace login for us-docker.pkg.dev" }),
    );
    expect(screen.getByLabelText("Host")).toHaveValue("us-docker.pkg.dev");
    expect(screen.getByLabelText("Host")).toHaveAttribute("readonly");
    expect(screen.getByLabelText("Service account key (JSON)")).toHaveValue("");
  });

  it("confirms a removal by host before sending it", async () => {
    deleteRegistry.mockResolvedValue({});
    mount();
    await screen.findByText("us-docker.pkg.dev");

    await userEvent.click(screen.getByRole("button", { name: "Remove us-docker.pkg.dev" }));
    expect(screen.getByRole("button", { name: "Remove registry" })).toBeDisabled();
    await userEvent.type(screen.getByLabelText(/to confirm/i), "us-docker.pkg.dev");
    await userEvent.click(screen.getByRole("button", { name: "Remove registry" }));
    await waitFor(() => expect(deleteRegistry).toHaveBeenCalledWith({ host: "us-docker.pkg.dev" }));
  });

  it("says the server has no master key when it answers FailedPrecondition", async () => {
    listRegistries.mockRejectedValue(new ConnectError("no master key", Code.FailedPrecondition));
    mount();
    expect(await screen.findByText(/no master key configured/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New registry" })).toBeDisabled();
  });
});
