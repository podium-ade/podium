import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { CheckCircle2, OctagonAlert, X } from "lucide-react";
import { cn } from "@/lib/utils";

interface Toast {
  id: number;
  text: string;
  tone: "err" | "ok";
}

const TTL_MS = 6000;

const ToastContext = createContext<(text: string, tone?: "err" | "ok") => void>(() => {});

// eslint-disable-next-line react-refresh/only-export-components
export function useToast() {
  return useContext(ToastContext);
}

function ToastCard({ toast, onDismiss }: { toast: Toast; onDismiss: () => void }) {
  const ok = toast.tone === "ok";
  const Icon = ok ? CheckCircle2 : OctagonAlert;
  return (
    <div
      role="status"
      className={cn(
        "pointer-events-auto flex items-start gap-2.5 rounded-lg border bg-panel py-2.5 pr-2 pl-3 shadow-lg",
        "animate-in slide-in-from-right-3 fade-in-0 duration-200",
        ok ? "border-ok/40" : "border-err/40",
      )}
    >
      <Icon className={cn("mt-px size-4 shrink-0", ok ? "text-ok" : "text-err")} />
      <p className="min-w-0 flex-1 text-xs leading-relaxed break-words text-fg">{toast.text}</p>
      <button
        type="button"
        onClick={onDismiss}
        aria-label="Dismiss"
        className="-mt-0.5 shrink-0 rounded-md p-1 text-faint transition-colors hover:bg-raised hover:text-fg focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:outline-none"
      >
        <X className="size-3.5" />
      </button>
    </div>
  );
}

export function ToastHost({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const next = useRef(0);
  const timers = useRef(new Map<number, ReturnType<typeof setTimeout>>());

  const dismiss = useCallback((id: number) => {
    const t = timers.current.get(id);
    if (t) clearTimeout(t);
    timers.current.delete(id);
    setToasts((prev) => prev.filter((x) => x.id !== id));
  }, []);

  const push = useCallback(
    (text: string, tone: "err" | "ok" = "err") => {
      const id = next.current++;
      setToasts((prev) => [...prev, { id, text, tone }]);
      timers.current.set(
        id,
        setTimeout(() => dismiss(id), TTL_MS),
      );
    },
    [dismiss],
  );

  useEffect(() => {
    const pending = timers.current;
    return () => {
      for (const t of pending.values()) clearTimeout(t);
      pending.clear();
    };
  }, []);

  const value = useMemo(() => push, [push]);

  return (
    <ToastContext.Provider value={value}>
      {children}
      <div className="pointer-events-none fixed right-4 bottom-4 z-100 flex w-84 flex-col gap-2">
        {toasts.map((t) => (
          <ToastCard key={t.id} toast={t} onDismiss={() => dismiss(t.id)} />
        ))}
      </div>
    </ToastContext.Provider>
  );
}
