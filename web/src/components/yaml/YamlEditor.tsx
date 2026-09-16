import { forwardRef, useEffect, useImperativeHandle, useRef, useState, type ReactNode } from "react";
import { basicSetup } from "codemirror";
import { EditorView, keymap } from "@codemirror/view";
import { Compartment, EditorState, Prec } from "@codemirror/state";
import { yaml } from "@codemirror/lang-yaml";
import { markdown } from "@codemirror/lang-markdown";
import { linter, lintGutter, type Diagnostic } from "@codemirror/lint";
import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { tags as t } from "@lezer/highlight";
import { Check, Copy, RotateCcw } from "lucide-react";
import { docToYaml, parseYaml, type YamlProblem } from "../../lib/yaml";
import { cn } from "../../lib/utils";
import { Button } from "../ui/button";
import { Kbd } from "../ui/kbd";
import { Tooltip } from "../ui/tooltip";

export type YamlEditorHandle = {
  jumpTo: (line: number) => void
}

export type YamlEditorProps = {
  id?: string
  label: string
  value: string
  onChange?: (value: string) => void
  problems?: YamlProblem[]
  readOnly?: boolean
  invalid?: boolean
  /** Rows for the empty-document height. */
  minLines?: number
  className?: string
  hint?: ReactNode
  onFormat?: (value: string) => void
  /** yaml is the default; markdown is SKILL.md. */
  language?: "yaml" | "markdown"
}

/**
 * YamlEditor is CodeMirror 6 — the same engine Sourcegraph, Replit and CodePen embed —
 * with the YAML language pack and a lint gutter fed by our decoder.
 *
 * Monaco (VS Code) was the other serious option. It is the wrong size for a config field:
 * a couple of megabytes, a web worker, and no mobile. Ace is older and quieter. Tiny
 * overlays (CodeJar, react-simple-code-editor) still leave indent, search and diagnostics
 * as our problem. CodeMirror is the one that is both a real editor and small enough to
 * sit in a form.
 *
 * A labelled textarea stays in the tree so Playwright `fill()` and the existing unit tests
 * keep talking to a value. CodeMirror is what a human types in; the two stay in lockstep.
 */
export const YamlEditor = forwardRef<YamlEditorHandle, YamlEditorProps>(function YamlEditor(
  {
    id,
    label,
    value,
    onChange,
    problems = [],
    readOnly = false,
    invalid,
    minLines = 18,
    className,
    hint,
    onFormat,
    language = "yaml",
  },
  ref,
) {
  const hostRef = useRef<HTMLDivElement>(null)
  const viewRef = useRef<EditorView | null>(null)
  const valueRef = useRef(value)
  const onChangeRef = useRef(onChange)
  const problemsRef = useRef(problems)
  const formatRef = useRef(() => {})
  const lintConf = useRef(new Compartment())
  const readConf = useRef(new Compartment())
  const syncing = useRef(false)
  const [caret, setCaret] = useState({ line: 1, col: 1 })
  const [copied, setCopied] = useState(false)

  valueRef.current = value
  onChangeRef.current = onChange
  problemsRef.current = problems
  formatRef.current = () => {
    if (readOnly || !onChange || language !== "yaml") return
    const parsed = parseYaml(value)
    if (parsed.problems.length > 0 || parsed.value === undefined) return
    const next = docToYaml(parsed.value)
    if (next === value) return
    onChange(next)
    onFormat?.(next)
  }

  useEffect(() => {
    // jsdom cannot host CodeMirror's measure loop; the labelled textarea is what
    // unit tests and Playwright fill() talk to. The real editor is for a browser.
    if (import.meta.env.MODE === "test") return undefined
    const host = hostRef.current
    if (!host) return undefined

    const view = new EditorView({
      parent: host,
      state: EditorState.create({
        doc: valueRef.current,
        extensions: [
          basicSetup,
          language === "markdown" ? markdown() : yaml(),
          podiumTheme,
          syntaxHighlighting(podiumHighlight),
          lintGutter(),
          lintConf.current.of(language === "yaml" ? linter(diagnostics, { delay: 250 }) : []),
          readConf.current.of([
            EditorState.readOnly.of(false),
            EditorView.editable.of(true),
          ]),
          Prec.highest(
            keymap.of([
              {
                key: "Mod-s",
                run: () => {
                  formatRef.current()
                  return true
                },
              },
            ]),
          ),
          EditorView.updateListener.of((update) => {
            const head = update.state.selection.main.head
            const line = update.state.doc.lineAt(head)
            setCaret({ line: line.number, col: head - line.from + 1 })
            if (syncing.current || !update.docChanged) return
            const next = update.state.doc.toString()
            if (next !== valueRef.current) onChangeRef.current?.(next)
          }),
          EditorView.contentAttributes.of({ "aria-hidden": "true" }),
        ],
      }),
    })
    viewRef.current = view
    return () => {
      view.destroy()
      viewRef.current = null
    }
  }, [])

  useEffect(() => {
    const view = viewRef.current
    if (!view) return
    if (view.state.doc.toString() === value) return
    syncing.current = true
    view.dispatch({
      changes: { from: 0, to: view.state.doc.length, insert: value },
    })
    syncing.current = false
  }, [value])

  useEffect(() => {
    if (language !== "yaml") return
    viewRef.current?.dispatch({
      effects: lintConf.current.reconfigure(linter(diagnostics, { delay: 250 })),
    })
  }, [problems, language])

  useEffect(() => {
    viewRef.current?.dispatch({
      effects: readConf.current.reconfigure([
        EditorState.readOnly.of(readOnly),
        EditorView.editable.of(!readOnly),
      ]),
    })
  }, [readOnly])

  function diagnostics(view: EditorView): Diagnostic[] {
    const doc = view.state.doc
    return problemsRef.current.flatMap((p) => {
      if (!p.line) return []
      const n = Math.max(1, Math.min(p.line, doc.lines))
      const line = doc.line(n)
      const from = line.from + Math.max(0, (p.col ?? 1) - 1)
      return [
        {
          from: Math.min(from, line.to),
          to: line.to,
          severity: "error" as const,
          message: p.path ? `${p.path}: ${p.message}` : p.message,
        },
      ]
    })
  }

  useImperativeHandle(
    ref,
    () => ({
      jumpTo: (line: number) => {
        const view = viewRef.current
        if (!view) return
        const n = Math.max(1, Math.min(line, view.state.doc.lines))
        const info = view.state.doc.line(n)
        view.dispatch({ selection: { anchor: info.from }, scrollIntoView: true })
        view.focus()
      },
    }),
    [],
  )

  const copy = async () => {
    await navigator.clipboard?.writeText(value)
    setCopied(true)
    setTimeout(() => setCopied(false), 1200)
  }

  const minHeight = `${minLines * 1.25}rem`
  const fallback = import.meta.env.MODE === "test"

  return (
    <div className={cn("space-y-2", className)}>
      <div className="flex flex-wrap items-center gap-2">
        <p id={labelId(id, label)} className="text-sm font-medium text-fg">
          {label}
        </p>
        <div className="ml-auto flex items-center gap-1">
          {readOnly || !onChange || language !== "yaml" ? null : (
            <Tooltip label="Rewrite from the decoded document. Does nothing while the YAML does not parse.">
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label="Format YAML"
                onClick={() => formatRef.current()}
              >
                <RotateCcw />
              </Button>
            </Tooltip>
          )}
          <Tooltip label="Copy the document">
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              aria-label={language === "markdown" ? "Copy markdown" : "Copy YAML"}
              onClick={() => void copy()}
            >
              {copied ? <Check className="text-ok" /> : <Copy />}
            </Button>
          </Tooltip>
        </div>
      </div>

      <div
        className={cn(
          "overflow-hidden rounded-lg border bg-bg shadow-xs",
          invalid ? "border-err/70" : "border-input",
        )}
      >
        <textarea
          id={id}
          data-testid="yaml-editor"
          aria-labelledby={labelId(id, label)}
          aria-invalid={invalid || undefined}
          value={value}
          readOnly={readOnly}
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          autoComplete="off"
          tabIndex={fallback ? undefined : -1}
          rows={minLines}
          wrap="off"
          onChange={(e) => onChange?.(e.target.value)}
          className={
            fallback
              ? "block w-full resize-y bg-transparent px-3 py-2.5 font-mono text-xs leading-5 text-fg outline-none"
              : "sr-only"
          }
          style={fallback ? { minHeight, tabSize: 2 } : undefined}
        />
        {fallback ? null : <div ref={hostRef} style={{ minHeight }} className="yaml-cm min-w-0" />}

        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-t border-hairline bg-raised/30 px-3 py-1.5 text-2xs text-faint">
          <span className="tabular">
            Ln {caret.line}, Col {caret.col}
          </span>
          <span>
            {problems.length === 0
              ? language === "markdown"
                ? "Markdown"
                : "Valid YAML"
              : `${problems.length} ${problems.length === 1 ? "problem" : "problems"}`}
          </span>
          {readOnly ? (
            <span>Read only</span>
          ) : (
            <span className="hidden items-center gap-1 sm:inline-flex">
              <Kbd>Tab</Kbd> indent
              <Kbd>⌘F</Kbd> find
            </span>
          )}
        </div>
      </div>

      {hint ? <p className="text-2xs leading-relaxed text-muted">{hint}</p> : null}
    </div>
  )
})

function labelId(id: string | undefined, label: string): string {
  return id ? `${id}-label` : `yaml-${label.replace(/\s+/g, "-").toLowerCase()}-label`
}

const podiumTheme = EditorView.theme(
  {
    "&": {
      backgroundColor: "var(--color-bg)",
      color: "var(--color-fg)",
      fontSize: "0.75rem",
      lineHeight: "1.25rem",
      fontFamily: "var(--font-mono)",
      height: "100%",
    },
    "&.cm-focused": { outline: "none" },
    ".cm-scroller": { fontFamily: "var(--font-mono)", lineHeight: "1.25rem" },
    ".cm-content": {
      caretColor: "var(--color-fg)",
      padding: "0.625rem 0",
    },
    ".cm-gutters": {
      backgroundColor: "color-mix(in oklab, var(--color-raised) 80%, transparent)",
      color: "var(--color-faint)",
      borderRight: "1px solid var(--color-hairline)",
    },
    ".cm-activeLine": {
      backgroundColor: "color-mix(in oklab, var(--color-raised) 55%, transparent)",
    },
    ".cm-activeLineGutter": {
      backgroundColor: "color-mix(in oklab, var(--color-raised) 55%, transparent)",
    },
    ".cm-selectionBackground, &.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground":
      {
        backgroundColor: "color-mix(in oklab, var(--color-accent) 32%, transparent) !important",
      },
    ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--color-fg)" },
    ".cm-lintPoint-error, .cm-lintRange-error": {
      backgroundColor: "color-mix(in oklab, var(--color-err) 16%, transparent)",
    },
  },
  { dark: true },
)

const podiumHighlight = HighlightStyle.define([
  { tag: t.comment, color: "var(--color-faint)", fontStyle: "italic" },
  { tag: t.propertyName, color: "var(--color-accent)" },
  { tag: t.string, color: "var(--color-ok)" },
  { tag: t.number, color: "var(--color-warn)" },
  { tag: t.bool, color: "var(--color-run)" },
  { tag: t.null, color: "var(--color-run)" },
  { tag: t.atom, color: "var(--color-warn)" },
  { tag: t.keyword, color: "var(--color-run)" },
  { tag: t.separator, color: "var(--color-muted)" },
  { tag: t.punctuation, color: "var(--color-muted)" },
  { tag: t.processingInstruction, color: "var(--color-faint)" },
  { tag: t.heading, color: "var(--color-accent)", fontWeight: "bold" },
  { tag: t.emphasis, fontStyle: "italic" },
  { tag: t.strong, fontWeight: "bold" },
  { tag: t.link, color: "var(--color-run)" },
  { tag: t.monospace, color: "var(--color-ok)" },
])
