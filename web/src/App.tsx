import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter, Navigate, Outlet, Route, Routes, useLocation } from "react-router";
import { Header } from "./components/Header";
import { ToastHost } from "./components/Toast";
import { TokenGate } from "./components/TokenGate";
import { TooltipProvider } from "./components/ui/tooltip";
import { cn } from "./lib/utils";
import { AgentPage } from "./pages/AgentPage";
import { NodeEditPage } from "./pages/NodeEditPage";
import { NodesPage } from "./pages/NodesPage";
import { SecretsPage } from "./pages/SecretsPage";
import { SubmitPage } from "./pages/SubmitPage";
import { TaskDetailPage } from "./pages/TaskDetailPage";
import { TasksPage } from "./pages/TasksPage";
import { UsagePage } from "./pages/UsagePage";

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } },
});

function Shell() {
  const { pathname } = useLocation();
  // Chat is a full-height pane; padding and a max width would clip the transcript.
  const agent = pathname.startsWith("/agent");
  return (
    <div className="flex h-full bg-background">
      <Header />
      <main
        className={cn(
          "min-w-0 flex-1",
          agent ? "overflow-hidden" : "overflow-y-auto px-6 py-7 lg:px-8",
        )}
      >
        {agent ? (
          <Outlet />
        ) : (
          <div key={pathname} className="mx-auto w-full max-w-7xl animate-in fade-in-0 duration-200">
            <Outlet />
          </div>
        )}
      </main>
    </div>
  );
}

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <TooltipProvider>
        <ToastHost>
          <TokenGate>
            <BrowserRouter>
              <Routes>
                <Route element={<Shell />}>
                  <Route path="/" element={<TasksPage />} />
                  <Route path="/tasks/:id" element={<TaskDetailPage />} />
                  <Route path="/usage/*" element={<UsagePage />} />
                  <Route path="/submit" element={<SubmitPage />} />
                  <Route path="/nodes" element={<NodesPage />} />
                  <Route path="/nodes/:id" element={<NodeEditPage />} />
                  <Route path="/secrets" element={<SecretsPage />} />
                  {/* /* because the tabs are real routes; step 21 adds /agent/chat. */}
                  <Route path="/agent/*" element={<AgentPage />} />
                  <Route path="*" element={<Navigate to="/" replace />} />
                </Route>
              </Routes>
            </BrowserRouter>
          </TokenGate>
        </ToastHost>
      </TooltipProvider>
    </QueryClientProvider>
  );
}
