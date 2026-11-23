import { afterEach, describe, expect, it } from "vitest";
import { render } from "@testing-library/react";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./table";

function grid() {
  return (
    <Table>
      <TableHeader>
        <TableRow>
          <TableHead>Name</TableHead>
          <TableHead pinned>Actions</TableHead>
        </TableRow>
      </TableHeader>
      <TableBody>
        <TableRow>
          <TableCell>alpha</TableCell>
          <TableCell pinned>go</TableCell>
        </TableRow>
      </TableBody>
    </Table>
  );
}

function measure(el: HTMLElement, width: number, visible: number, left: number) {
  Object.defineProperty(el, "scrollWidth", { configurable: true, get: () => width });
  Object.defineProperty(el, "clientWidth", { configurable: true, get: () => visible });
  Object.defineProperty(el, "scrollLeft", { configurable: true, get: () => left });
  el.dispatchEvent(new Event("scroll"));
}

describe("Table pinned column fade", () => {
  it("shows the overflow mark only while content is still hidden under the frozen column", () => {
    const { container } = render(grid());
    const scroller = container.querySelector("[data-slot=table-container]");
    expect(scroller).toBeInstanceOf(HTMLElement);
    const el = scroller as HTMLElement;

    measure(el, 800, 800, 0);
    expect(el).not.toHaveAttribute("data-overflow-end");

    measure(el, 1200, 800, 0);
    expect(el).toHaveAttribute("data-overflow-end");

    measure(el, 1200, 800, 400);
    expect(el).not.toHaveAttribute("data-overflow-end");
  });
});

/*
 * jsdom has no layout: give the table's scroller a content width and a visible width, and
 * every element a scrollLeft that holds what it is set to.
 */
function layout(content: number, visible: number) {
  const left = new WeakMap<Element, number>();
  const proto = HTMLElement.prototype;
  const slot = (el: HTMLElement) => el.dataset.slot;
  Object.defineProperty(proto, "scrollWidth", {
    configurable: true,
    get(this: HTMLElement) {
      return slot(this) === "table-container" ? content : 0;
    },
  });
  Object.defineProperty(proto, "clientWidth", {
    configurable: true,
    get(this: HTMLElement) {
      return slot(this) === "table-container" || slot(this) === "sticky-scroll-x" ? visible : 0;
    },
  });
  Object.defineProperty(proto, "scrollLeft", {
    configurable: true,
    get(this: HTMLElement) {
      return left.get(this) ?? 0;
    },
    set(this: HTMLElement, v: number) {
      left.set(this, v);
    },
  });
}


function wide() {
  return (
    <Table stickyScrollbar>
      <TableBody>
        <TableRow>
          <TableCell>alpha</TableCell>
        </TableRow>
      </TableBody>
    </Table>
  );
}

describe("Table sticky scrollbar", () => {
  afterEach(() => {
    // The real ones live on Element.prototype, which this uncovers again.
    for (const k of ["scrollWidth", "clientWidth", "scrollLeft"]) Reflect.deleteProperty(HTMLElement.prototype, k);
  });

  it("draws the strip only while the table overflows sideways", () => {
    layout(800, 800);
    const fits = render(wide());
    const strip = fits.container.querySelector("[data-slot=sticky-scroll-x]");
    expect(strip).toBeInstanceOf(HTMLElement);
    expect(strip).toHaveAttribute("hidden");
    expect(strip).toHaveAttribute("aria-hidden", "true");
    fits.unmount();

    layout(1200, 800);
    const { container } = render(wide());
    const bar = container.querySelector("[data-slot=sticky-scroll-x]") as HTMLElement;
    expect(bar).not.toHaveAttribute("hidden");
    expect((bar.firstElementChild as HTMLElement).style.width).toBe("1200px");
    expect(container.querySelector("[data-slot=table-container]")).toHaveClass("scroll-x-strip-target");
  });

  it("keeps the strip and the table scrolled to the same place, both ways", () => {
    layout(1200, 800);
    const { container } = render(wide());
    const el = container.querySelector("[data-slot=table-container]") as HTMLElement;
    const bar = container.querySelector("[data-slot=sticky-scroll-x]") as HTMLElement;

    bar.scrollLeft = 300;
    bar.dispatchEvent(new Event("scroll"));
    expect(el.scrollLeft).toBe(300);

    el.scrollLeft = 120;
    el.dispatchEvent(new Event("scroll"));
    expect(bar.scrollLeft).toBe(120);
  });

  it("leaves a table without it as it was", () => {
    const { container } = render(grid());
    expect(container.querySelector("[data-slot=sticky-scroll-x]")).toBeNull();
  });
});

describe("Table footer", () => {
  it("draws the footer inside the table's card, under the scroller, and moves the border to the card", () => {
    const { container } = render(
      <Table footer={<span>pager</span>}>
        <TableBody>
          <TableRow>
            <TableCell>alpha</TableCell>
          </TableRow>
        </TableBody>
      </Table>,
    );
    const frame = container.querySelector("[data-slot=table-frame]") as HTMLElement;
    const scroller = frame.querySelector("[data-slot=table-container]") as HTMLElement;
    const footer = frame.querySelector("[data-slot=table-footer]") as HTMLElement;
    expect(frame).toHaveClass("border");
    expect(scroller).not.toHaveClass("border");
    expect(footer).toHaveTextContent("pager");
    expect(scroller.contains(footer)).toBe(false);
    expect(scroller.compareDocumentPosition(footer) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });
});
