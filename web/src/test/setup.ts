import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

afterEach(cleanup);

if (!("DOMRect" in globalThis)) {
  globalThis.DOMRect = class {
    constructor(
      public x = 0,
      public y = 0,
      public width = 0,
      public height = 0,
    ) {}
    get top() {
      return this.y;
    }
    get left() {
      return this.x;
    }
    get right() {
      return this.x + this.width;
    }
    get bottom() {
      return this.y + this.height;
    }
    static fromRect(r?: DOMRectInit) {
      return new DOMRect(r?.x, r?.y, r?.width, r?.height);
    }
    toJSON() {
      return { ...this };
    }
  } as unknown as typeof DOMRect;
}

const box = () => new DOMRect(0, 0, 800, 400);

// jsdom's ResizeObserver, if it has one, reports 0×0. CodeMirror 6 measures in a
// loop until the box is non-zero, which hangs the suite. Always replace it.
globalThis.ResizeObserver = class {
  private readonly cb: ResizeObserverCallback;
  constructor(cb: ResizeObserverCallback) {
    this.cb = cb;
  }
  observe(target: Element) {
    this.cb(
      [
        {
          target,
          contentRect: box(),
          borderBoxSize: [{ inlineSize: 800, blockSize: 400 }],
          contentBoxSize: [{ inlineSize: 800, blockSize: 400 }],
          devicePixelContentBoxSize: [{ inlineSize: 800, blockSize: 400 }],
        } as ResizeObserverEntry,
      ],
      this,
    );
  }
  unobserve() {}
  disconnect() {}
};

const originalBox = Element.prototype.getBoundingClientRect;
Element.prototype.getBoundingClientRect = function () {
  if (this instanceof Element && (this.closest(".cm-editor") || this.closest(".yaml-cm"))) {
    return box();
  }
  return originalBox.call(this);
};
Range.prototype.getBoundingClientRect = () => box();
Range.prototype.getClientRects = () => [box()] as unknown as DOMRectList;

// Radix menus and selects call these on open; jsdom stubs neither.
if (!Element.prototype.hasPointerCapture) {
  Element.prototype.hasPointerCapture = () => false;
  Element.prototype.setPointerCapture = () => {};
  Element.prototype.releasePointerCapture = () => {};
}

if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {};
}

if (typeof URL.createObjectURL !== "function") {
  URL.createObjectURL = () => "blob:test";
  URL.revokeObjectURL = () => {};
}

// jsdom does no layout, and assistant-ui's thread viewport scrolls with it.
Element.prototype.scrollTo ??= function scrollTo() {};
