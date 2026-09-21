import { ChevronRight, File, Folder } from "lucide-react";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "../ui/collapsible";
import { cn } from "../../lib/utils";

export type SkillTreeNode = {
  name: string
  path: string
  kind: "file" | "folder"
  children: SkillTreeNode[]
}

type Dir = { dirs: Map<string, Dir>; files: string[] }

/** buildSkillTree turns relative paths into folders-then-files, sorted by name. */
export function buildSkillTree(paths: string[]): SkillTreeNode[] {
  const root: Dir = { dirs: new Map(), files: [] }
  for (const path of paths) {
    const parts = path.split("/").filter((part) => part !== "")
    if (parts.length === 0) continue
    let cur = root
    for (let i = 0; i < parts.length - 1; i++) {
      const name = parts[i]!
      let next = cur.dirs.get(name)
      if (!next) {
        next = { dirs: new Map(), files: [] }
        cur.dirs.set(name, next)
      }
      cur = next
    }
    cur.files.push(parts[parts.length - 1]!)
  }
  return nodesFrom(root, "")
}

function nodesFrom(dir: Dir, prefix: string): SkillTreeNode[] {
  const folders = [...dir.dirs.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([name, child]) => {
      const path = prefix ? `${prefix}/${name}` : name
      return { name, path, kind: "folder" as const, children: nodesFrom(child, path) }
    })
  const files = [...dir.files].sort((a, b) => a.localeCompare(b)).map((name) => {
    const path = prefix ? `${prefix}/${name}` : name
    return { name, path, kind: "file" as const, children: [] }
  })
  return [...folders, ...files]
}

/**
 * SkillFileTree is the skill directory. shadcn has no file-tree component; this is a
 * collapsible list of the paths the conductor returned.
 */
export function SkillFileTree({
  paths,
  selected,
  onSelect,
}: {
  paths: string[]
  selected: string
  onSelect: (path: string) => void
}) {
  const tree = buildSkillTree(paths)
  return (
    <div data-testid="skill-file-tree" className="rounded-xl border border-border bg-card">
      <div className="border-b border-hairline px-3 py-2 text-xs font-medium text-muted">Files</div>
      <div className="p-1.5">
        {tree.map((node) => (
          <TreeNode key={node.path} node={node} selected={selected} onSelect={onSelect} depth={0} />
        ))}
      </div>
    </div>
  )
}

function TreeNode({
  node,
  selected,
  onSelect,
  depth,
}: {
  node: SkillTreeNode
  selected: string
  onSelect: (path: string) => void
  depth: number
}) {
  const pad = { paddingLeft: 8 + depth * 12 }
  if (node.kind === "file") {
    const active = node.path === selected
    return (
      <button
        type="button"
        data-testid="skill-tree-file"
        aria-label={`Open ${node.path}`}
        aria-current={active ? "true" : undefined}
        onClick={() => onSelect(node.path)}
        className={cn(
          "flex w-full items-center gap-1.5 rounded-md py-1 pr-2 text-left font-mono text-xs transition-colors",
          active ? "bg-accent/15 text-fg" : "text-muted hover:bg-raised hover:text-fg",
        )}
        style={pad}
      >
        <File className="size-3.5 shrink-0 text-faint" />
        <span className="truncate">{node.name}</span>
      </button>
    )
  }
  return (
    <Collapsible defaultOpen>
      <CollapsibleTrigger
        type="button"
        className="group flex w-full items-center gap-1.5 rounded-md py-1 pr-2 text-left font-mono text-xs text-fg transition-colors hover:bg-raised"
        style={pad}
      >
        <ChevronRight className="size-3.5 shrink-0 text-faint transition-transform group-data-[state=open]:rotate-90" />
        <Folder className="size-3.5 shrink-0 text-faint" />
        <span className="truncate">{node.name}</span>
      </CollapsibleTrigger>
      <CollapsibleContent>
        {node.children.map((child) => (
          <TreeNode key={child.path} node={child} selected={selected} onSelect={onSelect} depth={depth + 1} />
        ))}
      </CollapsibleContent>
    </Collapsible>
  )
}
