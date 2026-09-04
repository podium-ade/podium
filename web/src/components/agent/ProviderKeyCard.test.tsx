import { beforeEach, describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Code, ConnectError } from "@connectrpc/connect";
import {
  ProviderSettingsSchema,
  SetProviderKeyResponseSchema,
  type ProviderSettings,
} from "../../gen/podium/agent/v1/agent_pb";
import { AGENT_UNREACHABLE } from "../../lib/client";
import { ProviderKeyCard } from "./ProviderKeyCard";
import { ToastHost } from "../Toast";

// An obvious fake. There is no real provider key anywhere in this repository.
const KEY = "sk-ant-not-a-real-key-abcd";

const onSave = vi.fn();
const onClear = vi.fn();

function mount(settings?: ProviderSettings, loading = false) {
  return render(
    <ToastHost>
      <ProviderKeyCard
        settings={settings}
        loading={loading}
        onSave={onSave}
        onClear={onClear}
      />
    </ToastHost>,
  );
}

const connected = create(ProviderSettingsSchema, {
  provider: "anthropic",
  keySet: true,
  keyHint: "abcd",
  model: "claude-opus-5",
  setBy: "alice@example.com",
  setAt: timestampFromDate(new Date(Date.now() - 120_000)),
});

const notSet = create(ProviderSettingsSchema, {
  provider: "anthropic",
  keySet: false,
  model: "claude-opus-5",
});

const saved = create(SetProviderKeyResponseSchema, {
  provider: connected,
  models: ["claude-opus-5", "claude-sonnet-5", "claude-haiku-4-5"],
});

describe("ProviderKeyCard", () => {
  beforeEach(() => {
    onSave.mockReset();
    onClear.mockReset();
  });

  it("says what the key is for and will not save nothing", async () => {
    mount(notSet);
    expect(screen.getByText("Not set")).toBeInTheDocument();
    expect(screen.getByText(/encrypted at rest by podium-server/i)).toBeInTheDocument();
    expect(screen.getByText(/leaves this host only to reach Anthropic/i)).toBeInTheDocument();
    expect(screen.getByTestId("provider-key-save")).toBeDisabled();
    // Nothing to remove yet, so no danger zone at all.
    expect(screen.queryByTestId("provider-key-remove")).toBeNull();
  });

  it("enables the button once there is something to save", async () => {
    mount(notSet);
    await userEvent.type(screen.getByTestId("provider-key-input"), KEY);
    expect(screen.getByTestId("provider-key-save")).toBeEnabled();
  });

  it("shows a skeleton and no badge while the settings load", () => {
    mount(undefined, true);
    expect(screen.getByLabelText("Loading")).toBeInTheDocument();
    expect(screen.queryByText("Not set")).toBeNull();
    expect(screen.queryByText("Connected")).toBeNull();
  });

  it("proves a saved key with a hint and the models it can see", async () => {
    onSave.mockResolvedValue(saved);
    mount(notSet);

    await userEvent.type(screen.getByTestId("provider-key-input"), KEY);
    await userEvent.click(screen.getByTestId("provider-key-save"));

    await waitFor(() => expect(onSave).toHaveBeenCalledWith(KEY));
    const status = await screen.findByTestId("provider-key-status");
    expect(status).toHaveTextContent("Saved. ••••abcd works — 3 models visible.");
    expect(status.className).toContain("text-ok");
    expect(screen.getByText("claude-opus-5")).toBeInTheDocument();
    expect(screen.getByText("claude-haiku-4-5")).toBeInTheDocument();
    // The key is out of the field the moment it is stored.
    expect(screen.getByTestId("provider-key-input")).toHaveValue("");
    expect(document.body.textContent).not.toContain(KEY);
  });

  it("passes an unfamiliar prefix through with the note the server sent", async () => {
    onSave.mockResolvedValue(
      create(SetProviderKeyResponseSchema, {
        provider: connected,
        models: ["claude-opus-5"],
        status: "key format looks unusual; validated anyway",
      }),
    );
    mount(notSet);
    await userEvent.type(screen.getByTestId("provider-key-input"), "ant-api03-whatever");
    await userEvent.click(screen.getByTestId("provider-key-save"));
    expect(await screen.findByText(/looks unusual; validated anyway/)).toBeInTheDocument();
    expect(await screen.findByText(/1 model visible/)).toBeInTheDocument();
  });

  it("renders a refusal in the error colour and keeps what was typed", async () => {
    onSave.mockRejectedValue(
      new ConnectError("Anthropic rejected this key", Code.PermissionDenied),
    );
    mount(notSet);

    await userEvent.type(screen.getByTestId("provider-key-input"), KEY);
    await userEvent.click(screen.getByTestId("provider-key-save"));

    const status = await screen.findByTestId("provider-key-status");
    expect(status).toHaveTextContent("Anthropic rejected this key");
    expect(status.className).toContain("text-err");
    // A typo in one character should not mean typing the whole key again.
    expect(screen.getByTestId("provider-key-input")).toHaveValue(KEY);
  });

  it("distinguishes a provider it could not reach from a conductor that is down", async () => {
    onSave.mockRejectedValue(
      new ConnectError("could not validate the key with Anthropic; nothing was saved", Code.Unavailable),
    );
    const view = mount(notSet);
    await userEvent.type(screen.getByTestId("provider-key-input"), KEY);
    await userEvent.click(screen.getByTestId("provider-key-save"));
    let status = await screen.findByTestId("provider-key-status");
    expect(status).toHaveTextContent("Couldn't reach Anthropic to validate. Nothing was saved.");
    expect(status.className).toContain("text-warn");
    view.unmount();

    onSave.mockRejectedValue(new ConnectError(AGENT_UNREACHABLE, Code.Unavailable));
    mount(notSet);
    await userEvent.type(screen.getByTestId("provider-key-input"), KEY);
    await userEvent.click(screen.getByTestId("provider-key-save"));
    status = await screen.findByTestId("provider-key-status");
    expect(status).toHaveTextContent("podium-agent is not reachable.");
    expect(status.className).toContain("text-warn");
  });

  it("trims the whitespace and quotes a paste out of a .env file brings with it", async () => {
    onSave.mockResolvedValue(saved);
    mount(notSet);
    const input = screen.getByTestId("provider-key-input");
    await userEvent.click(input);
    await userEvent.paste(`  "${KEY}"  `);
    expect(input).toHaveValue(KEY);
    await userEvent.click(screen.getByTestId("provider-key-save"));
    await waitFor(() => expect(onSave).toHaveBeenCalledWith(KEY));
  });

  it("submits on Enter", async () => {
    onSave.mockResolvedValue(saved);
    mount(notSet);
    await userEvent.type(screen.getByTestId("provider-key-input"), `${KEY}{Enter}`);
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1));
  });

  it("hides the key by default and reveals it on request", async () => {
    mount(notSet);
    const input = screen.getByTestId("provider-key-input");
    expect(input).toHaveAttribute("type", "password");
    const toggle = screen.getByRole("button", { name: "Show the key" });
    expect(toggle).toHaveAttribute("aria-pressed", "false");
    await userEvent.click(toggle);
    expect(input).toHaveAttribute("type", "text");
    expect(screen.getByRole("button", { name: "Hide the key" })).toHaveAttribute(
      "aria-pressed",
      "true",
    );
  });

  it("shows who set the key and when, and offers to replace it", () => {
    mount(connected);
    expect(screen.getByText("Connected")).toBeInTheDocument();
    const meta = screen.getByTestId("provider-key-meta");
    expect(meta).toHaveTextContent("••••abcd");
    expect(meta).toHaveTextContent("set by alice@example.com");
    expect(meta).toHaveTextContent("2m ago");
    expect(screen.getByTestId("provider-key-input")).toHaveAttribute(
      "placeholder",
      "Paste a new key to replace ••••abcd",
    );
    // Nothing about the stored key beyond its last four is anywhere on the page.
    expect(screen.queryByText(/sk-ant/)).toBeNull();
  });

  // A key set with `podium secret set`, or replaced with it since: the conductor confirms a
  // key exists but withholds a hint that is about a different one.
  it("says a key was set outside the UI when there is no hint for it", () => {
    mount(
      create(ProviderSettingsSchema, {
        provider: "anthropic",
        keySet: true,
        model: "claude-opus-5",
      }),
    );
    expect(screen.getByText("Connected")).toBeInTheDocument();
    expect(screen.getByTestId("provider-key-meta")).toHaveTextContent("set outside this UI");
    expect(screen.getByTestId("provider-key-meta")).not.toHaveTextContent("••••");
    expect(screen.getByTestId("provider-key-input")).toHaveAttribute(
      "placeholder",
      "Paste a key to replace the one that is set",
    );
  });

  it("confirms a removal before sending it, and Cancel sends nothing", async () => {
    mount(connected);
    await userEvent.click(screen.getByTestId("provider-key-remove"));
    expect(onClear).not.toHaveBeenCalled();
    expect(screen.getByText(/Agents will fail until a key is set again/)).toBeInTheDocument();
    // The safe option has the keyboard.
    expect(screen.getByRole("button", { name: "Cancel" })).toHaveFocus();

    await userEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(onClear).not.toHaveBeenCalled();
    expect(screen.getByTestId("provider-key-remove")).toBeInTheDocument();
  });

  it("removes the key exactly once on Confirm", async () => {
    onClear.mockResolvedValue(undefined);
    mount(connected);
    await userEvent.click(screen.getByTestId("provider-key-remove"));
    await userEvent.click(screen.getByRole("button", { name: "Confirm" }));
    await waitFor(() => expect(onClear).toHaveBeenCalledTimes(1));
    expect(await screen.findByText(/The Anthropic key was removed/)).toBeInTheDocument();
  });

  it("cancels the removal on Escape", async () => {
    mount(connected);
    await userEvent.click(screen.getByTestId("provider-key-remove"));
    await userEvent.keyboard("{Escape}");
    expect(screen.getByTestId("provider-key-remove")).toBeInTheDocument();
    expect(onClear).not.toHaveBeenCalled();
  });
});
