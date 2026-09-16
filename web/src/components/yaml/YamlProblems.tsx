import { Alert } from "../ui/alert";
import { problemText, type YamlProblem } from "../../lib/yaml";

/**
 * The list every YAML-backed editor shows: every problem at once, and a click that names
 * a line takes the caret there.
 */
export function YamlProblems({
  problems,
  onJump,
  testId = "spec-problems",
}: {
  problems: YamlProblem[]
  onJump?: (line: number) => void
  testId?: string
}) {
  if (problems.length === 0) return null
  return (
    <Alert
      role="alert"
      variant="destructive"
      title={`${problems.length} ${problems.length === 1 ? "problem" : "problems"} to fix`}
    >
      <ul data-testid={testId} className="mt-1 space-y-1">
        {problems.map((p, i) => {
          const text = problemText(p)
          const jumpable = onJump && p.line
          return (
            <li key={`${text}-${i}`} className="font-mono break-words">
              {jumpable ? (
                <button
                  type="button"
                  className="rounded-sm text-left underline-offset-2 hover:underline focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:outline-none"
                  onClick={() => onJump(p.line!)}
                >
                  <LineMark line={p.line} />
                  {text}
                </button>
              ) : (
                <>
                  <LineMark line={p.line} />
                  {text}
                </>
              )}
            </li>
          )
        })}
      </ul>
    </Alert>
  )
}

function LineMark({ line }: { line?: number }) {
  if (!line) return null
  return <span className="mr-1.5 text-faint">L{line}</span>
}
