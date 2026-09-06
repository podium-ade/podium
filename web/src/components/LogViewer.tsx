import { useCallback, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { ArrowDownToLine, CircleAlert, Search } from "lucide-react";
import { Badge, type Tone } from "./Badge";
import { Alert } from "./ui/alert";
import { Button } from "./ui/button";
import { Card } from "./ui/card";
import { Checkbox } from "./ui/checkbox";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Switch } from "./ui/switch";
import { Tooltip } from "./ui/tooltip";
import type { StreamPhase } from "../hooks/useTaskEvents";
import { filterLines, sidecarNames, sidecarOf, toRawText, type LogLine } from "../lib/logs";
import { cn } from "../lib/utils";

const ROW_PX = 18;
const OVERSCAN = 12;
/** jsdom and a not-yet-laid-out container both report clientHeight 0; render a screenful anyway. */
const FALLBACK_VIEWPORT_PX = 600;
const AT_BOTTOM_SLACK_PX = 24;
/** How far above the first error to land, so the lines that led to it are on screen too. */
const ERROR_CONTEXT_ROWS = 3;

const PHASE_TEXT: Record<StreamPhase, string> = {
  connecting: "connecting…",
  streaming: "live",
  finished: "stream ended",
  error: "reconnecting…",
};

const PHASE_TONE: Record<StreamPhase, Tone> = {
  connecting: "idle",
  streaming: "run",
  finished: "idle",
  error: "err",
};

export function LogViewer({
  lines,
  phase,
  error,
  taskId,
}: {
  lines: readonly LogLine[];
  phase: StreamPhase;
  /** The last stream failure. Shown only while the phase says it is still reconnecting. */
  error?: string;
  taskId: string;
}) {
  const [stdout, setStdout] = useState(true);
  const [stderr, setStderr] = useState(true);
  // Keyed by sidecar name and sparse: a name that is not in here is shown, so a sidecar that
  // first speaks half way through the run appears rather than arriving pre-hidden.
  const [sidecars, setSidecars] = useState<Record<string, boolean>>({});
  const [search, setSearch] = useState("");
  const [follow, setFollow] = useState(true);
  const [scrollTop, setScrollTop] = useState(0);
  const [viewport, setViewport] = useState(0);
  const ref = useRef<HTMLDivElement>(null);
  const uid = useId();

  const names = useMemo(() => sidecarNames(lines), [lines]);
  const visible = useMemo(
    () => filterLines(lines, { stdout, stderr, sidecars, search }),
    [lines, stdout, stderr, sidecars, search],
  );
  const firstError = useMemo(() => visible.findIndex((l) => l.stream === "stderr"), [visible]);

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = () => setViewport(el.clientHeight);
    measure();
    window.addEventListener("resize", measure);
    return () => window.removeEventListener("resize", measure);
  }, [visible.length]);

  const toBottom = useCallback(() => {
    const el = ref.current;
    if (el) el.scrollTop = el.scrollHeight;
  }, []);

  useEffect(() => {
    if (follow) toBottom();
  }, [follow, visible.length, toBottom]);

  const onScroll = useCallback(() => {
    const el = ref.current;
    if (!el) return;
    setScrollTop(el.scrollTop);
    setViewport(el.clientHeight);
    setFollow(el.scrollHeight - el.scrollTop - el.clientHeight <= AT_BOTTOM_SLACK_PX);
  }, []);

  const toFirstError = () => {
    const el = ref.current;
    if (!el || firstError < 0) return;
    setFollow(false);
    const top = Math.max(0, (firstError - ERROR_CONTEXT_ROWS) * ROW_PX);
    el.scrollTop = top;
    setScrollTop(top);
  };

  const height = viewport > 0 ? viewport : FALLBACK_VIEWPORT_PX;
  const first = Math.max(0, Math.floor(scrollTop / ROW_PX) - OVERSCAN);
  const last = Math.min(visible.length, Math.ceil((scrollTop + height) / ROW_PX) + OVERSCAN);
  const window_ = visible.slice(first, last);

  const download = () => {
    const url = URL.createObjectURL(new Blob([toRawText(visible)], { type: "text/plain" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = `${taskId}.log`;
    a.click();
    URL.revokeObjectURL(url);
  };

  return (
    <Card className="flex min-w-0 flex-col overflow-hidden">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-hairline px-4 py-2.5">
        <h2 className="text-sm font-semibold tracking-tight text-fg">Logs</h2>
        <Badge tone={PHASE_TONE[phase]}>{PHASE_TEXT[phase]}</Badge>

        <div className="flex flex-wrap items-center gap-0.5 rounded-lg border border-border bg-panel p-0.5">
          <StreamToggle
            id={`${uid}-stdout`}
            name="stdout"
            checked={stdout}
            onChange={setStdout}
            tone="text-fg"
          />
          <StreamToggle
            id={`${uid}-stderr`}
            name="stderr"
            checked={stderr}
            onChange={setStderr}
            tone="text-err"
          />
          {/* One toggle per sidecar that has actually produced output. A task with no sidecars
              shows nothing here, which is why the list is derived from the lines rather than from
              the spec: a declared sidecar that never logged has no lines to filter. */}
          {names.map((name) => (
            <StreamToggle
              key={name}
              id={`${uid}-sidecar-${name}`}
              name={name}
              label={`sidecar ${name}`}
              checked={sidecars[name] !== false}
              onChange={(on) => setSidecars((prev) => ({ ...prev, [name]: on }))}
              tone="text-warn"
            />
          ))}
        </div>

        <div className="relative">
          <Search
            aria-hidden
            className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-faint"
          />
          <Input
            type="search"
            aria-label="Search logs"
            placeholder="Filter output"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="h-7 w-64 pr-22 pl-7.5 font-mono text-xs"
          />
          <span className="tabular pointer-events-none absolute top-1/2 right-2.5 -translate-y-1/2 text-2xs text-faint">
            {visible.length} / {lines.length} lines
          </span>
        </div>

        <div className="ml-auto flex items-center gap-2">
          {firstError >= 0 ? (
            <Button variant="ghost" size="xs" onClick={toFirstError} className="text-err">
              <CircleAlert />
              First error
            </Button>
          ) : null}
          <Label htmlFor={`${uid}-follow`} className="text-2xs">
            Follow
          </Label>
          <Switch
            id={`${uid}-follow`}
            checked={follow}
            onCheckedChange={(on) => {
              setFollow(on);
              if (on) toBottom();
            }}
          />
          <Tooltip label="Download what is on screen as a text file">
            <Button
              variant="outline"
              size="icon-xs"
              aria-label="Download raw log"
              onClick={download}
            >
              <ArrowDownToLine />
            </Button>
          </Tooltip>
        </div>
      </div>

      {phase === "error" && error ? (
        <Alert variant="warn" className="rounded-none border-x-0 border-t-0">
          Lost the log stream: {error}. Reconnecting — nothing already delivered is lost.
        </Alert>
      ) : null}

      <div
        ref={ref}
        onScroll={onScroll}
        data-testid="log-scroll"
        role="log"
        aria-label="Task output"
        className="max-h-96 min-h-48 flex-1 overflow-auto bg-bg font-mono text-xs leading-[18px]"
      >
        {visible.length === 0 ? (
          <div className="flex min-h-48 flex-col items-center justify-center gap-1 px-4 text-center font-sans">
            <p className="text-xs text-muted">
              {lines.length === 0 ? "no output yet" : "no lines match the filter"}
            </p>
            <p className="text-2xs text-faint">
              {lines.length === 0
                ? "Anything the container writes to stdout or stderr appears here as it happens."
                : "Clear the search, or turn a stream back on."}
            </p>
          </div>
        ) : (
          <div style={{ height: visible.length * ROW_PX }}>
            <div style={{ transform: `translateY(${first * ROW_PX}px)` }}>
              {window_.map((l) => (
                <div key={l.id} style={{ height: ROW_PX }} className="flex w-max min-w-full">
                  {/* The gutter is sticky so the line numbers survive scrolling a wide log
                      sideways, which is exactly when you need them to quote a line. */}
                  <span
                    aria-hidden
                    className="tabular sticky left-0 z-10 w-12 shrink-0 border-r border-hairline bg-bg pr-2 text-right text-faint select-none"
                  >
                    {l.id + 1}
                  </span>
                  <span
                    data-testid="log-line"
                    data-stream={l.stream}
                    data-sidecar={l.stream === "sidecar" ? sidecarOf(l) : undefined}
                    className={cn(
                      "px-3 whitespace-pre",
                      l.stream === "stderr" ? "text-err" : l.stream === "sidecar" ? "text-warn" : "",
                    )}
                  >
                    {l.stream === "sidecar" && l.source !== "" ? `[${l.source}] ` : ""}
                    {l.text === "" ? " " : l.text}
                  </span>
                </div>
              ))}
            </div>
          </div>
        )}
      </div>
    </Card>
  );
}

/**
 * One stream's visibility, as a checkbox that carries the colour its lines are drawn in — the
 * toggle and the output it governs read as the same thing.
 */
function StreamToggle({
  id,
  name,
  label,
  checked,
  onChange,
  tone,
}: {
  id: string;
  name: string;
  /** An accessible name when the visible text is not enough on its own, e.g. "sidecar db". */
  label?: string;
  checked: boolean;
  onChange: (on: boolean) => void;
  tone: string;
}) {
  return (
    <span
      className={cn(
        "flex items-center gap-1.5 rounded-md px-2 py-1 transition-colors hover:bg-raised",
        checked ? tone : "text-faint",
      )}
    >
      <Checkbox
        id={id}
        aria-label={label}
        checked={checked}
        onCheckedChange={(on) => onChange(on === true)}
        className="size-3.5"
      />
      <Label htmlFor={id} className="cursor-pointer font-mono text-2xs text-current">
        {name}
      </Label>
    </span>
  );
}
