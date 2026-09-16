/**
 * Chat composer attachments: what can be pasted, dropped or picked, and how a file
 * becomes part of the message the server actually stores.
 *
 * The send RPC is still text. Images and binary files cannot ride as pixels today — the
 * assistant's host turn is a text prompt — so an image becomes a named attachment line
 * and a text file is inlined (capped) as a fenced block the model can actually read.
 * The composer still previews images, which is the affordance Claude/Gemini/Grok train.
 */

import { humanBytes } from "./format";

/** Matches api.maxChatMessageBytes — a composed message that goes over is refused here first. */
export const MAX_MESSAGE_BYTES = 32 * 1024;

/** How many files one send may carry. */
export const MAX_CHAT_FILES = 8;

/** A single dropped file larger than this is refused, even if we only keep its name. */
export const MAX_FILE_BYTES = 8 * 1024 * 1024;

/** How much of a text file is copied into the message. */
export const MAX_INLINE_TEXT_BYTES = 16 * 1024;

const IMAGE_MIME = /^(image\/(png|jpe?g|gif|webp|avif|bmp))$/i;
const IMAGE_EXT = /\.(png|jpe?g|gif|webp|avif|bmp)$/i;

const TEXT_MIME =
  /^(text\/|application\/(json|xml|yaml|x-yaml|javascript|typescript|sql|csv|x-sh))/i;
const TEXT_EXT =
  /\.(txt|md|markdown|csv|tsv|json|ya?ml|xml|html|css|js|jsx|mjs|cjs|ts|tsx|go|py|rs|java|kt|c|cc|cpp|h|hpp|rb|php|sh|bash|zsh|sql|log|toml|ini|env|diff|patch|graphql|proto)$/i;

/** Catalogue models are all multimodal. A typed-by-hand id is assumed to be too. */
const TEXT_ONLY = new Set<string>();

export type FileKind = "image" | "text" | "file";

export type PendingFile = {
  id: string;
  name: string;
  type: string;
  size: number;
  kind: FileKind;
  file: File;
  /** Object URL for an image preview. Revoked when the chip is removed. */
  previewUrl?: string;
  /** Inlined text, already capped. */
  text?: string;
};

export function modelAcceptsImages(modelId: string): boolean {
  return !TEXT_ONLY.has(modelId);
}

export function fileKind(file: File): FileKind {
  if (IMAGE_MIME.test(file.type) || (file.type === "" && IMAGE_EXT.test(file.name))) {
    return "image";
  }
  if (TEXT_MIME.test(file.type) || TEXT_EXT.test(file.name)) return "text";
  return "file";
}

/** The <input accept> list, so the picker itself hides what this model will refuse. */
export function acceptList(acceptImages: boolean): string {
  const text =
    ".txt,.md,.csv,.json,.yaml,.yml,.xml,.html,.css,.js,.ts,.tsx,.jsx,.go,.py,.rs,.sql,.sh,.log,.toml";
  return acceptImages ? `image/png,image/jpeg,image/gif,image/webp,image/avif,${text}` : text;
}

export type AddFilesResult = {
  added: PendingFile[];
  errors: string[];
};

/**
 * readFile turns one File into a PendingFile. It never throws: a read failure becomes a
 * binary chip (name and size only) rather than dropping the attach.
 */
export async function readFile(file: File): Promise<PendingFile> {
  const kind = fileKind(file);
  const id = `${file.name}:${file.size}:${file.lastModified}:${Math.random().toString(36).slice(2, 8)}`;
  const pending: PendingFile = {
    id,
    name: file.name || "untitled",
    type: file.type,
    size: file.size,
    kind,
    file,
  };
  if (kind === "image") {
    try {
      pending.previewUrl = URL.createObjectURL(file);
    } catch {
      // jsdom, or a browser that will not mint a blob URL — the chip still names the file.
    }
    return pending;
  }
  if (kind === "text") {
    try {
      const raw = await readText(file);
      pending.text =
        raw.length > MAX_INLINE_TEXT_BYTES
          ? `${raw.slice(0, MAX_INLINE_TEXT_BYTES)}\n…(truncated)`
          : raw;
    } catch {
      pending.kind = "file";
    }
  }
  return pending;
}

function readText(file: File): Promise<string> {
  if (typeof file.text === "function") return file.text();
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result ?? ""));
    reader.onerror = () => reject(reader.error ?? new Error("could not read file"));
    reader.readAsText(file);
  });
}

/**
 * addFiles applies the caps and the model gate, returning what was accepted and the
 * sentences to show for what was not.
 */
export async function addFiles(
  incoming: File[],
  current: PendingFile[],
  opts: { acceptImages: boolean },
): Promise<AddFilesResult> {
  const errors: string[] = [];
  const added: PendingFile[] = [];
  let remaining = MAX_CHAT_FILES - current.length;

  for (const file of incoming) {
    if (remaining <= 0) {
      errors.push(`At most ${MAX_CHAT_FILES} files per message.`);
      break;
    }
    if (file.size > MAX_FILE_BYTES) {
      errors.push(`${file.name} is ${humanBytes(file.size)}, over the ${humanBytes(MAX_FILE_BYTES)} limit.`);
      continue;
    }
    if (file.size === 0) {
      errors.push(`${file.name} is empty.`);
      continue;
    }
    const kind = fileKind(file);
    if (kind === "image" && !opts.acceptImages) {
      errors.push(`${file.name}: this model does not take images.`);
      continue;
    }
    const duplicate = current.some((f) => f.name === file.name && f.size === file.size);
    if (duplicate || added.some((f) => f.name === file.name && f.size === file.size)) {
      continue;
    }
    added.push(await readFile(file));
    remaining--;
  }
  return { added, errors };
}

export function revokePreview(file: PendingFile): void {
  if (file.previewUrl) URL.revokeObjectURL(file.previewUrl);
}

/**
 * composeWithFiles is the text SendChatMessage actually stores. Text files are inlined;
 * images and other binaries are named. Empty user text with only files is allowed — the
 * attachment lines are the message.
 */
export function composeWithFiles(text: string, files: PendingFile[]): string {
  const body = text.trim();
  if (files.length === 0) return body;

  const chunks: string[] = [];
  if (body !== "") chunks.push(body);

  for (const f of files) {
    if (f.kind === "text" && f.text !== undefined && f.text !== "") {
      const fence = fenceFor(f.name);
      chunks.push(`Attached \`${f.name}\`:\n${fence}\n${trimTrailingNewline(f.text)}\n${fence}`);
      continue;
    }
    const size = humanBytes(f.size);
    const type = f.type || "unknown type";
    if (f.kind === "image") {
      chunks.push(`Attached image \`${f.name}\` (${type}, ${size}).`);
    } else {
      chunks.push(`Attached file \`${f.name}\` (${type}, ${size}).`);
    }
  }

  let out = chunks.join("\n\n");
  if (byteLength(out) <= MAX_MESSAGE_BYTES) return out;

  // Prefer keeping the human's words; trim inlined file bodies from the end.
  out = body;
  for (const f of files) {
    const next =
      out === ""
        ? nameOnly(f)
        : `${out}\n\n${f.kind === "text" && f.text ? nameOnly(f) : nameOnly(f)}`;
    if (byteLength(next) > MAX_MESSAGE_BYTES) break;
    out = next;
  }
  if (out === "") out = "Attached files.";
  return out;
}

function nameOnly(f: PendingFile): string {
  return `Attached \`${f.name}\` (${humanBytes(f.size)}).`;
}

function fenceFor(name: string): string {
  const ext = name.includes(".") ? name.slice(name.lastIndexOf(".") + 1).toLowerCase() : "";
  const lang: Record<string, string> = {
    md: "markdown",
    markdown: "markdown",
    json: "json",
    yml: "yaml",
    yaml: "yaml",
    ts: "ts",
    tsx: "tsx",
    js: "js",
    jsx: "jsx",
    py: "python",
    go: "go",
    rs: "rust",
    sql: "sql",
    sh: "bash",
    bash: "bash",
    csv: "csv",
    html: "html",
    css: "css",
    toml: "toml",
  };
  const tag = lang[ext] ?? "";
  return "```" + tag;
}

function trimTrailingNewline(s: string): string {
  return s.endsWith("\n") ? s.slice(0, -1) : s;
}

function byteLength(s: string): number {
  return new TextEncoder().encode(s).length;
}
