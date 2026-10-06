import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { BrowserRouter, Navigate, Outlet, Route, Routes } from "react-router";
import { Header } from "./components/Header";
import { ToastHost } from "./components/Toast";
import { TokenGate } from "./components/TokenGate";
import { TooltipProvider } from "./components/ui/tooltip";
import { cn } from "./lib/utils";
import { useViewer } from "./lib/identity";
import { AgentPage } from "./pages/AgentPage";
import { NodeEditPage } from "./pages/NodeEditPage";
import { NodesPage } from "./pages/NodesPage";
import { RegistriesPage } from "./pages/RegistriesPage";
import { SecretsPage } from "./pages/SecretsPage";
import { SubmitPage } from "./pages/SubmitPage";
import { TaskDetailPage } from "./pages/TaskDetailPage";
import { TasksPage } from "./pages/TasksPage";
import { UsagePage } from "./pages/UsagePage";

const queryClient = new QueryClient({
  defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } },
});

function Home() {
  const who = useViewer();
  // The assistant is home when there is one to talk to. A control plane without a
  // conductor still opens on the task list.
  return <Navigate to={who?.agentEnabled ? "/agent" : "/tasks"} replace />;
}

function Shell() {
  return (
    <div className="flex h-full flex-col bg-background md:flex-row">
      <Header />
      <main
        className={cn(
          // relative because a scrolling pane has to be a containing block. Radix renders a
          // hidden absolutely-positioned input beside every Switch and Checkbox inside a form;
          // with nothing positioned above it that input hangs off the initial containing block,
          // escapes the pane's overflow and stretches the document instead of the pane.
          // Each page draws its own top bar and scrolls beneath it, so the column itself does not.
          "relative flex min-h-0 min-w-0 flex-1 flex-col overflow-hidden",
        )}
      >
        <Outlet />
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
                  <Route path="/" element={<Home />} />
                  <Route path="/tasks" element={<TasksPage />} />
                  <Route path="/tasks/:id" element={<TaskDetailPage />} />
                  <Route path="/usage/*" element={<UsagePage />} />
                  <Route path="/submit" element={<SubmitPage />} />
                  <Route path="/nodes" element={<NodesPage />} />
                  <Route path="/nodes/:id" element={<NodeEditPage />} />
                  <Route path="/secrets" element={<SecretsPage />} />
                  <Route path="/registries" element={<RegistriesPage />} />
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
