import { useState } from "react";
import { describe, expect, it } from "vitest";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { TagInput } from "./tag-input";

function Harness(props: Partial<Parameters<typeof TagInput>[0]> & { initial?: string[] }) {
  const [values, setValues] = useState<string[]>(props.initial ?? []);
  return (
    <>
      <label htmlFor="tags">Tags</label>
      <TagInput id="tags" testId="tags" noun="tag" {...props} values={values} onChange={setValues} />
      <output data-testid="values">{values.join("|")}</output>
    </>
  );
}

const values = () => screen.getByTestId("values").textContent;

describe("TagInput", () => {
  it("adds on Enter, a comma, a pasted list and leaving the field", async () => {
    render(<Harness />);
    const field = screen.getByLabelText("Tags");
    await userEvent.type(field, "one{Enter}two,");
    expect(values()).toBe("one|two");
    await userEvent.click(field);
    await userEvent.paste("three, four\nfive");
    expect(values()).toBe("one|two|three|four|five");
    await userEvent.type(field, "six");
    await userEvent.tab();
    expect(values()).toBe("one|two|three|four|five|six");
    expect(field).toHaveValue("");
  });

  it("keeps a comma when commas do not separate", async () => {
    render(<Harness commaSeparates={false} />);
    await userEvent.type(screen.getByLabelText("Tags"), "/^v{{1,2}$/{Enter}");
    expect(values()).toBe("/^v{1,2}$/");
  });

  it("takes two Backspaces to remove the last tag, and Escape lets go of it", async () => {
    render(<Harness initial={["a", "b"]} />);
    const field = screen.getByLabelText("Tags");
    await userEvent.type(field, "{Backspace}");
    expect(values()).toBe("a|b");
    expect(screen.getByText("b").closest("[data-selected]")).not.toBeNull();
    await userEvent.keyboard("{Escape}");
    expect(screen.getByText("b").closest("[data-selected]")).toBeNull();
    await userEvent.keyboard("{Backspace}{Backspace}");
    expect(values()).toBe("a");
  });

  it("does not add a value twice, ignoring case, and points at the one there", async () => {
    render(<Harness initial={["Alice"]} />);
    await userEvent.type(screen.getByLabelText("Tags"), "alice{Enter}");
    expect(values()).toBe("Alice");
    expect(within(screen.getByTestId("tags")).getByText("Alice").closest("[data-flash]")).not.toBeNull();
    expect(screen.getByText("Alice is already in the list")).toBeInTheDocument();
  });

  it("refuses a value validate rejects, and keeps it in the field to fix", async () => {
    render(<Harness validate={(v) => (v.includes(" ") ? "No spaces." : undefined)} />);
    const field = screen.getByLabelText("Tags");
    await userEvent.type(field, "a b{Enter}");
    expect(values()).toBe("");
    expect(field).toHaveValue("a b");
    expect(field).toHaveAttribute("aria-invalid", "true");
    expect(field).toHaveAccessibleDescription("No spaces.");
    await userEvent.type(field, "{Backspace}{Backspace}c{Enter}");
    expect(values()).toBe("ac");
    expect(field).not.toHaveAttribute("aria-invalid");
  });

  it("removes a tag with its button and offers the options not chosen", async () => {
    render(<Harness initial={["x"]} options={[{ value: "x", label: "X" }, { value: "y", label: "Y" }]} offer />);
    await userEvent.click(screen.getByRole("button", { name: "Y" }));
    expect(values()).toBe("x|y");
    await userEvent.click(within(screen.getByTestId("tags")).getByRole("button", { name: "Remove tag x" }));
    expect(values()).toBe("y");
    expect(screen.getByLabelText("Tags")).toHaveFocus();
    expect(screen.getByRole("button", { name: "X" })).toBeInTheDocument();
  });

  it("shows, once typing pauses, what Enter does with the value, and the options that match", async () => {
    render(
      <Harness
        initial={["bug"]}
        options={[
          { value: "infra", label: "infra · OPS" },
          { value: "info", label: "info" },
          { value: "bug", label: "bug" },
        ]}
        validate={(v) => (v.length > 6 ? "Too long." : undefined)}
      />,
    );
    const field = screen.getByLabelText("Tags");
    await userEvent.type(field, "inf");
    expect(screen.queryByRole("listbox")).toBeNull();
    const list = await screen.findByRole("listbox", { name: "Add a tag" });
    expect(within(list).getAllByRole("option").map((o) => o.textContent)).toEqual([
      "infAdd to list",
      "infra · OPS",
      "info",
    ]);
    expect(field).toHaveAttribute("aria-expanded", "true");

    // Arrow keys pick an option, and Enter adds that one rather than what was typed.
    await userEvent.keyboard("{ArrowDown}");
    expect(within(list).getAllByRole("option")[1]).toHaveAttribute("aria-selected", "true");
    await userEvent.keyboard("{Enter}");
    expect(values()).toBe("bug|infra");
    expect(screen.queryByRole("listbox")).toBeNull();

    // A click adds too, and the caret stays in the field.
    await userEvent.type(field, "zz");
    await userEvent.click(await screen.findByRole("option", { name: /zz/ }));
    expect(values()).toBe("bug|infra|zz");
    expect(field).toHaveFocus();

    // What cannot be added says why, in the row.
    await userEvent.type(field, "BUG");
    expect(await screen.findByRole("option", { name: /Already in the list/ })).toHaveAttribute("aria-disabled", "true");
    await userEvent.clear(field);
    await userEvent.type(field, "toolong!");
    expect(await screen.findByRole("option", { name: /Too long\./ })).toBeInTheDocument();

    // Escape closes the list and leaves the text.
    await userEvent.keyboard("{Escape}");
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(field).toHaveValue("toolong!");
  });
});
