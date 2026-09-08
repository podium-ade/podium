import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { cachedImageURL, clearArtifactCache } from "./artifactCache";
import { setToken, clearToken } from "./auth";

let calls: { url: string; auth: string | null }[] = [];
let blobTypes: string[] = [];

function pngResponse(): Response {
  return new Response(new Blob([new Uint8Array([137, 80, 78, 71])]), { status: 200 });
}

beforeEach(() => {
  calls = [];
  blobTypes = [];
  clearArtifactCache();
  setToken("devtoken-not-a-real-token");
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string, init?: RequestInit) => {
      const headers = new Headers(init?.headers);
      calls.push({ url: String(url), auth: headers.get("Authorization") });
      return Promise.resolve(pngResponse());
    }),
  );
  URL.createObjectURL = (b: Blob | MediaSource) => {
    blobTypes.push(b instanceof Blob ? b.type : "");
    return `blob:${blobTypes.length}`;
  };
  URL.revokeObjectURL = () => {};
});

afterEach(() => {
  clearArtifactCache();
  clearToken();
  vi.unstubAllGlobals();
});

describe("artifactCache", () => {
  it("fetches with the bearer and types an untyped blob as the image MIME", async () => {
    const url = await cachedImageURL("art_02png", "image/png");
    expect(calls).toEqual([{ url: "/artifacts/art_02png", auth: "Bearer devtoken-not-a-real-token" }]);
    expect(url).toBe("blob:1");
    expect(blobTypes).toEqual(["image/png"]);
  });

  it("serves a second lookup from memory without touching the network", async () => {
    const first = await cachedImageURL("art_02png", "image/png");
    const second = await cachedImageURL("art_02png", "image/png");
    expect(first).toBe(second);
    expect(calls).toHaveLength(1);
  });

  it("de-dupes concurrent lookups of the same artifact", async () => {
    const [a, b] = await Promise.all([
      cachedImageURL("art_02png", "image/png"),
      cachedImageURL("art_02png", "image/png"),
    ]);
    expect(a).toBe(b);
    expect(calls).toHaveLength(1);
  });

  it("reads a Cache Storage hit instead of refetching after memory is cleared", async () => {
    const store = new Map<string, Response>();
    vi.stubGlobal("caches", {
      open: async () => ({
        match: async (req: RequestInfo) => store.get(String(req)),
        put: async (req: RequestInfo, res: Response) => {
          store.set(String(req), res);
        },
      }),
    });

    await cachedImageURL("art_02png", "image/png");
    expect(calls).toHaveLength(1);

    clearArtifactCache();
    const url = await cachedImageURL("art_02png", "image/png");
    expect(url).toBe("blob:2");
    expect(calls).toHaveLength(1);
  });

  it("does not cache a failed download", async () => {
    const fetchMock = vi.fn(() => Promise.resolve(new Response("gone", { status: 404 })));
    vi.stubGlobal("fetch", fetchMock);
    await expect(cachedImageURL("art_missing", "image/png")).rejects.toThrow(/gone/);

    fetchMock.mockImplementation(() => Promise.resolve(pngResponse()));
    await cachedImageURL("art_missing", "image/png");
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });
});
