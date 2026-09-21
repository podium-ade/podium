import { describe, expect, it } from "vitest";
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
