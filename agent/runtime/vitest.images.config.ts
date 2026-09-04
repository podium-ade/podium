import { defineConfig } from "vitest/config";

// The image tests drive a real container, so they are a separate project from the unit
// tests: `pnpm test` must stay a two-second command that needs no Docker.
export default defineConfig({
  test: {
    environment: "node",
    include: ["images/**/*.test.ts"],
    testTimeout: 300_000,
    hookTimeout: 300_000,
  },
});
