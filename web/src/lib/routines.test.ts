import { describe, expect, it } from "vitest";
import { GITHUB_EVENTS, GITHUB_GROUPS, isGitHub, isLinear, onEvents, perTicket, repoNames, teamKeys } from "./routines";

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
});
