import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { create } from "@bufbuild/protobuf";
import { ChatPullRequestSchema, LinearIssueSchema } from "../gen/podium/agent/v1/agent_pb";
import { fetchLinearIssue, findIssueRefs, issueRefOfUrl, pullLinks, stateTone } from "./linearIssues";

const getLinearIssues = vi.fn();

vi.mock("./client", async () => {
  const actual = await vi.importActual<typeof import("./client")>("./client");
  return { ...actual, agent: { getLinearIssues: (...a: unknown[]) => getLinearIssues(...a) } };
});

const refs = (text: string) => findIssueRefs(text).map((m) => m.ref);

describe("findIssueRefs", () => {
  it("finds identifiers that stand on their own, in order", () => {
    expect(refs("can you look at EME-6? ENG-12 blocks it.")).toEqual(["EME-6", "ENG-12"]);
    expect(refs("(EME-6), \"ENG-1\": [A1-2]")).toEqual(["EME-6", "ENG-1", "A1-2"]);
    expect(refs("Fixed in EME-9.")).toEqual(["EME-9"]);
  });

  it("is only a candidate: the conductor decides which teams are real", () => {
    // UTF-8 is shaped like an identifier; no workspace team means it stays text, server-side.
    expect(refs("UTF-8")).toEqual(["UTF-8"]);
  });

  it("leaves out lower case, words, paths, versions and hyphenated tails", () => {
    expect(refs("eme-6 xEME-6 foo/EME-6 EME-6x EME-6.1 EME-6-beta EME-06 é-1 éEME-1")).toEqual([]);
  });

  it("says where each one is", () => {
    expect(findIssueRefs("see EME-6 now")).toEqual([{ start: 4, end: 9, ref: "EME-6" }]);
  });
});

describe("issueRefOfUrl", () => {
  it("reads the identifier out of a link to an issue in Linear", () => {
    expect(issueRefOfUrl("https://linear.app/acme/issue/EME-6/fix-the-thing")).toBe("EME-6");
    expect(issueRefOfUrl("https://linear.app/acme/issue/eme-6")).toBe("EME-6");
    expect(issueRefOfUrl("https://linear.app/acme/issue/EME-6?x=1")).toBe("EME-6");
  });

  it("is nothing for any other link", () => {
    expect(issueRefOfUrl("https://linear.app/acme/project/EME-6")).toBeUndefined();
    expect(issueRefOfUrl("https://evil.example/linear.app/acme/issue/EME-6")).toBeUndefined();
    expect(issueRefOfUrl(undefined)).toBeUndefined();
  });
});

describe("stateTone", () => {
  it("colours a state by its kind", () => {
    expect(stateTone("started")).toBe("warn");
    expect(stateTone("completed")).toBe("ok");
    expect(stateTone("something new")).toBe("idle");
  });
});

describe("pullLinks", () => {
  it("lists the chat's pull requests and Linear's, each once", () => {
    const chat = [
      create(ChatPullRequestSchema, { url: "https://github.com/acme/app/pull/12", owner: "acme", repo: "app", number: 12 }),
    ];
    const linear = [
      { url: "https://github.com/acme/app/pull/12/", title: "dup", source: "github" },
      { url: "https://github.com/acme/api/pull/3", title: "API", source: "github" },
      { url: "https://www.figma.com/file/x", title: "Design", source: "figma" },
    ];
    expect(pullLinks(linear, chat)).toEqual([
      { url: "https://github.com/acme/app/pull/12", label: "acme/app#12", from: "chat" },
      { url: "https://github.com/acme/api/pull/3", label: "acme/api#3", from: "linear" },
    ]);
  });
});

describe("fetchLinearIssue", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    getLinearIssues.mockReset();
  });
  afterEach(() => vi.useRealTimers());

  it("asks for the lookups made together in one request, and answers null for what it left out", async () => {
    getLinearIssues.mockResolvedValue({
      issues: [create(LinearIssueSchema, { ref: "EME-6", identifier: "EME-6", title: "Fix" })],
    });
    const a = fetchLinearIssue("EME-6", false);
    const b = fetchLinearIssue("SEC-9", false);
    const c = fetchLinearIssue("EME-6", false);
    await vi.advanceTimersByTimeAsync(20);
    expect(getLinearIssues).toHaveBeenCalledTimes(1);
    expect(getLinearIssues).toHaveBeenCalledWith({ refs: ["EME-6", "SEC-9"], detail: false });
    expect((await a)?.title).toBe("Fix");
    expect(await b).toBeNull();
    expect((await c)?.title).toBe("Fix");
  });

  it("asks for chips and cards apart", async () => {
    getLinearIssues.mockResolvedValue({ issues: [] });
    void fetchLinearIssue("EME-6", false);
    void fetchLinearIssue("EME-6", true);
    await vi.advanceTimersByTimeAsync(20);
    expect(getLinearIssues).toHaveBeenCalledTimes(2);
  });

  it("fails every lookup of a request that failed", async () => {
    getLinearIssues.mockRejectedValue(new Error("down"));
    const a = fetchLinearIssue("EME-6", true);
    const caught = a.catch((e: unknown) => e);
    await vi.advanceTimersByTimeAsync(20);
    expect(await caught).toBeInstanceOf(Error);
  });
});
