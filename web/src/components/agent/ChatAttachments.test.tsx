import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { create } from "@bufbuild/protobuf";
import { ChatAttachmentSchema } from "../../gen/podium/agent/v1/agent_pb";
import { clearArtifactCache } from "../../lib/artifactCache";
import { setToken, clearToken } from "../../lib/auth";
import { ToastHost } from "../Toast";
import { ChatAttachments } from "./ChatAttachments";

const csv = create(ChatAttachmentSchema, {
  artifactId: "art_01csv",
  name: "report.csv",
  contentType: "text/csv",
  sizeBytes: 4096n,
});

const png = create(ChatAttachmentSchema, {
  artifactId: "art_02png",
  name: "trend.png",
  contentType: "image/png",
  sizeBytes: 91000n,
});

function mount(attachments = [png, csv]) {
  return render(
    <ToastHost>
      <ChatAttachments attachments={attachments} />
    </ToastHost>,
  );
}

/** The requests the component made, so the URL and the header can be asserted. */
let calls: { url: string; auth: string | null }[] = [];

/** The MIME type of every blob handed to createObjectURL. */
let blobTypes: string[] = [];

beforeEach(() => {
  calls = [];
  clearArtifactCache();
  setToken("devtoken-not-a-real-token");
  vi.stubGlobal(
    "fetch",
    vi.fn((url: string, init?: RequestInit) => {
      const headers = new Headers(init?.headers);
      calls.push({ url: String(url), auth: headers.get("Authorization") });
      return Promise.resolve(
        new Response(new Blob([new Uint8Array([137, 80, 78, 71])]), { status: 200 }),
      );
    }),
  );
  // jsdom implements neither object-URL method, so there is nothing to spy on: assign
  // them. They are left in place — React's own unmount cleanup calls revokeObjectURL after
  // this file's afterEach hooks, and each test file gets its own environment anyway.
  // Stubbing the whole URL global is not an option: spreading a class copies no statics.
  blobTypes = [];
  URL.createObjectURL = (b: Blob | MediaSource) => {
    blobTypes.push(b instanceof Blob ? b.type : "");
    return "blob:artifact";
  };
  URL.revokeObjectURL = () => {};
});

afterEach(() => {
  clearToken();
  vi.unstubAllGlobals();
});

describe("ChatAttachments", () => {
  it("renders nothing when a message has no files", () => {
    mount([]);
    expect(screen.queryAllByTestId("chat-attachment")).toHaveLength(0);
    expect(screen.queryByRole("img")).toBeNull();
  });

  it("renders an image inline, fetched with the bearer", async () => {
    mount([png]);

    // An <img src> cannot carry a header, and GET /artifacts/{id} is behind the same
    // identity as every RPC — so the bytes are fetched and shown from a blob URL.
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0].url).toBe("/artifacts/art_02png");
    expect(calls[0].auth).toBe("Bearer devtoken-not-a-real-token");

    const img = await waitFor(() => screen.getByRole("img"));
    expect(img).toHaveAttribute("src", "blob:artifact");
    expect(img).toHaveAttribute("alt", "trend.png");
    // Clicking opens the blob in a new tab; there is no lightbox.
    const link = screen.getByTestId("chat-attachment");
    expect(link).toHaveAttribute("href", "blob:artifact");
    expect(link).toHaveAttribute("target", "_blank");
    expect(link).toHaveAttribute("rel", expect.stringContaining("noopener"));
  });

  it("renders anything else as a download chip with its size", async () => {
    mount([csv]);
    const chip = screen.getByTestId("chat-attachment");
    expect(chip).toHaveTextContent("report.csv");
    expect(chip).toHaveTextContent("4.0 KB");
    // Nothing is fetched until the human asks for it.
    expect(calls).toHaveLength(0);

    await userEvent.click(chip);
    await waitFor(() => expect(calls).toHaveLength(1));
    expect(calls[0].url).toBe("/artifacts/art_01csv");
    expect(calls[0].auth).toBe("Bearer devtoken-not-a-real-token");
  });

  it("renders an image whose bytes cannot be read as a chip instead", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(new Response("gone", { status: 404 }))),
    );
    mount([png]);
    // A broken-image icon says nothing; a chip at least names the file and can be retried.
    await waitFor(() => expect(screen.queryByRole("img")).toBeNull());
    expect(screen.getByTestId("chat-attachment")).toHaveTextContent("trend.png");
  });

  it("renders an image the artifact store gave no content type at all", async () => {
    // The common case, not an edge one: a file the agent writes into
    // /workspace/.podium/artifacts/ is auto-collected with an empty content type, so the
    // name is what says it is a chart.
    const untyped = create(ChatAttachmentSchema, {
      artifactId: "art_03png",
      name: "trend.png",
      contentType: "",
      sizeBytes: 15311n,
    });
    mount([untyped]);
    const img = await waitFor(() => screen.getByRole("img"));
    expect(img).toHaveAttribute("alt", "trend.png");
    // And the blob was given an image type. A blob: URL typed application/octet-stream is
    // not sniffed by any browser: the <img> stays blank with naturalWidth 0.
    expect(blobTypes).toEqual(["image/png"]);
  });

  it("refuses to render an SVG inline, whatever it is called", async () => {
    // A blob: URL inherits this origin, the one holding the dev token, and the "click to
    // open" link would put a task-produced document on it. A chip cannot run anything.
    for (const svg of [
      create(ChatAttachmentSchema, {
        artifactId: "art_04svg",
        name: "chart.svg",
        contentType: "image/svg+xml",
        sizeBytes: 900n,
      }),
      create(ChatAttachmentSchema, {
        artifactId: "art_05svg",
        name: "chart.svg",
        contentType: "",
        sizeBytes: 900n,
      }),
    ]) {
      const { unmount } = mount([svg]);
      expect(screen.queryByRole("img")).toBeNull();
      expect(screen.getByTestId("chat-attachment")).toHaveTextContent("chart.svg");
      unmount();
    }
  });

  it("shows both kinds on one message", async () => {
    mount([png, csv]);
    await waitFor(() => expect(screen.getAllByTestId("chat-attachment")).toHaveLength(2));
    expect(screen.getByRole("img")).toHaveAttribute("alt", "trend.png");
    expect(screen.getAllByTestId("chat-attachment")[1]).toHaveTextContent("report.csv");
  });

  it("does not refetch an image this tab has already shown", async () => {
    const { unmount } = mount([png]);
    await waitFor(() => expect(calls).toHaveLength(1));
    unmount();

    mount([png]);
    const img = await waitFor(() => screen.getByRole("img"));
    expect(img).toHaveAttribute("src", "blob:artifact");
    expect(calls).toHaveLength(1);
  });
});
