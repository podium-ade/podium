import { mkdirSync, writeFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { defineConfig, type Plugin } from "vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";

const root = path.dirname(fileURLToPath(import.meta.url));

// Vite empties dist/ before a build, which would take the committed placeholder with it.
// //go:embed of an empty directory is a compile error, so dist/.gitkeep has to survive every
// build as well as every clean checkout.
function keepDist(): Plugin {
  return {
    name: "podium-keep-dist",
    apply: "build",
    closeBundle() {
      mkdirSync("dist", { recursive: true });
      writeFileSync("dist/.gitkeep", "");
    },
  };
}

// The dev server proxies Connect calls to podium-server and injects the dev bearer token, so
// `pnpm dev` never asks for it. The embedded production build has no proxy and no token, which
// is why the UI prompts for one (see src/lib/auth.ts).
const target = process.env.PODIUM_SERVER ?? "http://127.0.0.1:8080";
const devToken = process.env.PODIUM_LOCAL_TOKEN ?? "";

export default defineConfig({
  plugins: [react(), tailwindcss(), keepDist()],
  resolve: {
    alias: { "@": path.resolve(root, "src") },
  },
  define: {
    __PODIUM_VERSION__: JSON.stringify(process.env.PODIUM_VERSION ?? "dev"),
  },
  build: {
    target: "es2022",
    sourcemap: false,
    reportCompressedSize: true,
  },
  server: {
    // Loopback IPv4, like everything else in the dev stack: vite's default `localhost` binds
    // ::1 first on macOS, which is not where podium-server is.
    host: "127.0.0.1",
    port: 5173,
    proxy: {
      "^/podium\\.v1\\.": {
        target,
        changeOrigin: false,
        configure: (proxy) => {
          proxy.on("proxyReq", (proxyReq) => {
            if (devToken) proxyReq.setHeader("authorization", `Bearer ${devToken}`);
          });
        },
      },
    },
  },
});
