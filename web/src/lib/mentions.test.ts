import { describe, expect, it } from "vitest";
import {
  insertMention,
  mentionQuery,
  mentionSuggestions,
  mentionsPodium,
  personToken,
  plainMentions,
} from "./mentions";

describe("mentionsPodium", () => {
  // The same table as internal/agent/chat/mention_test.go: the button and the server agree.
  const cases: [string, string, boolean][] = [
    ["@Podium what changed?", "", true],
    ["what changed, @podium?", "", true],
    ["thanks @PODIUM.", "", true],
    ["ask @Podium\nplease", "", true],
    ["no mention here", "", false],
    ["mail alice@podium.dev", "", false],
    ["@podiums are not it", "", false],
    ["x@Podium", "", false],
    ["@Podium-bot", "", false],
    ["@[Podium](mention:podium@example.com) hi", "", false],
    ["@Researcher dig into this", "Researcher", true],
    ["@research bot dig into this", "Research Bot", true],
    ["@Researcher dig into this", "", false],
    ["@Podium dig in", "Researcher", true],
  ];
  it.each(cases)("%s / %s", (text, personality, want) => {
    expect(mentionsPodium(text, personality)).toBe(want);
  });
});

describe("mention typing", () => {
  it("finds the @word at the caret", () => {
    expect(mentionQuery("hi @Pod", 7)).toEqual({ query: "Pod", start: 3 });
    expect(mentionQuery("@", 1)).toEqual({ query: "", start: 0 });
    expect(mentionQuery("mail a@b", 8)).toBeUndefined();
    expect(mentionQuery("no at", 5)).toBeUndefined();
  });

  it("inserts a token and a space", () => {
    expect(insertMention("hi @Pod", 3, 7, "@Podium")).toEqual({ text: "hi @Podium ", caret: 11 });
  });

  it("builds a person chip that survives brackets in a name", () => {
    expect(personToken("Bob [ops]", "bob@x.dev")).toBe("@[Bob ops](mention:bob@x.dev)");
    expect(personToken("", "bob@x.dev")).toBe("@[bob@x.dev](mention:bob@x.dev)");
  });
});

describe("the deployment's name as the mention", () => {
  it("starts a turn by the deployment's name, and still by the house name", () => {
    expect(mentionsPodium("@AI Buddy what changed?", "", "AI Buddy")).toBe(true);
    expect(mentionsPodium("thanks, @ai buddy.", "", "AI Buddy")).toBe(true);
    expect(mentionsPodium("@Podium what changed?", "", "AI Buddy")).toBe(true);
    expect(mentionsPodium("@AI Buddys", "", "AI Buddy")).toBe(false);
    expect(mentionsPodium("@[AI Buddy](mention:a@example.com) hi", "", "AI Buddy")).toBe(false);
    expect(mentionsPodium("@AI Buddy hi")).toBe(false);
  });

  it("offers the deployment's name in the list, once, beside the assistant's", () => {
    const names = (q: string, personality: string, brand?: string) =>
      mentionSuggestions(q, personality, [], brand).map((s) => s.token);
    expect(names("", "Researcher", "AI Buddy")).toEqual(["@AI Buddy", "@Researcher"]);
    expect(names("ai", "", "AI Buddy")).toEqual(["@AI Buddy"]);
    expect(names("po", "", "AI Buddy")).toEqual([]);
    expect(names("", "AI Buddy", "AI Buddy")).toEqual(["@AI Buddy"]);
    expect(names("po", "")).toEqual(["@Podium"]);
  });
});

describe("plainMentions", () => {
  it("shows a person chip as the name and leaves the rest alone", () => {
    expect(plainMentions("@[Alice Ng](mention:alice@example.com) and @[Bob](mention:bob@example.com): hi")).toBe(
      "@Alice Ng and @Bob: hi",
    );
    expect(plainMentions("mail alice@example.com, [a link](https://example.com)")).toBe(
      "mail alice@example.com, [a link](https://example.com)",
    );
  });
});
