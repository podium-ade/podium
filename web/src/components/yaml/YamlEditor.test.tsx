import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { YamlEditor } from "./YamlEditor";

function Harness({ initial = "image: alpine:3\n" }: { initial?: string }) {
  const [value, setValue] = useState(initial);
  return <YamlEditor id="spec-yaml" label="Task spec YAML" value={value} onChange={setValue} />;
}

describe("YamlEditor", () => {
  it("keeps a labelled textarea so fill() and toHaveValue still work", () => {
    render(<Harness />);
    const editor = screen.getByLabelText("Task spec YAML");
    expect(editor).toHaveValue("image: alpine:3\n");
    fireEvent.change(editor, { target: { value: "image: redis:7-alpine\n" } });
    expect(screen.getByLabelText("Task spec YAML")).toHaveValue("image: redis:7-alpine\n");
  });

  it("reports decoder problems in the status bar", () => {
    render(
      <YamlEditor
        label="Doc"
        value={"image: alpine:3\nprivileged: true\n"}
        problems={[{ path: "privileged", message: "is not a task spec field", line: 2 }]}
      />,
    );
    expect(screen.getByText("1 problem")).toBeInTheDocument();
  });

  it("does not offer format when read-only", () => {
    render(<YamlEditor label="Doc" value={"image: alpine:3\n"} readOnly />);
    expect(screen.getByLabelText("Doc")).toHaveAttribute("readOnly");
    expect(screen.queryByRole("button", { name: "Format YAML" })).toBeNull();
  });
});
