import { execFileSync } from "node:child_process";
import { expect, test, type Page } from "@playwright/test";

const SERVER = process.env.PODIUM_UI_URL ?? "http://127.0.0.1:8080";
const TOKEN = process.env.PODIUM_DEV_TOKEN ?? "devtoken";
const CLI = process.env.PODIUM_CLI ?? "../bin/podium";

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

test("a running task appears, streams its log live, and the node is online", async ({ page }) => {
  await authenticate(page);

  const out = podium(
    "run",
    "--detach",
    "--image",
    "alpine:3",
    "--",
    "sh",
    "-c",
    "for i in 1 2 3 4 5 6 7 8 9 10; do echo tick $i; sleep 1; done",
  );
  const taskId = out.match(/task_[0-9a-z]+/)?.[0];
  expect(taskId, `no task id in CLI output: ${out}`).toBeTruthy();

  await page.goto("/");
  await expect(page.getByRole("link", { name: taskId! })).toBeVisible();

  await page.getByRole("link", { name: taskId! }).click();
  await expect(page).toHaveURL(new RegExp(`/tasks/${taskId}$`));

  // Live, not after the fact: the assertion lands while the container is still counting.
  await expect(page.getByTestId("log-line").first()).toContainText("tick 1");
  await expect(page.getByTestId("log-line").nth(1)).toContainText("tick 2");
  await expect(page.getByText("running")).toBeVisible();

  await page.getByLabel("Search logs").fill("tick 3");
  await expect(page.getByTestId("log-line")).toHaveCount(1);
});

test("the nodes screen shows an online node and mints an enrollment token", async ({ page }) => {
  await authenticate(page);
  await page.goto("/nodes");

  await expect(page.getByRole("cell", { name: "online" })).toBeVisible();

  await page.getByLabel("Labels").fill("smoke");
  await page.getByRole("button", { name: "Create enrollment token" }).click();

  const command = page.getByTestId("enroll-command");
  await expect(command).toBeVisible();
  await expect(command).toContainText("PODIUM_NODE_ENROLL_TOKEN=");
  await expect(command).toContainText("podium-node");
});
