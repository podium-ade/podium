import { useRef, useState } from "react";
import { ChevronLeft, FolderOpen, Paperclip, Trash2 } from "lucide-react";
import { bundleSkillFolders, readDirectoryFiles, type SkillBundle } from "../../lib/skillZip";
import { Button } from "../ui/button";
import { YamlEditor } from "../yaml/YamlEditor";
import { SkillFileTree } from "./SkillFileTree";

const STARTER = `---
name: pr-review
description: Use when reviewing a pull request.
---

`

export type SkillFile = { name: string; markdown: string }

/**
 * SkillEditor writes one SKILL.md, or a whole skill folder.
 * A folder that contains SKILL.md is one skill: the other files travel with it.
 * A folder of markdown files and no SKILL.md is still one skill per file.
 */
export function SkillEditor({
  existing,
  saving,
  deleting,
  error,
  onSubmit,
  onSubmitMany,
  onSubmitBundles,
  onDelete,
  onCancel,
}: {
  existing?: { name: string; markdown: string; fileCount: number; files?: Record<string, string> }
  saving: boolean
  deleting?: boolean
  error?: string
  onSubmit: (file: SkillFile) => void
  onSubmitMany: (files: SkillFile[]) => void
  onSubmitBundles: (bundles: SkillBundle[]) => void
  onDelete?: () => void
  onCancel: () => void
}) {
  const fileRef = useRef<HTMLInputElement>(null)
  const dirRef = useRef<HTMLInputElement>(null)
  const [markdown, setMarkdown] = useState(existing?.markdown || STARTER)
  const [filename, setFilename] = useState("SKILL.md")
  const [tried, setTried] = useState(false)
  const [pickError, setPickError] = useState("")
  const creating = !existing
  // Saving the text installs a directory that holds only SKILL.md.
  const folderLocked = (existing?.fileCount ?? 1) > 1
  const fileMap =
    existing && existing.files && Object.keys(existing.files).length > 0
      ? existing.files
      : existing
        ? { "SKILL.md": existing.markdown }
        : {}
  const paths = Object.keys(fileMap).sort((a, b) => a.localeCompare(b))
  const [selected, setSelected] = useState(paths.includes("SKILL.md") ? "SKILL.md" : (paths[0] ?? "SKILL.md"))
  const showingSkill = selected === "SKILL.md" || !Object.prototype.hasOwnProperty.call(fileMap, selected)

  function submit() {
    if (folderLocked) return
    setTried(true)
    if (markdown.trim() === "") return
    onSubmit({ name: filename, markdown })
  }

  async function loadFiles(list: FileList | null, folder: boolean) {
    if (!list || list.length === 0) return
    setPickError("")
    if (folder) {
      const picked = await readDirectoryFiles(list)
      const bundles = bundleSkillFolders(picked)
      if (bundles) {
        onSubmitBundles(bundles)
        return
      }
      if (folderLocked) {
        setPickError("That folder has no SKILL.md. Choose the skill's directory.")
        return
      }
    }
    const md = markdownFiles([...list])
    if (md.length === 0) return
    if (folder && md.length > 1) {
      const files: SkillFile[] = []
      for (const f of md) files.push({ name: f.name, markdown: await f.text() })
      onSubmitMany(files)
      return
    }
    const f = md[0]!
    setFilename(f.name)
    setMarkdown(await f.text())
    setTried(false)
  }

  return (
    <form
      data-testid="skill-editor"
      className="space-y-5"
      onSubmit={(e) => {
        e.preventDefault()
        submit()
      }}
    >
      <header className="space-y-3">
        <button
          type="button"
          onClick={onCancel}
          className="-ml-1 inline-flex items-center gap-1 rounded-md px-1 py-0.5 text-xs text-muted transition-colors hover:text-fg focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:outline-none"
        >
          <ChevronLeft className="size-3.5" />
          Skills
        </button>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <h1 className="text-xl leading-tight font-semibold tracking-tight text-fg">
            {creating ? "New skill" : existing.name}
          </h1>
          <div className="flex items-center gap-2">
            <input
              ref={fileRef}
              type="file"
              accept=".md,.markdown,text/markdown"
              className="hidden"
              onChange={(e) => {
                const files = e.target.files
                e.target.value = ""
                void loadFiles(files, false)
              }}
            />
            <input
              ref={(el) => {
                dirRef.current = el
                if (el) el.setAttribute("webkitdirectory", "")
              }}
              type="file"
              className="hidden"
              onChange={(e) => {
                const files = e.target.files
                e.target.value = ""
                void loadFiles(files, true)
              }}
            />
            {folderLocked ? null : (
              <Button type="button" variant="outline" size="sm" onClick={() => fileRef.current?.click()}>
                <Paperclip />
                Choose a file
              </Button>
            )}
            <Button type="button" variant="outline" size="sm" onClick={() => dirRef.current?.click()}>
              <FolderOpen />
              Choose a folder
            </Button>
          </div>
        </div>
      </header>

      <div className={existing && paths.length > 0 ? "grid items-start gap-4 lg:grid-cols-[15rem_minmax(0,1fr)]" : "contents"}>
        {existing && paths.length > 0 ? (
          <SkillFileTree paths={paths} selected={selected} onSelect={setSelected} />
        ) : null}
        <div className="min-w-0 space-y-5">
          {showingSkill ? (
            <>
              {folderLocked ? (
                <p data-testid="skill-folder-note" className="text-xs leading-relaxed text-muted">
                  This skill is a folder of {existing?.fileCount} files. Choose the folder to update it.
                  Saving the text below would replace that folder with this file.
                </p>
              ) : null}
              <YamlEditor
                id="skill-md"
                label="SKILL.md"
                language="markdown"
                value={markdown}
                onChange={folderLocked ? undefined : setMarkdown}
                readOnly={folderLocked}
                invalid={tried && markdown.trim() === ""}
                minLines={22}
              />
            </>
          ) : (
            <div className="space-y-2">
              <p className="font-mono text-xs text-muted">{selected}</p>
              <pre
                data-testid="skill-file-body"
                className="max-h-[32rem] overflow-auto rounded-lg border border-border bg-card p-3 font-mono text-xs leading-5 whitespace-pre-wrap text-fg"
              >
                {fileMap[selected]}
              </pre>
            </div>
          )}
        </div>
      </div>
      {tried && markdown.trim() === "" ? (
        <p className="text-xs text-err">Write the skill, or choose a file.</p>
      ) : null}
      {pickError ? <p className="text-xs text-err">{pickError}</p> : null}
      {error ? <p className="text-xs text-err">{error}</p> : null}

      <div
        data-testid="skill-actions"
        className="sticky bottom-0 z-10 flex flex-wrap items-center gap-2 rounded-xl border border-border bg-panel/95 px-4 py-3 shadow-md backdrop-blur"
      >
        {folderLocked ? null : (
          <Button type="submit" size="sm" disabled={saving || deleting || markdown.trim() === ""}>
            {saving ? "Saving…" : creating ? "Create skill" : "Save skill"}
          </Button>
        )}
        <Button type="button" variant="outline" size="sm" onClick={onCancel} disabled={saving || deleting}>
          Cancel
        </Button>
        {onDelete && !creating ? (
          <Button
            type="button"
            variant="danger"
            size="sm"
            className="ml-auto"
            disabled={saving || deleting}
            onClick={onDelete}
          >
            <Trash2 />
            {deleting ? "Deleting…" : "Delete"}
          </Button>
        ) : null}
      </div>
    </form>
  )
}

export function markdownFiles(files: File[]): File[] {
  return files.filter((f) => /\.(md|markdown|mdown)$/i.test(f.name) || f.name === "SKILL.md")
}
