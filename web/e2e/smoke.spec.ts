import { execFileSync } from "node:child_process";
import { readFile } from "node:fs/promises";
import { expect, test, type Page } from "@playwright/test";

const SERVER = process.env.PODIUM_UI_URL ?? "http://127.0.0.1:8080";
const TOKEN = process.env.PODIUM_LOCAL_TOKEN ?? "devtoken";
const CLI = process.env.PODIUM_CLI ?? "../bin/podium";

function podium(...args: string[]): string {
  return execFileSync(CLI, ["--server", SERVER, "--token", TOKEN, ...args], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  }).trim();
}

/** The local transport has no session; seed the token the UI would otherwise prompt for. */
async function authenticate(page: Page) {
  await page.addInitScript(
    ([token]) => window.localStorage.setItem("podium.localToken", token),
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

test("the tasks list searches by image and by id prefix", async ({ page }) => {
  await authenticate(page);
  const taskId = podium("run", "--detach", "--image", "alpine:3", "--", "true").match(
    /task_[0-9a-z]+/,
  )![0];

  await page.goto("/");
  await expect(page.getByRole("link", { name: taskId })).toBeVisible();

  // Server-side: the whole set is searched, not the page already on screen.
  await page.getByLabel("Search tasks").fill(taskId);
  await expect(page.getByRole("link", { name: taskId })).toBeVisible();
  await expect(page.getByRole("link", { name: /^task_/ })).toHaveCount(1);

  await page.getByLabel("Search tasks").fill("alpine");
  await expect(page.getByRole("link", { name: taskId })).toBeVisible();

  await page.getByLabel("Search tasks").fill("no-such-image");
  await expect(page.getByText(/Nothing matches/)).toBeVisible();
});

test("a task can be submitted from the UI and watched to completion", async ({ page }) => {
  await authenticate(page);
  await page.goto("/");
  await page.getByRole("link", { name: "New task" }).click();
  await expect(page).toHaveURL(/\/submit$/);

  await page.getByLabel("Image").fill("alpine:3");
  await page.getByLabel("Command").fill("sh\n-c\necho submitted-from-the-ui; exit 0");
  await page.getByLabel("Environment").fill("GREETING=hello");
  await page.getByRole("button", { name: "Submit task" }).click();

  await expect(page).toHaveURL(/\/tasks\/task_[0-9a-z]+$/);
  await expect(page.getByTestId("log-line").first()).toContainText("submitted-from-the-ui", {
    timeout: 60_000,
  });
  await expect(page.getByText("succeeded")).toBeVisible({ timeout: 60_000 });
});

test("the submit form lists every validation problem the server reports", async ({ page }) => {
  await authenticate(page);
  await page.goto("/submit");

  // Client-side: an unknown field is rejected here rather than silently dropped.
  await page.getByRole("button", { name: "YAML spec" }).click();
  await page
    .getByLabel("Task spec YAML")
    .fill("image: alpine:3\nprivileged: true\ntimeout: whenever\n");
  await page.getByRole("button", { name: "Submit task" }).click();
  await expect(page.getByTestId("spec-problems")).toContainText("privileged");
  await expect(page.getByTestId("spec-problems")).toContainText("timeout");

  // Server-side: pkg/spec.Validate returns every problem at once and all of them are shown.
  await page
    .getByLabel("Task spec YAML")
    .fill("image: alpine:3\nmax_attempts: -1\nenv:\n  MY-VAR: x\n");
  await page.getByRole("button", { name: "Submit task" }).click();
  const problems = page.getByTestId("spec-problems").getByRole("listitem");
  await expect(problems).toHaveCount(2);
  await expect(problems.nth(0)).toContainText("max_attempts");
  await expect(problems.nth(1)).toContainText("MY-VAR");
});

test("re-run pre-fills the form from a finished task's spec", async ({ page }) => {
  await authenticate(page);
  const taskId = podium(
    "run",
    "--detach",
    "--image",
    "alpine:3",
    "--label",
    "demo",
    "--",
    "echo",
    "original",
  ).match(/task_[0-9a-z]+/)![0];

  await page.goto(`/tasks/${taskId}`);
  await expect(page.getByRole("link", { name: "Re-run" })).toBeVisible({ timeout: 60_000 });
  await page.getByRole("link", { name: "Re-run" }).click();

  await expect(page).toHaveURL(new RegExp(`/submit\\?rerun=${taskId}$`));
  await expect(page.getByLabel("Image")).toHaveValue("alpine:3");
  await expect(page.getByLabel("Command")).toHaveValue("echo\noriginal");
  await expect(page.getByLabel("Labels")).toHaveValue("demo");
});

test("a sidecar's logs can be filtered out on their own", async ({ page }) => {
  await authenticate(page);
  const spec = `image: alpine:3
command: ["sh", "-c", "echo task-said-hello; sleep 20"]
sidecars:
  db:
    image: redis:7-alpine
    readiness:
      tcp_port: 6379
`;
  const path = `${process.env.TMPDIR ?? "/tmp"}/podium-e2e-sidecar.yaml`;
  execFileSync("sh", ["-c", `cat > ${path} <<'EOF'\n${spec}EOF`]);
  const taskId = podium("run", "--detach", "--spec", path).match(/task_[0-9a-z]+/)![0];

  await page.goto(`/tasks/${taskId}`);
  await expect(page.getByLabel("sidecar db")).toBeVisible({ timeout: 120_000 });
  await expect(page.getByTestId("log-line").filter({ hasText: "[db]" }).first()).toBeVisible();
  await expect(
    page.getByTestId("log-line").filter({ hasText: "task-said-hello" }),
  ).toBeVisible();

  await page.getByLabel("sidecar db").uncheck();
  await expect(page.getByTestId("log-line").filter({ hasText: "[db]" })).toHaveCount(0);
  await expect(
    page.getByTestId("log-line").filter({ hasText: "task-said-hello" }),
  ).toBeVisible();

  await page.getByLabel("sidecar db").check();
  await expect(page.getByTestId("log-line").filter({ hasText: "[db]" }).first()).toBeVisible();

  podium("task", "cancel", taskId);
});

test("an artifact is listed and downloads through the server", async ({ page }) => {
  await authenticate(page);
  const taskId = podium(
    "run",
    "--detach",
    "--image",
    "alpine:3",
    "--",
    "sh",
    "-c",
    // The echo to stdout matters: the roll-up only produces a log artifact for a task that
    // actually wrote some output, and this task's real output all goes into the file.
    "mkdir -p /workspace/.podium/artifacts && echo artifact-body > /workspace/.podium/artifacts/report.txt && echo wrote-the-report",
  ).match(/task_[0-9a-z]+/)![0];

  await page.goto(`/tasks/${taskId}`);
  const fileRow = page.getByTestId("artifact-row").filter({ hasText: "report.txt" });
  await expect(fileRow).toBeVisible({ timeout: 120_000 });
  await expect(fileRow).toHaveAttribute("data-kind", "file");
  await expect(fileRow).toContainText("14 B");

  const download = page.waitForEvent("download");
  await fileRow.getByRole("button", { name: "Download" }).click();
  const file = await download;
  expect(file.suggestedFilename()).toBe("report.txt");
  expect((await readFile(await file.path(), "utf8")).trim()).toBe("artifact-body");

  // The rolled-up log turns up once the roll-up sweep has run, and is kept apart from files.
  await expect(page.getByText(/Archived logs/)).toBeVisible({ timeout: 120_000 });
  await expect(page.getByTestId("artifact-row").filter({ hasText: "stdout" })).toHaveAttribute(
    "data-kind",
    "log",
  );
});

test("the nodes screen shows an online node and mints an enrollment token", async ({ page }) => {
  await authenticate(page);
  await page.goto("/nodes");

  await expect(page.getByRole("cell", { name: "online", exact: false }).first()).toBeVisible();

  await page.getByLabel("Labels").fill("smoke");
  await page.getByRole("button", { name: "Create enrollment token" }).click();

  const command = page.getByTestId("enroll-command");
  await expect(command).toBeVisible();
  await expect(command).toContainText("PODIUM_NODE_ENROLL_TOKEN=");
  await expect(command).toContainText("podium-node");
});

test("a node can be drained and undrained, and delete is refused while it is online", async ({
  page,
}) => {
  await authenticate(page);
  await page.goto("/nodes");
  const row = page.getByTestId("node-row").first();

  // Delete refuses an online, undrained node and says so beside it.
  await row.getByRole("button", { name: "Delete" }).click();
  await row.getByRole("button", { name: "Yes, delete" }).click();
  await expect(row).toContainText(/drain/i);

  await row.getByRole("button", { name: "Drain" }).click();
  await row.getByRole("button", { name: "Yes, drain" }).click();
  await expect(row).toHaveAttribute("data-draining", "true");
  await expect(row).toContainText("takes no new work");

  await row.getByRole("button", { name: "Undrain" }).click();
  await row.getByRole("button", { name: "Yes, undrain" }).click();
  await expect(row).toHaveAttribute("data-draining", "false");
});

test("a secret can be set and deleted, and its value is never returned", async ({ page }) => {
  await authenticate(page);

  const bodies: string[] = [];
  page.on("response", async (res) => {
    if (!res.url().includes("SecretService")) return;
    bodies.push(await res.text().catch(() => ""));
  });

  await page.goto("/secrets");
  await expect(page.getByText(/value cannot be viewed after it is saved/i)).toBeVisible();

  await page.getByLabel("Secret name").fill("E2E_SECRET");
  await page.getByLabel("Secret value").fill("swordfish-do-not-echo");
  await page.getByRole("button", { name: "Save secret" }).click();

  const secretCell = page.getByRole("cell", { name: "E2E_SECRET", exact: true });
  await expect(secretCell).toBeVisible();
  await expect(page.getByLabel("Secret value")).toHaveValue("");

  // Nothing on the page and nothing in any SecretService response carries the value.
  expect(await page.content()).not.toContain("swordfish-do-not-echo");
  const echoed = bodies.filter((b) => b.includes("swordfish-do-not-echo"));
  expect(echoed, "a SecretService response echoed the value back").toEqual([]);

  await page.getByRole("button", { name: "Delete E2E_SECRET" }).click();
  await page.getByRole("button", { name: "Yes, delete" }).click();
  await expect(secretCell).toBeHidden();
});

test("a queued task that no node can run says why", async ({ page }) => {
  await authenticate(page);
  const taskId = podium(
    "run",
    "--detach",
    "--image",
    "alpine:3",
    "--label",
    "no-such-label",
    "--",
    "true",
  ).match(/task_[0-9a-z]+/)![0];

  await page.goto(`/tasks/${taskId}`);
  await expect(page.getByTestId("queued-reason")).toContainText(
    "no online node carries every label this task requires",
    { timeout: 60_000 },
  );
  await expect(page.getByTestId("queued-reason")).toContainText("no-such-label");

  podium("task", "cancel", taskId);
});

test("the page makes no third-party requests", async ({ page }) => {
  await authenticate(page);
  const origin = new URL(SERVER).origin;
  const foreign: string[] = [];
  page.on("request", (req) => {
    if (!req.url().startsWith(origin) && !req.url().startsWith("data:")) foreign.push(req.url());
  });

  for (const path of ["/", "/nodes", "/secrets", "/submit"]) {
    await page.goto(path);
    await page.waitForLoadState("networkidle");
  }
  expect(foreign, `third-party requests: ${foreign.join(", ")}`).toEqual([]);
});
