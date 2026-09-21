import { afterEach, describe, expect, it } from "vitest";
import { documentStyleNonce } from "./csp";

describe("documentStyleNonce", () => {
  afterEach(() => {
    document.querySelectorAll('meta[name="podium-csp-nonce"]').forEach((el) => el.remove());
  });

  it("reads the CSP nonce the server stamps on index.html", () => {
    const meta = document.createElement("meta");
    meta.name = "podium-csp-nonce";
    meta.content = "test-nonce";
    document.head.append(meta);
    expect(documentStyleNonce()).toBe("test-nonce");
  });

  it("is empty when the document has no nonce, as on the Vite dev server", () => {
    expect(documentStyleNonce()).toBe("");
  });
});
