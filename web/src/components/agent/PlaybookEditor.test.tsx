import { describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { MemoryRouter } from "react-router";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { PlaybookDefinitionSchema } from "../../gen/podium/agent/v1/agent_pb";
import { catalogue } from "../../test/agents";
import { PlaybookEditor } from "./PlaybookEditor";

const onSubmit = vi.fn();
const onCancel = vi.fn();

const REGISTERED = ["podium.agent.github_token", "podium.agent.warehouse_url"];

function mount(props: Partial<Parameters<typeof PlaybookEditor>[0]> = {}) {
  onSubmit.mockReset();
  onCancel.mockReset();
  return render(
    <MemoryRouter>
      <PlaybookEditor
        agents={catalogue()}
        profileDefault={{ agent: "claude", model: "claude-opus-5", effort: "" }}
        secretNames={REGISTERED}
        onSubmit={onSubmit}
        onCancel={onCancel}
        {...props}
      />
    </MemoryRouter>,
  );
}

describe("PlaybookEditor", () => {
  it("sends the whole definition, secrets included", async () => {
    mount();
    await userEvent.type(screen.getByLabelText("Playbook name"), "reporter");
    await userEvent.type(screen.getByLabelText("Image"), "ghcr.io/example/reporter:v1");
    await userEvent.type(screen.getByLabelText("System prompt"), "Write the weekly report.");
    await userEvent.type(screen.getByLabelText("Allowed tools"), "read\nbash");

    await userEvent.click(screen.getByRole("button", { name: "Add a secret" }));
    await userEvent.type(
      screen.getByLabelText("Secret name 1"),
      "podium.agent.warehouse_url",
    );
    await userEvent.type(screen.getByLabelText("Secret key 1"), "WAREHOUSE_URL");

    await userEvent.click(screen.getByRole("button", { name: "Create playbook" }));

    expect(onSubmit).toHaveBeenCalledTimes(1);
    expect(onSubmit.mock.calls[0][0]).toMatchObject({
      name: "reporter",
      image: "ghcr.io/example/reporter:v1",
      systemPrompt: "Write the weekly report.",
      allowedTools: ["read", "bash"],
      maxTurns: 50,
      timeout: "30m",
      linear: false,
      secrets: [
        { name: "podium.agent.warehouse_url", target: "env", key: "WAREHOUSE_URL" },
      ],
    });
  });

  it("offers the registered secret names and never a value", async () => {
    mount();
    await userEvent.click(screen.getByRole("button", { name: "Add a secret" }));
    const options = Array.from(document.querySelectorAll("datalist option")).map(
      (o) => (o as HTMLOptionElement).value,
    );
    expect(options).toEqual(REGISTERED);
    // There is no read API for a secret, so nothing here can offer one.
    expect(screen.queryByLabelText(/secret value/i)).toBeNull();
  });

  it("warns about a secret the control plane does not hold and points at the Secrets screen", async () => {
    mount();
    await userEvent.click(screen.getByRole("button", { name: "Add a secret" }));
    await userEvent.type(screen.getByLabelText("Secret name 1"), "nope.not.registered");

    expect(screen.getByText(/will fail admission/)).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Register it" })).toHaveAttribute(
      "href",
      "/secrets",
    );
  });

  it("does not warn while the secret list is unknown", async () => {
    mount({ secretNames: [], secretsUnknown: true });
    await userEvent.click(screen.getByRole("button", { name: "Add a secret" }));
    await userEvent.type(screen.getByLabelText("Secret name 1"), "anything");
    expect(screen.queryByText(/will fail admission/)).toBeNull();
  });

  it("refuses to send a name that could not be a playbook name", async () => {
    mount();
    await userEvent.type(screen.getByLabelText("Playbook name"), "Not A Name");
    await userEvent.type(screen.getByLabelText("Image"), "img:1");
    await userEvent.type(screen.getByLabelText("System prompt"), "hi");
    await userEvent.type(screen.getByLabelText("Allowed tools"), "read");
    await userEvent.click(screen.getByRole("button", { name: "Create playbook" }));

    expect(onSubmit).not.toHaveBeenCalled();
    expect(screen.getByText(/A name must match/)).toBeInTheDocument();
  });

  it("names no Podium image: the image is free text the operator supplies", () => {
    mount();
    const image = screen.getByLabelText("Image");
    expect(image).toHaveValue("");
    expect(screen.getByText(/Any image you supply/)).toBeInTheDocument();
    // A FROM line in the help is a hint, not a picker.
    expect(screen.queryByRole("combobox", { name: "Image" })).toBeNull();
  });

  it("keeps the name fixed when editing and loads what is stored", () => {
    mount({
      playbook: create(PlaybookDefinitionSchema, {
        name: "reporter",
        image: "ghcr.io/example/reporter:v1",
        systemPrompt: "Write the weekly report.",
        allowedTools: ["read"],
        maxTurns: 12,
        timeout: "5m",
        origin: "stored",
        editable: true,
      }),
    });
    expect(screen.getByLabelText("Playbook name")).toBeDisabled();
    expect(screen.getByLabelText("Playbook name")).toHaveValue("reporter");
    expect(screen.getByLabelText("Max turns")).toHaveValue(12);
    expect(screen.getByLabelText("Timeout")).toHaveValue("5m");
    expect(screen.getByRole("button", { name: "Save playbook" })).toBeInTheDocument();
  });

  it("offers no delete while creating: there is nothing to delete yet", () => {
    mount();
    expect(screen.queryByRole("button", { name: /^Delete/ })).toBeNull();
  });

  it("deletes only after a confirm, and only from here", async () => {
    const onDelete = vi.fn();
    mount({
      playbook: create(PlaybookDefinitionSchema, {
        name: "reporter",
        image: "ghcr.io/example/reporter:v1",
        origin: "stored",
        editable: true,
      }),
      onDelete,
    });
    await userEvent.click(screen.getByRole("button", { name: "Delete reporter" }));
    expect(onDelete).not.toHaveBeenCalled();

    await userEvent.click(screen.getByRole("button", { name: "Confirm deleting reporter" }));
    expect(onDelete).toHaveBeenCalledTimes(1);
  });

  it("shows a shadowed playbook read-only, with the delete as the only thing to do to it", () => {
    mount({
      playbook: create(PlaybookDefinitionSchema, {
        name: "general",
        image: "ghcr.io/example/general:v1",
        origin: "stored",
        editable: true,
        shadowed: true,
      }),
      onDelete: vi.fn(),
    });
    expect(screen.queryByRole("button", { name: "Save playbook" })).toBeNull();
    expect(screen.getByLabelText("Image")).toBeDisabled();
    expect(screen.getByText(/never runs and cannot be written over/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Delete general" })).toBeInTheDocument();
  });

  it("shows the server's refusal verbatim", () => {
    mount({ error: 'playbook "reporter": allowed_tools is required' });
    expect(screen.getByRole("alert")).toHaveTextContent(
      'playbook "reporter": allowed_tools is required',
    );
  });
});
