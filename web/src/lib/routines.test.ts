import { describe, expect, it } from "vitest";
import {
  GITHUB_EVENTS,
  GITHUB_GROUPS,
  isGitHub,
  isLinear,
  onEvents,
  perTicket,
  repoNames,
  teamKeys,
  GITHUB_CI,
  ciOutcome,
  withOutcome,
} from "./routines";

describe("repoNames", () => {
  it("reads repositories and owners separated by commas or spaces, lower case, each once", () => {
    expect(repoNames(" Acme/API, acme\nacme/api  Other/web,,")).toEqual(["acme/api", "acme", "other/web"]);
  });

  it("is nothing for an empty field", () => {
    expect(repoNames("  , ")).toEqual([]);
  });

  it("reads as teamKeys does, but keeps GitHub's lower case", () => {
    expect(teamKeys("eng, web eng")).toEqual(["ENG", "WEB"]);
    expect(repoNames("eng, web eng")).toEqual(["eng", "web"]);
  });
});

describe("triggers", () => {
  it("are on events for Linear and GitHub, and in a ticket's conversation only for Linear", () => {
    expect(onEvents({ trigger: "linear" })).toBe(true);
    expect(onEvents({ trigger: "github" })).toBe(true);
    expect(onEvents({ trigger: "schedule" })).toBe(false);
    expect(isLinear({ trigger: "github" })).toBe(false);
    expect(isGitHub({ trigger: "github" })).toBe(true);
    expect(perTicket({ trigger: "github", chatMode: "subject" })).toBe(true);
    expect(perTicket({ trigger: "github", chatMode: "routine" })).toBe(false);
    expect(perTicket({ trigger: "schedule", chatMode: "subject" })).toBe(false);
    expect(perTicket({ trigger: "linear", chatMode: "subject" })).toBe(true);
  });
});

describe("GITHUB_EVENTS", () => {
  it("are each in one of the groups, each once", () => {
    const events = GITHUB_EVENTS.map((e) => e.event);
    expect(new Set(events).size).toBe(events.length);
    for (const e of GITHUB_EVENTS) expect(GITHUB_GROUPS).toContain(e.group);
  });

  it("have every CI row's two events among them", () => {
    const events = GITHUB_EVENTS.map((e) => e.event);
    for (const r of GITHUB_CI) expect(events).toEqual(expect.arrayContaining([r.completed, r.failed]));
  });
});

describe("a CI row", () => {
  const [allChecks, workflowRun] = GITHUB_CI;

  it("runs on every finish, only on a failure, or not at all, never on both", () => {
    expect(withOutcome(["pull_request.opened"], allChecks, "finished")).toEqual(["checks.completed", "pull_request.opened"]);
    expect(withOutcome(["checks.completed", "pull_request.opened"], allChecks, "failed")).toEqual([
      "checks.failed",
      "pull_request.opened",
    ]);
    expect(withOutcome(["checks.failed", "workflow_run.failed"], allChecks, "off")).toEqual(["workflow_run.failed"]);
  });

  it("reads asking for both as every finish, which a failure is one of", () => {
    expect(ciOutcome(["workflow_run.failed", "workflow_run.completed"], workflowRun)).toBe("finished");
    expect(ciOutcome(["workflow_run.failed"], workflowRun)).toBe("failed");
    expect(ciOutcome(["checks.failed"], workflowRun)).toBe("off");
  });
});
