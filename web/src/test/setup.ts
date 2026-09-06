import "@testing-library/jest-dom/vitest";
import { cleanup } from "@testing-library/react";
import { afterEach } from "vitest";

afterEach(cleanup);

// jsdom implements neither of these, and Radix reaches for both: its popper measures with a
// ResizeObserver, and Switch/Checkbox pull @radix-ui/react-use-size, which throws without one.
// Without the stubs any screen that unit-tests a menu, a select, a switch or a checkbox fails
// on mount rather than on anything it asserts.
if (!("ResizeObserver" in globalThis)) {
  globalThis.ResizeObserver = class {
    observe() {}
    unobserve() {}
    disconnect() {}
  };
}

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

// Radix menus and selects call these on open; jsdom stubs neither.
if (!Element.prototype.hasPointerCapture) {
  Element.prototype.hasPointerCapture = () => false;
  Element.prototype.setPointerCapture = () => {};
  Element.prototype.releasePointerCapture = () => {};
}

if (!Element.prototype.scrollIntoView) {
  Element.prototype.scrollIntoView = () => {};
}
