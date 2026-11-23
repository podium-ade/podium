import { describe, expect, it } from "vitest";
import { useState } from "react";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { Pager } from "./Pager";
import { usePager } from "../lib/usePager";

const NOUN = { one: "memory", other: "memories" };

/** ByTotal is a list of `total` rows paged on the client, as Channels pages its catalogue. */
function ByTotal({ total, loading = false }: { total: number; loading?: boolean }) {
  const [filter, setFilter] = useState("");
  const pager = usePager(filter);
  const count = Math.max(0, Math.min(pager.size, total - pager.offset));
  return (
    <>
      <button type="button" onClick={() => setFilter("x")}>
        filter
      </button>
      <span data-testid="at">{`${pager.offset}/${pager.size}`}</span>
      <Pager pager={pager} count={count} noun={NOUN} total={total} loading={loading} />
    </>
  );
}

/** ByCursor is a list paged forward from a server cursor, with no total, as Tasks is. */
function ByCursor({ cursors }: { cursors: Record<string, { count: number; next: string }> }) {
  const pager = usePager(null);
  const page = cursors[pager.cursor];
  return (
    <>
      <span data-testid="cursor">{pager.cursor || "first"}</span>
      <Pager pager={pager} count={page.count} noun={{ one: "task", other: "tasks" }} nextCursor={page.next} />
    </>
  );
}

const summary = () => screen.getByTestId("pager-summary");
const previous = () => screen.getByRole("button", { name: /previous/i });
const next = () => screen.getByRole("button", { name: /next/i });

describe("Pager", () => {
  it("says which rows of the total a page holds, and steps through them", async () => {
    const user = userEvent.setup();
    render(<ByTotal total={60} />);
    expect(summary()).toHaveTextContent("1–25 of 60 memories");
    expect(previous()).toBeDisabled();
    expect(next()).toBeEnabled();

    await user.click(next());
    expect(summary()).toHaveTextContent("26–50 of 60 memories");
    await user.click(next());
    expect(summary()).toHaveTextContent("51–60 of 60 memories");
    expect(next()).toBeDisabled();

    await user.click(previous());
    expect(summary()).toHaveTextContent("26–50 of 60 memories");
  });

  it("names one row in the singular", () => {
    render(<ByTotal total={1} />);
    expect(summary()).toHaveTextContent("1–1 of 1 memory");
    expect(next()).toBeDisabled();
  });

  it("without a total says how many are on which page, and pages by the server's cursor", async () => {
    const user = userEvent.setup();
    render(<ByCursor cursors={{ "": { count: 25, next: "c1" }, c1: { count: 3, next: "" } }} />);
    expect(summary()).toHaveTextContent("25 tasks on page 1");
    expect(previous()).toBeDisabled();

    await user.click(next());
    expect(screen.getByTestId("cursor")).toHaveTextContent("c1");
    expect(summary()).toHaveTextContent("3 tasks on page 2");
    expect(next()).toBeDisabled();

    await user.click(previous());
    expect(screen.getByTestId("cursor")).toHaveTextContent("first");
  });

  it("ignores Previous and Next while a page is on its way", async () => {
    const user = userEvent.setup();
    const { rerender } = render(<ByTotal total={100} />);
    await user.click(next());
    rerender(<ByTotal total={100} loading />);
    await user.click(next());
    await user.click(previous());
    expect(summary()).toHaveTextContent("26–50 of 100 memories");
  });

  it("starts again from the first page when the filter changes", async () => {
    const user = userEvent.setup();
    render(<ByTotal total={60} />);
    await user.click(next());
    expect(screen.getByTestId("at")).toHaveTextContent("25/25");
    await user.click(screen.getByRole("button", { name: "filter" }));
    expect(screen.getByTestId("at")).toHaveTextContent("0/25");
    expect(summary()).toHaveTextContent("1–25 of 60 memories");
  });

  it("offers 25, 50 and 100 rows, and a new size starts from the first page", async () => {
    const user = userEvent.setup();
    render(<ByTotal total={160} />);
    await user.click(next());
    await user.click(screen.getByRole("combobox", { name: "Rows" }));
    const options = await screen.findAllByRole("option");
    expect(options.map((o) => o.textContent)).toEqual(["25", "50", "100"]);
    await user.click(screen.getByRole("option", { name: "50" }));
    expect(screen.getByTestId("at")).toHaveTextContent("0/50");
    expect(summary()).toHaveTextContent("1–50 of 160 memories");
  });
});
