import { describe, expect, it } from "vitest";
import type { SandboxProvider } from "../gen/podium/v1/sandbox_pb";
import { capacityOf, sidecarProblem } from "./sandboxProviders";

describe("sidecarProblem", () => {
  const both = { docker: true, browser: true };
  it("lets a Docker daemon run only on Modal's vm runtime with privileged sidecars on", () => {
    expect(sidecarProblem("modal", { runtime: "vm", privileged_sidecars: "true" }, "", both)).toBe("");
    expect(sidecarProblem("modal", { runtime: "vm" }, "", both)).toMatch(/Privileged sidecars/);
    expect(sidecarProblem("modal", { privileged_sidecars: "true" }, "", both)).toMatch(/vm runtime/);
    expect(sidecarProblem("modal", { privileged_sidecars: "true" }, "vm", both)).toBe("");
    expect(sidecarProblem("boat", {}, "", both)).toMatch(/boat sandboxes do not run/);
  });
  it("runs a browser wherever the sandbox has Docker", () => {
    const browser = { docker: false, browser: true };
    expect(sidecarProblem("boat", {}, "", browser)).toBe("");
    expect(sidecarProblem("modal", {}, "", browser)).toMatch(/vm runtime/);
    expect(sidecarProblem("modal", { runtime: "vm" }, "", browser)).toBe("");
    expect(sidecarProblem("nodes", {}, "", both)).toBe("");
  });
});

describe("capacityOf", () => {
  const p = (maxConcurrent: number, accountLimit: number, inUse = 1) =>
    ({ maxConcurrent, accountLimit, inUse }) as SandboxProvider;

  it("binds to the lower of the cap and the account's limit", () => {
    expect(capacityOf(p(8, 0))).toEqual({ inUse: 1, limit: 8, byAccount: false });
    expect(capacityOf(p(0, 10))).toEqual({ inUse: 1, limit: 10, byAccount: true });
    expect(capacityOf(p(12, 10))).toEqual({ inUse: 1, limit: 10, byAccount: true });
    expect(capacityOf(p(4, 10))).toEqual({ inUse: 1, limit: 4, byAccount: false });
  });

  it("has no limit when neither is set", () => {
    expect(capacityOf(p(0, 0))).toEqual({ inUse: 1, limit: 0, byAccount: false });
  });
});
