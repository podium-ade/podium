import js from "@eslint/js";
import globals from "globals";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";
import reactRefresh from "eslint-plugin-react-refresh";

export default tseslint.config(
  { ignores: ["dist", "src/gen", "node_modules", "playwright-report", "test-results"] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  reactHooks.configs.flat["recommended-latest"],
  {
    files: ["**/*.{ts,tsx}"],
    languageOptions: {
      ecmaVersion: 2022,
      globals: { ...globals.browser, __PODIUM_VERSION__: "readonly" },
    },
    plugins: { "react-refresh": reactRefresh },
    rules: {
      "react-refresh/only-export-components": ["warn", { allowConstantExport: true }],
      "@typescript-eslint/no-unused-vars": ["error", { argsIgnorePattern: "^_" }],
    },
  },
  {
    files: [
      "vite.config.ts",
      "vitest.config.ts",
      "playwright.config.ts",
      "e2e/**/*.ts",
      // The e2e fixtures are plain Node scripts, started beside the dev stack.
      "e2e/**/*.mjs",
    ],
    languageOptions: { globals: { ...globals.node } },
  },
);
