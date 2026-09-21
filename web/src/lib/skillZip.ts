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

/** One skill directory, zipped the way UploadSkill unpacks it. SKILL.md sits at the archive root. */
export type SkillBundle = { filename: string; content: Uint8Array };

const SKILL_MD = "SKILL.md";

/**
 * bundleSkillFolders zips every directory in the pick that holds a SKILL.md.
 * Paths inside each zip are relative to that directory, so a wrapping folder name
 * does not become part of the bundle. Files that sit beside those directories are
 * left out. Returns null when the pick has no SKILL.md — the caller then treats
 * markdown files as separate skills.
 */
export function bundleSkillFolders(files: SkillZipEntry[]): SkillBundle[] | null {
  const kept = files.filter((f) => f.path !== "" && !skipPath(f.path));
  const roots = skillRoots(kept.map((f) => f.path));
  if (roots.length === 0) return null;

  const groups = new Map<string, SkillZipEntry[]>();
  for (const root of roots) groups.set(root, []);
  for (const file of kept) {
    const root = longestRoot(file.path, roots);
    if (root === null) continue;
    const rel = root === "" ? file.path : file.path.slice(root.length + 1);
    if (rel === "") continue;
    groups.get(root)?.push({ path: rel, data: file.data });
  }

  const bundles: SkillBundle[] = [];
  for (const [root, entries] of groups) {
    if (!entries.some((e) => e.path === SKILL_MD)) continue;
    const base = root === "" ? "skill" : (root.split("/").pop() ?? "skill");
    bundles.push({ filename: `${base}.zip`, content: zipSkillFolder(entries) });
  }
  return bundles.length > 0 ? bundles : null;
}

function skillRoots(paths: string[]): string[] {
  const roots: string[] = [];
  for (const path of paths) {
    if (path !== SKILL_MD && !path.endsWith(`/${SKILL_MD}`)) continue;
    const dir = path.slice(0, path.length - SKILL_MD.length).replace(/\/$/, "");
    if (!roots.includes(dir)) roots.push(dir);
  }
  return roots;
}

function longestRoot(path: string, roots: string[]): string | null {
  let best: string | null = null;
  for (const root of roots) {
    const inside = root === "" || path === root || path.startsWith(`${root}/`);
    if (!inside) continue;
    if (best === null || root.length > best.length) best = root;
  }
  return best;
}

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
