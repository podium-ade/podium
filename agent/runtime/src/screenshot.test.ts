import { describe, expect, it } from "vitest";

import { MaxPixels, Usage, parseArgs } from "./screenshot.js";

describe("parseArgs", () => {
  it("takes a URL and an output path", () => {
    expect(parseArgs(["http://127.0.0.1:8000/", "/workspace/.podium/artifacts/shot.png"])).toEqual({
      url: "http://127.0.0.1:8000/",
      out: "/workspace/.podium/artifacts/shot.png",
      width: 1280,
      height: 800,
      fullPage: false,
    });
  });

  it("takes the three options in any order", () => {
    const opts = parseArgs([
      "--full-page",
      "--height",
      "1200",
      "https://example.test/x",
      "--width",
      "375",
      "out.png",
    ]);
    expect(opts).toEqual({
      url: "https://example.test/x",
      out: "out.png",
      width: 375,
      height: 1200,
      fullPage: true,
    });
  });

  it("refuses anything but two positional arguments", () => {
    expect(() => parseArgs([])).toThrow(Usage);
    expect(() => parseArgs(["http://x.test/"])).toThrow(Usage);
    expect(() => parseArgs(["http://x.test/", "a.png", "b.png"])).toThrow(Usage);
  });

  it("refuses a URL that is not http or https", () => {
    expect(() => parseArgs(["file:///etc/passwd", "out.png"])).toThrow("not an http:// or https:// URL");
    expect(() => parseArgs(["/workspace/index.html", "out.png"])).toThrow("not an http:// or https:// URL");
  });

  it("refuses an output that is not a png", () => {
    expect(() => parseArgs(["http://x.test/", "shot.jpg"])).toThrow("must end in .png");
  });

  it("refuses a dimension that is not a sane pixel count", () => {
    expect(() => parseArgs(["--width", "0", "http://x.test/", "o.png"])).toThrow("--width");
    expect(() => parseArgs(["--width", "-10", "http://x.test/", "o.png"])).toThrow("--width");
    expect(() => parseArgs(["--width", "wide", "http://x.test/", "o.png"])).toThrow("--width");
    expect(() => parseArgs(["--height", String(MaxPixels + 1), "http://x.test/", "o.png"])).toThrow("--height");
    expect(() => parseArgs(["--height", "http://x.test/", "o.png"])).toThrow("--height");
  });

  it("names an unknown option rather than treating it as a path", () => {
    expect(() => parseArgs(["--pdf", "http://x.test/", "o.png"])).toThrow("unknown option --pdf");
  });
});
