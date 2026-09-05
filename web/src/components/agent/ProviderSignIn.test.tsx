import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { timestampFromDate } from "@bufbuild/protobuf/wkt";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { Code, ConnectError } from "@connectrpc/connect";
import {
  PollProviderOAuthResponseSchema,
  ProviderSettingsSchema,
  StartProviderOAuthResponseSchema,
  type ProviderSettings,
} from "../../gen/podium/agent/v1/agent_pb";
import { XAI } from "../../lib/agents";
import { ProviderCard } from "./ProviderCard";
import { ToastHost } from "../Toast";

const onSave = vi.fn();
const onClear = vi.fn();
const onStartOAuth = vi.fn();
const onPollOAuth = vi.fn();
const onSignedIn = vi.fn();

/**
 * The clock is fake for the whole file. The card polls on a timer, and a test that waited on
 * a real one would be a test that passes on an idle machine and fails on a busy one — so
 * time is advanced explicitly and every assertion is about a poll that definitely happened.
 *
 * That is also why the clicks below are fireEvent and not userEvent: userEvent waits on
 * timers of its own between events, and under a fake clock those never fire.
 */

const notSet = create(ProviderSettingsSchema, { provider: "xai", keySet: false });

/** POLL_SECONDS is what the fake start response asks the card to wait between polls. */
const POLL_SECONDS = 5;

const started = create(StartProviderOAuthResponseSchema, {
  flowId: "flow_abc",
  userCode: "WDJB-MJHT",
  verificationUri: "https://accounts.x.ai/device",
  verificationUriComplete: "https://accounts.x.ai/device?user_code=WDJB-MJHT",
  interval: POLL_SECONDS,
  expiresAt: timestampFromDate(new Date(Date.now() + 600_000)),
});

function poll(fields: Parameters<typeof create<typeof PollProviderOAuthResponseSchema>>[1] = {}) {
  return create(PollProviderOAuthResponseSchema, { interval: POLL_SECONDS, ...fields });
}

function mount(settings: ProviderSettings = notSet) {
  return render(
    <ToastHost>
      <ProviderCard
        provider={XAI}
        settings={settings}
        onSave={onSave}
        onClear={onClear}
        onStartOAuth={onStartOAuth}
        onPollOAuth={onPollOAuth}
        onSignedIn={onSignedIn}
      />
    </ToastHost>,
  );
}

/** tick advances the fake clock and lets whatever it started settle. */
async function tick(seconds = POLL_SECONDS) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(seconds * 1000);
  });
}

/** signIn walks to the point where the card is polling, without any poll having run yet. */
async function signIn() {
  const rendered = mount();
  fireEvent.click(screen.getByTestId("provider-mode-subscription-xai"));
  fireEvent.click(screen.getByTestId("provider-oauth-start-xai"));
  // The start RPC resolves on a microtask; nothing is on the clock yet.
  await act(async () => {});
  expect(screen.getByTestId("provider-oauth-panel-xai")).toBeInTheDocument();
  return rendered;
}

describe("ProviderCard: the subscription sign-in", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    onSave.mockReset();
    onClear.mockReset();
    onStartOAuth.mockReset().mockResolvedValue(started);
    onPollOAuth.mockReset();
    onSignedIn.mockReset();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("offers both ways in for a provider that has both", () => {
    mount();
    expect(screen.getByTestId("provider-mode-key-xai")).toBeInTheDocument();
    expect(screen.getByTestId("provider-mode-subscription-xai")).toBeInTheDocument();
  });

  it("shows the code and the URL a human has to use", async () => {
    onPollOAuth.mockResolvedValue(poll({ state: "pending" }));
    await signIn();

    expect(onStartOAuth).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId("provider-oauth-code-xai")).toHaveTextContent("WDJB-MJHT");
    // The link carries the code so a human can skip typing it, but the text is the plain
    // URL: it is the one they will read out or type on a phone.
    const link = screen.getByRole("link", { name: "https://accounts.x.ai/device" });
    expect(link).toHaveAttribute("href", started.verificationUriComplete);
    expect(screen.getByText(/Waiting for you to approve it/)).toBeInTheDocument();
  });

  it("polls at the interval the server asked for, and stores nothing while it waits", async () => {
    onPollOAuth.mockResolvedValue(poll({ state: "pending" }));
    await signIn();
    expect(onPollOAuth).not.toHaveBeenCalled();

    await tick();
    expect(onPollOAuth).toHaveBeenCalledTimes(1);
    expect(onPollOAuth).toHaveBeenCalledWith("flow_abc");

    await tick();
    expect(onPollOAuth).toHaveBeenCalledTimes(2);

    expect(onSignedIn).not.toHaveBeenCalled();
    expect(screen.getByTestId("provider-oauth-panel-xai")).toBeInTheDocument();
  });

  it("backs off when the server says slow down", async () => {
    onPollOAuth.mockResolvedValue(poll({ state: "slow_down", interval: POLL_SECONDS + 3 }));
    await signIn();
    await tick();
    expect(onPollOAuth).toHaveBeenCalledTimes(1);

    // The old interval is no longer enough: the next poll is on the new one.
    await tick(POLL_SECONDS);
    expect(onPollOAuth).toHaveBeenCalledTimes(1);
    await tick(3);
    expect(onPollOAuth).toHaveBeenCalledTimes(2);
  });

  it("says who signed in and tells the page to re-read the settings", async () => {
    onPollOAuth.mockResolvedValue(
      poll({
        state: "done",
        provider: create(ProviderSettingsSchema, {
          provider: "xai",
          keySet: true,
          authKind: "oauth",
          account: "someone@example.com",
          refreshable: true,
        }),
      }),
    );
    await signIn();
    await tick();

    expect(screen.getByTestId("provider-key-status-xai")).toHaveTextContent(
      "Signed in as someone@example.com",
    );
    expect(onSignedIn).toHaveBeenCalledTimes(1);
    // The waiting panel is gone: there is nothing left to approve.
    expect(screen.queryByTestId("provider-oauth-panel-xai")).toBeNull();
  });

  it("stops and explains when the provider refuses, in the provider's own words", async () => {
    onPollOAuth.mockResolvedValue(
      poll({ state: "denied", detail: "this client is not allowed on the OAuth API" }),
    );
    await signIn();
    await tick();

    expect(screen.getByTestId("provider-key-status-xai")).toHaveTextContent(
      "The sign-in was refused.",
    );
    expect(screen.getByTestId("provider-key-detail-xai")).toHaveTextContent(
      "this client is not allowed on the OAuth API",
    );

    // A refused flow is over: nothing keeps polling behind the message.
    await tick(POLL_SECONDS * 4);
    expect(onPollOAuth).toHaveBeenCalledTimes(1);
  });

  it("says the code expired rather than pretending it is still waiting", async () => {
    onPollOAuth.mockResolvedValue(poll({ state: "expired" }));
    await signIn();
    await tick();
    expect(screen.getByTestId("provider-key-status-xai")).toHaveTextContent(
      "The code expired before it was approved.",
    );
  });

  it("says which knob is unset when the control plane has no OAuth client", async () => {
    onStartOAuth.mockRejectedValue(
      new ConnectError(
        "subscription sign-in is not configured on this control plane: set " +
          "PODIUM_AGENT_XAI_OAUTH_CLIENT_ID",
        Code.FailedPrecondition,
      ),
    );
    mount();
    fireEvent.click(screen.getByTestId("provider-mode-subscription-xai"));
    fireEvent.click(screen.getByTestId("provider-oauth-start-xai"));
    await act(async () => {});

    const status = screen.getByTestId("provider-key-status-xai");
    expect(status).toHaveTextContent("PODIUM_AGENT_XAI_OAUTH_CLIENT_ID");
    // A configuration problem is not a refusal, so it is not red.
    expect(status).toHaveClass("text-warn");
  });

  it("stops polling when the human cancels", async () => {
    onPollOAuth.mockResolvedValue(poll({ state: "pending" }));
    await signIn();
    await tick();
    expect(onPollOAuth).toHaveBeenCalledTimes(1);

    fireEvent.click(screen.getByRole("button", { name: "Cancel" }));
    expect(screen.queryByTestId("provider-oauth-panel-xai")).toBeNull();

    await tick(POLL_SECONDS * 4);
    expect(onPollOAuth).toHaveBeenCalledTimes(1);
  });

  it("stops polling when the card goes away", async () => {
    onPollOAuth.mockResolvedValue(poll({ state: "pending" }));
    const { unmount } = await signIn();
    await tick();
    expect(onPollOAuth).toHaveBeenCalledTimes(1);

    unmount();
    await tick(POLL_SECONDS * 4);
    // The effect's cleanup is what stops it. Without it the loop would outlive the card and
    // keep asking the provider about a sign-in nobody is waiting for.
    expect(onPollOAuth).toHaveBeenCalledTimes(1);
  });

  it("shows a stored subscription as one, not as a key with no hint", () => {
    mount(
      create(ProviderSettingsSchema, {
        provider: "xai",
        keySet: true,
        authKind: "oauth",
        account: "someone@example.com",
        refreshable: true,
      }),
    );
    const meta = screen.getByTestId("provider-key-meta-xai");
    expect(meta).toHaveTextContent("subscription");
    expect(meta).toHaveTextContent("someone@example.com");
    expect(meta).toHaveTextContent("auto-renewing");
    // Signing out is not the same words as removing a key, because it is not the same act.
    expect(screen.getByTestId("provider-key-remove-xai")).toHaveTextContent("Sign out");
  });

  it("warns when a stored subscription cannot renew itself", () => {
    mount(
      create(ProviderSettingsSchema, {
        provider: "xai",
        keySet: true,
        authKind: "oauth",
        refreshable: false,
      }),
    );
    expect(screen.getByTestId("provider-key-meta-xai")).toHaveTextContent("not renewable");
  });
});
