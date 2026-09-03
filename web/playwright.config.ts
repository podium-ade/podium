import { defineConfig } from "@playwright/test";

/**
 * The smoke suite runs against a live dev stack (postgres + podium-server + podium-node) and the
 * real CLI; it starts nothing itself. The stack recipe is in the hand-off notes of
 * docs/node-setup.md; point it at that stack with
 *
 *   PODIUM_UI_URL=http://127.0.0.1:18080 PODIUM_DEV_TOKEN=devtoken PODIUM_CLI=../bin/podium \
 *     pnpm e2e
 *
 * agent.spec.ts needs two more things in that stack, and neither can be started by the spec
 * (podium-agent reads the base URL once, at startup):
 *
 *   FAKE_ANTHROPIC_PORT=18999 node e2e/fixtures/fake-anthropic.mjs &
 *   PODIUM_AGENT_ANTHROPIC_BASE_URL=http://127.0.0.1:18999 ./bin/podium-agent &   # plus its own env
 *
 * and podium-server started with PODIUM_AGENT_URL=http://127.0.0.1:8090 and a matching
 * PODIUM_AGENT_TOKEN, which is what mounts the proxy the settings page calls through.
 * FAKE_ANTHROPIC_KEY overrides the one key the fake accepts (default `sk-ant-test-good`);
 * both processes must agree on it.
 */
export default defineConfig({
  testDir: "./e2e",
  timeout: 120_000,
  expect: { timeout: 30_000 },
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [["list"]],
  use: {
    baseURL: process.env.PODIUM_UI_URL ?? "http://127.0.0.1:8080",
    trace: "retain-on-failure",
  },
});
