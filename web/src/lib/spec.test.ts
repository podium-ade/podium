import { describe, expect, it } from "vitest";
import { create } from "@bufbuild/protobuf";
import { TaskSpecSchema } from "../gen/podium/v1/common_pb";
import {
  EMPTY_FIELDS,
  fieldsToDoc,
  formatGoDuration,
  hasAdvancedFields,
  parseGoDuration,
  parseSpecYaml,
  specToYaml,
  splitServerProblems,
} from "./spec";

describe("parseGoDuration", () => {
  it("reads the spellings the task spec uses", () => {
    expect(parseGoDuration("30s")).toBe(30e9);
    expect(parseGoDuration("1h")).toBe(3600e9);
    expect(parseGoDuration("1m30s")).toBe(90e9);
    expect(parseGoDuration("500ms")).toBe(5e8);
    expect(parseGoDuration("1h30m0s")).toBe(5400e9);
    expect(parseGoDuration("0")).toBe(0);
  });

  it("rejects anything that is not one", () => {
    for (const bad of ["", "30", "1 hour", "s", "30sx", "abc"]) {
      expect(parseGoDuration(bad), bad).toBeUndefined();
    }
  });
});

describe("formatGoDuration", () => {
  it("round-trips through parseGoDuration", () => {
    for (const ns of [0, 5e8, 30e9, 90e9, 3600e9, 5400e9]) {
      expect(parseGoDuration(formatGoDuration(ns / 1e9))).toBe(ns);
    }
  });
});

describe("parseSpecYaml", () => {
  it("decodes the document the CLI's --spec takes", () => {
    const { spec, problems } = parseSpecYaml(`
image: alpine:3
command: ["sh", "-c", "echo hi"]
working_dir: /src
env:
  CI: "true"
labels: [linux/arm64]
timeout: 30s
max_attempts: 3
retry_on_node_loss: true
resources:
  cpu: 1.5
  memory_mb: 512
hardening:
  read_only_rootfs: true
  capabilities: [CHOWN]
secrets:
  - name: DB_PASSWORD
    target: env
    key: PGPASSWORD
sidecars:
  db:
    image: postgres:16-alpine
    env:
      POSTGRES_PASSWORD: pw
    readiness:
      tcp_port: 5432
      timeout: 1m
`);
    expect(problems).toEqual([]);
    expect(spec).toBeDefined();
    expect(spec!.image).toBe("alpine:3");
    expect(spec!.command).toEqual(["sh", "-c", "echo hi"]);
    expect(spec!.workingDir).toBe("/src");
    expect(spec!.env).toEqual({ CI: "true" });
    expect(spec!.labels).toEqual(["linux/arm64"]);
    expect(spec!.timeout).toEqual({ seconds: 30n, nanos: 0 });
    expect(spec!.maxAttempts).toBe(3);
    expect(spec!.retryOnNodeLoss).toBe(true);
    expect(spec!.resources).toEqual({ cpu: 1.5, memoryMb: 512n, pids: 0 });
    expect(spec!.hardening).toEqual({ readOnlyRootfs: true, capabilities: ["CHOWN"] });
    expect(spec!.secrets).toEqual([{ name: "DB_PASSWORD", target: "env", key: "PGPASSWORD" }]);
    expect(spec!.sidecars!.db.image).toBe("postgres:16-alpine");
    expect(spec!.sidecars!.db.readiness).toMatchObject({
      tcpPort: 5432,
      timeout: { seconds: 60n, nanos: 0 },
    });
  });

  it("rejects a field the schema does not have rather than dropping it", () => {
    // The whole reason this decoder exists: yaml.parse would happily discard `privileged` and
    // submit a task that does not do what was asked.
    const { spec, problems } = parseSpecYaml("image: alpine:3\nprivileged: true\n");
    expect(spec).toBeUndefined();
    expect(problems).toHaveLength(1);
    expect(problems[0]).toContain("privileged");
    expect(problems[0]).toContain("is not a task spec field");
  });

  it("rejects an unknown field inside a sidecar, but not the sidecar's own name", () => {
    const { problems } = parseSpecYaml(
      "image: alpine:3\nsidecars:\n  my-db:\n    image: postgres:16-alpine\n    privileged: true\n",
    );
    expect(problems).toEqual(["sidecars.my-db.privileged: is not a task spec field (known: image, command, env, readiness, resources)"]);
  });

  it("reports every problem at once, the way the server's validator does", () => {
    const { spec, problems } = parseSpecYaml("timeout: soon\nmax_attempts: half\nnope: 1\n");
    expect(spec).toBeUndefined();
    expect(problems).toHaveLength(4);
    expect(problems.join("\n")).toContain("nope");
    expect(problems.join("\n")).toContain("timeout");
    expect(problems.join("\n")).toContain("max_attempts");
    expect(problems.join("\n")).toContain("image: is required");
  });

  it("says so for an empty or unparseable document", () => {
    expect(parseSpecYaml("").problems).toEqual(["the spec is empty"]);
    expect(parseSpecYaml("# just a comment\n").problems).toEqual(["the spec is empty"]);
    expect(parseSpecYaml("image: [unclosed\n").problems).toHaveLength(1);
    expect(parseSpecYaml("- a\n- b\n").problems).toEqual([": must be a mapping"]);
  });
});

describe("specToYaml", () => {
  it("round-trips a spec back through the decoder", () => {
    const spec = create(TaskSpecSchema, {
      image: "alpine:3",
      command: ["sh", "-c", "echo hi"],
      workingDir: "/workspace",
      env: { B: "2", A: "1" },
      labels: ["demo"],
      timeout: { seconds: 90n, nanos: 0 },
      maxAttempts: 2,
      resources: { cpu: 1.5, memoryMb: 512n, pids: 4096 },
      hardening: { readOnlyRootfs: true, capabilities: ["CHOWN"] },
      secrets: [{ name: "TOKEN", target: "file", key: "/podium/secrets/token" }],
      sidecars: { db: { image: "postgres:16-alpine", readiness: { tcpPort: 5432 } } },
      retryOnNodeLoss: true,
    });
    const text = specToYaml(spec);
    expect(text).toContain("timeout: 1m30s");
    const { spec: back, problems } = parseSpecYaml(text);
    expect(problems).toEqual([]);
    expect(back!.image).toBe("alpine:3");
    expect(back!.env).toEqual({ A: "1", B: "2" });
    expect(back!.secrets).toHaveLength(1);
    expect(back!.sidecars!.db.image).toBe("postgres:16-alpine");
    expect(back!.resources).toEqual({ cpu: 1.5, memoryMb: 512n, pids: 4096 });
    expect(back!.retryOnNodeLoss).toBe(true);
  });

  it("says so when there is no spec at all", () => {
    expect(specToYaml(undefined)).toBe("# no spec");
  });
});

describe("hasAdvancedFields", () => {
  it("is true for anything the simple form cannot show", () => {
    const plain = create(TaskSpecSchema, { image: "alpine:3" });
    expect(hasAdvancedFields(plain)).toBe(false);
    expect(hasAdvancedFields(create(TaskSpecSchema, { image: "a", secrets: [{ name: "X" }] }))).toBe(
      true,
    );
    expect(
      hasAdvancedFields(create(TaskSpecSchema, { image: "a", sidecars: { db: { image: "b" } } })),
    ).toBe(true);
    expect(
      hasAdvancedFields(create(TaskSpecSchema, { image: "a", hardening: { readOnlyRootfs: true } })),
    ).toBe(true);
  });
});

describe("fieldsToDoc", () => {
  it("splits a command one argument per line and env as KEY=VALUE", () => {
    const { doc, problems } = fieldsToDoc({
      ...EMPTY_FIELDS,
      image: "  alpine:3 ",
      command: "sh\n-c\n echo hi \n\n",
      env: "CI=true\nURL=https://x/y=z\n",
      labels: " a , b ,, ",
      timeout: "30s",
      maxAttempts: "2",
      cpu: "1.5",
      memoryMb: "512",
      retryOnNodeLoss: true,
    });
    expect(problems).toEqual([]);
    expect(doc).toEqual({
      image: "alpine:3",
      command: ["sh", "-c", "echo hi"],
      env: { CI: "true", URL: "https://x/y=z" },
      labels: ["a", "b"],
      timeout: "30s",
      max_attempts: 2,
      resources: { cpu: 1.5, memory_mb: 512 },
      retry_on_node_loss: true,
    });
  });

  it("reports an env line that is not KEY=VALUE and a number that is not one", () => {
    const { problems } = fieldsToDoc({
      ...EMPTY_FIELDS,
      image: "alpine:3",
      env: "NOTANASSIGNMENT\n=novalue",
      cpu: "lots",
    });
    expect(problems).toHaveLength(3);
    expect(problems[0]).toContain("NOTANASSIGNMENT");
    expect(problems[2]).toContain("resources.cpu");
  });

  it("omits every empty field, so the server applies its own defaults", () => {
    expect(fieldsToDoc({ ...EMPTY_FIELDS, image: "alpine:3" }).doc).toEqual({ image: "alpine:3" });
  });
});

describe("splitServerProblems", () => {
  it("unpacks the joined error the server sends back", () => {
    expect(
      splitServerProblems(
        "invalid task spec: image is required\ntimeout must be positive, got 0s\n",
      ),
    ).toEqual(["image is required", "timeout must be positive, got 0s"]);
  });

  it("leaves a single-problem message alone", () => {
    expect(splitServerProblems("create task: spec is required")).toEqual([
      "create task: spec is required",
    ]);
  });
});
