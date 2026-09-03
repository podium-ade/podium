import { defineConfig } from "@playwright/test";

/**
 * The smoke suite runs against a live dev stack (postgres + podium-server + podium-node) and the
 * real CLI; it starts nothing itself. The stack recipe is in the hand-off notes of
 * docs/node-setup.md; point it at that stack with
 *
 *   PODIUM_UI_URL=http://127.0.0.1:18080 PODIUM_DEV_TOKEN=devtoken PODIUM_CLI=../bin/podium \
 *     pnpm e2e
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
