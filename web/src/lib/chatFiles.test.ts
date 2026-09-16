import { describe, expect, it } from "vitest";
import {
  addFiles,
  composeWithFiles,
  fileKind,
  modelAcceptsImages,
  type PendingFile,
} from "./chatFiles";

function file(name: string, body: string, type: string): File {
  return new File([body], name, { type });
}

function pending(over: Partial<PendingFile> & Pick<PendingFile, "name" | "kind">): PendingFile {
  return {
    id: over.id ?? over.name,
    type: over.type ?? "",
    size: over.size ?? 1,
    file: over.file ?? file(over.name, over.text ?? "", over.type ?? "text/plain"),
    ...over,
  };
}

describe("fileKind", () => {
  it("classifies images, text and everything else", () => {
    expect(fileKind(file("shot.png", "x", "image/png"))).toBe("image");
    expect(fileKind(file("notes.csv", "a,b", "text/csv"))).toBe("text");
    expect(fileKind(file("app.go", "package main", ""))).toBe("text");
    expect(fileKind(file("report.pdf", "%PDF", "application/pdf"))).toBe("file");
  });
});

describe("modelAcceptsImages", () => {
  it("accepts the catalogue models", () => {
    expect(modelAcceptsImages("claude-opus-5")).toBe(true);
    expect(modelAcceptsImages("grok-4.6")).toBe(true);
  });
});

describe("addFiles", () => {
  it("inlines a text file and previews an image", async () => {
    const { added, errors } = await addFiles(
      [file("notes.txt", "hello stack", "text/plain"), file("shot.png", "PNG", "image/png")],
      [],
      { acceptImages: true },
    );
    expect(errors).toEqual([]);
    expect(added).toHaveLength(2);
    expect(added[0].kind).toBe("text");
    expect(added[0].text).toBe("hello stack");
    expect(added[1].kind).toBe("image");
    expect(added[1].previewUrl).toBeTruthy();
  });

  it("refuses an image when the model does not take them", async () => {
    const { added, errors } = await addFiles([file("shot.png", "PNG", "image/png")], [], {
      acceptImages: false,
    });
    expect(added).toEqual([]);
    expect(errors[0]).toMatch(/does not take images/);
  });

  it("caps how many files one message may carry", async () => {
    const current = Array.from({ length: 8 }, (_, i) =>
      pending({ name: `a${i}.txt`, kind: "text" }),
    );
    const { added, errors } = await addFiles([file("more.txt", "x", "text/plain")], current, {
      acceptImages: true,
    });
    expect(added).toEqual([]);
    expect(errors[0]).toMatch(/At most 8/);
  });
});

describe("composeWithFiles", () => {
  it("is a no-op without files", () => {
    expect(composeWithFiles("  hello  ", [])).toBe("hello");
  });

  it("inlines a text file as a fenced block", () => {
    const out = composeWithFiles("look", [
      pending({ name: "notes.csv", kind: "text", text: "a,b\n1,2", type: "text/csv" }),
    ]);
    expect(out).toContain("look");
    expect(out).toContain("Attached `notes.csv`");
    expect(out).toContain("```csv");
    expect(out).toContain("a,b\n1,2");
  });

  it("names an image rather than inlining pixels", () => {
    const out = composeWithFiles("", [
      pending({ name: "shot.png", kind: "image", type: "image/png", size: 2048 }),
    ]);
    expect(out).toContain("Attached image `shot.png`");
    expect(out).toContain("2.0 KB");
    expect(out).not.toContain("PNG");
  });
});
