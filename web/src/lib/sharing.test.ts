import { describe, expect, it } from "vitest";
import { canWrite, generalAccessLabel, roleOf, shareLink, sharedByLabel, unread } from "./sharing";

describe("sharing helpers", () => {
  it("reads only the roles the server has", () => {
    expect(roleOf({ myRole: "editor" })).toBe("editor");
    expect(roleOf({ myRole: "admin" })).toBe("");
    expect(roleOf(undefined)).toBe("");
  });

  it("offers the composer to owners and editors only", () => {
    expect(canWrite("owner")).toBe(true);
    expect(canWrite("editor")).toBe(true);
    expect(canWrite("viewer")).toBe(false);
    expect(canWrite("")).toBe(false);
  });

  it("builds a link that carries the token as k=", () => {
    expect(shareLink("https://podium.example/", "chat_1", "abc")).toBe(
      "https://podium.example/agent/chat/chat_1?k=abc",
    );
  });

  it("says how a shared chat got into the list", () => {
    expect(sharedByLabel({ accessReason: "shared", sharedBy: "alice", myRole: "editor" })).toBe("Shared by alice");
    expect(sharedByLabel({ accessReason: "shared", sharedBy: "link", myRole: "viewer" })).toBe(
      "Opened from a link · view only",
    );
    expect(sharedByLabel({ accessReason: "workspace", sharedBy: "", myRole: "viewer" })).toBe("Workspace · view only");
    expect(sharedByLabel({ accessReason: "owner", sharedBy: "", myRole: "owner" })).toBe("");
  });

  it("names general access and counts unread", () => {
    expect(generalAccessLabel("link")).toBe("Anyone with the link");
    expect(generalAccessLabel("restricted")).toBe("Restricted");
    expect(unread({ unreadCount: 3n })).toBe(3);
  });
});
