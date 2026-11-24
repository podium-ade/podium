#!/usr/bin/env node
// walkthrough.cjs <flow.cjs> --out <file.mp4> [--viewport 1440x900] [--cdp <url>]
//
// Records ONE walkthrough of a feature, at a person's pace, as an H.264 MP4 a reviewer can
// watch in any browser. The flow file says what to do; this runner decides how fast:
//
//   module.exports = {
//     title: "Shared chats ask for @Podium once a second person speaks",
//     url: "http://task:5173/",
//     async run(w) {
//       await w.say("A chat shared with the team, one person in it so far");
//       await w.click(w.page.getByRole("link", { name: "Launch plan" }));
//       await w.type(w.page.getByPlaceholder("Ask anything"), "Can you draft the agenda?");
//       await w.press("Enter");
//       await w.see(w.page.getByText("Here's a draft"), { timeout: 120_000 });
//     },
//   };
//
// Why a runner and not the browser tools with a screencast on: a model driving the page
// fires its clicks back to back, jumps to a field and fills it in one frame, and stops the
// recorder before the answer it was waiting for arrives. The result was four-second clips
// with no cursor, where nothing a person would notice could be followed. Here every action
// is paced the same way: a caption says what is about to happen, a cursor travels to the
// target, the click shows, text is typed a key at a time, and the result is held on screen.
//
// The page is a new tab in the browser's DEFAULT context, so it has whatever the turn already
// set up there (cookies, local storage, a signed-in session). The recording is the tab's own
// frames, captured over CDP while the flow runs and written out in real time: what the video
// shows is what happened, at the speed it happened.
//
// A step that fails still produces a video, up to the failure and held on it, and a non-zero
// exit naming the step. The failure is evidence too.
"use strict";

const { execFileSync, spawn } = require("node:child_process");
const dns = require("node:dns/promises");
const fs = require("node:fs");
const path = require("node:path");

const FPS = 30;

function usage(msg) {
  if (msg) console.error(`walkthrough: ${msg}`);
  console.error("usage: walkthrough.cjs <flow.cjs> --out <file.mp4> [--viewport WxH] [--cdp URL]");
  process.exit(2);
}

function parseArgs(argv) {
  const args = { viewport: "1440x900" };
  const rest = [];
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === "--out" || a === "--viewport" || a === "--cdp") {
      if (i + 1 >= argv.length) usage(`${a} needs a value`);
      args[a.slice(2)] = argv[++i];
    } else if (a.startsWith("--")) {
      usage(`unknown flag ${a}`);
    } else {
      rest.push(a);
    }
  }
  if (rest.length !== 1) usage("one flow file");
  if (!args.out) usage("--out is required");
  if (!args.out.endsWith(".mp4")) usage("--out must end in .mp4");
  const m = /^(\d+)x(\d+)$/.exec(args.viewport);
  if (!m) usage(`--viewport must be WIDTHxHEIGHT, not ${args.viewport}`);
  // H.264 in yuv420p needs even dimensions.
  args.width = Number(m[1]) & ~1;
  args.height = Number(m[2]) & ~1;
  args.flow = path.resolve(rest[0]);
  args.out = path.resolve(args.out);
  return args;
}

// playwright-core is installed globally in the image (no browsers: it attaches to the one the
// turn already has). A checkout's own copy is the fallback.
function loadPlaywright() {
  const tries = [];
  try {
    const root = execFileSync("npm", ["root", "-g"], { encoding: "utf8" }).trim();
    tries.push(path.join(root, "playwright-core"));
  } catch {
    // no npm on PATH; fall through to a local copy
  }
  tries.push("playwright-core");
  for (const t of tries) {
    try {
      return require(t);
    } catch {
      // try the next
    }
  }
  console.error("walkthrough: playwright-core is not installed (npm install --global playwright-core)");
  process.exit(2);
}

// Chrome answers DevTools only to a Host header that is an IP or localhost, so a sidecar's
// name is resolved first — the same thing the runtime does for the browser tools.
async function resolveCDP(explicit) {
  const candidates = explicit ? [explicit] : ["http://127.0.0.1:9222", "http://chrome:9222"];
  for (const c of candidates) {
    const url = new URL(c);
    if (url.hostname !== "localhost" && !/^[\d.]+$/.test(url.hostname)) {
      try {
        url.hostname = (await dns.lookup(url.hostname)).address;
      } catch {
        continue;
      }
    }
    try {
      const res = await fetch(new URL("/json/version", url), { signal: AbortSignal.timeout(2000) });
      if (res.ok) return url.toString().replace(/\/$/, "");
    } catch {
      // not this one
    }
  }
  throw new Error(`no browser answering DevTools at ${candidates.join(" or ")} (pass --cdp)`);
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const jitter = (lo, hi) => lo + Math.random() * (hi - lo);

// The overlay: a cursor that follows the page's own mouse events, a ripple on each press, a
// caption, and a box around what a step points at. In a closed shadow root at the top of the
// z-order and ignoring the pointer, so the page under it behaves exactly as it does without.
const OVERLAY = String.raw`(() => {
  if (window.__wt) return;
  const state = { x: -100, y: -100, caption: "", title: false };
  const host = document.createElement("div");
  host.setAttribute("data-walkthrough", "");
  host.style.cssText = "position:fixed;inset:0;pointer-events:none;z-index:2147483647;";
  const root = host.attachShadow({ mode: "closed" });
  root.innerHTML = '<style>' +
    '.c{position:fixed;left:0;top:0;width:22px;height:22px;transform:translate(-100px,-100px);will-change:transform;filter:drop-shadow(0 1px 2px rgba(0,0,0,.45))}' +
    '.r{position:fixed;width:36px;height:36px;margin:-18px 0 0 -18px;border-radius:50%;border:3px solid #ff7a1a;opacity:.9;animation:p .55s ease-out forwards}' +
    '@keyframes p{from{transform:scale(.3);opacity:.95}to{transform:scale(1.6);opacity:0}}' +
    '.cap{position:fixed;left:50%;bottom:28px;transform:translateX(-50%);max-width:min(86vw,900px);padding:10px 18px;border-radius:12px;' +
      'background:rgba(17,17,20,.86);color:#fff;font:500 17px/1.35 system-ui,-apple-system,Segoe UI,sans-serif;text-align:center;' +
      'box-shadow:0 6px 24px rgba(0,0,0,.3);transition:opacity .25s}' +
    '.cap.up{bottom:auto;top:18px}' +
    '.cap.t{bottom:auto;top:50%;transform:translate(-50%,-50%);font-size:26px;padding:18px 28px}' +
    '.cap:empty{opacity:0}' +
    '.box{position:fixed;border:3px solid #ff7a1a;border-radius:8px;box-shadow:0 0 0 4px rgba(255,122,26,.25);transition:opacity .3s}' +
    '</style>' +
    '<svg class="c" viewBox="0 0 22 22"><path d="M3 2l14 9-6.2 1.3L14 19l-2.6 1.2-3.2-6.6L3 18z" fill="#fff" stroke="#111" stroke-width="1.4" stroke-linejoin="round"/></svg>' +
    '<div class="cap"></div>';
  const cursor = root.querySelector(".c");
  const cap = root.querySelector(".cap");
  const place = () => { cursor.style.transform = "translate(" + (state.x - 3) + "px," + (state.y - 2) + "px)"; };
  const attach = () => { if (!host.isConnected) document.documentElement.appendChild(host); };
  addEventListener("mousemove", (e) => { state.x = e.clientX; state.y = e.clientY; place(); }, { capture: true, passive: true });
  addEventListener("mousedown", (e) => {
    const r = document.createElement("div");
    r.className = "r"; r.style.left = e.clientX + "px"; r.style.top = e.clientY + "px";
    root.appendChild(r); setTimeout(() => r.remove(), 600);
  }, { capture: true, passive: true });
  window.__wt = {
    set(s) {
      attach();
      Object.assign(state, s);
      cap.textContent = state.caption || "";
      cap.classList.toggle("t", !!state.title);
      cap.classList.toggle("up", !!state.up);
      place();
    },
    box(rect, ms) {
      attach();
      const b = document.createElement("div");
      b.className = "box";
      b.style.left = rect.x - 6 + "px"; b.style.top = rect.y - 6 + "px";
      b.style.width = rect.width + 12 + "px"; b.style.height = rect.height + 12 + "px";
      root.appendChild(b);
      setTimeout(() => { b.style.opacity = "0"; setTimeout(() => b.remove(), 350); }, ms);
    },
  };
  if (document.readyState === "loading") addEventListener("DOMContentLoaded", attach); else attach();
})()`;

// Recorder: the tab's screencast frames, written to ffmpeg at a steady 30 fps in real time —
// each tick repeats the latest frame as often as the clock says, so a ten-second wait for an
// answer is ten seconds in the video, as it was for the person who would have waited.
async function startRecorder(page, { width, height, out }) {
  fs.mkdirSync(path.dirname(out), { recursive: true });
  const ff = spawn(
    "ffmpeg",
    [
      "-loglevel", "error", "-y",
      "-f", "image2pipe", "-c:v", "mjpeg", "-framerate", String(FPS), "-i", "pipe:0",
      "-an",
      "-vf", `scale=${width}:${height}:force_original_aspect_ratio=decrease,pad=${width}:${height}:(ow-iw)/2:(oh-ih)/2:color=white,format=yuv420p`,
      "-c:v", "libx264", "-preset", "veryfast", "-crf", "26", "-tune", "stillimage",
      "-movflags", "+faststart",
      out,
    ],
    { stdio: ["pipe", "ignore", "pipe"] },
  );
  let ffErr = "";
  ff.stderr.on("data", (d) => (ffErr += d));
  const ffExit = new Promise((resolve) => ff.on("close", resolve));
  ff.on("error", (e) => {
    console.error(`walkthrough: ffmpeg did not start: ${e.message}`);
    process.exit(2);
  });

  const cdp = await page.context().newCDPSession(page);
  let latest = null;
  cdp.on("Page.screencastFrame", (f) => {
    latest = Buffer.from(f.data, "base64");
    cdp.send("Page.screencastFrameAck", { sessionId: f.sessionId }).catch(() => {});
  });
  await cdp.send("Page.startScreencast", { format: "jpeg", quality: 88, maxWidth: width, maxHeight: height });

  const t0 = Date.now();
  let written = 0;
  let writing = Promise.resolve();
  const tick = () => {
    if (!latest) return;
    const due = Math.floor(((Date.now() - t0) * FPS) / 1000) - written;
    const frame = latest;
    for (let i = 0; i < due; i++) {
      written++;
      writing = writing.then(
        () => new Promise((r) => (ff.stdin.write(frame) ? r() : ff.stdin.once("drain", r))),
      );
    }
  };
  const timer = setInterval(tick, 1000 / FPS);

  return {
    seconds: () => (Date.now() - t0) / 1000,
    async stop() {
      tick();
      clearInterval(timer);
      await cdp.send("Page.stopScreencast").catch(() => {});
      await writing;
      ff.stdin.end();
      const code = await ffExit;
      if (code !== 0) throw new Error(`ffmpeg exited ${code}: ${ffErr.trim()}`);
    },
  };
}

function helpers(page) {
  let cursor = { x: 40, y: 40 };
  let caption = "";

  const overlay = (s) => page.evaluate((s) => window.__wt && window.__wt.set(s), s).catch(() => {});
  // A navigation throws the overlay away with the old document. The init script puts it back;
  // this restores where the cursor was and what the caption said.
  page.on("load", () => overlay({ ...cursor, caption, title: false }));

  // The first VISIBLE match: a responsive page carries its other layout hidden beside the one
  // on screen (a phone's <select> of the same chats, a collapsed menu), and the first match in
  // the document is often that one. Pass a locator without .first(), so this choice is made
  // among the ones a person could actually see.
  const resolve = (target) =>
    (typeof target === "string" ? page.locator(target) : target).filter({ visible: true }).first();

  async function glide(x, y) {
    const from = cursor;
    const dist = Math.hypot(x - from.x, y - from.y);
    const ms = Math.min(1100, 350 + dist * 0.7);
    const steps = Math.max(8, Math.round(ms / 16));
    for (let i = 1; i <= steps; i++) {
      const t = i / steps;
      const e = t < 0.5 ? 4 * t * t * t : 1 - Math.pow(-2 * t + 2, 3) / 2; // ease in-out
      await page.mouse.move(from.x + (x - from.x) * e, from.y + (y - from.y) * e);
      await sleep(ms / steps);
    }
    cursor = { x, y };
  }

  // Waits for the target BEFORE the cursor moves, so a wait on screen is the page loading,
  // never a cursor parked on something it cannot press. `trial` runs every check a click
  // would (visible, stable, enabled, nothing on top of it) without clicking.
  async function pointAt(target, opts = {}) {
    const loc = resolve(target);
    await loc.waitFor({ state: "visible", timeout: opts.timeout ?? 15_000 });
    if (opts.actionable) await loc.click({ trial: true, timeout: opts.timeout ?? 15_000 });
    await loc.scrollIntoViewIfNeeded();
    const box = await loc.boundingBox();
    if (!box) throw new Error("the element has no box on screen");
    const x = box.x + Math.min(box.width / 2, 24 + box.width * 0.2);
    const y = box.y + box.height / 2;
    // The caption gets out of the way of what the step is about: a composer, a footer, a
    // bottom sheet would otherwise be under it for the whole step.
    const vh = page.viewportSize()?.height ?? 900;
    await overlay({ up: box.y + box.height > vh * 0.6 });
    await glide(x, y);
    return { loc, box };
  }

  const w = {
    page,

    /** Caption what is about to happen, and give a person time to read it. */
    async say(text) {
      caption = text;
      await overlay({ caption, title: false });
      await sleep(Math.min(3500, 900 + text.length * 35));
    },

    /** Move to it, pause, click — the way a hand does. */
    async click(target, opts = {}) {
      const { loc } = await pointAt(target, { ...opts, actionable: true });
      await sleep(jitter(220, 380));
      await loc.click({ timeout: 5_000 });
      await sleep(opts.settle ?? 800);
    },

    /** Click into a field and type, a key at a time. */
    async type(target, text, opts = {}) {
      await w.click(target, { ...opts, settle: 300 });
      for (const ch of text) {
        await page.keyboard.type(ch);
        await sleep(ch === " " ? jitter(70, 140) : jitter(40, 105));
      }
      await sleep(opts.settle ?? 600);
    },

    /** A key or chord, e.g. "Enter", "Escape", "Control+K". */
    async press(key, opts = {}) {
      await sleep(jitter(250, 400));
      await page.keyboard.press(key);
      await sleep(opts.settle ?? 900);
    },

    /** Hover without clicking — a tooltip, a menu that opens on hover. */
    async hover(target, opts = {}) {
      await pointAt(target, opts);
      await sleep(opts.settle ?? 1200);
    },

    /** Scroll the page (or the element under the cursor) by dy pixels, smoothly. */
    async scroll(dy, opts = {}) {
      const step = Math.sign(dy) * 60;
      for (let done = 0; Math.abs(done) < Math.abs(dy); done += step) {
        await page.mouse.wheel(0, step);
        await sleep(28);
      }
      await sleep(opts.settle ?? 700);
    },

    /**
     * Wait for the result — as long as it really takes, which the video keeps — then point at
     * it, frame it, and hold it on screen long enough to be read.
     */
    async see(target, opts = {}) {
      const { box } = await pointAt(target, { timeout: opts.timeout ?? 30_000 });
      await page.evaluate(([b, ms]) => window.__wt && window.__wt.box(b, ms), [box, opts.hold ?? 2200]).catch(() => {});
      await sleep(opts.hold ?? 2200);
    },

    /** Go to a URL the way a person would land on it: then let it settle. */
    async goto(url, opts = {}) {
      await page.goto(url, { waitUntil: "load", timeout: opts.timeout ?? 30_000 });
      await sleep(opts.settle ?? 1200);
    },

    /** Just wait, on screen. */
    hold: (ms) => sleep(ms),

    caption: () => caption,

    async _title(text) {
      await overlay({ caption: text, title: true });
      await sleep(2600);
      caption = "";
      await overlay({ caption: "", title: false });
    },
  };
  return w;
}

async function main() {
  const args = parseArgs(process.argv.slice(2));
  const flow = require(args.flow);
  if (typeof flow.run !== "function") usage(`${args.flow} must export run(w)`);
  if (!flow.url) usage(`${args.flow} must export the url to start from`);

  const { chromium } = loadPlaywright();
  const cdpURL = await resolveCDP(args.cdp);
  const browser = await chromium.connectOverCDP(cdpURL);
  const context = browser.contexts()[0] ?? (await browser.newContext());
  const page = await context.newPage();
  await page.setViewportSize({ width: args.width, height: args.height });
  await page.addInitScript(OVERLAY);
  await page.goto(flow.url, { waitUntil: "load", timeout: 30_000 });
  await page.evaluate(OVERLAY);
  await sleep(800);

  const w = helpers(page);
  const rec = await startRecorder(page, args);
  let failure = null;
  try {
    await w._title(flow.title || path.basename(args.flow, ".cjs"));
    await flow.run(w);
    await sleep(2500); // the end state, held
  } catch (err) {
    failure = err;
    const step = w.caption() ? `${w.caption()} — ` : "";
    await page
      .evaluate((m) => window.__wt && window.__wt.set({ caption: m, title: false }), `✖ ${step}${String(err.message).split("\n")[0]}`)
      .catch(() => {});
    await sleep(3000);
  }
  await rec.stop();
  const secs = rec.seconds();
  await page.close().catch(() => {});
  // Disconnect only: the browser and its default context are the turn's, not ours.
  await browser.close().catch(() => {});

  // The still the pull request shows, linked to the video: GitHub plays no video from outside
  // GitHub inline, so this is what a reviewer sees first. Its last frame — where the feature
  // ended up — written beside it under the same name.
  const poster = args.out.replace(/\.mp4$/, ".png");
  await new Promise((resolve) =>
    spawn("ffmpeg", ["-loglevel", "error", "-y", "-sseof", "-0.5", "-i", args.out, "-frames:v", "1", "-update", "1", poster], {
      stdio: "ignore",
    }).on("close", resolve),
  );

  const size = fs.statSync(args.out).size;
  console.log(`walkthrough: ${args.out} — ${secs.toFixed(1)}s, ${(size / 1e6).toFixed(1)} MB`);
  if (fs.existsSync(poster)) console.log(`walkthrough: ${poster} — its last frame, the still to link it from`);
  if (secs > 150) console.log("walkthrough: longer than 2½ minutes; cut it to the feature itself");
  if (size > 10 * 1024 * 1024) console.log("walkthrough: over publish.sh's 10 MB cap; shorten it");
  if (failure) {
    console.error(`walkthrough: a step failed — the video ends on it:\n${failure.stack || failure}`);
    process.exit(1);
  }
}

main().catch((err) => {
  console.error(`walkthrough: ${err.stack || err}`);
  process.exit(1);
});
