import { describe, expect, it } from "vitest";
import { sidecarProblem } from "./sandboxProviders";

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
