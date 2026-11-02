import { beforeEach, describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { act, fireEvent, render, screen } from "@testing-library/react";
import {
  PollProviderOAuthResponseSchema,
  ProviderSettingsSchema,
  StartProviderOAuthResponseSchema,
} from "../../gen/podium/agent/v1/agent_pb";
import { ANTHROPIC } from "../../lib/agents";
import { ProviderCard } from "./ProviderCard";
import { ToastHost } from "../Toast";

const onStartOAuth = vi.fn();
const onPollOAuth = vi.fn();
const onSignedIn = vi.fn();

const authorize =
  "https://claude.com/cai/oauth/authorize?code=true&client_id=x&response_type=code&state=st";

const started = create(StartProviderOAuthResponseSchema, {
  flowId: "flow_claude",
  verificationUri: authorize,
  verificationUriComplete: authorize,
  interval: 5,
  expiresAt: timestampFromDate(new Date(Date.now() + 900_000)),
  codePaste: true,
});

function mount() {
  return render(
    <ToastHost>
      <table>
        <tbody>
          <ProviderCard
            provider={ANTHROPIC}
            scope="personal"
            settings={create(ProviderSettingsSchema, {
              provider: "anthropic",
              keySet: false,
            })}
            onSave={vi.fn()}
            onClear={vi.fn()}
            onStartOAuth={onStartOAuth}
            onPollOAuth={onPollOAuth}
            onSignedIn={onSignedIn}
          />
        </tbody>
      </table>
    </ToastHost>,
  );
}

async function start() {
  mount();
  fireEvent.click(screen.getByRole("button", { name: "Connect" }));
  fireEvent.click(screen.getByTestId("provider-mode-subscription-anthropic"));
  fireEvent.click(screen.getByTestId("provider-oauth-start-anthropic"));
  await act(async () => {});
}

describe("ProviderCard: the Claude sign-in, finished by pasting a code", () => {
  beforeEach(() => {
    onStartOAuth.mockReset().mockResolvedValue(started);
    onPollOAuth.mockReset();
    onSignedIn.mockReset();
  });

  it("links to Claude and asks for the code instead of showing one", async () => {
    await start();
    const link = screen.getByTestId("provider-oauth-link-anthropic");
    expect(link).toHaveAttribute("href", authorize);
    expect(link).toHaveTextContent("claude.com");
    expect(screen.queryByTestId("provider-oauth-code-anthropic")).toBeNull();
    expect(
      screen.getByTestId("provider-oauth-finish-anthropic"),
    ).toBeDisabled();
  });

  it("does not poll on its own: there is nothing to wait for until a code is pasted", async () => {
    vi.useFakeTimers();
    try {
      await start();
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30_000);
      });
      expect(onPollOAuth).not.toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  it("sends the pasted code with the flow and reports who signed in", async () => {
    onPollOAuth.mockResolvedValue(
      create(PollProviderOAuthResponseSchema, {
        state: "done",
        provider: create(ProviderSettingsSchema, {
          provider: "anthropic",
          keySet: true,
          authKind: "oauth",
          account: "alice@example.test",
        }),
      }),
    );
    await start();
    fireEvent.change(screen.getByTestId("provider-oauth-paste-anthropic"), {
      target: { value: '  "abc#st"  ' },
    });
    fireEvent.click(screen.getByTestId("provider-oauth-finish-anthropic"));
    await act(async () => {});

    expect(onPollOAuth).toHaveBeenCalledWith("flow_claude", "abc#st");
    expect(onSignedIn).toHaveBeenCalledTimes(1);
    expect(
      screen.getByText(/Signed in as alice@example.test/),
    ).toBeInTheDocument();
  });

  it("says a refused code was refused, in Anthropic's words", async () => {
    onPollOAuth.mockResolvedValue(
      create(PollProviderOAuthResponseSchema, {
        state: "denied",
        detail: "Invalid authorization code",
      }),
    );
    await start();
    fireEvent.change(screen.getByTestId("provider-oauth-paste-anthropic"), {
      target: { value: "typo#st" },
    });
    fireEvent.click(screen.getByTestId("provider-oauth-finish-anthropic"));
    await act(async () => {});

    expect(screen.getByText("The sign-in was refused.")).toBeInTheDocument();
    expect(screen.getByText(/Invalid authorization code/)).toBeInTheDocument();
  });
});
