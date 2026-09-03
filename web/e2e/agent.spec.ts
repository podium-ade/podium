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

test("the agent settings page validates and stores a provider key", async ({ page }) => {
  await authenticate(page);
  await page.goto("/agent/settings");

  // The Agent tab exists at all only because WhoAmI said the server proxies a conductor.
  await expect(page.getByRole("link", { name: "Agent" })).toBeVisible();
  await expect(page.getByText("Not set")).toBeVisible();
  await expect(page.getByText(/encrypted at rest by podium-server/i)).toBeVisible();
  await expect(page.getByTestId("provider-key-save")).toBeDisabled();

  // A key the provider refuses is refused here, and nothing is stored.
  await page.getByTestId("provider-key-input").fill(BAD_KEY);
  await page.getByTestId("provider-key-save").click();
  await expect(page.getByTestId("provider-key-status")).toContainText(
    "Anthropic rejected this key",
  );
  await expect(page.getByText("Not set")).toBeVisible();
  expect(podium("secret", "ls")).not.toContain(KEY_SECRET);

  // A key it accepts is validated against the real endpoint shape, then stored.
  await page.getByTestId("provider-key-input").fill(GOOD_KEY);
  await page.getByTestId("provider-key-save").click();
  await expect(page.getByTestId("provider-key-status")).toContainText("Saved.");
  await expect(page.getByTestId("provider-key-status")).toContainText("••••good");
  await expect(page.getByText("claude-opus-5")).toBeVisible();
  await expect(page.getByText("Connected")).toBeVisible();
  await expect(page.getByTestId("provider-key-meta")).toContainText("••••good");

  // It is a Podium secret now, and only its metadata is visible anywhere.
  const listed = podium("secret", "ls");
  expect(listed).toContain(KEY_SECRET);
  expect(listed).not.toContain(GOOD_KEY);

  // A reload reads it back out of the settings row, not out of the browser.
  await page.reload();
  await expect(page.getByText("Connected")).toBeVisible();
  await expect(page.getByTestId("provider-key-meta")).toContainText("••••good");
  await expect(page.getByTestId("provider-key-input")).toHaveAttribute(
    "placeholder",
    "Paste a new key to replace ••••good",
  );

  // Removing it takes both the secret and the metadata.
  await page.getByTestId("provider-key-remove").click();
  await expect(page.getByText(/Agents will fail until a key is set again/)).toBeVisible();
  await page.getByRole("button", { name: "Confirm" }).click();
  await expect(page.getByText("Not set")).toBeVisible();
  await expect.poll(() => podium("secret", "ls")).not.toContain(KEY_SECRET);
});

test("the agent tabs are real routes", async ({ page }) => {
  await authenticate(page);

  // /agent lands on Settings.
  await page.goto("/agent");
  await expect(page).toHaveURL(/\/agent\/settings$/);
  await expect(page.getByRole("heading", { name: "Anthropic" })).toBeVisible();

  await page.getByRole("link", { name: "Sessions" }).click();
  await expect(page).toHaveURL(/\/agent\/sessions$/);

  // The Memory tab. This harness's conductor has no memory service configured, so what it
  // must show is the sentence saying so — not an error, and not a blank panel.
  await page.getByRole("link", { name: "Memory" }).click();
  await expect(page).toHaveURL(/\/agent\/memory$/);
  await expect(page.getByText("Memory is not configured on this host")).toBeVisible();

  await page.getByRole("link", { name: "Sessions" }).click();
  await expect(page).toHaveURL(/\/agent\/sessions$/);

  // And the back button works, because a tab is a navigation and not a state flag.
  await page.goBack();
  await expect(page).toHaveURL(/\/agent\/memory$/);
});

test("the agent page makes no third-party requests", async ({ page }) => {
  await authenticate(page);
  const origin = new URL(SERVER).origin;
  const foreign: string[] = [];
  page.on("request", (req) => {
    if (!req.url().startsWith(origin) && !req.url().startsWith("data:")) foreign.push(req.url());
  });

  for (const path of ["/agent", "/agent/settings", "/agent/sessions", "/agent/memory"]) {
    await page.goto(path);
    await page.waitForLoadState("networkidle");
  }

  // Including the save, which is the one call that has anything to do with a third party:
  // the browser asks podium-server, podium-server asks the conductor, the conductor asks
  // Anthropic. The page itself never leaves this origin.
  await page.goto("/agent/settings");
  await page.getByTestId("provider-key-input").fill(GOOD_KEY);
  await page.getByTestId("provider-key-save").click();
  await expect(page.getByTestId("provider-key-status")).toContainText("Saved.");

  expect(foreign, `third-party requests: ${foreign.join(", ")}`).toEqual([]);
});
