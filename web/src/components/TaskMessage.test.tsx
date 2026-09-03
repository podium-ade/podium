import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { detailOf } from "../hooks/useTaskEvents";
import { messageTone } from "../lib/format";
import { messageEvent } from "../test/events";
import { TaskMessage } from "./TaskMessage";

function message(type: string, text: string, attachments: string[] = []) {
  const ev = messageEvent(1, type, text, attachments);
  if (ev.payload.case !== "message") throw new Error("not a message event");
  return ev.payload.value;
}

describe("detailOf", () => {
  it("summarises a message as its type and first line", () => {
    expect(detailOf(messageEvent(1, "final", "the answer\nand the rest"))).toBe(
      "final: the answer",
    );
  });

  it("truncates a long first line", () => {
    const detail = detailOf(messageEvent(1, "progress", "x".repeat(400)));
    expect(detail).toBe(`progress: ${"x".repeat(200)}…`);
  });
});

describe("messageTone", () => {
  it("distinguishes an answer from a progress note and from anything else", () => {
    expect(messageTone("final")).toBe("ok");
    expect(messageTone("progress")).toBe("run");
    expect(messageTone("plan")).toBe("idle");
    expect(messageTone("")).toBe("idle");
  });
});

describe("TaskMessage", () => {
  it("shows the type, the whole text and every attachment", () => {
    render(<TaskMessage message={message("final", "line one\nline two", ["a.png", "b.png"])} />);

    expect(screen.getByText("final")).toBeInTheDocument();
    // The text is one node with its newlines intact: whitespace is preserved, not collapsed.
    expect(screen.getByText(/line one/)).toHaveTextContent("line one line two");
    expect(screen.getByText("a.png")).toBeInTheDocument();
    expect(screen.getByText("b.png")).toBeInTheDocument();
  });

  it("renders the text verbatim rather than interpreting it", () => {
    const hostile = "**bold** <b>markup</b> [link](https://example.com)";
    const { container } = render(<TaskMessage message={message("final", hostile)} />);

    expect(screen.getByText(hostile)).toBeInTheDocument();
    expect(container.querySelector("b")).toBeNull();
    expect(container.querySelector("a")).toBeNull();
  });

  it("says nothing about attachments when there are none", () => {
    const { container } = render(<TaskMessage message={message("progress", "working")} />);
    expect(container.textContent).toBe("progressworking");
  });

  it("labels a message whose type the task left empty", () => {
    render(<TaskMessage message={message("", "typeless")} />);
    expect(screen.getByText("message")).toBeInTheDocument();
  });
});
