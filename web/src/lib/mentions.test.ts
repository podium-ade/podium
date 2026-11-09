import { describe, expect, it } from "vitest";
import { insertMention, mentionQuery, mentionsPodium, personToken } from "./mentions";

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
