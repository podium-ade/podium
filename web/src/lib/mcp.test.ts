import { describe, expect, it } from "vitest";
import { presetFor, tokenHelp } from "./mcp";

describe("presetFor", () => {
  it("matches a known product by registration name", () => {
    expect(presetFor({ name: "linear" })?.label).toBe("Linear");
    expect(presetFor({ name: "notion" })?.tokenPlaceholder).toBe("ntn_…");
    expect(presetFor({ name: "slack" })?.tokenPlaceholder).toBe("xoxb-…");
    expect(presetFor({ name: "stripe" })?.url).toBe("https://mcp.stripe.com");
    expect(presetFor({ name: "figma" })?.url).toBe("https://mcp.figma.com/mcp");
  });

  it("matches a known product by endpoint host when the name is custom", () => {
    expect(presetFor({ name: "work", url: "https://mcp.sentry.dev/mcp" })?.name).toBe("sentry");
  });

  it("does not invent a product for an unknown URL", () => {
    expect(presetFor({ name: "internal", url: "https://mcp.example.internal/mcp" })).toBeUndefined();
  });
});

describe("tokenHelp", () => {
  it("uses the product's own key format", () => {
    const linear = presetFor({ name: "linear" });
    expect(tokenHelp(linear).placeholder).toBe("lin_api_…");
    expect(tokenHelp(linear).hint).toMatch(/lin_api_/);
  });

  it("falls back to a generic bearer hint for custom", () => {
    expect(tokenHelp(undefined).placeholder).toBe("");
    expect(tokenHelp(undefined).hint).toMatch(/Authorization: Bearer/);
  });
});
