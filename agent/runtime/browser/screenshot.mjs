// screenshot URL OUT.png [--width N] [--height N] [--full-page]
//
// It exists so an agent can verify a UI change by looking at it without writing Playwright
// code every turn: Chromium headless, one navigation, one PNG, exit 0. Anything Playwright
// complains about goes to stderr and exits 1 — there is no partial success worth reporting.
//
// It sits beside its OWN node_modules (playwright-core, pinned to the same version as the
// base image's browsers) rather than in the runtime's, because only this image has a
// browser to drive and neither of the other two images should carry the driver.
//
// The argument parsing is ../dist/screenshot.js, in the runtime's typechecked source.

import { mkdir } from "node:fs/promises";
import { dirname, resolve } from "node:path";

import { chromium } from "playwright-core";

import { NavigationTimeoutMs, parseArgs } from "../dist/screenshot.js";

async function shoot(opts) {
  const out = resolve(opts.out);
  await mkdir(dirname(out), { recursive: true });
  // --disable-dev-shm-usage: Docker gives a container 64 MB of /dev/shm and a Podium task
  // spec has no knob for it, so Chromium is told to put its shared buffers in /tmp — where
  // there is real space — instead of crashing the renderer on a heavy page. An agent that
  // writes its own Playwright must pass the same flag; the coder prompt says so.
  const browser = await chromium.launch({ args: ["--disable-dev-shm-usage"] });
  try {
    const page = await browser.newPage({ viewport: { width: opts.width, height: opts.height } });
    page.setDefaultNavigationTimeout(NavigationTimeoutMs);
    await page.goto(opts.url, { waitUntil: "networkidle" });
    await page.screenshot({ path: out, fullPage: opts.fullPage });
    process.stdout.write(`${out}\n`);
  } finally {
    await browser.close();
  }
}

try {
  await shoot(parseArgs(process.argv.slice(2)));
} catch (err) {
  process.stderr.write(`screenshot: ${err instanceof Error ? err.message : String(err)}\n`);
  process.exit(1);
}
