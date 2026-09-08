import { execFileSync } from "node:child_process";
import { expect, test, type Page } from "@playwright/test";

const SERVER = process.env.PODIUM_UI_URL ?? "http://127.0.0.1:8080";
const TOKEN = process.env.PODIUM_DEV_TOKEN ?? "devtoken";
const CLI = process.env.PODIUM_CLI ?? "../bin/podium";

/** The one key the fake Anthropic accepts. An obvious fake; not a credential. */
const GOOD_KEY = process.env.FAKE_ANTHROPIC_KEY ?? "sk-ant-test-good";
const BAD_KEY = "sk-ant-test-bad";

/** The reserved secret SetProviderKey writes. */
const KEY_SECRET = "podium.agent.anthropic_api_key";

/**
 * DRY_RUN says the stack's conductor is running a profile whose playbooks set
 * PODIUM_AGENT_DRY_RUN=1 — the test seam step 16 defined, which makes the agent runtime skip
 * the model and answer "dry run: <instruction>".
 *
 * The chat round trip needs more of a stack than the rest of this file: a node, Docker, and
 * the agent runtime image. It also needs a real turn, and this machine has no provider key.
 * So the round trip runs only when the operator says the profile is a dry run; the rest of
 * the chat screen is exercised either way. See playwright.config.ts for the recipe.
 */
const DRY_RUN = process.env.PODIUM_AGENT_DRY_RUN === "1";

/**
 * anthropicCard scopes a query to one provider's card. Settings shows a card per
 * provider now, so "Not set" and "Connected" appear more than once on it.
 */
function anthropicCard(page: Page) {
  return page.getByTestId("provider-card-anthropic");
}

function podium(...args: string[]): string {
  return execFileSync(CLI, ["--server", SERVER, "--token", TOKEN, ...args], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  }).trim();
}

/** The dev transport has no session; seed the token the UI would otherwise prompt for. */
async function authenticate(page: Page) {
  await page.addInitScript(
    ([token]) => window.localStorage.setItem("podium.devToken", token),
    [TOKEN],
  );
}

/** Leave the control plane as it was found: no key set. */
test.afterEach(() => {
  try {
    podium("secret", "rm", KEY_SECRET);
  } catch {
    // Already gone, which is the state this wants.
  }
});

/**
 * seedProviderKey makes the reserved secret EXIST, which is what a turn needs before the
 * control plane will admit its task. A dry run never reads it.
 *
 * Every test in this file removes it again in afterEach — the settings test asserts the
 * empty state and has to start from it — so a test that needs a turn seeds it itself rather
 * than relying on the stack having one. Without this a chat turn fails admission in about a
 * second with "This bot is missing a credential", which is correct behaviour and not what
 * these tests are about.
 */
function seedProviderKey() {
  podium("secret", "set", KEY_SECRET, "--value", "sk-ant-not-a-real-key");
}

test("the agent settings page validates and stores a provider key", async ({ page }) => {
  await authenticate(page);
  await page.goto("/agent/settings");

  // The Agent tab exists at all only because WhoAmI said the server proxies a conductor.
  await expect(page.getByRole("link", { name: "Agent" })).toBeVisible();
  await expect(anthropicCard(page).getByText("Not set")).toBeVisible();
  await expect(page.getByText(/encrypted at rest by podium-server/i)).toBeVisible();
  await expect(page.getByTestId("provider-key-save-anthropic")).toBeDisabled();

  // A key the provider refuses is refused here, and nothing is stored.
  await page.getByTestId("provider-key-input-anthropic").fill(BAD_KEY);
  await page.getByTestId("provider-key-save-anthropic").click();
  await expect(page.getByTestId("provider-key-status-anthropic")).toContainText(
    "Anthropic rejected this key",
  );
  // And why, in the provider's own words, all the way through podium-server's proxy.
  await expect(page.getByTestId("provider-key-detail-anthropic")).toContainText(
    "Anthropic said: API key is invalid.",
  );
  await expect(anthropicCard(page).getByText("Not set")).toBeVisible();
  expect(podium("secret", "ls")).not.toContain(KEY_SECRET);

  // A key it accepts is validated against the real endpoint shape, then stored.
  await page.getByTestId("provider-key-input-anthropic").fill(GOOD_KEY);
  await page.getByTestId("provider-key-save-anthropic").click();
  await expect(page.getByTestId("provider-key-status-anthropic")).toContainText("Saved.");
  await expect(page.getByTestId("provider-key-status-anthropic")).toContainText("••••good");
  await expect(page.getByText("claude-opus-5")).toBeVisible();
  await expect(anthropicCard(page).getByText("Connected")).toBeVisible();
  await expect(page.getByTestId("provider-key-meta-anthropic")).toContainText("••••good");

  // It is a Podium secret now, and only its metadata is visible anywhere.
  const listed = podium("secret", "ls");
  expect(listed).toContain(KEY_SECRET);
  expect(listed).not.toContain(GOOD_KEY);

  // A reload reads it back out of the settings row, not out of the browser.
  await page.reload();
  await expect(anthropicCard(page).getByText("Connected")).toBeVisible();
  await expect(page.getByTestId("provider-key-meta-anthropic")).toContainText("••••good");
  await expect(page.getByTestId("provider-key-input-anthropic")).toHaveAttribute(
    "placeholder",
    "Paste a new key to replace ••••good",
  );

  // Removing it takes both the secret and the metadata.
  await page.getByTestId("provider-key-remove-anthropic").click();
  await expect(page.getByText(/Agents will fail until a key is set again/)).toBeVisible();
  await page.getByRole("button", { name: "Confirm" }).click();
  await expect(anthropicCard(page).getByText("Not set")).toBeVisible();
  await expect.poll(() => podium("secret", "ls")).not.toContain(KEY_SECRET);
});

test("the agent tabs are real routes", async ({ page }) => {
  await authenticate(page);

  // /agent lands on Chat — that is the thing an operator opens this tab to do.
  await page.goto("/agent");
  await expect(page).toHaveURL(/\/agent\/chat$/);
  await expect(page.getByTestId("chat-new")).toBeVisible();

  await page.getByRole("link", { name: "Settings" }).click();
  await expect(page).toHaveURL(/\/agent\/settings$/);
  await expect(page.getByRole("heading", { name: "Anthropic" })).toBeVisible();

  // Playbooks and Skills are sidebar destinations, not tabs. This harness's profile comes
  // from files, so every playbook on it must be read-only: the files are authoritative for
  // the names they hold.
  await page.getByRole("link", { name: "Playbooks" }).click();
  await expect(page).toHaveURL(/\/agent\/playbooks$/);
  await expect(page.getByTestId("playbook-row").first()).toBeVisible();
  await expect(page.getByTestId("playbook-image").first()).not.toBeEmpty();
  await expect(page.getByText("file · read-only").first()).toBeVisible();
  await expect(page.getByTestId("playbook-new")).toBeVisible();

  // Whatever this harness holds, two things have to be here: a way in, and the sentence
  // about what a skill actually is — it runs in the turn's container with the turn's
  // credentials, and granting one is now a click rather than a file edit.
  await page.getByRole("link", { name: "Skills" }).click();
  await expect(page).toHaveURL(/\/agent\/skills$/);
  await expect(page.getByTestId("skill-new")).toBeVisible();
  await expect(
    page.getByText("A skill runs in the turn's container with the turn's credentials"),
  ).toBeVisible();

  // Back into the talk screens: Agent in the sidebar, then the remaining tabs.
  await page.getByRole("link", { name: "Agent" }).click();
  await expect(page).toHaveURL(/\/agent\/chat$/);

  // The Assistant tab reads the profile the conductor is actually running, files and stored
  // playbooks merged, so the directory it was loaded from is the proof it is the real one.
  await page.getByRole("link", { name: "Assistant" }).click();
  await expect(page).toHaveURL(/\/agent\/profile$/);
  await expect(page.getByTestId("profile-card")).toBeVisible();

  await page.getByRole("link", { name: "Sessions" }).click();
  await expect(page).toHaveURL(/\/agent\/sessions$/);

  // The Memory tab. This harness's conductor has no memory service configured, so what it
  // must show is the sentence saying so — not an error, and not a blank panel.
  await page.getByRole("link", { name: "Memory" }).click();
  await expect(page).toHaveURL(/\/agent\/memory$/);
  await expect(page.getByText("Memory is not configured on this host")).toBeVisible();

  // The Chat tab. It needs no configuration at all — the conductor serves it always — so
  // what must be here is the New chat button.
  await page.getByRole("link", { name: "Chat" }).click();
  await expect(page).toHaveURL(/\/agent\/chat$/);
  await expect(page.getByTestId("chat-new")).toBeVisible();

  await page.getByRole("link", { name: "Sessions" }).click();
  await expect(page).toHaveURL(/\/agent\/sessions$/);

  // And the back button works, because a tab is a navigation and not a state flag.
  await page.goBack();
  await expect(page).toHaveURL(/\/agent\/chat$/);
});

test("the agent page makes no third-party requests", async ({ page }) => {
  await authenticate(page);
  const origin = new URL(SERVER).origin;
  const foreign: string[] = [];
  page.on("request", (req) => {
    if (!req.url().startsWith(origin) && !req.url().startsWith("data:")) foreign.push(req.url());
  });

  for (const path of [
    "/agent",
    "/agent/settings",
    "/agent/profile",
    "/agent/playbooks",
    "/agent/skills",
    "/agent/sessions",
    "/agent/memory",
    "/agent/chat",
  ]) {
    await page.goto(path);
    await page.waitForLoadState("networkidle");
  }

  // Including the save, which is the one call that has anything to do with a third party:
  // the browser asks podium-server, podium-server asks the conductor, the conductor asks
  // Anthropic. The page itself never leaves this origin.
  await page.goto("/agent/settings");
  await page.getByTestId("provider-key-input-anthropic").fill(GOOD_KEY);
  await page.getByTestId("provider-key-save-anthropic").click();
  await expect(page.getByTestId("provider-key-status-anthropic")).toContainText("Saved.");

  expect(foreign, `third-party requests: ${foreign.join(", ")}`).toEqual([]);
});

test("a new chat stores the question and disables the composer", async ({ page }) => {
  seedProviderKey();
  await authenticate(page);
  await page.goto("/agent/chat");

  await page.getByTestId("chat-new").click();
  // A chat is a real URL, so this is a deep link somebody can send to a colleague.
  await expect(page).toHaveURL(/\/agent\/chat\/chat_/);
  const url = page.url();

  // One control, and only one: which model answers. A conversation runs no playbook, so
  // there is nothing here to pick one with.
  await expect(page.getByTestId("chat-run-config")).toBeVisible();
  await expect(page.getByTestId("chat-playbook")).toHaveCount(0);

  await page.getByTestId("chat-composer").fill("how many active accounts last month");
  await page.getByTestId("chat-send").click();

  // The human's own bubble appears immediately, and the composer closes: turn-based, one
  // question in flight per conversation.
  const mine = page.locator('[data-testid="chat-message"][data-role="user"]');
  await expect(mine).toHaveCount(1);
  await expect(mine).toContainText("how many active accounts last month");
  await expect(page.getByTestId("chat-composer")).toBeDisabled();

  // And it survives a reload, because the question is a row rather than a frame.
  await page.reload();
  await expect(page.locator('[data-testid="chat-message"][data-role="user"]')).toContainText(
    "how many active accounts last month",
  );
  expect(page.url()).toBe(url);
});

test("a chat turn streams progress and lands a final message", async ({ page }) => {
  test.skip(
    !DRY_RUN,
    "needs a stack whose chat playbook sets PODIUM_AGENT_DRY_RUN=1, plus a node and the " +
      "agent runtime image; set PODIUM_AGENT_DRY_RUN=1 when running against one",
  );
  test.setTimeout(240_000);
  seedProviderKey();
  await authenticate(page);
  await page.goto("/agent/chat");

  await page.getByTestId("chat-new").click();
  await expect(page).toHaveURL(/\/agent\/chat\/chat_/);

  await page.getByTestId("chat-composer").fill("hello");
  await page.getByTestId("chat-send").click();

  // A progress line appears while the turn works — the whole reason StreamChat is a
  // server-streaming RPC and the proxy does not buffer.
  await expect(page.getByTestId("chat-progress")).toBeVisible({ timeout: 60_000 });
  await expect(page.getByTestId("chat-composer")).toBeDisabled();

  // Then the answer the runtime produced, verbatim.
  const bot = page.locator('[data-testid="chat-message"][data-role="assistant"]');
  await expect(bot).toContainText("dry run: hello", { timeout: 180_000 });
  await expect(page.getByTestId("chat-progress")).toBeHidden();
  await expect(page.getByTestId("chat-composer")).toBeEnabled();

  // A reload shows the two messages and no progress line: progress is never stored.
  await page.reload();
  await expect(page.locator('[data-testid="chat-message"]')).toHaveCount(2);
  await expect(page.getByTestId("chat-progress")).toBeHidden();
});

test("chats are per login", async ({ page }) => {
  // NOT RUN, and it says so rather than pretending. The dev transport has exactly one
  // identity ("dev"), so a browser cannot present a second login on this stack: the proxy
  // derives X-Podium-Login from the authenticated identity and strips anything the client
  // sent. Two logins need the tailnet transport and two real tailnet users.
  //
  // The partition itself is proved where it is decided, without a browser:
  // internal/agent/store's TestTwoLoginsSeeDisjointChatLists, internal/agent/api's
  // TestTwoLoginsSeeDisjointChatsThroughTheService, and test/e2e's TestChatsArePerLogin,
  // which drives two logins straight at the conductor with the server's own bearer.
  test.skip(true, "the dev transport has one identity; see the comment and TestChatsArePerLogin");
  await authenticate(page);
});
