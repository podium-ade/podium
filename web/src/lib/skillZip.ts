/**
 * A store-only zip of a skill folder, so the browser can upload the same bytes the conductor
 * already accepts as an archive. Compression is pointless at these sizes; the backend unpacks
 * under its own caps either way.
 */

const CRC_TABLE = (() => {
  const table = new Uint32Array(256);
  for (let n = 0; n < 256; n++) {
    let c = n;
    for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
    table[n] = c >>> 0;
  }
  return table;
})();

function crc32(data: Uint8Array): number {
  let c = 0xffffffff;
  for (let i = 0; i < data.length; i++) c = CRC_TABLE[(c ^ data[i]) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

function u16(n: number): Uint8Array {
  return Uint8Array.of(n & 0xff, (n >>> 8) & 0xff);
}

function u32(n: number): Uint8Array {
  return Uint8Array.of(n & 0xff, (n >>> 8) & 0xff, (n >>> 16) & 0xff, (n >>> 24) & 0xff);
}

function concat(parts: Uint8Array[]): Uint8Array {
  const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
  let off = 0;
  for (const p of parts) {
    out.set(p, off);
    off += p.length;
  }
  return out;
}

export type SkillZipEntry = { path: string; data: Uint8Array };

/**
 * zipSkillFolder packs the files of a picked directory. Paths must be relative, with `/`
 * separators and no `..`. A wrapping folder (the usual "I selected the skill's directory"
 * shape) is left in place: the conductor strips it the same way it strips `zip -r`.
 */
export function zipSkillFolder(files: SkillZipEntry[]): Uint8Array {
  const locals: Uint8Array[] = [];
  const centrals: Uint8Array[] = [];
  let offset = 0;

  for (const file of files) {
    const name = new TextEncoder().encode(file.path);
    const crc = crc32(file.data);
    const local = concat([
      Uint8Array.of(0x50, 0x4b, 0x03, 0x04),
      u16(20),
      u16(0),
      u16(0),
      u16(0),
      u16(0),
      u32(crc),
      u32(file.data.length),
      u32(file.data.length),
      u16(name.length),
      u16(0),
      name,
      file.data,
    ]);
    const central = concat([
      Uint8Array.of(0x50, 0x4b, 0x01, 0x02),
      u16(20),
      u16(20),
      u16(0),
      u16(0),
      u16(0),
      u16(0),
      u32(crc),
      u32(file.data.length),
      u32(file.data.length),
      u16(name.length),
      u16(0),
      u16(0),
      u16(0),
      u16(0),
      u32(0),
      u32(offset),
      name,
    ]);
    locals.push(local);
    centrals.push(central);
    offset += local.length;
  }

  const cd = concat(centrals);
  const eocd = concat([
    Uint8Array.of(0x50, 0x4b, 0x05, 0x06),
    u16(0),
    u16(0),
    u16(files.length),
    u16(files.length),
    u32(cd.length),
    u32(offset),
    u16(0),
  ]);
  return concat([...locals, cd, eocd]);
}

function skipPath(rel: string): boolean {
  if (rel === ".DS_Store" || rel.endsWith("/.DS_Store")) return true;
  if (rel.startsWith("__MACOSX/") || rel.includes("/__MACOSX/")) return true;
  return rel.split("/").some((part) => part.startsWith("._"));
}

/** readDirectoryFiles loads the bytes of a directory pick, dropping archiver noise. */
export async function readDirectoryFiles(list: FileList | File[]): Promise<SkillZipEntry[]> {
  const files = Array.from(list);
  const out: SkillZipEntry[] = [];
  for (const file of files) {
    const rel = (file.webkitRelativePath || file.name).replaceAll("\\", "/");
    if (rel === "" || skipPath(rel)) continue;
    out.push({ path: rel, data: new Uint8Array(await file.arrayBuffer()) });
  }
  return out;
}
