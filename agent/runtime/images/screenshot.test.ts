import { execFileSync } from "node:child_process";

import { describe, expect, it } from "vitest";

/** BrowserImage is what `make agent-runtime` tags the browser runtime as. */
const BrowserImage = process.env["PODIUM_BROWSER_IMAGE"] ?? "podium-agent-runtime-browser:dev";

/** ScreenshotBin is the documented path the coder prompt tells an agent to call. */
const ScreenshotBin = "/opt/podium-agent/bin/screenshot";

type Run = { status: number; stdout: string; stderr: string };

/** docker runs one docker command and never throws: the exit code is the assertion. */
function docker(args: string[], input?: string): Run {
  try {
    const stdout = execFileSync("docker", args, {
      encoding: "utf8",
      input,
      stdio: ["pipe", "pipe", "pipe"],
      maxBuffer: 16 * 1024 * 1024,
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
 * available reports whether this machine can run the test at all. The image is 4 GB and
 * `make agent-runtime` is what builds it; a test that built it itself would make `pnpm
 * test:images` a twenty-minute command on every run.
 */
function available(): string | null {
  if (docker(["version", "--format", "{{.Server.Version}}"]).status !== 0) {
    return "no docker engine is reachable";
  }
  if (docker(["image", "inspect", BrowserImage]).status !== 0) {
    return `${BrowserImage} is not on this engine; run \`make agent-runtime\` first`;
  }
  return null;
}

const unavailable = available();

/**
 * inContainer runs a shell script inside the browser image, as the image's own unprivileged
 * user and with the default 64 MB /dev/shm — the same shape a Podium task container has,
 * because Podium sets no shm size and the node adds no capabilities.
 */
function inContainer(script: string): Run {
  return docker(["run", "--rm", "--entrypoint", "sh", BrowserImage, "-c", script]);
}

describe.skipIf(unavailable !== null)(`${BrowserImage}`, () => {
  it("takes a screenshot of a page served inside the container", () => {
    // The measurement the /dev/shm decision rests on is printed by the script and logged
    // below: Chromium's own shared-memory use, at the default 64 MB, on this engine.
    const run = inContainer(`
      set -e
      mkdir -p /tmp/site /workspace/.podium/artifacts
      printf '<!doctype html><title>podium</title><h1>hello podium</h1>' > /tmp/site/index.html
      cd /tmp/site && python3 -m http.server 8000 >/dev/null 2>&1 &
      i=0; while ! curl -sf http://127.0.0.1:8000/ >/dev/null; do i=$((i+1)); [ $i -gt 50 ] && exit 9; sleep 0.2; done
      echo "SHM-BEFORE: $(df -k /dev/shm | tail -1)"
      ${ScreenshotBin} http://127.0.0.1:8000/ /workspace/.podium/artifacts/shot.png --width 900 --height 600
      echo "SHM-AFTER: $(df -k /dev/shm | tail -1)"
      echo "BYTES: $(wc -c < /workspace/.podium/artifacts/shot.png)"
      echo "MAGIC: $(head -c 4 /workspace/.podium/artifacts/shot.png | od -An -tx1 | tr -d ' ')"
    `);
    // eslint-disable-next-line no-console
    console.log(`${BrowserImage}\n${run.stdout}${run.stderr}`);

    expect(run.status, `stdout:\n${run.stdout}\nstderr:\n${run.stderr}`).toBe(0);
    // The helper prints the absolute path it wrote, which is what an agent reads back.
    expect(run.stdout).toContain("/workspace/.podium/artifacts/shot.png");
    // A real PNG, not an empty file or an HTML error page.
    expect(run.stdout).toContain("MAGIC: 89504e47");
    const bytes = Number(/BYTES: (\d+)/.exec(run.stdout)?.[1] ?? "0");
    expect(bytes).toBeGreaterThan(1024);
  });

  it("exits 1 with the browser's own error when the page is not there", () => {
    const run = inContainer(`${ScreenshotBin} http://127.0.0.1:9/nothing /tmp/out.png`);
    expect(run.status).toBe(1);
    expect(run.stderr).toContain("screenshot:");
    expect(run.stderr).toMatch(/ERR_CONNECTION_REFUSED|net::|Timeout/);
  });

  it("refuses bad arguments before it launches anything", () => {
    const run = inContainer(`${ScreenshotBin} http://127.0.0.1:8000/ out.jpg`);
    expect(run.status).toBe(1);
    expect(run.stderr).toContain("must end in .png");
  });
});

if (unavailable !== null) {
  // eslint-disable-next-line no-console
  console.log(`skipping the browser image tests: ${unavailable}`);
}
