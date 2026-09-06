import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { StreamPhase } from "../hooks/useTaskEvents";
import { filterLines, sidecarNames, sidecarOf, toRawText, type LogLine } from "../lib/logs";

const ROW_PX = 18;
const OVERSCAN = 12;
/** jsdom and a not-yet-laid-out container both report clientHeight 0; render a screenful anyway. */
const FALLBACK_VIEWPORT_PX = 600;
const AT_BOTTOM_SLACK_PX = 24;

const PHASE_TEXT: Record<StreamPhase, string> = {
  connecting: "connecting…",
  streaming: "live",
  finished: "stream ended",
  error: "reconnecting…",
};

export function LogViewer({
  lines,
  phase,
  taskId,
}: {
  lines: readonly LogLine[];
  phase: StreamPhase;
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

  const names = useMemo(() => sidecarNames(lines), [lines]);
  const visible = useMemo(
    () => filterLines(lines, { stdout, stderr, sidecars, search }),
    [lines, stdout, stderr, sidecars, search],
  );

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = () => setViewport(el.clientHeight);
    measure();
    window.addEventListener("resize", measure);
    return () => window.removeEventListener("resize", measure);
  }, []);

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
    <section className="rounded-xl border border-border bg-card shadow-xs">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1 border-b border-border px-3 py-2 text-xs">
        <span className="font-medium">Logs</span>
        <span className={phase === "error" ? "text-err" : "text-muted"}>{PHASE_TEXT[phase]}</span>
        <label className="flex items-center gap-1">
          <input type="checkbox" checked={stdout} onChange={(e) => setStdout(e.target.checked)} />
          stdout
        </label>
        <label className="flex items-center gap-1">
          <input type="checkbox" checked={stderr} onChange={(e) => setStderr(e.target.checked)} />
          stderr
        </label>
        {/* One checkbox per sidecar that has actually produced output. A task with no sidecars
            shows nothing here, which is why the list is derived from the lines rather than from
            the spec: a declared sidecar that never logged has no lines to filter. */}
        {names.map((name) => (
          <label key={name} className="flex items-center gap-1 text-warn">
            <input
              type="checkbox"
              aria-label={`sidecar ${name}`}
              checked={sidecars[name] !== false}
              onChange={(e) => setSidecars((prev) => ({ ...prev, [name]: e.target.checked }))}
            />
            {name}
          </label>
        ))}
        <label className="flex items-center gap-1">
          <input
            type="checkbox"
            checked={follow}
            onChange={(e) => {
              setFollow(e.target.checked);
              if (e.target.checked) toBottom();
            }}
          />
          follow
        </label>
        <input
          type="search"
          aria-label="Search logs"
          placeholder="search"
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="w-28 rounded border border-border bg-bg px-2 py-0.5 font-mono outline-none focus:border-accent"
        />
        <span className="text-muted">
          {visible.length} / {lines.length} lines
        </span>
        <button
          type="button"
          onClick={download}
          className="ml-auto rounded border border-border px-2 py-0.5 hover:border-accent"
        >
          download raw
        </button>
      </div>

      <div
        ref={ref}
        onScroll={onScroll}
        data-testid="log-scroll"
        role="log"
        aria-label="Task output"
        className="h-96 overflow-auto bg-bg font-mono text-xs leading-[18px]"
      >
        {visible.length === 0 ? (
          <p className="p-3 text-muted">
            {lines.length === 0 ? "no output yet" : "no lines match the filter"}
          </p>
        ) : (
          <div style={{ height: visible.length * ROW_PX }}>
            <div style={{ transform: `translateY(${first * ROW_PX}px)` }}>
              {window_.map((l) => (
                <div
                  key={l.id}
                  data-testid="log-line"
                  data-stream={l.stream}
                  data-sidecar={l.stream === "sidecar" ? sidecarOf(l) : undefined}
                  style={{ height: ROW_PX }}
                  className={`whitespace-pre px-3 ${
                    l.stream === "stderr" ? "text-err" : l.stream === "sidecar" ? "text-warn" : ""
                  }`}
                >
                  {l.stream === "sidecar" && l.source !== "" ? `[${l.source}] ` : ""}
                  {l.text === "" ? " " : l.text}
                </div>
              ))}
            </div>
          </div>
        )}
      </div>
    </section>
  );
}
