import { describe, expect, it, vi } from "vitest";

import { Client, DelegateError } from "./delegate.js";

/** capture is a fetch that records the call and answers with a canned body. */
function capture(status: number, body: unknown): { calls: { url: string; init: RequestInit }[]; impl: typeof fetch } {
  const calls: { url: string; init: RequestInit }[] = [];
  const impl = vi.fn(async (url: string | URL | Request, init?: RequestInit) => {
    calls.push({ url: String(url), init: init ?? {} });
    const text = typeof body === "string" ? body : JSON.stringify(body);
    return new Response(text, { status });
  });
  return { calls, impl: impl as unknown as typeof fetch };
}

describe("the delegation resources client", () => {
  it("posts a delegation and carries the token as a bearer, never in the body", async () => {
    const { calls, impl } = capture(200, { delegation: { id: "dlg_01", playbook: "podium", status: "running" } });
    const client = new Client("http://127.0.0.1:8090", "s3cret", impl);
    await client.delegate("podium", "fix it");

    expect(calls).toHaveLength(1);
    expect(calls[0]!.url).toBe("http://127.0.0.1:8090/v1/delegations");
    expect(calls[0]!.init.method).toBe("POST");
    const headers = calls[0]!.init.headers as Record<string, string>;
    expect(headers.Authorization).toBe("Bearer s3cret");
    expect(headers["Content-Type"]).toBe("application/json");
    expect(calls[0]!.init.body).toBe(JSON.stringify({ playbook: "podium", instruction: "fix it" }));
    expect(String(calls[0]!.init.body)).not.toContain("s3cret");
  });

  it("asks for a read-only task only when told to", async () => {
    const { calls, impl } = capture(200, { delegation: { id: "dlg_01", playbook: "podium", status: "running" } });
    const client = new Client("http://127.0.0.1:8090", "s3cret", impl);
    await client.delegate("podium", "why is it racy?", true);
    expect(calls[0]!.init.body).toBe(
      JSON.stringify({ playbook: "podium", instruction: "why is it racy?", read_only: true }),
    );
  });

  it("does not double the slash when the address has a trailing one", async () => {
    const { calls, impl } = capture(200, {});
    await new Client("http://127.0.0.1:8090/", "t", impl).list();
    expect(calls[0]!.url).toBe("http://127.0.0.1:8090/v1/delegations");
    expect(calls[0]!.init.method).toBe("GET");
  });

  it("reads the conductor's error document, so the model gets the conductor's own words", async () => {
    const { impl } = capture(400, {
      code: "invalid_argument",
      message: 'conductor: that playbook was not offered to this turn: "nope"',
    });
    const client = new Client("http://h", "t", impl);
    await expect(client.delegate("nope", "x")).rejects.toMatchObject({
      code: "invalid_argument",
      message: expect.stringContaining("was not offered"),
    });
    await expect(client.delegate("nope", "x")).rejects.toBeInstanceOf(DelegateError);
  });

  it("falls back to the status when the body is not the conductor's error", async () => {
    // What a proxy in front of the conductor returns, or a listener that is not the
    // conductor at all.
    const { impl } = capture(502, "<html>bad gateway</html>");
    await expect(new Client("http://h", "t", impl).list()).rejects.toMatchObject({
      code: "502",
      message: expect.stringContaining("bad gateway"),
    });
  });

  it("treats an empty 200 as an empty message", async () => {
    const { impl } = capture(200, "");
    await expect(new Client("http://h", "t", impl).list()).resolves.toEqual({});
  });

  it("fails loudly on a 200 that is not JSON rather than returning nonsense", async () => {
    const { impl } = capture(200, "not json at all");
    await expect(new Client("http://h", "t", impl).get("dlg_01")).rejects.toMatchObject({
      code: "internal",
      message: expect.stringContaining("not JSON"),
    });
  });

  it("addresses each delegation as a resource", async () => {
    const { calls, impl } = capture(200, {});
    const client = new Client("http://h/turn-surface", "t", impl);
    await client.get("dlg_01");
    await client.cancel("dlg_01", "why");
    await client.inject("dlg_01", "also this");
    await client.releasePreview("dlg_01");
    expect(calls.map((c) => `${c.init.method} ${c.url}`)).toEqual([
      "GET http://h/turn-surface/v1/delegations/dlg_01",
      "POST http://h/turn-surface/v1/delegations/dlg_01/cancel",
      "POST http://h/turn-surface/v1/delegations/dlg_01/messages",
      "DELETE http://h/turn-surface/v1/delegations/dlg_01/preview",
    ]);
    expect(calls[0]!.init.body).toBeUndefined();
    expect(calls[1]!.init.body).toBe(JSON.stringify({ reason: "why" }));
    expect(calls[2]!.init.body).toBe(JSON.stringify({ text: "also this" }));
  });
});
