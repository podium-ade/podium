import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { render, screen, waitFor, within } from "@testing-library/react";
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
    // One image is the gallery without its controls.
    expect(screen.getByTestId("chat-attachment")).toHaveAccessibleName("Open trend.png full size");
    expect(screen.queryByRole("button", { name: "Next image" })).toBeNull();
    expect(screen.queryByText(/ \/ /)).toBeNull();
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

describe("ChatAttachments gallery", () => {
  const shots = ["a", "b", "c"].map((n) =>
    create(ChatAttachmentSchema, {
      artifactId: `art_shot_${n}`,
      name: `shot-${n}.png`,
      contentType: "image/png",
      sizeBytes: 2048n,
    }),
  );

  /** The picture in the stage, not a thumbnail: thumbnails carry an empty alt. */
  const stageImage = () => screen.getByRole("img", { name: /shot-/ });

  it("shows one image at a time with a counter and a thumbnail for each", async () => {
    mount(shots);
    await waitFor(() => expect(stageImage()).toHaveAttribute("alt", "shot-a.png"));
    expect(screen.getByText(/1 \/ 3/)).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: /^Show image/ })).toHaveLength(3);
    // The thumbnails are not <img role=img> with names of their own: one named image only.
    expect(screen.getAllByRole("img", { name: /shot-/ })).toHaveLength(1);

    await userEvent.click(screen.getByRole("button", { name: "Next image" }));
    expect(stageImage()).toHaveAttribute("alt", "shot-b.png");
    expect(screen.getByText(/2 \/ 3/)).toBeInTheDocument();

    await userEvent.click(screen.getByRole("button", { name: /Show image 3 of 3/ }));
    expect(stageImage()).toHaveAttribute("alt", "shot-c.png");
    expect(screen.getByRole("button", { name: /Show image 3 of 3/ })).toHaveAttribute(
      "aria-current",
      "true",
    );
    // The ends do not wrap, and say so to assistive technology rather than vanishing.
    expect(screen.getByRole("button", { name: "Next image" })).toHaveAttribute(
      "aria-disabled",
      "true",
    );
    await userEvent.click(screen.getByRole("button", { name: "Next image" }));
    expect(stageImage()).toHaveAttribute("alt", "shot-c.png");
  });

  it("steps with the arrow keys from inside the gallery", async () => {
    mount(shots);
    await waitFor(() => expect(stageImage()).toBeInTheDocument());
    screen.getByTestId("chat-attachment").focus();
    await userEvent.keyboard("{ArrowRight}{ArrowRight}");
    expect(stageImage()).toHaveAttribute("alt", "shot-c.png");
    await userEvent.keyboard("{ArrowLeft}");
    expect(stageImage()).toHaveAttribute("alt", "shot-b.png");
  });

  it("opens a lightbox, steps once per arrow key, and closes on Escape with focus back", async () => {
    mount(shots);
    await waitFor(() => expect(stageImage()).toBeInTheDocument());
    const stage = screen.getByTestId("chat-attachment");
    await userEvent.click(stage);

    const box = await screen.findByRole("dialog");
    expect(box).toHaveAccessibleName("shot-a.png");
    expect(within(box).getByText(/1 \/ 3/)).toBeInTheDocument();
    // Focus is inside the dialog, not left behind on the page.
    expect(box).toContainElement(document.activeElement as HTMLElement);

    // The lightbox renders in a portal but React still bubbles its keys to ancestors:
    // one press must be one step, not two.
    await userEvent.keyboard("{ArrowRight}");
    expect(within(box).getByRole("img")).toHaveAttribute("alt", "shot-b.png");
    expect(within(box).getByText(/2 \/ 3/)).toBeInTheDocument();
    await userEvent.keyboard("{ArrowLeft}{ArrowLeft}");
    expect(within(box).getByRole("img")).toHaveAttribute("alt", "shot-a.png");

    await userEvent.keyboard("{Escape}");
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(stage).toHaveFocus();
    // And the gallery behind it is where the reader left it.
    expect(stageImage()).toHaveAttribute("alt", "shot-a.png");
  });

  it("keeps focus inside the lightbox when Tab runs off the end", async () => {
    mount(shots);
    await waitFor(() => expect(stageImage()).toBeInTheDocument());
    await userEvent.click(screen.getByTestId("chat-attachment"));
    const box = await screen.findByRole("dialog");
    for (let i = 0; i < 8; i++) {
      await userEvent.tab();
      expect(box).toContainElement(document.activeElement as HTMLElement);
    }
  });

  it("offers a failed image as a chip and leaves the rest in the gallery", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn((url: string) =>
        Promise.resolve(
          url.endsWith("art_shot_b")
            ? new Response("gone", { status: 404 })
            : new Response(new Blob([new Uint8Array([137, 80, 78, 71])]), { status: 200 }),
        ),
      ),
    );
    mount(shots);
    await waitFor(() => expect(screen.getByText(/1 \/ 2/)).toBeInTheDocument());
    expect(screen.getByRole("button", { name: "Download shot-b.png" })).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: /^Show image/ })).toHaveLength(2);
  });
});
