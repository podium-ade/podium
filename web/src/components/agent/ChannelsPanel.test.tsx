import { beforeEach, describe, expect, it, vi } from "vitest";
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
});
