import { execFileSync } from "node:child_process";

import { describe, expect, it } from "vitest";

/** DesktopImage is what `make agent-runtime-desktop` tags the desktop runtime as. */
const DesktopImage = process.env["PODIUM_DESKTOP_IMAGE"] ?? "podium-agent-runtime-desktop:dev";

/** VoxlVersion is the voxl release Dockerfile.desktop builds; its source ships in the image. */
const VoxlVersion = "0.3.3";

type Run = { status: number; stdout: string; stderr: string };

/** docker runs one docker command and never throws: the exit code is the assertion. */
function docker(args: string[]): Run {
  try {
    const stdout = execFileSync("docker", args, {
      encoding: "utf8",
      stdio: ["pipe", "pipe", "pipe"],
      maxBuffer: 32 * 1024 * 1024,
    });
    return { status: 0, stdout, stderr: "" };
  } catch (err) {
    const e = err as { status?: number; stdout?: string; stderr?: string; message?: string };
    return {
      status: e.status ?? -1,
      stdout: e.stdout ?? "",
      stderr: e.stderr ?? e.message ?? "",
    };
  }
}

/**
 * available reports whether this machine can run the test at all. `make
 * agent-runtime-desktop` is what builds the image, and it compiles voxl: a test that built it
 * itself would make `pnpm test:images` a long command on every run.
 */
function available(): string | null {
  if (docker(["version", "--format", "{{.Server.Version}}"]).status !== 0) {
    return "no docker engine is reachable";
  }
  if (docker(["image", "inspect", DesktopImage]).status !== 0) {
    return `${DesktopImage} is not on this engine; run \`make agent-runtime && make agent-runtime-desktop\` first`;
  }
  return null;
}

const unavailable = available();

/**
 * inContainer runs a shell script inside the desktop image as its own unprivileged user,
 * the way a turn runs: no extra privileges, and /dev/shm at Docker's default size.
 */
function inContainer(script: string, env: Record<string, string> = {}): Run {
  const envArgs = Object.entries(env).flatMap(([k, v]) => ["-e", `${k}=${v}`]);
  return docker(["run", "--rm", ...envArgs, "--entrypoint", "sh", DesktopImage, "-c", script]);
}

function show(run: Run): string {
  return `stdout:\n${run.stdout}\nstderr:\n${run.stderr}`;
}

describe.skipIf(unavailable !== null)(`${DesktopImage}`, () => {
  it("brings the desktop up, then runs the command in its place", () => {
    // The command checks both ends the wrapper waited for — the browser the agent drives and
    // the stream the person watches — and that the desktop is whole, then exits 7: the
    // wrapper exec'd it, so 7 is what the container must exit with.
    const run = inContainer(`
      podium-desktop -- sh -c '
        echo "DISPLAY: $DISPLAY"
        echo "VOXL: $(curl -fsS http://127.0.0.1:6901/healthz)"
        curl -fsS http://127.0.0.1:9222/json/version | grep -q "\\"Browser\\"" && echo "CDP: up"
        for p in Xvfb xfce4-session xfwm4 xfce4-panel xfdesktop xfsettingsd chromium voxl; do
          pgrep -x "$p" >/dev/null && echo "RUNNING: $p"
        done
        curl -fsS -o /tmp/shot.png http://127.0.0.1:6901/screenshot
        echo "SHOT: $(od -An -tx1 -N8 /tmp/shot.png | tr -d " \\n")"
        exit 7
      '
    `);
    // eslint-disable-next-line no-console
    console.log(`${DesktopImage}\n${run.stdout}${run.stderr}`);

    expect(run.status, show(run)).toBe(7);
    expect(run.stdout).toContain("DISPLAY: :1");
    expect(run.stdout).toContain("VOXL: ok");
    expect(run.stdout).toContain("CDP: up");
    for (const p of ["Xvfb", "xfce4-session", "xfwm4", "xfce4-panel", "xfdesktop", "xfsettingsd", "chromium", "voxl"]) {
      expect(run.stdout).toContain(`RUNNING: ${p}`);
    }
    // A real frame from voxl's capture of the display: the PNG signature.
    expect(run.stdout).toContain("SHOT: 89504e470d0a1a0a");
    // xfce-setup.sh ran clean: a failure there is only a warning, so assert it is absent.
    expect(run.stderr).not.toContain("XFCE kept its defaults");
  });

  it("starts at the largest size, which voxl may only shrink", () => {
    const run = inContainer(`
      podium-desktop -- sh -c '
        curl -fsS -o /tmp/shot.png http://127.0.0.1:6901/screenshot
        od -An -tu1 -j16 -N8 /tmp/shot.png
      '
    `);
    expect(run.status, show(run)).toBe(0);
    // IHDR width and height, big-endian: 2560 = 0 0 10 0, 1600 = 0 0 6 64.
    expect(run.stdout.trim().split(/\s+/).map(Number)).toEqual([0, 0, 10, 0, 0, 0, 6, 64]);
  });

  it("gives pages WebGL on the CPU, with Mesa's OpenGL left out of the image", () => {
    // The page reports into its own title, which the DevTools endpoint lists. The renderer
    // must be SwiftShader: it is what podium-desktop asks Chromium for, and it is why the
    // image can leave Mesa out — with Mesa and without the flag, there was no WebGL at all.
    const page =
      "data:text/html,<script>var g=document.createElement('canvas').getContext('webgl')," +
      "r=g&&g.getExtension('WEBGL_debug_renderer_info');" +
      "document.title='WEBGL='+(g?(r?g.getParameter(r.UNMASKED_RENDERER_WEBGL):'yes'):'no')</script>";
    const run = inContainer(
      `
      podium-desktop -- sh -c '
        sleep 3
        curl -fsS http://127.0.0.1:9222/json/list | grep -o "WEBGL=[^\\"]*" | head -1
        echo "GPU EXITS: $(grep -c "Exiting GPU process" /tmp/podium-desktop/chromium.log)"
        echo "MESA: $(ls /usr/lib/*/libLLVM* /usr/lib/*/libgallium-* 2>/dev/null | wc -l)"
      '
    `,
      { PODIUM_DESKTOP_START_URL: page },
    );
    expect(run.status, show(run)).toBe(0);
    expect(run.stdout).toMatch(/WEBGL=.*SwiftShader/);
    expect(run.stdout).toContain("GPU EXITS: 0");
    expect(run.stdout).toContain("MESA: 0");
  });

  it("carries the browser tools' MCP client, and it drives the desktop's Chromium", () => {
    // The runtime starts `chrome-devtools-mcp --browserUrl <cdp_url>` for the browser* tools;
    // the base runtime image does not carry it. An MCP exchange over stdio against the
    // desktop's DevTools port — initialize, then list the pages once it has answered — proves
    // it is installed and drives this browser.
    const client = [
      'const p = require("node:child_process").spawn("chrome-devtools-mcp", ["--browserUrl", "http://127.0.0.1:9222"]);',
      'let out = "";',
      'const send = (m) => p.stdin.write(JSON.stringify(m) + "\\n");',
      'p.stdout.on("data", (d) => {',
      '  out += d;',
      '  if (out.includes("\\"id\\":1") && !out.includes("sent")) {',
      '    out += "sent";',
      '    send({ jsonrpc: "2.0", method: "notifications/initialized" });',
      '    send({ jsonrpc: "2.0", id: 2, method: "tools/call", params: { name: "list_pages", arguments: {} } });',
      '  }',
      '  const page = /\\d+: (about:blank)/.exec(out);',
      '  if (page) { console.log("PAGE: " + page[1]); p.kill(); process.exit(0); }',
      '});',
      'send({ jsonrpc: "2.0", id: 1, method: "initialize", params: { protocolVersion: "2025-06-18", capabilities: {}, clientInfo: { name: "test", version: "1" } } });',
      'setTimeout(() => { console.log("NO PAGE: " + out); p.kill(); process.exit(1); }, 30000);',
    ].join("\n");
    const run = docker([
      "run", "--rm", "-e", `MCP_CLIENT=${client}`, "--entrypoint", "sh", DesktopImage, "-c",
      'echo "MCP: $(chrome-devtools-mcp --version)"; podium-desktop -- sh -c \'node -e "$MCP_CLIENT"\'',
    ]);
    expect(run.status, show(run)).toBe(0);
    expect(run.stdout).toMatch(/MCP: \d+\.\d+\.\d+/);
    expect(run.stdout).toContain("PAGE: about:blank");
  });

  it("fails the turn at the start, saying why, when the desktop cannot come up", () => {
    // Another X server already holds display :1 (a lock file naming a live process — this
    // shell): Xvfb refuses to start, and the turn's command must not run at all. The lock is
    // in X's own format, the pid right-aligned in ten columns; anything else X treats as
    // stale, removes, and starts anyway.
    const run = inContainer(`
      printf '%10d\\n' $$ > /tmp/.X1-lock
      podium-desktop -- sh -c 'echo "COMMAND RAN"'
    `);
    expect(run.status, show(run)).toBe(1);
    expect(run.stdout).not.toContain("COMMAND RAN");
    expect(run.stderr).toContain("podium-desktop: the X display did not start");
    // The end of the log that explains it comes with the message.
    expect(run.stderr).toContain("xvfb.log");
  });

  it("refuses a malformed size before starting anything", () => {
    const run = inContainer(`podium-desktop -- true`, { PODIUM_DESKTOP_SIZE: "big" });
    expect(run.status, show(run)).toBe(1);
    expect(run.stderr).toContain("PODIUM_DESKTOP_SIZE must be WIDTHxHEIGHT, not 'big'");
  });

  it("runs as the runtime's user, with a home nothing root-owned blocks", () => {
    const run = inContainer(`
      set -e
      echo "UID: $(id -u)"
      echo "STRAY: $(find "$HOME" ! -user agent | wc -l | tr -d ' ')"
      echo "NODE: $(node --version)"
      echo "DEBIAN: $(cat /etc/debian_version)"
    `);
    expect(run.status, show(run)).toBe(0);
    expect(run.stdout).toContain("UID: 1000");
    // A single root-owned directory in $HOME has killed turns before (see dev.test.ts); the
    // desktop writes Chromium's profile and XFCE's settings there.
    expect(run.stdout).toContain("STRAY: 0");
    expect(run.stdout).toMatch(/NODE: v2[6-9]\./);
    expect(run.stdout).toMatch(/DEBIAN: 13\./);
  });

  it("carries the licence material the GPL asks for if the image is ever published", () => {
    const run = inContainer(`
      set -e
      test -s /usr/share/src/voxl/voxl-server-${VoxlVersion}.crate && echo "SOURCE: voxl"
      test -s /usr/share/doc/podium-desktop/THIRD_PARTY.md && echo "NOTICE: third party"
      test -s /usr/share/doc/libx264-164/copyright && echo "COPYRIGHT: x264"
      grep -q "^License: GPL-2+" /usr/share/doc/libx264-164/copyright && echo "X264: GPL-2+"
    `);
    expect(run.status, show(run)).toBe(0);
    expect(run.stdout).toContain("SOURCE: voxl");
    expect(run.stdout).toContain("NOTICE: third party");
    expect(run.stdout).toContain("COPYRIGHT: x264");
    expect(run.stdout).toContain("X264: GPL-2+");

    const label = docker(["image", "inspect", "--format", '{{index .Config.Labels "org.opencontainers.image.licenses"}}', DesktopImage]);
    expect(label.status, show(label)).toBe(0);
    expect(label.stdout.trim()).toBe("Apache-2.0 AND MIT AND GPL-2.0-or-later");
  });
});

if (unavailable !== null) {
  // eslint-disable-next-line no-console
  console.log(`skipping the desktop image tests: ${unavailable}`);
}
