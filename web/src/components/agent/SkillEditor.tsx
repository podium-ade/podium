import { useRef, useState } from "react";
import { ChevronLeft, FolderOpen, Paperclip } from "lucide-react";
import { Button } from "../ui/button";
import { YamlEditor } from "../yaml/YamlEditor";

const STARTER = `---
name: pr-review
description: Use when reviewing a pull request.
---

`

export type SkillFile = { name: string; markdown: string }

/**
 * SkillEditor is one SKILL.md: pick a file, pick a folder of them, or write it here.
 * Zip archives are not a shape this screen speaks.
 */
export function SkillEditor({
  existing,
  saving,
  deleting,
  error,
  onSubmit,
  onSubmitMany,
  onDelete,
  onCancel,
}: {
  existing?: { name: string; markdown: string }
  saving: boolean
  deleting?: boolean
  error?: string
  onSubmit: (file: SkillFile) => void
  onSubmitMany: (files: SkillFile[]) => void
  onDelete?: () => void
  onCancel: () => void
}) {
  const fileRef = useRef<HTMLInputElement>(null)
  const dirRef = useRef<HTMLInputElement>(null)
  const [markdown, setMarkdown] = useState(existing?.markdown || STARTER)
  const [filename, setFilename] = useState("SKILL.md")
  const [tried, setTried] = useState(false)
  const creating = !existing

  function submit() {
    setTried(true)
    if (markdown.trim() === "") return
    onSubmit({ name: filename, markdown })
  }

  async function loadFiles(list: FileList | null, folder: boolean) {
    if (!list || list.length === 0) return
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
            <Button type="button" variant="outline" size="sm" onClick={() => fileRef.current?.click()}>
              <Paperclip />
              Choose a file
            </Button>
            {creating ? (
            <Button type="button" variant="outline" size="sm" onClick={() => dirRef.current?.click()}>
              <FolderOpen />
              Choose a folder
            </Button>
            ) : null}
          </div>
        </div>
      </header>

      <YamlEditor
        id="skill-md"
        label="SKILL.md"
        language="markdown"
        value={markdown}
        onChange={setMarkdown}
        invalid={tried && markdown.trim() === ""}
        minLines={22}
      />
      {tried && markdown.trim() === "" ? (
        <p className="text-xs text-err">Write the skill, or choose a file.</p>
      ) : null}
      {error ? <p className="text-xs text-err">{error}</p> : null}

      <div className="sticky bottom-0 z-10 -mx-1 flex flex-wrap items-center gap-2 border-t border-hairline bg-bg/80 px-1 py-3 backdrop-blur-md">
        <Button type="submit" size="sm" disabled={saving || deleting || markdown.trim() === ""}>
          {saving ? "Saving…" : creating ? "Create skill" : "Save skill"}
        </Button>
        <Button type="button" variant="outline" size="sm" onClick={onCancel} disabled={saving || deleting}>
          Cancel
        </Button>
        {onDelete && !creating ? (
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="ml-auto hover:bg-err/12 hover:text-err"
            disabled={saving || deleting}
            onClick={onDelete}
          >
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
