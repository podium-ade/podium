import { execFileSync } from "node:child_process";
import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

import { describe, expect, it } from "vitest";

/** DevImage is what `make agent-runtime` tags the dev runtime as. */
const DevImage = process.env["PODIUM_DEV_IMAGE"] ?? "podium-agent-runtime-dev:dev";

/** repoRoot: this file is agent/runtime/images/, three levels below the checkout. */
const repoRoot = resolve(dirname(fileURLToPath(import.meta.url)), "../../..");

function repoFile(path: string): string {
  return readFileSync(resolve(repoRoot, path), "utf8");
}

/**
 * The two versions this image is not allowed to disagree with, read out of the files that
 * decide them rather than repeated here. go.mod is what the compiler has to satisfy, and
 * ci.yml is the golangci-lint every pull request is judged by — an image that lints with a
 * different v2 reports clean on findings CI will fail on.
 */
const goVersion = /^go (\d+\.\d+(?:\.\d+)?)$/m.exec(repoFile("go.mod"))?.[1];
const golangciVersion = /-b "\$\(go env GOPATH\)\/bin" v(\d+\.\d+\.\d+)/.exec(
  repoFile(".github/workflows/ci.yml"),
)?.[1];

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
 * available reports whether this machine can run the test at all. `make agent-runtime` is
 * what builds the image; a test that built it itself would make `pnpm test:images` a
 * twenty-minute command on every run.
 */
function available(): string | null {
  if (docker(["version", "--format", "{{.Server.Version}}"]).status !== 0) {
    return "no docker engine is reachable";
  }
  if (docker(["image", "inspect", DevImage]).status !== 0) {
    return `${DevImage} is not on this engine; run \`make agent-runtime\` first`;
  }
  return null;
}

const unavailable = available();

/**
 * inContainer runs a shell script inside the dev image, as the image's own unprivileged
 * user and with no engine reachable — the toolchain has to be present and runnable without
 * a dind sidecar, which is a separate thing to test.
 */
function inContainer(script: string): Run {
  return docker(["run", "--rm", "--entrypoint", "sh", DevImage, "-c", script]);
}

describe.skipIf(unavailable !== null)(`${DevImage}`, () => {
  it("carries the toolchain CONTRIBUTING.md asks a contributor for", () => {
    const run = inContainer(`
      set -e
      echo "GO: $(go version)"
      echo "LINT: $(golangci-lint --version)"
      echo "MAKE: $(make --version | head -1)"
      echo "GIT: $(git --version)"
      echo "NODE: $(node --version)"
      echo "PNPM: $(pnpm --version)"
    `);
    // eslint-disable-next-line no-console
    console.log(`${DevImage}\n${run.stdout}${run.stderr}`);

    expect(run.status, `stdout:\n${run.stdout}\nstderr:\n${run.stderr}`).toBe(0);
    // The compiler must be the one go.mod names: a turn that builds on an older toolchain
    // fails on the module, and one that builds on a newer one green-lights syntax CI rejects.
    expect(goVersion, "no `go` directive found in go.mod").toBeTruthy();
    expect(run.stdout).toContain(`GO: go version go${goVersion} linux/`);
    expect(golangciVersion, "no golangci-lint version found in .github/workflows/ci.yml").toBeTruthy();
    expect(run.stdout).toContain(`LINT: golangci-lint has version ${golangciVersion} `);
    expect(run.stdout).toContain("MAKE: GNU Make");
    expect(run.stdout).toMatch(/GIT: git version \d/);
    expect(run.stdout).toMatch(/NODE: v2[2-9]\./);
    expect(run.stdout).toMatch(/PNPM: 11\./);
  });

  it("has the docker client and both plugins, and no daemon", () => {
    const run = inContainer(`
      set -e
      echo "CLI: $(docker --version)"
      echo "COMPOSE: $(docker compose version)"
      echo "BUILDX: $(docker buildx version)"
      echo "DAEMON: $(command -v dockerd || echo none)"
    `);
    // eslint-disable-next-line no-console
    console.log(`${DevImage}\n${run.stdout}${run.stderr}`);

    expect(run.status, `stdout:\n${run.stdout}\nstderr:\n${run.stderr}`).toBe(0);
    expect(run.stdout).toMatch(/CLI: Docker version \d+\./);
    expect(run.stdout).toMatch(/COMPOSE: Docker Compose version v\d+\./);
    expect(run.stdout).toContain("BUILDX: github.com/docker/buildx v");
    // The engine belongs in a sidecar. If a daemon ever lands in here the image has to run
    // privileged, and a privileged container is not where a model's output should execute.
    expect(run.stdout).toContain("DAEMON: none");
  });

  it("is a development image the build tools can actually write in", () => {
    const run = inContainer(`
      set -e
      echo "UID: $(id -u)"
      echo "NODE_ENV: \${NODE_ENV:-unset}"
      echo "DOCKER_HOST: \${DOCKER_HOST:-unset}"
      mkdir -p /tmp/gobuild
      printf 'package main\\nimport ("fmt"; "net/http")\\nfunc main() { fmt.Println(http.MethodGet) }\\n' > /tmp/gobuild/main.go
      printf 'module gobuild\\n\\ngo 1.26\\n' > /tmp/gobuild/go.mod
      go build -C /tmp/gobuild -o /tmp/gobuild/bin ./...
      echo "BUILT: $(/tmp/gobuild/bin)"
    `);
    // eslint-disable-next-line no-console
    console.log(`${DevImage}\n${run.stdout}${run.stderr}`);

    expect(run.status, `stdout:\n${run.stdout}\nstderr:\n${run.stderr}`).toBe(0);
    expect(run.stdout).toContain("UID: 1000");
    // The base image sets NODE_ENV=production, and pnpm reads it: under it `pnpm install
    // --frozen-lockfile` omits devDependencies and `make web` dies on a missing vite.
    expect(run.stdout).not.toContain("NODE_ENV: production");
    // Unset on purpose. The engine a turn uses is a sidecar on the task's network, so its
    // address is task spec (`env: DOCKER_HOST`), not something this image can know.
    expect(run.stdout).toContain("DOCKER_HOST: unset");
    // A real compile and link of net/http, as uid 1000, out of the caches the image
    // pre-creates. net/http is the package that would need a C toolchain if cgo were on.
    expect(run.stdout).toContain("BUILT: GET");
  });
});

if (unavailable !== null) {
  // eslint-disable-next-line no-console
  console.log(`skipping the dev image tests: ${unavailable}`);
}
