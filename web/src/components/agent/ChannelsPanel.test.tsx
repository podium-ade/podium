import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { MemoryRouter } from "react-router";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { ToastHost } from "../Toast";
import { ChannelsPanel } from "./ChannelsPanel";

const listSlackChannels = vi.fn();
const setSlackChannelDescription = vi.fn();

vi.mock("../../lib/client", async () => {
  const actual = await vi.importActual<typeof import("../../lib/client")>("../../lib/client");
  return {
    ...actual,
    agent: {
      listSlackChannels: (...a: unknown[]) => listSlackChannels(...a),
      setSlackChannelDescription: (...a: unknown[]) => setSlackChannelDescription(...a),
    },
  };
});

function channel(over: Record<string, unknown> = {}) {
  return {
    id: "C0123",
    name: "support",
    description: "",
    updatedAt: timestampFromDate(new Date()),
    ...over,
  };
}

function mount() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={qc}>
      <ToastHost>
        <MemoryRouter>
          <ChannelsPanel />
        </MemoryRouter>
      </ToastHost>
    </QueryClientProvider>,
  );
}

describe("ChannelsPanel", () => {
  beforeAll(() => {
    // Radix's Select drives pointer capture and scrolling, neither of which jsdom has.
    Element.prototype.hasPointerCapture = vi.fn(() => false) as never;
    Element.prototype.setPointerCapture = vi.fn() as never;
    Element.prototype.releasePointerCapture = vi.fn() as never;
    Element.prototype.scrollIntoView = vi.fn() as never;
  });

  beforeEach(() => {
    listSlackChannels.mockReset();
    setSlackChannelDescription.mockReset();
  });

  it("lists a channel and saves a description", async () => {
    listSlackChannels.mockResolvedValue({
      channels: [channel()],
      slackConnected: true,
    });
    setSlackChannelDescription.mockResolvedValue({
      channel: channel({ description: "Customer complaints." }),
    });
    mount();

    const row = await screen.findByTestId("slack-channel-row");
    expect(row).toHaveTextContent("#support");
    expect(row).toHaveTextContent("C0123");
    expect(row).toHaveTextContent("No context yet");

    await userEvent.click(screen.getByTestId("slack-channel-edit"));
    const input = await screen.findByTestId("slack-channel-description");
    await userEvent.clear(input);
    await userEvent.type(input, "Customer complaints.");
    await userEvent.click(screen.getByTestId("slack-channel-save"));

    await waitFor(() =>
      expect(setSlackChannelDescription).toHaveBeenCalledWith({
        id: "C0123",
        description: "Customer complaints.",
      }),
    );
  });

  it("says so when Slack is not connected", async () => {
    listSlackChannels.mockResolvedValue({ channels: [], slackConnected: false });
    mount();
    expect(await screen.findByText(/Slack is not connected/)).toBeInTheDocument();
  });
  async function pick(control: string, option: string) {
    await userEvent.click(screen.getByRole("combobox", { name: control }));
    await userEvent.click(await screen.findByRole("option", { name: option }));
  }

  it("searches by name, id and context", async () => {
    listSlackChannels.mockResolvedValue({
      channels: [
        channel({ id: "C1", name: "support", description: "Customer complaints." }),
        channel({ id: "C2", name: "eng", description: "Engineering." }),
      ],
      slackConnected: true,
    });
    mount();
    expect(await screen.findAllByTestId("slack-channel-row")).toHaveLength(2);

    const search = screen.getByTestId("channel-search");
    await userEvent.type(search, "eng");
    await waitFor(() => expect(screen.getAllByTestId("slack-channel-row")).toHaveLength(1));
    expect(screen.getByTestId("slack-channel-row")).toHaveTextContent("#eng");

    // The id is searchable too, which is the only handle on an unnamed channel.
    await userEvent.clear(search);
    await userEvent.type(search, "C1");
    await waitFor(() => expect(screen.getAllByTestId("slack-channel-row")).toHaveLength(1));
    expect(screen.getByTestId("slack-channel-row")).toHaveTextContent("#support");

    // And so is the operator's note.
    await userEvent.clear(search);
    await userEvent.type(search, "complaints");
    await waitFor(() => expect(screen.getAllByTestId("slack-channel-row")).toHaveLength(1));
    expect(screen.getByTestId("slack-channel-row")).toHaveTextContent("#support");
  });

  it("filters to the channels a description has been set on", async () => {
    listSlackChannels.mockResolvedValue({
      channels: [
        channel({ id: "C1", name: "support", description: "Customer complaints." }),
        channel({ id: "C2", name: "eng", description: "" }),
        channel({ id: "C3", name: "spaces", description: "   " }),
      ],
      slackConnected: true,
    });
    mount();
    expect(await screen.findAllByTestId("slack-channel-row")).toHaveLength(3);

    await pick("Filter by context", "With context");
    await waitFor(() => expect(screen.getAllByTestId("slack-channel-row")).toHaveLength(1));
    expect(screen.getByTestId("slack-channel-row")).toHaveTextContent("#support");

    // Whitespace is not a description, so #spaces counts as without.
    await pick("Filter by context", "Without context");
    await waitFor(() => expect(screen.getAllByTestId("slack-channel-row")).toHaveLength(2));
  });

  it("sorts by name", async () => {
    listSlackChannels.mockResolvedValue({
      channels: [
        channel({ id: "C1", name: "support" }),
        channel({ id: "C2", name: "eng" }),
      ],
      slackConnected: true,
    });
    mount();
    await screen.findAllByTestId("slack-channel-row");

    await pick("Sort channels", "Name A–Z");
    await waitFor(() =>
      expect(screen.getAllByTestId("slack-channel-row")[0]).toHaveTextContent("#eng"),
    );

    await pick("Sort channels", "Name Z–A");
    await waitFor(() =>
      expect(screen.getAllByTestId("slack-channel-row")[0]).toHaveTextContent("#support"),
    );
  });

  it("paginates a catalogue longer than one page", async () => {
    listSlackChannels.mockResolvedValue({
      channels: Array.from({ length: 25 }, (_, i) =>
        channel({ id: `C${i}`, name: `chan-${String(i).padStart(2, "0")}` }),
      ),
      slackConnected: true,
    });
    mount();
    expect(await screen.findAllByTestId("slack-channel-row")).toHaveLength(20);
    expect(screen.getByTestId("channel-range")).toHaveTextContent("1–20 of 25");
    expect(screen.getByTestId("channel-prev")).toBeDisabled();

    await userEvent.click(screen.getByTestId("channel-next"));
    await waitFor(() => expect(screen.getAllByTestId("slack-channel-row")).toHaveLength(5));
    expect(screen.getByTestId("channel-range")).toHaveTextContent("21–25 of 25");
    expect(screen.getByTestId("channel-next")).toBeDisabled();

    // A search that shrinks the list past the current page shows the last page, not a
    // blank one.
    await userEvent.type(screen.getByTestId("channel-search"), "chan-01");
    await waitFor(() => expect(screen.getAllByTestId("slack-channel-row")).toHaveLength(1));
  });

  it("says so when nothing matches", async () => {
    listSlackChannels.mockResolvedValue({
      channels: [channel({ id: "C1", name: "support" })],
      slackConnected: true,
    });
    mount();
    await screen.findAllByTestId("slack-channel-row");
    await userEvent.type(screen.getByTestId("channel-search"), "nothing-like-this");
    expect(await screen.findByText(/No channels match/)).toBeInTheDocument();
  });
});
