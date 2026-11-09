import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig } from "vitest/config";
import react from "@vitejs/plugin-react";

const root = path.dirname(fileURLToPath(import.meta.url));

export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: { "@": path.resolve(root, "src") },
  },
  define: { __PODIUM_VERSION__: JSON.stringify("test") },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
    include: ["src/**/*.test.{ts,tsx}"],
    // A shared CI runner is several times slower than a laptop, and a test that types a form
    // with userEvent already takes over a second here. These budgets are for that runner.
    testTimeout: 20_000,
    // Node 25 turned on its own Web Storage, so from Node 26 a worker starts with a global
    // `localStorage` that is undefined unless --localstorage-file names a file. It is an own
    // property of globalThis, and vitest 3's jsdom environment does not overwrite a global
    // that is already there — so every test that touches localStorage gets Node's
    // undefined instead of jsdom's Storage and dies on `.getItem`. Turning Node's off puts
    // jsdom's back. The flag is accepted from Node 22 on, where it is already the default.
    poolOptions: {
      forks: { execArgv: ["--no-experimental-webstorage"] },
    },
  },
});
