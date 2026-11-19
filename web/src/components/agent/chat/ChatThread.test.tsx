import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import type { ThreadMessageLike } from "@assistant-ui/react";
import { ChatThread } from "./ChatThread";

// jsdom does no layout, so the scroller is given one: a view of `client` pixels over content
// `height` pixels tall, scrolled to `top`, which the browser clamps to what can be scrolled.
interface Layout {
  height: number;
  client: number;
  top: number;
}

function lay(el: HTMLElement, layout: Layout) {
  const max = () => Math.max(0, layout.height - layout.client);
  Object.defineProperty(el, "scrollHeight", { configurable: true, get: () => layout.height });
  Object.defineProperty(el, "clientHeight", { configurable: true, get: () => layout.client });
  Object.defineProperty(el, "scrollTop", {
    configurable: true,
    get: () => layout.top,
    set: (v: number) => {
      layout.top = Math.min(Math.max(0, v), max());
    },
  });
  el.scrollTo = ((opts: ScrollToOptions) => {
    layout.top = Math.min(Math.max(0, opts.top ?? layout.top), max());
    el.dispatchEvent(new Event("scroll"));
  }) as typeof el.scrollTo;
}

// The ResizeObservers are held so a test can say the content changed size — the change that
// comes with no scroll and no DOM mutation: markdown laid out, an image or a font loaded.
let observers: { cb: ResizeObserverCallback; self: ResizeObserver }[] = [];
const Original = globalThis.ResizeObserver;

function resized() {
  act(() => {
    for (const o of observers) o.cb([], o.self);
  });
}

const messages: ThreadMessageLike[] = [
  { id: "u1", role: "user", content: [{ type: "text", text: "Look into the failing build" }] },
  {
    id: "a1",
    role: "assistant",
    content: [{ type: "text", text: "## Findings\n\n- one\n- two\n\nSee [the run](https://example.com)." }],
  },
];

function mount() {
  render(
    <QueryClientProvider client={new QueryClient()}>
      <MemoryRouter>
        <ChatThread botName="Podium" busy messages={messages} onSend={() => {}} />
      </MemoryRouter>
    </QueryClientProvider>,
  );
  return screen.getByTestId("chat-scroller");
}

const jumpButton = () => screen.queryByRole("button", { name: /jump to latest/i });

describe("ChatThread Jump to latest", () => {
  beforeEach(() => {
    observers = [];
    globalThis.ResizeObserver = class {
      constructor(cb: ResizeObserverCallback) {
        observers.push({ cb, self: this as unknown as ResizeObserver });
      }
      observe() {}
      unobserve() {}
      disconnect() {}
    } as unknown as typeof ResizeObserver;
  });
  afterEach(() => {
    globalThis.ResizeObserver = Original;
    vi.restoreAllMocks();
  });

  it("stays hidden, and the view stays at the bottom, as the content grows under a reader at the bottom", () => {
    const vp = mount();
    const layout = { height: 1000, client: 400, top: 600 };
    lay(vp, layout);
    resized();
    expect(jumpButton()).toBeNull();

    // The answer lays out taller after it landed, with nothing scrolled.
    layout.height = 1600;
    resized();
    expect(layout.top).toBe(1200);
    expect(jumpButton()).toBeNull();

    // A turn that goes quiet changes nothing more, and the button stays away.
    resized();
    expect(jumpButton()).toBeNull();
  });

  it("stays hidden at the bottom of a fractionally zoomed view, a pixel and a half short", () => {
    const vp = mount();
    const layout = { height: 1000.5, client: 399, top: 600 };
    lay(vp, layout);
    act(() => {
      vp.dispatchEvent(new Event("scroll"));
    });
    resized();
    expect(jumpButton()).toBeNull();
  });

  it("shows when the reader scrolls up, and takes them back down", () => {
    const vp = mount();
    const layout = { height: 1000, client: 400, top: 600 };
    lay(vp, layout);
    resized();

    act(() => {
      fireEvent.wheel(vp);
      layout.top = 200;
      vp.dispatchEvent(new Event("scroll"));
    });
    expect(jumpButton()).toBeInTheDocument();

    // Content changing size around a reader who left the bottom does not pull them down to it.
    layout.height = 1300;
    resized();
    expect(layout.top).toBeLessThan(900);
    expect(jumpButton()).toBeInTheDocument();

    act(() => {
      fireEvent.click(jumpButton()!);
    });
    expect(layout.top).toBe(900);
    expect(jumpButton()).toBeNull();
  });
});
