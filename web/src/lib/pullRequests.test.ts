import { describe, expect, it } from "vitest";
import { findPullRequests } from "./pullRequests";

describe("findPullRequests", () => {
  it("finds each pull request once, canonical, in the order it was named", () => {
    const text =
      "Opened [the fix](https://github.com/acme/web/pull/12/files). See https://github.com/acme/web/pull/12#discussion_r1 " +
      "and <https://github.com/acme/api.v2/pull/7>. Also https://github.com/acme/web/pull/3.";
    expect(findPullRequests(text).map((p) => p.url)).toEqual([
      "https://github.com/acme/web/pull/12",
      "https://github.com/acme/api.v2/pull/7",
      "https://github.com/acme/web/pull/3",
    ]);
  });

  it("ignores issues, other hosts and bare numbers", () => {
    expect(
      findPullRequests("#12, https://github.com/acme/web/issues/4, https://gitlab.com/acme/web/pull/5"),
    ).toEqual([]);
  });
});
